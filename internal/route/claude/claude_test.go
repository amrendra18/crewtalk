package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
)

type received struct {
	m   message.Message
	err error
}

// shortHome keeps socket paths under the 104-byte macOS limit; t.TempDir is too deep.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "ct-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("CREWTALK_HOME", home)
	return home
}

func sockPath(home, name string) string {
	return filepath.Join(home, "inbox", name+".sock")
}

func listenOnce(name string, timeout time.Duration, onReady func()) (message.Message, error) {
	var got message.Message
	err := listen(name, timeout, onReady, func(m message.Message) error {
		got = m
		return nil
	})
	return got, err
}

func shortExchange(t *testing.T, wait time.Duration) {
	t.Helper()
	old := exchangeWait
	exchangeWait = wait
	t.Cleanup(func() { exchangeWait = old })
}

func startListen(t *testing.T, name string, timeout time.Duration) (<-chan received, <-chan struct{}) {
	t.Helper()
	out := make(chan received, 1)
	ready := make(chan struct{}, 1)
	go func() {
		m, err := listenOnce(name, timeout, func() {
			// A listener reopens after each bad frame; a full channel must not keep it from its deadline.
			select {
			case ready <- struct{}{}:
			default:
			}
		})
		out <- received{m, err}
	}()
	awaitReady(t, ready)
	return out, ready
}

func awaitReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not report a listening socket within 2s")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Lstat(path); err == nil {
			return
		}
	}
	t.Fatalf("no file at %s", path)
}

func agent(name string) route.Agent {
	return route.Agent{Address: name}
}

func sameMessage(t *testing.T, got, want message.Message) {
	t.Helper()
	if got.Wire() != want.Wire() || !got.Time.Equal(want.Time) {
		t.Fatalf("received %q at %v, want %q at %v", got.Wire(), got.Time, want.Wire(), want.Time)
	}
}

