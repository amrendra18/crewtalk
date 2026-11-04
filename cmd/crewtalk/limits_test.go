package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/held"
	"github.com/amrendra18/crewtalk/internal/journal"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/state"
)

func fakeHerdr(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs("../../internal/notice/testdata/herdr")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(bin, filepath.Join(dir, "herdr")); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_HERDR_CALLS", calls)
	t.Setenv("FAKE_HERDR_MODE", "")
	t.Setenv("HERDR_ENV", "1")
	return calls
}

func herdrCalls(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
	}
	return out
}

func refusedOnce(t *testing.T, home string, got result, f *fakeRoute, want string) {
	t.Helper()
	if got.exit != 3 || got.stdout != "" {
		t.Fatalf("exit %d, stdout %q; want 3 and nothing on stdout; stderr %q", got.exit, got.stdout, got.stderr)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a refused send reached the route %d times", len(f.sent))
	}
	if strings.Count(got.stderr, "\n") != 1 || !strings.HasPrefix(got.stderr, "crewtalk send: "+want+": ") {
		t.Fatalf("stderr %q, want one line that starts with crewtalk send: %s:", got.stderr, want)
	}
	lines := journalLines(t, home)
	if len(lines) == 0 || lines[len(lines)-1]["result"] != want {
		t.Fatalf("journal %v, want a last line with result %s", lines, want)
	}
}

func pairLines(from, to string, n int, at time.Time, result journal.Result) []journal.Entry {
	var es []journal.Entry
	for i := range n {
		es = append(es, journal.Entry{Time: at, ID: fmt.Sprintf("%s.%06dZ-%s", at.Format("20060102T150405"), i, strings.Repeat(string(result[0]), 8)), From: from, To: to, Thread: "t", Turn: 1, Route: "fake", Result: result})
	}
	return es
}

func TestLimitsHelpShowsTheConstants(t *testing.T) {
	minutes := fmt.Sprintf("%d minutes", int(held.Deadline.Minutes()))
	for _, c := range []struct {
		args  []string
		wants []string
	}{
		{[]string{"--help"}, []string{minutes}},
		{[]string{"send", "--help"}, []string{fmt.Sprintf("turn is over %d", maxTurns), fmt.Sprintf("sent %d messages", maxPairSends), minutes}},
		{[]string{"log", "--help"}, []string{minutes}},
	} {
		setup(t)
		got := crewtalk(&fakeRoute{}, "", c.args...)
		for _, want := range c.wants {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("%v: stdout %q, want %q", c.args, got.stdout, want)
			}
		}
	}
}

func TestLimitsHelpNamesTheBusyOrNotWritableLog(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"send", "--help"}} {
		setup(t)
		got := crewtalk(&fakeRoute{}, "", args...)
		if !strings.Contains(got.stdout, "busy or not writable") {
			t.Errorf("%v: stdout %q does not name the log under exit 5", args, got.stdout)
		}
	}
}

func TestLimitsSixTurnsPerThread(t *testing.T) {
	home := setup(t)
	f := &fakeRoute{}
	if got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "--thread", "loop", "--turn", "6", "the sixth turn"); got.exit != 0 {
		t.Fatalf("turn 6: exit %d; stderr %q", got.exit, got.stderr)
	}
	sixth := f.sent[0].ID
	f.sent = nil
	got := crewtalk(f, "", "send", "--from", "fake:w1", "--to", "fake:lead", "--reply-to", sixth, "the seventh turn")
	refusedOnce(t, home, got, f, "refused-limit")
	if want := "crewtalk send: refused-limit: turn 7 is over the limit of 6 turns in thread loop; stop this conversation and report this to your operator\n"; got.stderr != want {
		t.Fatalf("stderr %q, want %q", got.stderr, want)
	}
	refusedOnce(t, home, crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "--turn", "7", "a seventh turn by hand"), f, "refused-limit")
}

