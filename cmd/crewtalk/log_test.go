package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/journal"
)

func writeJournal(t *testing.T, home string, entries ...journal.Entry) {
	t.Helper()
	j, err := journal.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, e := range entries {
		if err := j.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

var logEntries = []journal.Entry{
	{Time: time.Date(2026, 11, 3, 6, 5, 17, 16757000, time.UTC), ID: "20261103T060517.016757Z-b45afa51", From: "claude:lead", To: "codex:" + codexThread, Thread: "proj-a", Turn: 1, Route: "codex", Result: journal.Delivered},
	{Time: time.Date(2026, 11, 3, 6, 5, 40, 0, time.UTC), ID: "20261103T060540.000000Z-0c1d2e3f", From: "codex:" + codexThread, To: "claude:lead", Thread: "proj-a", Turn: 2, ReplyTo: "20261103T060517.016757Z-b45afa51", Route: "claude", Result: journal.NotListening},
	{Time: time.Date(2026, 11, 3, 6, 6, 0, 0, time.UTC), ID: "20261103T060600.000000Z-99aa88bb", From: "claude:lead", To: "w9", Thread: "proj-b", Turn: 1, Result: journal.ReceiverUnknown},
	{Time: time.Date(2026, 11, 3, 6, 18, 14, 250000000, time.UTC), ID: "20261103T060540.000000Z-0c1d2e3f", From: "codex:" + codexThread, To: "claude:lead", Thread: "proj-a", Turn: 2, ReplyTo: "20261103T060517.016757Z-b45afa51", Route: "claude", Result: journal.Delivered, HeldFor: 754.25},
}

const logText = `2026-11-03T06:05:17Z claude:lead to codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a, thread proj-a, turn 1, id 20261103T060517.016757Z-b45afa51, delivered
2026-11-03T06:05:40Z codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a to claude:lead, thread proj-a, turn 2, reply-to 20261103T060517.016757Z-b45afa51, id 20261103T060540.000000Z-0c1d2e3f, not-listening
2026-11-03T06:06:00Z claude:lead to w9, thread proj-b, turn 1, id 20261103T060600.000000Z-99aa88bb, receiver-unknown
2026-11-03T06:18:14Z codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a to claude:lead, thread proj-a, turn 2, reply-to 20261103T060517.016757Z-b45afa51, id 20261103T060540.000000Z-0c1d2e3f, delivered, held for 754.25 s
`

func writeBrokenLine(t *testing.T, home string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(home, "log.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(`{"id":"broken` + "\n"); err != nil {
		t.Fatal(err)
	}
}

func oneWarningForLine2(t *testing.T, stderr string) {
	t.Helper()
	if strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, "line 2") {
		t.Fatalf("stderr %q, want one warning naming line 2", stderr)
	}
}

func TestLogPrintsOneLinePerMessage(t *testing.T) {
	setup(t)
	home := socketHome(t)
	writeJournal(t, home, logEntries...)
	got := runWith(routeTable(), "", "log")
	if got.exit != 0 || got.stdout != logText {
		t.Fatalf("log: exit %d, stdout\n%s\nwant 0 and\n%s", got.exit, got.stdout, logText)
	}
}

func TestLogSkipsALineThatIsNotJSON(t *testing.T) {
	setup(t)
	home := socketHome(t)
	writeJournal(t, home, logEntries[0])
	writeBrokenLine(t, home)
	writeJournal(t, home, logEntries[1:]...)
	got := runWith(routeTable(), "", "log")
	if got.exit != 0 || got.stdout != logText {
		t.Fatalf("log: exit %d, stdout\n%s\nwant 0 and\n%s", got.exit, got.stdout, logText)
	}
	oneWarningForLine2(t, got.stderr)
}

func TestLogJSONPrintsOneObjectPerLine(t *testing.T) {
	setup(t)
	home := socketHome(t)
	writeJournal(t, home, logEntries...)
	got := runWith(routeTable(), "", "log", "--json")
	lines := strings.Split(strings.TrimSuffix(got.stdout, "\n"), "\n")
	if got.exit != 0 || len(lines) != len(logEntries) {
		t.Fatalf("log --json: exit %d, %d lines; want 0 and %d", got.exit, len(lines), len(logEntries))
	}
	for i, line := range lines {
		var e journal.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d %q is not JSON: %v", i+1, line, err)
		}
		if e != logEntries[i] {
			t.Fatalf("line %d = %+v, want %+v", i+1, e, logEntries[i])
		}
	}
}

func TestLogWithNoJournalPrintsNothing(t *testing.T) {
	setup(t)
	socketHome(t)
	if got := runWith(routeTable(), "", "log"); got.exit != 0 || got.stdout != "" || got.stderr != "" {
		t.Fatalf("log: exit %d, stdout %q, stderr %q; want 0 and nothing", got.exit, got.stdout, got.stderr)
	}
}

func TestLogFollowPrintsNewLinesUntilInterrupted(t *testing.T) {
	setup(t)
	home := socketHome(t)
	writeJournal(t, home, logEntries[0])
	follow := crewtalkProcess("log", "--follow")
	var stderr bytes.Buffer
	follow.Stderr = &stderr
	out, err := follow.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := follow.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { follow.Process.Kill() })
	lines := make(chan string)
	go func() {
		scan := bufio.NewScanner(out)
		for scan.Scan() {
			lines <- scan.Text()
		}
		close(lines)
	}()
	want := strings.Split(logText, "\n")
	next := func(i int) {
		t.Helper()
		select {
		case line := <-lines:
			if line != want[i] {
				t.Fatalf("followed line %d = %q, want %q", i+1, line, want[i])
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("line %d not printed within 5s", i+1)
		}
	}
	next(0)
	writeBrokenLine(t, home)
	writeJournal(t, home, logEntries[1])
	next(1)
	writeJournal(t, home, logEntries[2])
	next(2)
	if err := follow.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	for range lines {
	}
	if exit := exitCode(t, follow.Wait()); exit != 0 {
		t.Fatalf("log --follow exit %d after SIGINT, want 0", exit)
	}
	oneWarningForLine2(t, stderr.String())
}