func TestClaudeRoundTrip(t *testing.T) {
	home := shortHome(t)
	out, _ := startListen(t, "lead", 5*time.Second)
	info, err := os.Stat(sockPath(home, "lead"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Join(home, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("inbox mode %v, want 0700", dir.Mode().Perm())
	}

	m := message.New("codex:worker-a", "claude:lead", "", "the tests pass\nsecond line", 2)
	if err := (Route{}).Deliver(context.Background(), agent("lead"), m); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	r := <-out
	if r.err != nil {
		t.Fatalf("Listen: %v", r.err)
	}
	sameMessage(t, r.m, m)
	if _, err := os.Lstat(sockPath(home, "lead")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present after Listen returned: %v", err)
	}
}

func TestClaudeNotListening(t *testing.T) {
	shortHome(t)
	m := message.New("codex:a", "claude:nobody", "", "hello", 1)
	err := (Route{}).Deliver(context.Background(), agent("nobody"), m)
	if !errors.Is(err, route.ErrNotListening) {
		t.Fatalf("Deliver with no inbox: %v, want ErrNotListening", err)
	}
}

func TestClaudeNotListeningLeavesNoLockFile(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := message.New("codex:a", "claude:nobody", "", "hello", 1)
	if err := (Route{}).Deliver(context.Background(), agent("nobody"), m); !errors.Is(err, route.ErrNotListening) {
		t.Fatalf("Deliver to a name with no socket: %v, want ErrNotListening", err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("inbox holds %v after a send to a name with no socket, want nothing", entries)
	}
}

func TestClaudeListenerClosedAfterReplyIsDelivered(t *testing.T) {
	shortHome(t)
	for i := range 50 {
		out, _ := startListen(t, "lead", 5*time.Second)
		m := message.New("codex:a", "claude:lead", "", fmt.Sprint("message ", i), 1)
		if err := (Route{}).Deliver(context.Background(), agent("lead"), m); err != nil {
			t.Fatalf("round %d: Deliver to a listener that replied and closed: %v", i, err)
		}
		if r := <-out; r.err != nil {
			t.Fatalf("round %d: Listen: %v", i, r.err)
		}
	}
}

func TestClaudeSocketPathOverTheLimit(t *testing.T) {
	home := shortHome(t)
	t.Setenv("CREWTALK_HOME", filepath.Join(home, strings.Repeat("d", 100)))
	_, err := listenOnce("lead", time.Second, func() { t.Error("Listen reported a socket over the path limit as ready") })
	if err == nil || !strings.Contains(err.Error(), "104") || !strings.Contains(err.Error(), "CREWTALK_HOME") {
		t.Fatalf("Listen with a long CREWTALK_HOME: %v, want an error naming the 104-byte limit and CREWTALK_HOME", err)
	}
}

func TestClaudeNameCheck(t *testing.T) {
	shortHome(t)
	for _, name := range []string{"", "a b", "a,b", "a;b", "a[b]", "a/b", "a:b", "a\x00b"} {
		if err := (Route{}).CheckAddress(name); err == nil {
			t.Errorf("CheckAddress(%q) = nil, want an error", name)
		}
		m := message.New("codex:a", "claude:x", "", "hello", 1)
		if err := (Route{}).Deliver(context.Background(), agent(name), m); !errors.Is(err, route.ErrReceiverUnknown) {
			t.Errorf("Deliver to %q: %v, want ErrReceiverUnknown", name, err)
		}
	}
	for _, name := range []string{"lead", "verify-001", "w1.a_b"} {
		if err := (Route{}).CheckAddress(name); err != nil {
			t.Errorf("CheckAddress(%q) = %v, want nil", name, err)
		}
	}
}

func TestClaudeNameOverTheSocketLimit(t *testing.T) {
	home := shortHome(t)
	room := maxSocketPath - len(sockPath(home, ""))
	if err := (Route{}).CheckAddress(strings.Repeat("n", room-1)); err != nil {
		t.Fatalf("CheckAddress of a name whose socket path is 103 bytes: %v, want nil", err)
	}
	err := (Route{}).CheckAddress(strings.Repeat("n", room))
	if err == nil || !strings.Contains(err.Error(), "104") || !strings.Contains(err.Error(), "CREWTALK_HOME") {
		t.Fatalf("CheckAddress of a name whose socket path is 104 bytes: %v, want an error naming the 104-byte limit and CREWTALK_HOME", err)
	}
}

func TestClaudeGivesNoSessionAddress(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "6f1c")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "a2b9")
	if got := (Route{}).SessionAddress(); got != "" {
		t.Fatalf("SessionAddress() = %q, want none", got)
	}
}

func TestClaudeDefaultAddressIsTheName(t *testing.T) {
	if got := (Route{}).DefaultAddress("lead"); got != "lead" {
		t.Fatalf("DefaultAddress(lead) = %q, want lead", got)
	}
}

func TestClaudeLargeMessage(t *testing.T) {
	shortHome(t)
	out, _ := startListen(t, "big", 10*time.Second)
	text := strings.Repeat("0123456789abcde\n", 1<<16)
	if len(text) != 1<<20 {
		t.Fatalf("text is %d bytes, want 1 MB", len(text))
	}
	m := message.New("codex:a", "claude:big", "", text, 1)
	if err := (Route{}).Deliver(context.Background(), agent("big"), m); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	r := <-out
	if r.err != nil {
		t.Fatalf("Listen: %v", r.err)
	}
	if r.m.Text != text {
		t.Fatalf("received %d bytes of text, want %d", len(r.m.Text), len(text))
	}
	sameMessage(t, r.m, m)
}

func TestClaudeTwoSenders(t *testing.T) {
	for i := range 10 {
		shortHome(t)
		first, _ := startListen(t, "lead", 5*time.Second)
		sent := []message.Message{
			message.New("codex:a", "claude:lead", "", "from a", 1),
			message.New("codex:b", "claude:lead", "", "from b", 1),
		}
		errs := make([]error, len(sent))
		var wg sync.WaitGroup
		for j, m := range sent {
			wg.Go(func() { errs[j] = (Route{}).Deliver(context.Background(), agent("lead"), m) })
		}

		r1 := <-first
		second := make(chan received, 1)
		go func() {
			m, err := listenOnce("lead", 200*time.Millisecond, func() {})
			second <- received{m, err}
		}()
		wg.Wait()
		r2 := <-second

		got := map[string]bool{}
		for _, r := range []received{r1, r2} {
			if r.err == nil {
				got[r.m.ID] = true
			} else if !errors.Is(r.err, os.ErrDeadlineExceeded) {
				t.Fatalf("round %d: Listen: %v", i, r.err)
			}
		}
		for j, m := range sent {
			switch {
			case errs[j] == nil && !got[m.ID]:
				t.Fatalf("round %d: sender %d told delivered, but no Listen received it", i, j)
			case errors.Is(errs[j], route.ErrNotListening) && got[m.ID]:
				t.Fatalf("round %d: sender %d told not listening, but it was received", i, j)
			case errs[j] != nil && !errors.Is(errs[j], route.ErrNotListening):
				t.Fatalf("round %d: sender %d: %v, want delivered or ErrNotListening", i, j, errs[j])
			}
		}
		if len(got) == 0 {
			t.Fatalf("round %d: neither sender was delivered", i)
		}
	}
}

func TestClaudeStaleSocket(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath(home, "lead"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	waitForFile(t, sockPath(home, "lead"))

	m := message.New("codex:a", "claude:lead", "", "after a crash", 1)
	if err := (Route{}).Deliver(context.Background(), agent("lead"), m); !errors.Is(err, route.ErrNotListening) {
		t.Fatalf("Deliver to a stale socket: %v, want ErrNotListening", err)
	}

	out, _ := startListen(t, "lead", 5*time.Second)
	if err := (Route{}).Deliver(context.Background(), agent("lead"), m); err != nil {
		t.Fatalf("Deliver after Listen cleared the stale socket: %v", err)
	}
	r := <-out
	if r.err != nil {
		t.Fatalf("Listen: %v", r.err)
	}
	sameMessage(t, r.m, m)
}

func TestClaudeLiveSocketKept(t *testing.T) {
	shortHome(t)
	out, _ := startListen(t, "lead", 5*time.Second)
	if _, err := listenOnce("lead", time.Second, func() {}); err == nil {
		t.Fatal("a second Listen on a live name returned no error")
	}
	m := message.New("codex:a", "claude:lead", "", "still there", 1)
	if err := (Route{}).Deliver(context.Background(), agent("lead"), m); err != nil {
		t.Fatalf("Deliver to the first listener: %v", err)
	}
	r := <-out
	if r.err != nil {
		t.Fatalf("Listen: %v", r.err)
	}
	sameMessage(t, r.m, m)
}

func TestClaudeBadFrameKeepsWaiting(t *testing.T) {
	cut := message.New("codex:a", "claude:lead", "", strings.Repeat("y", 1000), 1).Wire()
	for _, tc := range []struct{ name, sent string }{
		{"no length line", "no header here"},
		{"bad length", "len ten\n" + cut},
		{"negative length", "len -1\n" + cut},
		{"over 8 MiB", fmt.Sprintf("len %d\n", 8<<20+1)},
		{"fewer bytes", fmt.Sprintf("len %d\n%s", len(cut), cut[:len(cut)/2])},
		{"malformed message", "len 14\nno header here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := shortHome(t)
			out, ready := startListen(t, "lead", 5*time.Second)

			c, err := net.Dial("unix", sockPath(home, "lead"))
			if err != nil {
				t.Fatal(err)
			}
			conn := c.(*net.UnixConn)
			io.WriteString(conn, tc.sent)
			conn.CloseWrite()
			reply, err := io.ReadAll(conn)
			conn.Close()
			if err != nil || string(reply) != "bad\n" {
				t.Fatalf("reply %q (%v), want \"bad\\n\"", reply, err)
			}

			awaitReady(t, ready)
			m := message.New("codex:a", "claude:lead", "", "well formed", 1)
			if err := (Route{}).Deliver(context.Background(), agent("lead"), m); err != nil {
				t.Fatalf("Deliver after a bad frame: %v", err)
			}
			r := <-out
			if r.err != nil {
				t.Fatalf("Listen: %v", r.err)
			}
			sameMessage(t, r.m, m)
		})
	}
}

func TestClaudeListenTimeout(t *testing.T) {
	home := shortHome(t)
	start := time.Now()
	_, err := listenOnce("lead", 200*time.Millisecond, func() {})
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Listen with no sender: %v, want os.ErrDeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Listen took %v to time out after 200ms", elapsed)
	}
	if _, err := os.Lstat(sockPath(home, "lead")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left behind after a timeout: %v", err)
	}
}