func TestLimitsTwentySendsPerPairPerHour(t *testing.T) {
	home := setup(t)
	now := time.Now().UTC()
	var lines []journal.Entry
	lines = append(lines, pairLines("fake:a", "fake:b", 10, now.Add(-50*time.Minute), journal.Delivered)...)
	held := pairLines("fake:a", "fake:b", 5, now.Add(-40*time.Minute), journal.Held)
	lines = append(lines, held...)
	for _, h := range held {
		h.Result, h.HeldFor, h.Time = journal.Delivered, 30, h.Time.Add(30*time.Second)
		lines = append(lines, h)
	}
	lines = append(lines, pairLines("fake:a", "fake:b", 2, now.Add(-35*time.Minute), journal.Held)...)
	lines = append(lines, pairLines("fake:a", "fake:b", 2, now.Add(-30*time.Minute), journal.OutcomeUnknown)...)
	lines = append(lines, pairLines("fake:a", "fake:b", 9, now.Add(-61*time.Minute), journal.Delivered)...)
	lines = append(lines, pairLines("fake:a", "fake:b", 9, now.Add(-time.Minute), journal.RefusedLimit)...)
	lines = append(lines, pairLines("fake:a", "fake:b", 9, now.Add(-time.Minute), journal.Failed)...)
	lines = append(lines, pairLines("fake:b", "fake:a", 25, now.Add(-time.Minute), journal.Delivered)...)
	writeJournal(t, home, lines...)

	f := &fakeRoute{}
	if got := crewtalk(f, "", "send", "--from", "fake:a", "--to", "fake:b", "the twentieth"); got.exit != 0 {
		t.Fatalf("the twentieth send in an hour: exit %d; stderr %q", got.exit, got.stderr)
	}
	if got := crewtalk(f, "", "send", "--from", "fake:a", "--to", "fake:c", "another receiver"); got.exit != 0 {
		t.Fatalf("a send to another receiver: exit %d; stderr %q", got.exit, got.stderr)
	}
	f.sent = nil
	got := crewtalk(f, "", "send", "--from", "fake:a", "--to", "fake:b", "the twenty-first")
	refusedOnce(t, home, got, f, "refused-limit")
	clears := now.Add(-50 * time.Minute).Add(time.Hour).Format("15:04:05")
	if !strings.Contains(got.stderr, "20 messages") || !strings.Contains(got.stderr, clears) {
		t.Fatalf("stderr %q, want the limit of 20 messages and the time %s UTC when it clears", got.stderr, clears)
	}
}

func TestLimitsRetryTimeWhenMoreThanTwentyAreCounted(t *testing.T) {
	home := setup(t)
	start := time.Now().UTC().Add(-50 * time.Minute)
	var lines []journal.Entry
	for i := range 25 {
		lines = append(lines, pairLines("fake:a", "fake:b", 1, start.Add(time.Duration(i)*time.Minute), journal.Delivered)...)
	}
	writeJournal(t, home, lines...)
	f := &fakeRoute{}
	got := crewtalk(f, "", "send", "--from", "fake:a", "--to", "fake:b", "hi")
	refusedOnce(t, home, got, f, "refused-limit")
	if clears := start.Add(5 * time.Minute).Add(time.Hour).Format("15:04:05"); !strings.Contains(got.stderr, "sent 25 messages") || !strings.Contains(got.stderr, clears+" UTC") {
		t.Fatalf("stderr %q, want 25 messages and the time %s UTC, when the count is back to 19", got.stderr, clears)
	}
}

func TestLimitsWarnOnceForALogLineThatIsNotJSON(t *testing.T) {
	home := setup(t)
	writeJournal(t, home, pairLines("fake:a", "fake:b", 1, time.Now().UTC(), journal.Delivered)...)
	writeBrokenLine(t, home)
	got := crewtalk(&fakeRoute{}, "", "send", "--from", "fake:a", "--to", "fake:b", "hi")
	if got.exit != 0 || !strings.HasPrefix(got.stderr, "crewtalk send: warning: ") {
		t.Fatalf("exit %d, stderr %q; want 0 and a warning", got.exit, got.stderr)
	}
	oneWarningForLine2(t, got.stderr)
}

