package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/state"
)

func TestMain(m *testing.M) {
	if os.Getenv("CREWTALK_TEST_RUN_MAIN") == "1" {
		main()
	}
	os.Exit(m.Run())
}

func crewtalkProcess(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "CREWTALK_TEST_RUN_MAIN=1")
	return cmd
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	if exit == nil {
		return 0
	}
	return exit.ExitCode()
}

func waitForListener(t *testing.T, home, name string) {
	t.Helper()
	sock := filepath.Join(home, "inbox", name+".sock")
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if info, err := os.Lstat(sock); err == nil && info.Mode().Perm() == 0o600 {
			return
		}
	}
	t.Fatalf("no listening socket at %s", sock)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Lstat(path); err == nil {
			return
		}
	}
	t.Fatalf("no file at %s", path)
}

func TestWaitClosedStdoutExits5(t *testing.T) {
	setup(t)
	home := socketHome(t)

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	read.Close()
	wait := crewtalkProcess("wait", "--as", "lead", "--timeout", "10s")
	wait.Stdout = write
	if err := wait.Start(); err != nil {
		t.Fatal(err)
	}
	write.Close()
	waitForListener(t, home, "lead")

	var stdout, stderr bytes.Buffer
	exit := run([]string{"send", "--from", "codex:" + codexThread, "--to", "claude:lead", "important reply"}, strings.NewReader(""), &stdout, &stderr, routeTable())
	waitExit := exitCode(t, wait.Wait())

	if exit != exitFailed {
		t.Fatalf("send exit %d, want %d (outcome unknown); stderr %q", exit, exitFailed, stderr.String())
	}
	lines := journalLines(t, home)
	if len(lines) != 1 || lines[0]["result"] != "outcome-unknown" {
		t.Fatalf("journal %v, want one outcome-unknown line", lines)
	}
	if waitExit != exitFailed {
		t.Fatalf("wait exit %d with a closed stdout, want %d", waitExit, exitFailed)
	}
}

func TestLogOneLineWhenSendIsSignalled(t *testing.T) {
	fake, err := filepath.Abs("../../internal/route/codex/testdata/codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			setup(t)
			home := socketHome(t)
			dir := t.TempDir()
			if err := os.Symlink(fake, filepath.Join(dir, "codex")); err != nil {
				t.Fatal(err)
			}
			pidFile := filepath.Join(dir, "pid")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_MODE", "hang")
			t.Setenv("FAKE_CODEX_RUNS", filepath.Join(dir, "runs"))
			t.Setenv("FAKE_CODEX_ARGS", filepath.Join(dir, "args"))
			t.Setenv("FAKE_CODEX_PID", pidFile)

			send := crewtalkProcess("send", "--from", "claude:lead", "--to", "codex:"+codexThread, "hi")
			if err := send.Start(); err != nil {
				t.Fatal(err)
			}
			child := 0
			for end := time.Now().Add(5 * time.Second); child == 0; time.Sleep(10 * time.Millisecond) {
				if time.Now().After(end) {
					send.Process.Kill()
					t.Fatal("the fake codex did not start within 5s")
				}
				data, _ := os.ReadFile(pidFile)
				child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			}
			if err := send.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			exit := exitCode(t, send.Wait())

			if exit != exitFailed {
				t.Fatalf("send exit %d after %v, want %d (outcome unknown)", exit, sig, exitFailed)
			}
			lines := journalLines(t, home)
			if len(lines) != 1 || lines[0]["result"] != "outcome-unknown" {
				t.Fatalf("journal %v, want one outcome-unknown line", lines)
			}
			for end := time.Now().Add(2 * time.Second); syscall.Kill(child, 0) == nil; time.Sleep(10 * time.Millisecond) {
				if time.Now().After(end) {
					syscall.Kill(child, syscall.SIGKILL)
					t.Fatalf("child %d of the fake codex still running after %v", child, sig)
				}
			}
		})
	}
}

func TestCutOffSendIsNeverPrinted(t *testing.T) {
	home := socketHome(t)
	var printed bytes.Buffer
	wait := crewtalkProcess("wait", "--as", "lead", "--timeout", "30s")
	wait.Stdout = &printed
	if err := wait.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		wait.Process.Signal(syscall.SIGCONT)
		wait.Process.Kill()
	})
	waitForListener(t, home, "lead")
	if err := wait.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}

	send := crewtalkProcess("send", "--from", "codex:"+codexThread, "--to", "claude:lead", "-")
	send.Stdin = strings.NewReader(strings.Repeat("z", 200_000))
	if err := send.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, filepath.Join(home, "inbox", "lead.send.lock"))
	time.Sleep(200 * time.Millisecond)
	if err := send.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if exit := exitCode(t, send.Wait()); exit != exitFailed {
		t.Fatalf("cut-off send exit %d, want %d (outcome unknown)", exit, exitFailed)
	}
	lines := journalLines(t, home)
	if len(lines) != 1 || lines[0]["result"] != "outcome-unknown" {
		t.Fatalf("journal %v, want one outcome-unknown line", lines)
	}
	cutID := lines[0]["id"].(string)

	if err := wait.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	exit := exitReceiver
	var stderr bytes.Buffer
	for end := time.Now().Add(5 * time.Second); exit == exitReceiver && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		stderr.Reset()
		exit = run([]string{"send", "--from", "codex:" + codexThread, "--to", "claude:lead", "after the cut"}, strings.NewReader(""), io.Discard, &stderr, routeTable())
	}
	if exit != exitOK {
		t.Fatalf("send after the cut exit %d, want 0; stderr %q", exit, stderr.String())
	}
	if exit := exitCode(t, wait.Wait()); exit != exitOK {
		t.Fatalf("wait exit %d, want 0", exit)
	}
	got := printed.String()
	if strings.Contains(got, cutID) || strings.Count(got, "[crewtalk from") != 1 || !strings.Contains(got, "after the cut") {
		t.Fatalf("wait printed %d bytes %.200q, want only the message sent after the cut", len(got), got)
	}
}

func TestSignalDuringSendLockWaitEndsTheSend(t *testing.T) {
	setup(t)
	home := socketHome(t)
	inbox := filepath.Join(home, "inbox")
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", filepath.Join(inbox, "lead.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	unlock, err := state.Lock(context.Background(), filepath.Join(inbox, "lead.send.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	send := crewtalkProcess("send", "--from", "codex:"+codexThread, "--to", "claude:lead", "behind a stuck sender")
	if err := send.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, filepath.Join(home, "log.jsonl"))
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := send.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	exit := exitCode(t, send.Wait())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("send ended %v after SIGINT, want at once", elapsed)
	}
	if exit != exitFailed {
		t.Fatalf("send exit %d, want %d (outcome unknown)", exit, exitFailed)
	}
	lines := journalLines(t, home)
	if len(lines) != 1 || lines[0]["result"] != "outcome-unknown" {
		t.Fatalf("journal %v, want one outcome-unknown line", lines)
	}
}
