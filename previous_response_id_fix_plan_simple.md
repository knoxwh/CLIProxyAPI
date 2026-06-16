# previous_response_id 多轮传递修复计划（单用户精简版）

## 问题确认

### 1. Stale ID 不清理（P0 - 阻断性）

**现象**：上游返回 `previous_response_not_found` 后不清理本地缓存，下次请求继续注入 stale ID，连续 400 错误持续 24h 直到 TTL 自然到期

**触发**：上游过期、手动删除历史、API key 切换组织

**影响**：阻断对话，必须重启代理

---

### 2. 客户端意图被覆盖（P0 - 协议语义）

**现象**：`codex_executor.go:828` 删除客户端传入的 `previous_response_id`，`CacheOptPostTKLite` 只按缓存注入

**影响**：破坏客户端显式控制对话链的能力

---

### 3. OAuth 分支没有防御性删除（P1 - 防御性）

**风险**：若未来 trunk 不删除 `previous_response_id`，chatgpt.com 可能拒绝

---

## 修复方案

### 1. Stale ID 清理

**新增函数**：
```go
// internal/runtime/executor/helps/cache_helpers.go

// DeleteSessionResponseID removes a stale session→response_id entry.
func DeleteSessionResponseID(sessionKey string) {
	if sessionKey == "" {
		return
	}
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	sessionResponseMu.Lock()
	delete(sessionResponseMap, sessionKey)
	sessionResponseMu.Unlock()
}
```

**错误路径清理**（复用现有 `codexStatusErrorClassification`）：

位置1：`codex_executor.go:885-894`
```go
if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
	b, _ := io.ReadAll(httpResp.Body)
	b = applyCodexIdentityConfuseResponsePayload(b, identityState)
	
	// 清理 stale response ID
	if code, _, ok := codexStatusErrorClassification(httpResp.StatusCode, b); ok && code == "previous_response_not_found" {
		sessionKey := cacheOptSessionResponseKey(auth, req)
		if sessionKey != "" {
			helps.DeleteSessionResponseID(sessionKey)
		}
	}
	
	if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, b); errClearReplay != nil {
		return resp, errClearReplay
	}
	// ... 原有逻辑 ...
}
```

位置2：`codex_executor.go:1174-1190`（ExecuteStream，同样逻辑）

---

### 2. 客户端意图尊重

**修改 CacheOptPostTKLite**：
```go
// internal/runtime/executor/codex_cache_optimizer.go:48-74

func CacheOptPostTKLite(auth *cliproxyauth.Auth, body []byte, req cliproxyexecutor.Request, originalPayloadSource []byte) []byte {
	if isAPIKeyAuth(auth) {
		// ── API key path ──
		body, _ = sjson.SetBytes(body, "store", true)

		// 仅在客户端未显式指定时注入 previous_response_id
		if gjson.GetBytes(originalPayloadSource, "previous_response_id").Exists() {
			// 客户端显式指定，恢复
			clientValue := gjson.GetBytes(originalPayloadSource, "previous_response_id").String()
			if strings.TrimSpace(clientValue) != "" {
				body, _ = sjson.SetBytes(body, "previous_response_id", clientValue)
			}
		} else if !gjson.GetBytes(body, "previous_response_id").Exists() {
			// 客户端未指定，从 session map 注入
			sessionKey := cacheOptSessionResponseKey(auth, req)
			if sessionKey != "" {
				if lastRespID, ok := helps.GetSessionResponseID(sessionKey); ok && lastRespID != "" {
					body, _ = sjson.SetBytes(body, "previous_response_id", lastRespID)
				}
			}
		}
	} else {
		// ── OAuth path ──
		body, _ = sjson.SetBytes(body, "store", false)
		body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
		body, _ = sjson.DeleteBytes(body, "previous_response_id")  // 防御性删除
	}
	return body
}
```

**调用点修改**：
```go
// codex_executor.go:845 修改
// originalPayloadSource 选择：
//   - opts.OriginalRequest 存在时用它（WebSocket 上游）
//   - 否则用 req.Payload（原始客户端 payload）
var originalPayloadSource []byte
if opts.OriginalRequest != nil && len(opts.OriginalRequest) > 0 {
	originalPayloadSource = opts.OriginalRequest
} else {
	originalPayloadSource = req.Payload
}
body = CacheOptPostTKLite(auth, body, req, originalPayloadSource)
```

**注意**：`originalTranslated` 已被 `sdktranslator.TranslateRequest` 改过，必须用翻译前的原始 payload

---

### 3. OAuth 防御性删除

已包含在上面 `CacheOptPostTKLite` OAuth 分支中

---

## 测试

### 单元测试

