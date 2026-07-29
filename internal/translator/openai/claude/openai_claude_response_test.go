package claude

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type sseEvent struct {
	Type    string
	Payload string
}

func runStream(t *testing.T, originalReq string, chunks ...string) []sseEvent {
	t.Helper()

	var paramAny any
	var emitted [][]byte
	for _, chunk := range chunks {
		emitted = append(emitted, ConvertOpenAIResponseToClaude(
			context.Background(),
			"",
			[]byte(originalReq),
			nil,
			[]byte("data: "+chunk),
			&paramAny,
		)...)
	}
	emitted = append(emitted, ConvertOpenAIResponseToClaude(
		context.Background(),
		"",
		[]byte(originalReq),
		nil,
		[]byte("data: [DONE]"),
		&paramAny,
	)...)

	var events []sseEvent
	for _, raw := range emitted {
		s := string(raw)
		if !strings.HasPrefix(s, "event: ") {
			continue
		}
		nl := strings.Index(s, "\n")
		if nl < 0 {
			continue
		}
		typ := strings.TrimPrefix(s[:nl], "event: ")
		rest := s[nl+1:]
		if !strings.HasPrefix(rest, "data: ") {
			continue
		}
		payload := strings.TrimRight(strings.TrimPrefix(rest, "data: "), "\n")
		events = append(events, sseEvent{Type: typ, Payload: payload})
	}
	return events
}

func countByType(events []sseEvent, typ string) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func toolUseStarts(events []sseEvent) []sseEvent {
	var out []sseEvent
	for _, e := range events {
		if e.Type != "content_block_start" {
			continue
		}
		if gjson.Get(e.Payload, "content_block.type").String() == "tool_use" {
			out = append(out, e)
		}
	}
	return out
}

func blockIndices(events []sseEvent) []int64 {
	var idx []int64
	for _, e := range events {
		if e.Type == "content_block_start" {
			idx = append(idx, gjson.Get(e.Payload, "index").Int())
		}
	}
	return idx
}

func lastStopReason(events []sseEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "message_delta" {
			return gjson.Get(events[i].Payload, "delta.stop_reason").String()
		}
	}
	return ""
}

const streamReq = `{"stream":true}`

func TestStreaming_LateUsageOnlyDoesNotEmitAfterMessageStop(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`{"id":"c1","model":"m","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	)

	if got := countByType(events, "message_delta"); got != 1 {
		t.Fatalf("expected exactly one message_delta, got %d (events=%+v)", got, events)
	}
	if got := countByType(events, "message_stop"); got != 1 {
		t.Fatalf("expected exactly one message_stop, got %d (events=%+v)", got, events)
	}
	if len(events) == 0 || events[len(events)-1].Type != "message_stop" {
		t.Fatalf("message_stop must be the last semantic event (events=%+v)", events)
	}
}

func TestConvertOpenAIResponseToClaude_StreamIgnoresNullToolNameDelta(t *testing.T) {
	originalRequest := []byte(streamReq)
	var param any

	firstChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}]}`),
		&param,
	)
	firstOutput := bytes.Join(firstChunks, nil)
	if !bytes.Contains(firstOutput, []byte(`"name":"read_file"`)) {
		t.Fatalf("expected first chunk to start read_file tool block, got %s", string(firstOutput))
	}

	secondChunks := ConvertOpenAIResponseToClaude(
		context.Background(),
		"test-model",
		originalRequest,
		nil,
		[]byte(`data: {"id":"chatcmpl_1","model":"test-model","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":null,"arguments":"{\"path\":\"/tmp/a\"}"}}]},"finish_reason":null}]}`),
		&param,
	)
	secondOutput := bytes.Join(secondChunks, nil)
	if bytes.Contains(secondOutput, []byte(`content_block_start`)) {
		t.Fatalf("did not expect null tool name delta to start a new content block, got %s", string(secondOutput))
	}
	if bytes.Contains(secondOutput, []byte(`"name":""`)) {
		t.Fatalf("did not expect null tool name delta to emit an empty tool name, got %s", string(secondOutput))
	}
}

func TestStreamingTool_EmptyNameThroughout(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"x\":1}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("expected zero tool_use content_block_start, got %d (events=%+v)", got, events)
	}
	if got := countByType(events, "content_block_delta"); got != 0 {
		t.Fatalf("expected zero content_block_delta when start was suppressed, got %d", got)
	}
	if got := countByType(events, "content_block_stop"); got != 0 {
		t.Fatalf("expected zero content_block_stop when start was suppressed, got %d", got)
	}
	if got := lastStopReason(events); got == "tool_use" {
		t.Fatalf("stop_reason must not be tool_use when zero tool_use blocks were emitted; got %q", got)
	}
}

func TestStreamingTool_NullName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":null,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("null name must not produce a tool_use start; got %d", got)
	}
	if got := countByType(events, "content_block_stop"); got != 0 {
		t.Fatalf("null name must not produce content_block_stop; got %d", got)
	}
}

