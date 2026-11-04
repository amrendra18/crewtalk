package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/held"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
	"github.com/amrendra18/crewtalk/internal/route/claude"
)

func heldFile(home, name string) string {
	return filepath.Join(home, "held", name+".jsonl")
}

func heldIDs(t *testing.T, home, name string) []string {
	t.Helper()
	data, err := os.ReadFile(heldFile(home, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		var m message.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("held line %q: %v", line, err)
		}
		ids = append(ids, m.ID)
	}
	return ids
}

func holdOld(t *testing.T, home, name string, m message.Message, age time.Duration) message.Message {
	t.Helper()
	m.Time = time.Now().UTC().Add(-age)
	box, unlock, err := held.Lock(context.Background(), home, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := box.Hold(name, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func joinClaude(t *testing.T, routes map[string]route.Route, name string) {
	t.Helper()
	if got := runWith(routes, "", "join", "--as", name, "--kind", "claude"); got.exit != 0 {
		t.Fatalf("join %s: exit %d; stderr %q", name, got.exit, got.stderr)
	}
}

type lateListener struct {
	claude.Route
	start func()
	tries int
}

func (l *lateListener) Deliver(ctx context.Context, to route.Agent, m message.Message) error {
	l.tries++
	if l.tries == 1 {
		l.start()
		return fmt.Errorf("claude:%s: %w", to.Address, route.ErrNotListening)
	}
	return l.Route.Deliver(ctx, to, m)
}

func results(lines []map[string]any) []string {
	var out []string
	for _, l := range lines {
		out = append(out, fmt.Sprint(l["result"]))
	}
	return out
}

func TestHeldSendToAClaudeNameNotListeningIsHeld(t *testing.T) {
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	joinClaude(t, routes, "lead")
	got := runWith(routes, "", "send", "--from", "codex:"+codexThread, "--to", "claude:lead", "while you were away")
	if got.exit != 0 || !strings.HasPrefix(got.stdout, "held, id ") || got.stderr != "" {
		t.Fatalf("send: exit %d, stdout %q, stderr %q; want 0, held, and no error", got.exit, got.stdout, got.stderr)
	}
	got = runWith(routes, "", "send", "--json", "--from", "codex:"+codexThread, "--to", "claude:lead", "a second one")
	var report map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil || got.exit != 0 {
		t.Fatalf("send --json: exit %d, stdout %q: %v", got.exit, got.stdout, err)
	}
	if report["result"] != "held" || report["error"] != nil || report["route"] != "claude" {
		t.Fatalf("report %v, want result held, route claude and no error", report)
	}
	lines := journalLines(t, home)
	if len(lines) != 2 || lines[0]["result"] != "held" || lines[1]["result"] != "held" {
		t.Fatalf("journal %v, want two held lines", lines)
	}
	if ids := heldIDs(t, home, "lead"); len(ids) != 2 || ids[0] != lines[0]["id"] || ids[1] != report["id"] {
		t.Fatalf("held ids %v, want the two sends in order", ids)
	}
}

func TestHeldOnlyForAnAddressInTheAddressBook(t *testing.T) {
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	got := runWith(routes, "", "send", "--from", "claude:alice", "--to", "claude:zed", "next")
	if got.exit != 4 || got.stdout != "" || !strings.HasPrefix(got.stderr, "crewtalk send: not-listening: ") {
		t.Fatalf("send to a claude address nobody joined: exit %d, stdout %q, stderr %q; want 4 and not-listening", got.exit, got.stdout, got.stderr)
	}
	if r := results(journalLines(t, home)); fmt.Sprint(r) != "[not-listening]" {
		t.Fatalf("journal results %v, want [not-listening]", r)
	}
	if ids := heldIDs(t, home, "zed"); len(ids) != 0 {
		t.Fatalf("held ids %v for a name nobody joined, want none", ids)
	}
	joinClaude(t, routes, "zed")
	if got := runWith(routes, "", "send", "--from", "claude:alice", "--to", "claude:zed", "next"); got.exit != 0 || !strings.HasPrefix(got.stdout, "held, id ") {
		t.Fatalf("send after join: exit %d, stdout %q, stderr %q; want 0 and held", got.exit, got.stdout, got.stderr)
	}
}

func TestHeldOnlyWhenTheRouteSaysAbsent(t *testing.T) {
	for _, c := range []struct {
		name     string
		presence route.Presence
		joined   bool
		exit     int
		result   string
		tries    int
		held     int
	}{
		{"unheld", route.Unheld, true, 4, "not-listening", 1, 0},
		{"absent and not joined", route.Absent, false, 4, "not-listening", 1, 0},
		{"absent and joined", route.Absent, true, 0, "held", 1, 1},
		{"present after all", route.Present, true, 4, "not-listening", 2, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := setup(t)
			f := &fakeRoute{err: route.ErrNotListening, presence: c.presence}
			if c.joined {
				if got := crewtalk(f, "", "join", "--as", "w1", "--kind", "fake", "--address", "w1"); got.exit != 0 {
					t.Fatalf("join: exit %d; stderr %q", got.exit, got.stderr)
				}
			}
			got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "hello")
			if got.exit != c.exit || len(f.sent) != c.tries {
				t.Fatalf("exit %d after %d route calls, want %d after %d; stderr %q", got.exit, len(f.sent), c.exit, c.tries, got.stderr)
			}
			if r := results(journalLines(t, home)); len(r) != 1 || r[0] != c.result {
				t.Fatalf("journal results %v, want [%s]", r, c.result)
			}
			if n := len(heldIDs(t, home, "w1")); n != c.held {
				t.Fatalf("%d held messages, want %d", n, c.held)
			}
		})
	}
}

func TestCodexNotListeningSendQueuesOnceAndHoldsNothing(t *testing.T) {
	setup(t)
	home := socketHome(t)
	fake, err := filepath.Abs("../../internal/route/codex/testdata/codex")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(fake, filepath.Join(dir, "codex")); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(dir, "runs")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_MODE", "inactive")
	t.Setenv("FAKE_CODEX_RUNS", runs)
	t.Setenv("FAKE_CODEX_ARGS", filepath.Join(dir, "args"))

	routes := routeTable()
	if got := runWith(routes, "", "join", "--as", "worker", "--kind", "codex", "--address", codexThread); got.exit != 0 {
		t.Fatalf("join: exit %d; stderr %q", got.exit, got.stderr)
	}
	got := runWith(routes, "", "send", "--from", "claude:lead", "--to", "worker", "hello")
	if got.exit != exitReceiver {
		t.Fatalf("send: exit %d, want %d; stderr %q", got.exit, exitReceiver, got.stderr)
	}
	data, err := os.ReadFile(runs)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "run\n"); n != 1 {
		t.Fatalf("codex queue ran %d times, want exactly 1", n)
	}
	if r := results(journalLines(t, home)); len(r) != 1 || r[0] != "not-listening" {
		t.Fatalf("journal results %v, want [not-listening]", r)
	}
	if ids := heldIDs(t, home, codexThread); len(ids) != 0 {
		t.Fatalf("held messages %v, want none", ids)
	}
}