```go
// internal/runtime/executor/codex_cache_optimizer_test.go

func TestStaleResponseIDCleared(t *testing.T) {
	sessionKey := "test-session"
	helps.SetSessionResponseID(sessionKey, "stale-resp-123")
	
	body := []byte(`{"error":{"code":"previous_response_not_found","message":"..."}}`)
	code, _, ok := codexStatusErrorClassification(http.StatusBadRequest, body)
	if !ok || code != "previous_response_not_found" {
		t.Fatal("error classification failed")
	}
	
	helps.DeleteSessionResponseID(sessionKey)
	
	if _, exists := helps.GetSessionResponseID(sessionKey); exists {
		t.Fatal("stale response_id should be cleared")
	}
}

func TestClientPreviousResponseIDRespected(t *testing.T) {
	originalPayload := []byte(`{"previous_response_id":"client-chosen-id","input":[...]}`)
	body := []byte(`{"input":[...]}`)
	
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test"}}
	req := cliproxyexecutor.Request{Payload: originalPayload}
	result := CacheOptPostTKLite(auth, body, req, originalPayload)
	
	if got := gjson.GetBytes(result, "previous_response_id").String(); got != "client-chosen-id" {
		t.Fatalf("previous_response_id = %q, want client-chosen-id", got)
	}
}

func TestSessionResponseIDRoundTrip(t *testing.T) {
	sessionKey := "round-trip-session"
	
	// 第一轮：空
	respID1, ok := helps.GetSessionResponseID(sessionKey)
	if ok {
		t.Fatal("first request should have no previous_response_id")
	}
	
	// 存储
	helps.SetSessionResponseID(sessionKey, "resp-001")
	
	// 第二轮：读取到 resp-001
	respID2, ok := helps.GetSessionResponseID(sessionKey)
	if !ok || respID2 != "resp-001" {
		t.Fatalf("second request should get resp-001, got %q", respID2)
	}
	
	// 更新
	helps.SetSessionResponseID(sessionKey, "resp-002")
	
	// 第三轮：读取到 resp-002
	respID3, ok := helps.GetSessionResponseID(sessionKey)
	if !ok || respID3 != "resp-002" {
		t.Fatalf("third request should get resp-002, got %q", respID3)
	}
}

func TestOAuthPathDeletesPreviousResponseID(t *testing.T) {
	body := []byte(`{"previous_response_id":"should-be-deleted","input":[...]}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{}} // 非 API key
	req := cliproxyexecutor.Request{}
	
	result := CacheOptPostTKLite(auth, body, req, body)
	
	if gjson.GetBytes(result, "previous_response_id").Exists() {
		t.Fatal("OAuth path should delete previous_response_id")
	}
	
	if gjson.GetBytes(result, "store").Bool() != false {
		t.Fatal("OAuth path should set store=false")
	}
}
```

---

## 实施清单

### 文件变更

**1. `internal/runtime/executor/helps/cache_helpers.go`**
- [ ] 新增 `DeleteSessionResponseID(sessionKey string)` 函数

**2. `internal/runtime/executor/codex_cache_optimizer.go`**
- [ ] `CacheOptPostTKLite` 改签名：新增 `originalPayloadSource []byte` 参数
- [ ] API key 路径：用 `originalPayloadSource` 判断客户端意图
- [ ] OAuth 路径：新增 `sjson.DeleteBytes(body, "previous_response_id")`

**3. `internal/runtime/executor/codex_executor.go`**
- [ ] 3 处 `CacheOptPostTKLite` 调用点：传入 `originalPayloadSource`（约 :845, :1120, :1281）
- [ ] 2 处错误路径（`:885` 和 `:1174`）：调用 `codexStatusErrorClassification` + `helps.DeleteSessionResponseID`

**4. `internal/runtime/executor/codex_cache_optimizer_test.go`**
- [ ] 4 个新测试（Stale、Client、RoundTrip、OAuth）

### 验证步骤

- [ ] `gofmt -w .`
- [ ] `go build -o test-output ./cmd/server && rm test-output`
- [ ] `go test ./internal/runtime/executor/...`
- [ ] 手动测试：模拟 `previous_response_not_found` 错误
- [ ] 手动测试：客户端显式传入 `previous_response_id`

---

## 预期工作量

**2-3 小时**（编码 + 测试）

---

## 删除的内容（单用户不需要）

- ~~Phase 2.1 并发锁~~（单用户无并发）
- ~~Phase 2.2 OpenAI session 推导~~（只用 Claude Code 格式）
- ~~Phase 3 KV 存储改造~~（单进程内存够用）
- ~~Metrics/Prometheus~~（单用户不需要监控）
- ~~Baseline 收集 + 量化成功标准~~（无生产环境）
- ~~集成测试~~（单元测试 + 手动验证够了）
- ~~详细日志~~（关键错误即可）