func TestLimitsRefusalFollowsTheWarningForALogLineThatIsNotJSON(t *testing.T) {
	home := setup(t)
	writeJournal(t, home, pairLines("fake:a", "fake:b", 20, time.Now().UTC().Add(-time.Minute), journal.Delivered)...)
	writeBrokenLine(t, home)
	f := &fakeRoute{}
	got := crewtalk(f, "", "send", "--from", "fake:a", "--to", "fake:b", "hi")
	lines := strings.Split(strings.TrimSuffix(got.stderr, "\n"), "\n")
	if got.exit != 3 || len(f.sent) != 0 || len(lines) != 2 {
		t.Fatalf("exit %d, %d route calls, stderr %q; want 3, none, and a warning line then the refusal line", got.exit, len(f.sent), got.stderr)
	}
	if want := "crewtalk send: warning: " + filepath.Join(home, "log.jsonl") + " line 21 is not one JSON object"; !strings.HasPrefix(lines[0], want) || !strings.HasSuffix(lines[0], "; the operator can remove that line from the log file") {
		t.Fatalf("first stderr line %q, want it to start with %q and say the operator can remove that line", lines[0], want)
	}
	if !strings.HasPrefix(lines[1], "crewtalk send: refused-limit: fake:a sent 20 messages") {
		t.Fatalf("second stderr line %q, want the refused-limit line", lines[1])
	}
}

func TestLimitsCountAHeldMessageOnceAtItsHeldLine(t *testing.T) {
	for _, c := range []struct {
		name  string
		later time.Duration
		exit  int
	}{
		{"now", 0, 3},
		{"11 minutes later", 11 * time.Minute, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := setup(t)
			now := time.Now().UTC().Add(-c.later)
			heldLine := pairLines("fake:a", "fake:b", 1, now.Add(-50*time.Minute), journal.Held)[0]
			deliveredLine := heldLine
			deliveredLine.Result, deliveredLine.HeldFor, deliveredLine.Time = journal.Delivered, 45*60, now.Add(-5*time.Minute)
			lines := append([]journal.Entry{heldLine, deliveredLine}, pairLines("fake:a", "fake:b", 19, now.Add(-time.Minute), journal.Delivered)...)
			writeJournal(t, home, lines...)

			f := &fakeRoute{}
			got := crewtalk(f, "", "send", "--from", "fake:a", "--to", "fake:b", "hi")
			if got.exit != c.exit {
				t.Fatalf("exit %d, want %d; stderr %q", got.exit, c.exit, got.stderr)
			}
			if clears := heldLine.Time.Add(time.Hour).Format("15:04:05"); c.exit == 3 && !strings.Contains(got.stderr, clears) {
				t.Fatalf("stderr %q, want the next send allowed at %s UTC, an hour after the held line", got.stderr, clears)
			}
		})
	}
}

func TestLimitsSendNothingWhileTheLogIsNotWritable(t *testing.T) {
	home := setup(t)
	lock := filepath.Join(home, "log.jsonl.lock")
	if err := os.WriteFile(lock, nil, 0); err != nil {
		t.Fatal(err)
	}
	f := &fakeRoute{}
	for i := 1; i <= 25; i++ {
		got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", fmt.Sprintf("m%d", i))
		if got.exit != 5 || got.stdout != "" {
			t.Fatalf("send %d: exit %d, stdout %q; want 5 and nothing; stderr %q", i, got.exit, got.stdout, got.stderr)
		}
		if strings.Count(got.stderr, "\n") != 1 {
			t.Fatalf("send %d: stderr %q, want one line", i, got.stderr)
		}
		for _, want := range []string{
			"not writable",
			"chmod 600 " + filepath.Join(home, "log.jsonl") + " " + lock,
			"in a Codex session this can mean the shell line missed the command rule",
			"one quoted string with no backslash",
			"do not ask to leave the sandbox",
			"tell your operator",
			"nothing was sent",
		} {
			if !strings.Contains(got.stderr, want) {
				t.Fatalf("send %d: stderr %q, want %q", i, got.stderr, want)
			}
		}
	}
	if len(f.sent) != 0 {
		t.Fatalf("%d sends reached the route while the log was not writable, want 0", len(f.sent))
	}
}

