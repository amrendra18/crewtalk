package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
	"github.com/amrendra18/crewtalk/internal/route/claude"
)

func socketHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "ctw-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("CREWTALK_HOME", home)
	return home
}

func waitFor(t *testing.T, m message.Message, flags ...string) result {
	t.Helper()
	done := make(chan result)
	args := append([]string{"wait", "--as", "lead"}, flags...)
	go func() { done <- crewtalk(&fakeRoute{}, "", args...) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := claude.Route{}.Deliver(context.Background(), route.Agent{Address: "lead"}, m)
		if err == nil {
			break
		}
		if !errors.Is(err, route.ErrNotListening) || time.Now().After(deadline) {
			t.Fatalf("Deliver: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return <-done
}

func TestWaitPrintsWireText(t *testing.T) {
	socketHome(t)
	m := message.New("codex:"+codexThread, "claude:lead", "proj-a", "the marker", 2)
	got := waitFor(t, m)
	if got.exit != 0 || got.stdout != m.Wire()+"\n" {
		t.Fatalf("exit %d, stdout %q; want 0 and %q", got.exit, got.stdout, m.Wire()+"\n")
	}
}

func TestWaitJSONPrintsMessage(t *testing.T) {
	socketHome(t)
	m := message.New("codex:"+codexThread, "claude:lead", "proj-a", "the marker", 2)
	got := waitFor(t, m, "--json", "--timeout", "10s")
	var printed message.Message
	if err := json.Unmarshal([]byte(got.stdout), &printed); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", got.stdout, err)
	}
	if got.exit != 0 || printed != m {
		t.Fatalf("exit %d, printed %+v; want 0 and %+v", got.exit, printed, m)
	}
}

func TestWaitTimeoutPrintsNothing(t *testing.T) {
	socketHome(t)
	got := crewtalk(&fakeRoute{}, "", "wait", "--as", "lead", "--timeout", "100ms")
	if got.exit != 4 || got.stdout != "" || got.stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 4 and no output", got.exit, got.stdout, got.stderr)
	}
}

func TestWaitSecondListenerFails(t *testing.T) {
	socketHome(t)
	first := make(chan result)
	go func() { first <- crewtalk(&fakeRoute{}, "", "wait", "--as", "lead", "--timeout", "1s") }()
	time.Sleep(200 * time.Millisecond)
	got := crewtalk(&fakeRoute{}, "", "wait", "--as", "lead", "--timeout", "1s")
	if got.exit != 5 || got.stderr == "" {
		t.Fatalf("exit %d, stderr %q; want 5 and the reason", got.exit, got.stderr)
	}
	<-first
}
