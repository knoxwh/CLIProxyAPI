package tklite

import (
	"testing"
)

func resetDefaultClientForTest() {
	defaultClientMu.Lock()
	defer defaultClientMu.Unlock()
	defaultClient = nil
	defaultClientSocket = ""
}

func TestGetClientSwitchesWhenSocketPathChanges(t *testing.T) {
	resetDefaultClientForTest()

	first := getClient("/tmp/tklite-a.sock")
	second := getClient("/tmp/tklite-b.sock")

	if first == nil || second == nil {
		t.Fatal("clients must not be nil")
	}
	if first == second {
		t.Fatal("different socket paths must use different clients")
	}
	if second.socketPath != "/tmp/tklite-b.sock" {
		t.Fatalf("second socket path = %q, want /tmp/tklite-b.sock", second.socketPath)
	}
}

func TestGetClientReusesSameClientForSamePath(t *testing.T) {
	resetDefaultClientForTest()

	first := getClient("/tmp/tklite-same.sock")
	second := getClient("/tmp/tklite-same.sock")

	if first != second {
		t.Fatal("same socket path must reuse the same client")
	}
}