func TestLimitsSendNothingWhileTheLogIsBusy(t *testing.T) {
	home := setup(t)
	unlock, err := state.Lock(context.Background(), filepath.Join(home, "log.jsonl.lock"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	f := &fakeRoute{}
	got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "hi")
	if got.exit != 5 || got.stdout != "" || len(f.sent) != 0 {
		t.Fatalf("exit %d, stdout %q, %d sends; want 5, nothing and none; stderr %q", got.exit, got.stdout, len(f.sent), got.stderr)
	}
	if strings.Count(got.stderr, "\n") != 1 {
		t.Fatalf("stderr %q, want one line", got.stderr)
	}
	for _, want := range []string{"the log is busy", "send again in a moment", "tell your operator", "nothing was sent"} {
		if !strings.Contains(got.stderr, want) {
			t.Fatalf("stderr %q, want %q", got.stderr, want)
		}
	}
	for _, not := range []string{"command rule", "chmod", "not writable"} {
		if strings.Contains(got.stderr, not) {
			t.Fatalf("stderr %q blames %q for a busy lock", got.stderr, not)
		}
	}
}

func TestPauseRefusesEverySend(t *testing.T) {
	home := setup(t)
	pause := filepath.Join(home, "PAUSE")
	if err := os.WriteFile(pause, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRoute{}
	got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "hello")
	refusedOnce(t, home, got, f, "refused-paused")
	if want := "crewtalk send: refused-paused: every send is refused while " + pause + " exists; report this to your operator\n"; got.stderr != want {
		t.Fatalf("stderr %q, want %q", got.stderr, want)
	}
	refusedOnce(t, home, crewtalk(f, "", "send", "--from", "fake:lead", "--to", "nobody", "hello"), f, "refused-paused")

	got = crewtalk(f, "", "send", "--json", "--from", "fake:lead", "--to", "fake:w1", "hello")
	var report map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil || got.exit != 3 || report["result"] != "refused-paused" || report["error"] == nil {
		t.Fatalf("send --json while paused: exit %d, stdout %q; want 3 and result refused-paused with an error", got.exit, got.stdout)
	}

	if err := os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	if got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "hello"); got.exit != 0 || len(f.sent) != 1 {
		t.Fatalf("send after PAUSE was removed: exit %d, %d route calls; want 0 and 1", got.exit, len(f.sent))
	}
}