func TestClaudeListenNoTimeout(t *testing.T) {
	shortHome(t)
	out, _ := startListen(t, "lead", NoTimeout)
	time.Sleep(50 * time.Millisecond)
	m := message.New("codex:a", "claude:lead", "", "no deadline", 1)
	if err := (Route{}).Deliver(context.Background(), agent("lead"), m); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	r := <-out
	if r.err != nil {
		t.Fatalf("Listen with NoTimeout: %v", r.err)
	}
	sameMessage(t, r.m, m)
}

func TestClaudeHandleFailureIsNotOK(t *testing.T) {
	shortHome(t)
	printFailed := errors.New("stdout closed")
	out := make(chan error, 1)
	ready := make(chan struct{}, 1)
	go func() {
		out <- listen("lead", 5*time.Second, func() { ready <- struct{}{} }, func(message.Message) error { return printFailed })
	}()
	awaitReady(t, ready)
	m := message.New("codex:a", "claude:lead", "", "lost on the way out", 1)
	if err := (Route{}).Deliver(context.Background(), agent("lead"), m); !errors.Is(err, route.ErrUnknownOutcome) {
		t.Fatalf("Deliver to a listener that could not print: %v, want ErrUnknownOutcome", err)
	}
	if err := <-out; !errors.Is(err, printFailed) {
		t.Fatalf("Listen: %v, want the print error", err)
	}
}