func TestStreamingTool_NonStringName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":123,"arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := len(toolUseStarts(events)); got != 0 {
		t.Fatalf("non-string name must not produce a tool_use start; got %d", got)
	}
}

func TestStreamingTool_RepeatedName(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_it","arguments":"{\"x\""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_it","arguments":":1}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start, got %d", len(starts))
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}
}

func TestStreamingTool_MixedSuppressedAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_skip","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":""}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[
			{"index":1,"function":{"arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start, got %d", len(starts))
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}

	indices := blockIndices(events)
	if len(indices) == 0 || indices[0] != 0 {
		t.Fatalf("first content_block_start index must be 0, got %v", indices)
	}
}

func TestStreamingTool_EmptyIDDeferStart(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"","function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_real","function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start once id arrived, got %d", len(starts))
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_real" {
		t.Fatalf("announced tool id = %q, want %q", id, "call_real")
	}
}

func TestStreamingTool_IDInDeltaWithoutFunction(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_real"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected exactly one tool_use start when id arrives in a function-less delta, got %d", len(starts))
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_real" {
		t.Fatalf("announced tool id = %q, want %q", id, "call_real")
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := countByType(events, "content_block_stop"); got != 1 {
		t.Fatalf("expected exactly one content_block_stop, got %d", got)
	}
}