func TestHeldHelpTextsSayWhenAMessageIsHeldAndWhenItExpires(t *testing.T) {
	setup(t)
	for _, args := range [][]string{{"--help"}, {"send", "--help"}, {"wait", "--help"}} {
		got := runWith(routeTable(), "", args...)
		text := strings.Join(strings.Fields(got.stdout), " ")
		for _, want := range []string{
			"A message to a claude agent in the address book that is not listening is held",
			"A held message expires at the first send, wait or log that runs 30 minutes or more after it was held",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%v: stdout %q, want %q", args, got.stdout, want)
			}
		}
	}
}

func TestHeldSendTriesAgainWhenAListenerStartedInBetween(t *testing.T) {
	setup(t)
	home := socketHome(t)
	waited := make(chan result, 1)
	late := &lateListener{start: func() {
		go func() { waited <- runWith(routeTable(), "", "wait", "--as", "lead", "--timeout", "5s") }()
		waitForListener(t, home, "lead")
	}}
	routes := routeTable()
	routes["claude"] = late
	got := runWith(routes, "", "send", "--from", "claude:w1", "--to", "claude:lead", "just in time")
	if got.exit != 0 || !strings.HasPrefix(got.stdout, "delivered, id ") || got.stderr != "" || late.tries != 2 {
		t.Fatalf("send: exit %d, stdout %q, stderr %q after %d tries; want 0, delivered and no error after 2", got.exit, got.stdout, got.stderr, late.tries)
	}
	if w := <-waited; w.exit != 0 || !strings.HasSuffix(w.stdout, "just in time\n") {
		t.Fatalf("wait: exit %d, stdout %q; want the message", w.exit, w.stdout)
	}
	if r := results(journalLines(t, home)); fmt.Sprint(r) != "[delivered]" {
		t.Fatalf("journal results %v, want [delivered]", r)
	}
	if ids := heldIDs(t, home, "lead"); len(ids) != 0 {
		t.Fatalf("held ids %v, want none", ids)
	}
}