func TestPauseWaitPrintsNothingAndTheMessageStaysHeld(t *testing.T) {
	setup(t)
	home := socketHome(t)
	m := holdOld(t, home, "lead", message.New("fake:a", "claude:lead", "", "held before pause", 1), time.Minute)
	pause := filepath.Join(home, "PAUSE")
	if err := os.WriteFile(pause, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got := runWith(routeTable(), "", "wait", "--as", "lead", "--timeout", "5s")
	if want := "crewtalk wait: refused-paused: every message is stopped while " + pause + " exists; report this to your operator\n"; got.exit != 3 || got.stdout != "" || got.stderr != want {
		t.Fatalf("wait while paused: exit %d, stdout %q, stderr %q; want 3, nothing and %q", got.exit, got.stdout, got.stderr, want)
	}
	if ids := heldIDs(t, home, "lead"); !slices.Equal(ids, []string{m.ID}) {
		t.Fatalf("held ids %v while paused, want %s", ids, m.ID)
	}

	if err := os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	got = runWith(routeTable(), "", "wait", "--as", "lead", "--timeout", "5s")
	if got.exit != 0 || !strings.HasSuffix(got.stdout, "held before pause\n") {
		t.Fatalf("wait after PAUSE was removed: exit %d, stdout %q; want 0 and the held message", got.exit, got.stdout)
	}
	if ids := heldIDs(t, home, "lead"); len(ids) != 0 {
		t.Fatalf("held ids %v after the print, want none", ids)
	}

	if err := os.WriteFile(pause, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runWith(routeTable(), "", "wait", "--as", "lead", "--timeout", "100ms"); got.exit != 3 || got.stdout != "" {
		t.Fatalf("wait with nothing held while paused: exit %d, stdout %q; want 3 and nothing", got.exit, got.stdout)
	}
}

func TestPauseLetsAHeldMessageExpireAndTellsTheOperator(t *testing.T) {
	setup(t)
	home := socketHome(t)
	calls := fakeHerdr(t)
	holdOld(t, home, "lead", message.New("fake:a", "claude:lead", "", "too late", 1), 2*held.Deadline)
	if err := os.WriteFile(filepath.Join(home, "PAUSE"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runWith(routeTable(), "", "wait", "--as", "lead", "--timeout", "5s"); got.exit != 3 || got.stdout != "" {
		t.Fatalf("wait while paused: exit %d, stdout %q; want 3 and nothing", got.exit, got.stdout)
	}
	if r := results(journalLines(t, home)); !slices.Equal(r, []string{"expired"}) {
		t.Fatalf("journal results %v, want one expired line", r)
	}
	if got := herdrCalls(t, calls); len(got) != 1 {
		t.Fatalf("herdr calls %q, want one notice", got)
	}
}

func TestNoticeOnEveryRefusal(t *testing.T) {
	home := setup(t)
	calls := fakeHerdr(t)
	f := &fakeRoute{}
	crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "--turn", "7", "hello")
	writeJournal(t, home, pairLines("fake:a", "fake:b", 20, time.Now().UTC().Add(-time.Minute), journal.Delivered)...)
	crewtalk(f, "", "send", "--from", "fake:a", "--to", "fake:b", "hello")
	if err := os.WriteFile(filepath.Join(home, "PAUSE"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "hello")
	got := herdrCalls(t, calls)
	wants := []string{"refused-limit: turn 7", "refused-limit: fake:a sent 20 messages", "refused-paused"}
	if len(got) != len(wants) {
		t.Fatalf("herdr calls %q, want %d", got, len(wants))
	}
	for i, want := range wants {
		c := got[i]
		if len(c) != 5 || c[0] != "notification" || c[1] != "show" || c[3] != "--body" || !strings.Contains(c[4], want) {
			t.Errorf("herdr call %q, want notification show TITLE --body naming %s", c, want)
		}
	}
}

func TestNoticeFailingLeavesARefusedSendUnchanged(t *testing.T) {
	home := setup(t)
	if err := os.WriteFile(filepath.Join(home, "PAUSE"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	send := func() (result, map[string]any) {
		t.Helper()
		got := crewtalk(&fakeRoute{}, "", "send", "--json", "--from", "fake:lead", "--to", "fake:w1", "--thread", "t", "hello")
		var report map[string]any
		if err := json.Unmarshal([]byte(got.stdout), &report); err != nil || strings.Count(got.stdout, "\n") != 1 {
			t.Fatalf("stdout %q, want one JSON object: %v", got.stdout, err)
		}
		delete(report, "id")
		delete(report, "time")
		return got, report
	}
	quiet, quietReport := send()
	calls := fakeHerdr(t)
	t.Setenv("FAKE_HERDR_MODE", "fail")
	failing, failingReport := send()
	if n := len(herdrCalls(t, calls)); n != 1 {
		t.Fatalf("the failing herdr ran %d times, want 1", n)
	}
	if failing.exit != 3 || failing.exit != quiet.exit || failing.stderr != quiet.stderr || fmt.Sprint(failingReport) != fmt.Sprint(quietReport) {
		t.Fatalf("with a failing herdr: exit %d, stderr %q, report %v; without a notice: exit %d, stderr %q, report %v; want the same, and exit 3", failing.exit, failing.stderr, failingReport, quiet.exit, quiet.stderr, quietReport)
	}
}

func TestNoticeNotRaisedForADeliveredSend(t *testing.T) {
	setup(t)
	calls := fakeHerdr(t)
	crewtalk(&fakeRoute{}, "", "send", "--from", "fake:lead", "--to", "fake:w1", "hello")
	if got := herdrCalls(t, calls); len(got) != 0 {
		t.Fatalf("herdr calls %q for a delivered send, want none", got)
	}
}

func TestLogFailureAfterDeliveryIsReported(t *testing.T) {
	home := setup(t)
	lock := filepath.Join(home, "log.jsonl.lock")
	f := &fakeRoute{during: func() {
		if err := os.WriteFile(lock, nil, 0); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lock, 0); err != nil {
			t.Fatal(err)
		}
	}}
	got := crewtalk(f, "", "send", "--json", "--from", "fake:lead", "--to", "fake:w1", "hello")
	var report map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", got.stdout, err)
	}
	if got.exit != 0 || len(f.sent) != 1 || report["result"] != "delivered" || report["logged"] != false || report["error"] != nil {
		t.Fatalf("exit %d, report %v; want 0, delivered, logged false and no error", got.exit, report)
	}
	if strings.Count(got.stderr, "\n") != 1 || !strings.Contains(got.stderr, "log line was not written") {
		t.Fatalf("stderr %q, want one line saying the log line was not written", got.stderr)
	}
	if n := len(journalLines(t, home)); n != 0 {
		t.Fatalf("journal holds %d lines, want 0", n)
	}

	if err := os.Chmod(lock, 0o600); err != nil {
		t.Fatal(err)
	}
	f.during = nil
	got = crewtalk(f, "", "send", "--json", "--from", "fake:lead", "--to", "fake:w1", "hello")
	if strings.Contains(got.stdout, "logged") {
		t.Fatalf("stdout %q names logged for a send whose line was written", got.stdout)
	}
}