func TestStreamingTool_StopReasonWithEmittedTool(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","function":{"name":"do_it","arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_StopReasonWhenIDNeverArrives(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it","arguments":""}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one belated tool_use start with synthetic id, got %d", len(starts))
	}
	id := gjson.Get(starts[0].Payload, "content_block.id").String()
	if !strings.HasPrefix(id, "toolu_") {
		t.Fatalf("synthetic id should match toolu_<nanos>_<n>, got %q", id)
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "do_it" {
		t.Fatalf("announced tool name = %q, want %q", name, "do_it")
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestStreamingTool_BelatedStartsUseOpenAIToolIndexOrder(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":2,"function":{"name":"third_tool","arguments":"{}"}},
			{"index":0,"function":{"name":"first_tool","arguments":"{}"}},
			{"index":1,"function":{"name":"second_tool","arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 3 {
		t.Fatalf("expected three belated tool_use starts, got %d", len(starts))
	}

	wantNames := []string{"first_tool", "second_tool", "third_tool"}
	for i, wantName := range wantNames {
		if name := gjson.Get(starts[i].Payload, "content_block.name").String(); name != wantName {
			t.Fatalf("tool_use start %d name = %q, want %q (starts=%+v)", i, name, wantName, starts)
		}
		if blockIndex := gjson.Get(starts[i].Payload, "index").Int(); blockIndex != int64(i) {
			t.Fatalf("tool_use start %d block index = %d, want %d", i, blockIndex, i)
		}
	}
}

func TestStreamingTool_LateIDAfterFinalization(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"function":{"name":"do_it"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_late"}]}}]}`,
	)

	starts := toolUseStarts(events)
	if len(starts) != 1 {
		t.Fatalf("expected one belated tool_use start, got %d", len(starts))
	}

	var sawMessageStop bool
	for _, e := range events {
		if e.Type == "message_stop" {
			sawMessageStop = true
			continue
		}
		if sawMessageStop {
			switch e.Type {
			case "content_block_start", "content_block_delta", "content_block_stop":
				t.Fatalf("event %q emitted after message_stop (events=%+v)", e.Type, events)
			}
		}
	}
}

func TestStreamingTool_StopReasonMixedSuppressedAndValid(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_skip","function":{"name":"","arguments":""}},
			{"index":1,"id":"call_real","function":{"name":"do_it","arguments":"{}"}}
		]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func thinkingDeltaTexts(events []sseEvent) []string {
	var out []string
	for _, e := range events {
		if e.Type != "content_block_delta" {
			continue
		}
		if gjson.Get(e.Payload, "delta.type").String() == "thinking_delta" {
			out = append(out, gjson.Get(e.Payload, "delta.thinking").String())
		}
	}
	return out
}

func TestStreamingReasoning_DuplicateChunksDeduped(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"hello"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"hello"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"hello"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}]}`,
	)
	got := thinkingDeltaTexts(events)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("expected single thinking delta [hello], got %v", got)
	}
}

func TestStreamingReasoning_DistinctChunksAllEmitted(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"A"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"B"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"C"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	got := thinkingDeltaTexts(events)
	if strings.Join(got, "") != "ABC" {
		t.Fatalf("expected [A B C], got %v", got)
	}
}

func TestStreamingReasoning_WhitespaceChunksSkipped(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"  "}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":"real"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	got := thinkingDeltaTexts(events)
	if len(got) != 1 || got[0] != "real" {
		t.Fatalf("expected single thinking delta [real], got %v", got)
	}
}

func TestStreamingReasoning_ArrayFormEachDeduped(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":[{"text":"part1"},{"text":"part2"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"reasoning_content":[{"text":"part1"},{"text":"part2"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	got := thinkingDeltaTexts(events)
	if strings.Join(got, "") != "part1part2" {
		t.Fatalf("expected [part1 part2] once, got %v", got)
	}
}

func TestStreamingReasoning_ReasoningDetailsTextForm(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":[{"type":"reasoning.text","text":"hello"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	got := thinkingDeltaTexts(events)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("expected [hello], got %v", got)
	}
}

func TestStreamingReasoning_ReasoningDetailsSummaryForms(t *testing.T) {
	// summary as string field
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":[{"type":"reasoning.summary","summary":"sum1"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	if got := thinkingDeltaTexts(events); len(got) != 1 || got[0] != "sum1" {
		t.Fatalf("string summary: expected [sum1], got %v", got)
	}
	// summary as array of {text}
	events = runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":[{"type":"reasoning.summary","summary":[{"text":"s1"},{"text":"s2"}]}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	if got := thinkingDeltaTexts(events); strings.Join(got, "") != "s1s2" {
		t.Fatalf("array summary: expected [s1 s2], got %v", got)
	}
}

func TestStreamingReasoning_ReasoningDetailsEncryptedSkipped(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_details":[{"type":"reasoning.encrypted","data":"abc"}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	if got := thinkingDeltaTexts(events); len(got) != 0 {
		t.Fatalf("expected no thinking deltas, got %v", got)
	}
}

func TestStreamingReasoning_FlatAliases(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"viaReasoning"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"thinking":"viaThinking"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
	)
	if got := thinkingDeltaTexts(events); strings.Join(got, "") != "viaReasoningviaThinking" {
		t.Fatalf("expected aliases collected, got %v", got)
	}
}

func nonStreamThinkingTexts(out []byte) []string {
	var got []string
	gjson.GetBytes(out, "content").ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "thinking" {
			got = append(got, block.Get("thinking").String())
		}
		return true
	})
	return got
}

func TestNonStreamReasoning_ReasoningDetails(t *testing.T) {
	raw := `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"done","reasoning_details":[{"type":"reasoning.text","text":"deep thought"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
	out := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "", []byte(`{"stream":false}`), nil, []byte(raw), nil)
	got := nonStreamThinkingTexts(out)
	if len(got) != 1 || got[0] != "deep thought" {
		t.Fatalf("expected thinking [deep thought], got %v. Out: %s", got, string(out))
	}
}

func TestNonStreamReasoning_FlatAlias(t *testing.T) {
	raw := `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"done","reasoning":"flat thought"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
	out := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "", []byte(`{"stream":false}`), nil, []byte(raw), nil)
	got := nonStreamThinkingTexts(out)
	if len(got) != 1 || got[0] != "flat thought" {
		t.Fatalf("expected thinking [flat thought], got %v. Out: %s", got, string(out))
	}
}

func inputJSONDeltaPayloads(events []sseEvent) []string {
	var out []string
	for _, e := range events {
		if e.Type != "content_block_delta" {
			continue
		}
		if gjson.Get(e.Payload, "delta.type").String() == "input_json_delta" {
			out = append(out, gjson.Get(e.Payload, "delta.partial_json").String())
		}
	}
	return out
}

func TestStreamingToolCall_ArgumentsSnapshotReplaces(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":"{\"loc"}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":{"location":"hangzhou"}}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	got := inputJSONDeltaPayloads(events)
	if len(got) != 1 || got[0] != `{"location":"hangzhou"}` {
		t.Fatalf("expected snapshot to replace partial string, got %v", got)
	}
}

func TestStreamingToolCall_RepeatedSnapshotsLastWins(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":{"location":"a"}}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":{"location":"b"}}}]}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	got := inputJSONDeltaPayloads(events)
	if len(got) != 1 || got[0] != `{"location":"b"}` {
		t.Fatalf("expected last snapshot to win, got %v", got)
	}
}

func messageDeltaUsage(events []sseEvent) (input, output int64, found bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == "message_delta" {
			return gjson.Get(events[i].Payload, "usage.input_tokens").Int(),
				gjson.Get(events[i].Payload, "usage.output_tokens").Int(), true
		}
	}
	return 0, 0, false
}

func TestStreamingUsage_BeforeFinish_Preserved(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		`{"id":"c1","model":"m","choices":[],"usage":{"prompt_tokens":42,"completion_tokens":7,"total_tokens":49}}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	input, output, found := messageDeltaUsage(events)
	if !found {
		t.Fatal("expected a message_delta event")
	}
	if input != 42 || output != 7 {
		t.Fatalf("expected usage 42/7, got %d/%d", input, output)
	}
}

func TestStreamingUsage_AfterFinish_EmittedOnce(t *testing.T) {
	events := runStream(t, streamReq,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		`{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c1","model":"m","choices":[],"usage":{"prompt_tokens":42,"completion_tokens":7,"total_tokens":49}}`,
	)
	if n := countByType(events, "message_delta"); n != 1 {
		t.Fatalf("expected 1 message_delta, got %d", n)
	}
	input, output, _ := messageDeltaUsage(events)
	if input != 42 || output != 7 {
		t.Fatalf("expected usage 42/7, got %d/%d", input, output)
	}
}
