//go:build !crewtalk_test

package codex

import (
	"testing"

	"github.com/amrendra18/crewtalk/internal/message"
)

func TestNormalBuildIgnoresCodexBin(t *testing.T) {
	f := fakeCodex(t, "ok")
	t.Setenv("CREWTALK_CODEX_BIN", "/usr/bin/false")
	if err := deliver(Route{}, message.New("claude:lead", "codex:"+thread, "", "hi", 1)); err != nil {
		t.Fatalf("Deliver: %v, want codex from PATH to run", err)
	}
	if n := f.runCount(t); n != 1 {
		t.Fatalf("codex from PATH ran %d times, want 1", n)
	}
}