func TestHeldLineThatIsNotJSONIsSkippedWithOneWarning(t *testing.T) {
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	joinClaude(t, routes, "b")
	runWith(routes, "", "send", "--from", "claude:a", "--to", "b", "first")
	f, err := os.OpenFile(heldFile(home, "b"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	warning := func(t *testing.T, what string, line int, stderr string) {
		t.Helper()
		want := fmt.Sprintf("crewtalk %s: warning: %s line %d is not one JSON object, so it was skipped: ", what, heldFile(home, "b"), line)
		if strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, want) || !strings.HasSuffix(stderr, "; the operator can remove that line\n") {
			t.Fatalf("%s stderr %q, want one warning that starts %q and says the operator can remove that line", what, stderr, want)
		}
	}

	got := runWith(routes, "", "send", "--from", "claude:a", "--to", "b", "second")
	if got.exit != 0 || !strings.HasPrefix(got.stdout, "held, id ") {
		t.Fatalf("send: exit %d, stdout %q, stderr %q; want 0 and held", got.exit, got.stdout, got.stderr)
	}
	warning(t, "send", 2, got.stderr)
	for i, text := range []string{"first", "second"} {
		got := runWith(routes, "", "wait", "--as", "b", "--timeout", "5s")
		if got.exit != 0 || !strings.HasSuffix(got.stdout, text+"\n") {
			t.Fatalf("wait: exit %d, stdout %q, stderr %q; want %q", got.exit, got.stdout, got.stderr, text)
		}
		warning(t, "wait", 3-i, got.stderr)
	}
	if data, err := os.ReadFile(heldFile(home, "b")); err != nil || string(data) != "not json\n" {
		t.Fatalf("held file %q, %v; want only the line that is not JSON, kept for the operator", data, err)
	}
}

func TestHeldWaitPrintsTheOldestFirstAndLogsHowLongItWaited(t *testing.T) {
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	joinClaude(t, routes, "lead")
	var sent []string
	for _, text := range []string{"first", "second"} {
		got := runWith(routes, "", "send", "--json", "--from", "codex:"+codexThread, "--to", "claude:lead", "--thread", "proj-a", text)
		var report map[string]any
		json.Unmarshal([]byte(got.stdout), &report)
		sent = append(sent, fmt.Sprint(report["id"]))
	}
	time.Sleep(20 * time.Millisecond)

	for i, text := range []string{"first", "second"} {
		got := runWith(routes, "", "wait", "--as", "lead", "--timeout", "5s")
		m, err := message.Parse(strings.TrimSuffix(got.stdout, "\n"))
		if got.exit != 0 || err != nil || m.ID != sent[i] || m.Text != text || m.Thread != "proj-a" {
			t.Fatalf("wait %d: exit %d, stdout %q, stderr %q; want %s with text %q", i, got.exit, got.stdout, got.stderr, sent[i], text)
		}
	}
	if ids := heldIDs(t, home, "lead"); len(ids) != 0 {
		t.Fatalf("held ids %v after both waits, want none", ids)
	}
	lines := journalLines(t, home)
	if r := results(lines); strings.Join(r, " ") != "held held delivered delivered" {
		t.Fatalf("journal results %v, want held held delivered delivered", r)
	}
	for i, l := range lines[2:] {
		if l["id"] != sent[i] || l["route"] != "claude" {
			t.Errorf("delivered line %v, want id %s and route claude", l, sent[i])
		}
		if waited, ok := l["held_for"].(float64); !ok || waited < 0.02 {
			t.Errorf("delivered line %v, want held_for of at least 0.02 seconds", l)
		}
	}
	if got := runWith(routes, "", "wait", "--as", "lead", "--timeout", "100ms"); got.exit != 4 || got.stdout != "" {
		t.Fatalf("wait with nothing held: exit %d, stdout %q; want 4 and nothing", got.exit, got.stdout)
	}
}

func TestHeldLargeMessageIsPrinted(t *testing.T) {
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	joinClaude(t, routes, "lead")
	text := strings.Repeat("<", 8<<20-1024)
	from := "codex:" + codexThread

	got := runWith(routes, text, "send", "--from", from, "--to", "claude:lead", "-")
	if got.exit != 0 || !strings.HasPrefix(got.stdout, "held, id ") || got.stderr != "" {
		t.Fatalf("large send: exit %d, stdout %q, stderr %q; want 0, held, and no error", got.exit, got.stdout, got.stderr)
	}
	got = runWith(routes, "", "send", "--from", from, "--to", "claude:lead", "later")
	if got.exit != 0 || !strings.HasPrefix(got.stdout, "held, id ") || got.stderr != "" {
		t.Fatalf("later send: exit %d, stdout %q, stderr %q; want 0, held, and no error", got.exit, got.stdout, got.stderr)
	}

	got = runWith(routes, "", "wait", "--as", "lead", "--timeout", "5s")
	m, err := message.Parse(strings.TrimSuffix(got.stdout, "\n"))
	if got.exit != 0 || err != nil || got.stderr != "" || m.Text != text {
		t.Fatalf("first wait: exit %d, %d stdout bytes, stderr %q, parse error %v, text is the whole message: %v", got.exit, len(got.stdout), got.stderr, err, m.Text == text)
	}
	got = runWith(routes, "", "wait", "--as", "lead", "--timeout", "5s")
	m, err = message.Parse(strings.TrimSuffix(got.stdout, "\n"))
	if got.exit != 0 || err != nil || got.stderr != "" || m.Text != "later" {
		t.Fatalf("second wait: exit %d, stdout %q, stderr %q, parse error %v; want the later message", got.exit, got.stdout, got.stderr, err)
	}
	if ids := heldIDs(t, home, "lead"); len(ids) != 0 {
		t.Fatalf("held ids %v after both waits, want none", ids)
	}
}

func TestHeldWaitThatCannotPrintKeepsTheMessage(t *testing.T) {
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	joinClaude(t, routes, "lead")
	runWith(routes, "", "send", "--from", "codex:"+codexThread, "--to", "claude:lead", "keep me")
	ids := heldIDs(t, home, "lead")

	var stderr strings.Builder
	if exit := run([]string{"wait", "--as", "lead", "--timeout", "5s"}, strings.NewReader(""), brokenWriter{}, &stderr, routes); exit != 5 {
		t.Fatalf("wait with a closed output: exit %d, want 5; stderr %q", exit, stderr.String())
	}
	if got := heldIDs(t, home, "lead"); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("held ids %v after a failed print, want %v", got, ids)
	}
	got := runWith(routes, "", "wait", "--as", "lead", "--timeout", "5s")
	if got.exit != 0 || !strings.Contains(got.stdout, ids[0]) || !strings.HasSuffix(got.stdout, "keep me\n") {
		t.Fatalf("next wait: exit %d, stdout %q; want the kept message", got.exit, got.stdout)
	}
}