func TestClaudeUnresponsivePeerTimesOut(t *testing.T) {
	for _, peer := range []string{"stopped", "silent"} {
		t.Run(peer, func(t *testing.T) {
			home := shortHome(t)
			shortExchange(t, 300*time.Millisecond)
			if err := os.MkdirAll(filepath.Join(home, "inbox"), 0o700); err != nil {
				t.Fatal(err)
			}
			l, err := net.Listen("unix", sockPath(home, "lead"))
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			if peer == "silent" {
				done := make(chan struct{})
				t.Cleanup(func() { close(done) })
				go func() {
					c, err := l.Accept()
					if err == nil {
						defer c.Close()
						<-done
					}
				}()
			}
			m := message.New("codex:a", "claude:lead", "", strings.Repeat("x", 1<<20), 1)
			start := time.Now()
			err = (Route{}).Deliver(context.Background(), agent("lead"), m)
			if !errors.Is(err, route.ErrUnknownOutcome) {
				t.Fatalf("Deliver to a %s peer: %v, want ErrUnknownOutcome", peer, err)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("Deliver to a %s peer took %v with a 300ms exchange limit", peer, elapsed)
			}
		})
	}
}

func TestClaudeSendLockWaitIsBounded(t *testing.T) {
	home := shortHome(t)
	shortExchange(t, 100*time.Millisecond)
	if err := os.MkdirAll(filepath.Join(home, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sockPath(home, "lead"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	holder, err := os.OpenFile(filepath.Join(home, "inbox", "lead.send.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	m := message.New("codex:b", "claude:lead", "", "behind a stuck sender", 1)
	start := time.Now()
	err = (Route{}).Deliver(context.Background(), agent("lead"), m)
	if !errors.Is(err, route.ErrNotListening) {
		t.Fatalf("Deliver while another sender holds the lock: %v, want ErrNotListening", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Deliver waited %v for the send lock with a 100ms exchange limit", elapsed)
	}
}

func TestClaudeStalledConnectionIsDropped(t *testing.T) {
	home := shortHome(t)
	shortExchange(t, 200*time.Millisecond)
	out, ready := startListen(t, "lead", NoTimeout)
	c, err := net.Dial("unix", sockPath(home, "lead"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "[crewtalk from codex:a")

	awaitReady(t, ready)
	m := message.New("codex:b", "claude:lead", "", "after a stalled sender", 1)
	if err := (Route{}).Deliver(context.Background(), agent("lead"), m); err != nil {
		t.Fatalf("Deliver after a stalled connection: %v", err)
	}
	r := <-out
	if r.err != nil {
		t.Fatalf("Listen: %v", r.err)
	}
	sameMessage(t, r.m, m)
}

func TestClaudeOversizeMessageIsRefusedBeforeSending(t *testing.T) {
	shortHome(t)
	out, _ := startListen(t, "lead", 5*time.Second)
	huge := message.New("codex:a", "claude:lead", "", strings.Repeat("h", 8<<20), 1)
	err := (Route{}).Deliver(context.Background(), agent("lead"), huge)
	if err == nil || errors.Is(err, route.ErrNotListening) || errors.Is(err, route.ErrUnknownOutcome) {
		t.Fatalf("Deliver of a message over 8 MiB: %v, want a plain failure", err)
	}
	m := message.New("codex:a", "claude:lead", "", "small", 1)
	if err := (Route{}).Deliver(context.Background(), agent("lead"), m); err != nil {
		t.Fatalf("Deliver after the oversize refusal: %v", err)
	}
	r := <-out
	if r.err != nil {
		t.Fatalf("Listen: %v", r.err)
	}
	sameMessage(t, r.m, m)
}

func TestClaudeAbsentWithNoListenerMakesNoFile(t *testing.T) {
	home := shortHome(t)
	if p := (Route{}).Presence("lead"); p != route.Absent {
		t.Fatalf("Presence with no inbox = %v, want Absent", p)
	}
	if err := os.MkdirAll(filepath.Join(home, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	if p := (Route{}).Presence("lead"); p != route.Absent {
		t.Fatalf("Presence with no socket = %v, want Absent", p)
	}
	if entries, err := os.ReadDir(filepath.Join(home, "inbox")); err != nil || len(entries) != 0 {
		t.Fatalf("inbox holds %v, %v after Presence, want nothing", entries, err)
	}
}

func TestClaudePresentWhileListening(t *testing.T) {
	shortHome(t)
	out, _ := startListen(t, "lead", 5*time.Second)
	if p := (Route{}).Presence("lead"); p != route.Present {
		t.Fatalf("Presence while a listener waits = %v, want Present", p)
	}
	if err := (Route{}).Deliver(context.Background(), agent("lead"), message.New("codex:a", "claude:lead", "", "still listening", 1)); err != nil {
		t.Fatalf("Deliver after Presence: %v", err)
	}
	if r := <-out; r.err != nil {
		t.Fatalf("Listen: %v", r.err)
	}
	if p := (Route{}).Presence("lead"); p != route.Absent {
		t.Fatalf("Presence after the listener ended = %v, want Absent", p)
	}
}

func TestClaudeAbsentWithAStaleSocket(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath(home, "lead"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	waitForFile(t, sockPath(home, "lead"))
	if err := os.WriteFile(filepath.Join(home, "inbox", "lead.wait.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if p := (Route{}).Presence("lead"); p != route.Absent {
		t.Fatalf("Presence with a socket and a lock file that no listener holds = %v, want Absent", p)
	}
}

func TestClaudeClaimHoldsTheNameUntilClose(t *testing.T) {
	shortHome(t)
	l, err := Claim("lead")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Claim("lead"); err == nil {
		t.Fatal("a second Claim of a claimed name returned no error")
	}
	l.Close()
	again, err := Claim("lead")
	if err != nil {
		t.Fatalf("Claim after Close: %v", err)
	}
	again.Close()
}

func TestClaudeClaimWaitsOutAPresenceProbe(t *testing.T) {
	home := shortHome(t)
	if err := os.MkdirAll(filepath.Join(home, "inbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	probe, err := os.OpenFile(filepath.Join(home, "inbox", "lead.wait.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(50*time.Millisecond, func() { probe.Close() })
	l, err := Claim("lead")
	if err != nil {
		t.Fatalf("Claim while a sender briefly probed the name: %v", err)
	}
	l.Close()
}

func listen(name string, timeout time.Duration, onReady func(), handle func(message.Message) error) error {
	l, err := Claim(name)
	if err != nil {
		return err
	}
	defer l.Close()
	return l.Receive(timeout, func() error { return nil }, onReady, handle)
}
