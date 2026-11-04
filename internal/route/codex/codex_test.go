package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
)

const thread = "019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a"

type fake struct {
	runs, args, pid string
}

func fakeCodex(t *testing.T, mode string) fake {
	t.Helper()
	bin, err := filepath.Abs("testdata/codex")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(bin, filepath.Join(dir, "codex")); err != nil {
		t.Fatal(err)
	}
	f := fake{filepath.Join(dir, "runs"), filepath.Join(dir, "args"), filepath.Join(dir, "pid")}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_MODE", mode)
	t.Setenv("FAKE_CODEX_RUNS", f.runs)
	t.Setenv("FAKE_CODEX_ARGS", f.args)
	t.Setenv("FAKE_CODEX_PID", f.pid)
	return f
}

func (f fake) runCount(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(f.runs)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run\n")
}

func deliver(r Route, m message.Message) error {
	return r.Deliver(context.Background(), route.Agent{Address: thread}, m)
}

func TestCodexExitMapping(t *testing.T) {
	cases := []struct {
		mode string
		want error
	}{
		{"inactive", route.ErrNotListening},
		{"unknown", route.ErrReceiverUnknown},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			fakeCodex(t, c.mode)
			m := message.New("claude:lead", "codex:"+thread, "", "hi", 1)
			if err := deliver(Route{}, m); !errors.Is(err, c.want) {
				t.Fatalf("Deliver: %v, want %v", err, c.want)
			}
		})
	}

	t.Run("ok", func(t *testing.T) {
		f := fakeCodex(t, "ok")
		m := message.New("claude:lead", "codex:"+thread, "", "line one\nline two", 3)
		if err := deliver(Route{}, m); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		data, err := os.ReadFile(f.args)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		want := []string{"queue", "--thread=" + thread, "--message", m.Wire()}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("codex got args %q, want %q", got, want)
		}
	})

	for _, c := range []struct{ mode, code, first string }{
		{"other", "1", "Error: the daemon is not running"},
		{"usage", "2", "error: the following required arguments were not provided:"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			fakeCodex(t, c.mode)
			err := deliver(Route{}, message.New("claude:lead", "codex:"+thread, "", "hi", 1))
			var exit *exec.ExitError
			if !errors.As(err, &exit) || strconv.Itoa(exit.ExitCode()) != c.code {
				t.Fatalf("Deliver: %v, want an error wrapping exit code %s", err, c.code)
			}
			if !strings.Contains(err.Error(), c.first) {
				t.Fatalf("Deliver: %v, want the first output line %q", err, c.first)
			}
			for _, sentinel := range []error{route.ErrNotListening, route.ErrReceiverUnknown, route.ErrUnknownOutcome} {
				if errors.Is(err, sentinel) {
					t.Fatalf("Deliver: %v, must not match %v", err, sentinel)
				}
			}
		})
	}

	t.Run("dash address", func(t *testing.T) {
		f := fakeCodex(t, "inactive")
		m := message.New("claude:lead", "codex:--help", "", "hi", 1)
		err := Route{}.Deliver(context.Background(), route.Agent{Address: "--help"}, m)
		if !errors.Is(err, route.ErrNotListening) {
			t.Fatalf("Deliver to --help: %v, want ErrNotListening", err)
		}
		data, err := os.ReadFile(f.args)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Split(string(data), "\x00")[1]; got != "--thread=--help" {
			t.Fatalf("codex got thread argument %q, want \"--thread=--help\"", got)
		}
	})
}

func TestQueueNoRetry(t *testing.T) {
	f := fakeCodex(t, "hang")
	start := time.Now()
	err := deliver(Route{Timeout: 300 * time.Millisecond}, message.New("claude:lead", "codex:"+thread, "", "hi", 1))
	if !errors.Is(err, route.ErrUnknownOutcome) {
		t.Fatalf("Deliver: %v, want ErrUnknownOutcome", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Deliver took %v with a 300ms timeout", elapsed)
	}
	if n := f.runCount(t); n != 1 {
		t.Fatalf("codex ran %d times, want exactly 1", n)
	}
	data, err := os.ReadFile(f.pid)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(2 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("child %d of the fake codex still running after the timeout", pid)
		}
	}
}

func TestCodexAddressIsAThreadUUID(t *testing.T) {
	for _, address := range []string{thread, "11111111-2222-4333-8444-555555555555", "019A3C1E-6F2B-7D40-9E8A-3B5C2D1E0F4A"} {
		if err := (Route{}).CheckAddress(address); err != nil {
			t.Errorf("CheckAddress(%q) = %v, want nil", address, err)
		}
	}
	for _, address := range []string{"", "w1", "6f1c", "x/y", "not-a-uuid", "--help", thread + "0", thread[1:], "019a3c1e6f2b-7d40-9e8a-3b5c2d1e0f4a0", "019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4g", " " + thread} {
		if err := (Route{}).CheckAddress(address); err == nil {
			t.Errorf("CheckAddress(%q) = nil, want an error", address)
		}
	}
}

func TestCodexSessionAddressIsTheThreadID(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", thread)
	if got := (Route{}).SessionAddress(); got != thread {
		t.Fatalf("SessionAddress() = %q, want %q", got, thread)
	}
	t.Setenv("CODEX_THREAD_ID", "")
	if got := (Route{}).SessionAddress(); got != "" {
		t.Fatalf("SessionAddress() with no CODEX_THREAD_ID = %q, want none", got)
	}
}

func TestCodexHasNoDefaultAddress(t *testing.T) {
	if got := (Route{}).DefaultAddress("w1"); got != "" {
		t.Fatalf("DefaultAddress(w1) = %q, want none", got)
	}
}

func TestCodexIsNeverAbsent(t *testing.T) {
	if p := (Route{}).Presence(thread); p != route.Unheld {
		t.Fatalf("Presence(thread) = %v, want Unheld; a Codex thread has no listener, so its messages are never held or sent twice", p)
	}
}