func TestHeldSendsToABusyListenerAreAllPrintedOnce(t *testing.T) {
	const sends = 8
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	first := make(chan result, 1)
	go func() { first <- runWith(routes, "", "wait", "--as", "lead", "--timeout", "10s") }()
	waitForListener(t, home, "lead")

	var senders sync.WaitGroup
	exits := make(chan int, sends)
	for k := range sends {
		senders.Go(func() {
			exits <- runWith(routes, "", "send", "--from", "codex:"+codexThread, "--to", "claude:lead", fmt.Sprintf("message %d", k)).exit
		})
	}
	senders.Wait()
	close(exits)
	for exit := range exits {
		if exit != 0 {
			t.Fatalf("a send to a busy listener exited %d, want 0 (delivered or held)", exit)
		}
	}

	printed := map[string]int{}
	for i, r := 0, <-first; r.exit != 4; i, r = i+1, runWith(routes, "", "wait", "--as", "lead", "--timeout", "100ms") {
		if i > sends {
			t.Fatalf("%d waits printed a message after %d sends, want at most %d", i, sends, sends)
		}
		m, err := message.Parse(strings.TrimSuffix(r.stdout, "\n"))
		if r.exit != 0 || err != nil {
			t.Fatalf("wait: exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
		}
		printed[m.Text]++
	}
	for k := range sends {
		if n := printed[fmt.Sprintf("message %d", k)]; n != 1 {
			t.Errorf("message %d printed %d times, want 1", k, n)
		}
	}
}

func TestHeldNoMessageWaitsWhileItsListenerListens(t *testing.T) {
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	refused := 0
	for round := range 30 {
		waited := make(chan result, 1)
		go func() { waited <- runWith(routes, "", "wait", "--as", "lead", "--timeout", "2s") }()
		text := fmt.Sprintf("round %d", round)
		sent := runWith(routes, "", "send", "--from", fmt.Sprintf("claude:w%d", round), "--to", "claude:lead", text)
		got := <-waited
		switch {
		case sent.exit == 4:
			refused++
		case sent.exit != 0:
			t.Fatalf("round %d: send exit %d; stderr %q", round, sent.exit, sent.stderr)
		case got.exit != 0 || !strings.HasSuffix(got.stdout, text+"\n"):
			t.Fatalf("round %d: send said %q, but wait exited %d with %q; a held message waited while its listener listened", round, sent.stdout, got.exit, got.stdout)
		}
		if ids := heldIDs(t, home, "lead"); len(ids) != 0 {
			t.Fatalf("round %d: %v still held after the wait", round, ids)
		}
	}
	t.Logf("30 rounds of a send racing a starting wait: %d told not-listening, none left held", refused)
}

func TestExpiryRunsAtTheStartOfSendWaitAndLog(t *testing.T) {
	commands := map[string][]string{
		"send": {"send", "--from", "fake:w9", "--to", "fake:w1", "hello"},
		"wait": {"wait", "--as", "w2", "--timeout", "100ms"},
		"log":  {"log"},
	}
	for name, args := range commands {
		t.Run(name, func(t *testing.T) {
			setup(t)
			home := socketHome(t)
			old := holdOld(t, home, "lead", message.New("fake:lead", "claude:lead", "proj-a", "too late", 2), held.Deadline+time.Minute)
			fresh := holdOld(t, home, "lead", message.New("fake:lead", "claude:lead", "proj-a", "in time", 3), time.Minute)
			routes := routeTable()
			routes["fake"] = &fakeRoute{}
			got := runWith(routes, "", args...)
			if got.exit != 0 && got.exit != 4 {
				t.Fatalf("%v: exit %d; stderr %q", args, got.exit, got.stderr)
			}
			if ids := heldIDs(t, home, "lead"); len(ids) != 1 || ids[0] != fresh.ID {
				t.Fatalf("held ids %v, want only %s", ids, fresh.ID)
			}
			lines := journalLines(t, home)
			if len(lines) == 0 || lines[0]["result"] != "expired" || lines[0]["id"] != old.ID || lines[0]["from"] != "fake:lead" || lines[0]["route"] != "claude" || lines[0]["turn"] != 2.0 {
				t.Fatalf("journal %v, want an expired line for %s first", lines, old.ID)
			}
			if n := strings.Count(fmt.Sprint(results(lines)), "expired"); n != 1 {
				t.Fatalf("journal results %v, want one expired line", results(lines))
			}
			if name == "log" && !strings.Contains(got.stdout, old.ID+", expired") {
				t.Fatalf("log printed %q, want the expired line", got.stdout)
			}
		})
	}
}

func TestExpiryTellsTheSenderOnItsNextSendOnce(t *testing.T) {
	setup(t)
	home := socketHome(t)
	old := holdOld(t, home, "lead", message.New("fake:a", "claude:lead", "", "too late", 1), 2*held.Deadline)
	routes := routeTable()
	routes["fake"] = &fakeRoute{}
	if got := runWith(routes, "", "log"); got.exit != 0 {
		t.Fatalf("log: exit %d; stderr %q", got.exit, got.stderr)
	}
	other := runWith(routes, "", "send", "--from", "fake:b", "--to", "fake:w1", "hello")
	if other.exit != 0 || other.stderr != "" {
		t.Fatalf("another sender: exit %d, stderr %q; want 0 and nothing", other.exit, other.stderr)
	}
	next := runWith(routes, "", "send", "--from", "fake:a", "--to", "fake:w1", "hello")
	if want := fmt.Sprintf("crewtalk send: 1 held message from fake:a waited over %d minutes for a crewtalk wait and expired: %s\n", int(held.Deadline.Minutes()), old.ID); next.exit != 0 || next.stderr != want {
		t.Fatalf("the sender's next send: exit %d, stderr %q; want 0 and %q", next.exit, next.stderr, want)
	}
	again := runWith(routes, "", "send", "--from", "fake:a", "--to", "fake:w1", "hello")
	if again.stderr != "" {
		t.Fatalf("the send after that: stderr %q, want nothing", again.stderr)
	}
}

func TestExpiryTellsTheSenderOnlyOnASendThatIsNotRefused(t *testing.T) {
	setup(t)
	home := socketHome(t)
	old := holdOld(t, home, "lead", message.New("fake:a", "claude:lead", "", "too late", 1), 2*held.Deadline)
	pause := filepath.Join(home, "PAUSE")
	if err := os.WriteFile(pause, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	routes := routeTable()
	f := &fakeRoute{}
	routes["fake"] = f
	refusedOnce(t, home, runWith(routes, "", "send", "--from", "fake:a", "--to", "fake:w1", "hello"), f, "refused-paused")
	if err := os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	next := runWith(routes, "", "send", "--from", "fake:a", "--to", "fake:w1", "hello")
	if next.exit != 0 || strings.Count(next.stderr, "\n") != 1 || !strings.Contains(next.stderr, old.ID) {
		t.Fatalf("the first send that is not refused: exit %d, stderr %q; want 0 and one line naming %s", next.exit, next.stderr, old.ID)
	}
}

func TestExpiryTellsTheOperatorThroughANotice(t *testing.T) {
	setup(t)
	home := socketHome(t)
	calls := fakeHerdr(t)
	old := holdOld(t, home, "lead", message.New("fake:a", "claude:lead", "", "too late", 1), 2*held.Deadline)
	if got := runWith(routeTable(), "", "log"); got.exit != 0 {
		t.Fatalf("log: exit %d; stderr %q", got.exit, got.stderr)
	}
	got := herdrCalls(t, calls)
	if want := fmt.Sprintf("1 held message waited over %d minutes and expired unread: %s from fake:a to claude:lead", int(held.Deadline.Minutes()), old.ID); len(got) != 1 || got[0][0] != "notification" || got[0][1] != "show" || got[0][4] != want {
		t.Fatalf("herdr calls %q, want one notification naming %s and its sender", got, old.ID)
	}
}

func TestExpirySkipsAHeldFileThatIsNotJSON(t *testing.T) {
	setup(t)
	home := socketHome(t)
	a := holdOld(t, home, "a", message.New("fake:s", "claude:a", "", "too late", 1), 2*held.Deadline)
	if err := os.WriteFile(heldFile(home, "b"), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := holdOld(t, home, "c", message.New("fake:s", "claude:c", "", "too late", 1), 2*held.Deadline)
	routes := routeTable()
	routes["fake"] = &fakeRoute{}
	got := runWith(routes, "", "log")
	if got.exit != 0 || strings.Count(got.stderr, "\n") != 1 || !strings.Contains(got.stderr, heldFile(home, "b")) {
		t.Fatalf("log: exit %d, stderr %q; want 0 and one warning naming %s", got.exit, got.stderr, heldFile(home, "b"))
	}
	if ids := append(heldIDs(t, home, "a"), heldIDs(t, home, "c")...); len(ids) != 0 {
		t.Fatalf("still held %v, want a and c expired", ids)
	}
	if r := fmt.Sprint(results(journalLines(t, home))); r != "[expired expired]" {
		t.Fatalf("journal results %s, want two expired lines", r)
	}
	next := runWith(routes, "", "send", "--from", "fake:s", "--to", "fake:w1", "hello")
	if next.exit != 0 || !strings.Contains(next.stderr, "\ncrewtalk send: 2 held messages from fake:s ") || !strings.Contains(next.stderr, a.ID) || !strings.Contains(next.stderr, c.ID) {
		t.Fatalf("the sender's next send: exit %d, stderr %q; want 0 and both expired ids %s and %s", next.exit, next.stderr, a.ID, c.ID)
	}
}

func heldLockFree(t *testing.T, home string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, unlock, err := held.Lock(ctx, home, func(error) {})
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	return true
}

func TestHeldLockStaysHeldUntilTheSocketIsOpen(t *testing.T) {
	home := socketHome(t)
	l, err := claude.Claim("lead")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	opening, ready, _ := heldGate(home, "lead", func(error) {})
	readies := 0
	onReady := func() {
		readies++
		if _, err := os.Lstat(filepath.Join(home, "inbox", "lead.sock")); err != nil {
			t.Errorf("the socket is not open at the ready step: %v", err)
		}
		if heldLockFree(t, home) {
			t.Error("the held lock was free when the socket opened, so a send could hold a message nobody takes")
		}
		ready()
		if !heldLockFree(t, home) {
			t.Error("the ready step left the held lock taken, so every send would wait for it")
		}
	}
	err = l.Receive(300*time.Millisecond, opening, onReady, func(message.Message) error { return nil })
	if !errors.Is(err, os.ErrDeadlineExceeded) || readies != 1 {
		t.Fatalf("Receive: %v after %d ready steps; want the timeout after 1", err, readies)
	}
}

func TestHeldCheckRunsWhenTheSocketReopens(t *testing.T) {
	home := socketHome(t)
	l, err := claude.Claim("lead")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	opening, ready, waiting := heldGate(home, "lead", func(error) {})
	m := message.New("claude:a", "claude:lead", "t", "held during a stalled connection", 1)
	onReady := func() {
		ready()
		box, unlock, err := held.Lock(context.Background(), home, func(error) {})
		if err != nil {
			t.Error(err)
			return
		}
		defer unlock()
		if err := box.Hold("lead", m); err != nil {
			t.Error(err)
		}
		conn, err := net.Dial("unix", filepath.Join(home, "inbox", "lead.sock"))
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("not a frame\n")); err != nil {
			t.Error(err)
		}
	}
	err = l.Receive(5*time.Second, opening, onReady, func(message.Message) error {
		t.Error("a bad frame reached the handler")
		return nil
	})
	if !errors.Is(err, errHeldFirst) || waiting.ID != m.ID {
		t.Fatalf("Receive: %v, waiting %q; want the held message %q first", err, waiting.ID, m.ID)
	}
}
