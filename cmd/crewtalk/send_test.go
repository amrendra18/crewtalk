package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
)

const codexThread = "019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a"

type fakeRoute struct {
	err      error
	session  string
	presence route.Presence
	during   func()
	sent     []message.Message
	to       []route.Agent
}

func (*fakeRoute) Kind() string { return "fake" }
func (*fakeRoute) CheckAddress(address string) error {
	if strings.Contains(address, "/") {
		return errors.New("a fake address holds no slash")
	}
	return nil
}
func (f *fakeRoute) SessionAddress() string         { return f.session }
func (*fakeRoute) DefaultAddress(string) string     { return "" }
func (f *fakeRoute) Presence(string) route.Presence { return f.presence }
func (f *fakeRoute) Deliver(_ context.Context, to route.Agent, m message.Message) error {
	f.sent = append(f.sent, m)
	f.to = append(f.to, to)
	if f.during != nil {
		f.during()
	}
	return f.err
}

type result struct {
	stdout, stderr string
	exit           int
}

func setup(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CREWTALK_HOME", home)
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("HERDR_ENV", "")
	return home
}

func crewtalk(f *fakeRoute, stdin string, args ...string) result {
	return runWith(map[string]route.Route{"fake": f}, stdin, args...)
}

func runWith(routes map[string]route.Route, stdin string, args ...string) result {
	var stdout, stderr bytes.Buffer
	exit := run(args, strings.NewReader(stdin), &stdout, &stderr, routes)
	return result{stdout.String(), stderr.String(), exit}
}

func journalLines(t *testing.T, home string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(home, "log.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []map[string]any
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			t.Fatalf("journal line %q: %v", scan.Text(), err)
		}
		lines = append(lines, line)
	}
	return lines
}

var routeResults = []struct {
	name   string
	err    error
	exit   int
	result string
}{
	{"delivered", nil, 0, "delivered"},
	{"not listening", fmt.Errorf("claude:lead: %w", route.ErrNotListening), 4, "not-listening"},
	{"receiver unknown", route.ErrReceiverUnknown, 4, "receiver-unknown"},
	{"outcome unknown", fmt.Errorf("codex queue timed out: %w", route.ErrUnknownOutcome), 5, "outcome-unknown"},
	{"route failed", errors.New("codex queue exit 2: usage"), 5, "failed"},
}

func TestSendExitCodesPerRouteResult(t *testing.T) {
	for _, c := range routeResults {
		t.Run(c.name, func(t *testing.T) {
			setup(t)
			f := &fakeRoute{err: c.err}
			got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "hello")
			if got.exit != c.exit {
				t.Fatalf("exit = %d, want %d; stderr %q", got.exit, c.exit, got.stderr)
			}
			if len(f.sent) != 1 {
				t.Fatalf("route called %d times, want 1", len(f.sent))
			}
			if c.err != nil && !strings.Contains(got.stderr, c.err.Error()) {
				t.Fatalf("stderr %q does not name the route error %q", got.stderr, c.err)
			}
		})
	}
}

func TestSendExitCodesUsage(t *testing.T) {
	cases := map[string][]string{
		"no command":      {},
		"unknown command": {"talk"},
		"no flags":        {"send"},
		"no to":           {"send", "--from", "fake:lead", "hello"},
		"unknown kind":    {"send", "--from", "fake:lead", "--to", "pi:w1", "hello"},
		"empty address":   {"send", "--from", "fake:lead", "--to", "fake:", "hello"},
		"no text":         {"send", "--from", "fake:lead", "--to", "fake:w1"},
		"empty text":      {"send", "--from", "fake:lead", "--to", "fake:w1", ""},
		"two texts":       {"send", "--from", "fake:lead", "--to", "fake:w1", "a", "b"},
		"turn zero":       {"send", "--from", "fake:lead", "--to", "fake:w1", "--turn", "0", "hello"},
		"comma in thread": {"send", "--from", "fake:lead", "--to", "fake:w1", "--thread", "a,b", "hello"},
		"space in from":   {"send", "--from", "fake:c os", "--to", "fake:w1", "hello"},
		"comma in to":     {"send", "--from", "fake:lead", "--to", "fake:w,1", "hello"},
		"no from":         {"send", "--to", "fake:w1", "hello"},
		"from no kind":    {"send", "--from", "lead", "--to", "fake:w1", "hello"},
		"to name comma":   {"send", "--from", "fake:lead", "--to", "w,1", "hello"},
		"reply-to empty":  {"send", "--from", "fake:lead", "--to", "fake:w1", "--reply-to", "", "hello"},
		"reply-to absent": {"send", "--from", "fake:lead", "--to", "fake:w1", "--reply-to", "20261103T060517.016757Z-b45afa51", "hello"},
		"join without as": {"join"},
		"join extra arg":  {"join", "--as", "w1", "--kind", "fake", "--address", "a1", "now"},
		"who extra arg":   {"who", "now"},
		"log extra arg":   {"log", "now"},
		"from no name":    {"send", "--from", "fake:", "--to", "fake:w1", "hello"},
		"from slash name": {"send", "--from", "fake:a/b", "--to", "fake:w1", "hello"},
		"to slash name":   {"send", "--from", "fake:w1", "--to", "fake:a/b", "hello"},
		"from bad kind":   {"send", "--from", "nokind:x", "--to", "fake:w1", "hello"},
		"unknown flag":    {"send", "--priority", "high", "--to", "fake:w1", "hello"},
		"wait without as": {"wait"},
		"wait bad time":   {"wait", "--as", "lead", "--timeout", "soon"},
		"wait negative":   {"wait", "--as", "lead", "--timeout", "-1s"},
		"wait zero":       {"wait", "--as", "lead", "--timeout", "0"},
		"wait minus 1ns":  {"wait", "--as", "lead", "--timeout", "-1ns"},
		"wait extra arg":  {"wait", "--as", "lead", "now"},
		"wait space name": {"wait", "--as", "a b", "--timeout", "1s"},
		"wait slash name": {"wait", "--as", "a/b", "--timeout", "1s"},
		"wait comma name": {"wait", "--as", "a,b", "--timeout", "1s"},
		"escape in from":  {"send", "--from", "fake:a\x1bcb", "--to", "fake:w1", "hello"},
		"c1 in from":      {"send", "--from", "fake:a\u009b2Jb", "--to", "fake:w1", "hello"},
		"bad utf-8 from":  {"send", "--from", "fake:a\x9bb", "--to", "fake:w1", "hello"},
		"escape in to":    {"send", "--from", "fake:lead", "--to", "fake:w\x1bc1", "hello"},
		"to name escape":  {"send", "--from", "fake:lead", "--to", "w\x1bc1", "hello"},
		"thread escape":   {"send", "--from", "fake:lead", "--to", "fake:w1", "--thread", "t\x1bc", "hello"},
		"join escape":     {"join", "--as", "w\x1bc1", "--kind", "fake", "--address", "a1"},
		"wait escape":     {"wait", "--as", "w\x1bc1", "--timeout", "1s"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t)
			home := socketHome(t)
			f := &fakeRoute{}
			got := crewtalk(f, "", args...)
			if got.exit != 64 {
				t.Fatalf("exit = %d, want 64; stderr %q", got.exit, got.stderr)
			}
			if got.stderr == "" {
				t.Fatal("usage error printed nothing to stderr")
			}
			if len(f.sent) != 0 {
				t.Fatal("route called on a usage error")
			}
			if n := len(journalLines(t, home)); n != 0 {
				t.Fatalf("usage error wrote %d journal lines, want 0", n)
			}
		})
	}
}

func TestSendExitCodesHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"send", "--help"}, {"wait", "--help"}, {"join", "--help"}, {"who", "--help"}, {"log", "--help"}} {
		setup(t)
		got := crewtalk(&fakeRoute{}, "", args...)
		if got.exit != 0 || !strings.Contains(got.stdout, "usage: crewtalk") {
			t.Errorf("%v: exit %d, stdout %q; want 0 and usage text", args, got.exit, got.stdout)
		}
	}
}

func TestLogOneLinePerSendForEveryResult(t *testing.T) {
	for _, c := range routeResults {
		t.Run(c.name, func(t *testing.T) {
			home := setup(t)
			f := &fakeRoute{err: c.err}
			crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "--thread", "proj-a", "--turn", "3", "hello")
			lines := journalLines(t, home)
			if len(lines) != 1 {
				t.Fatalf("got %d journal lines, want 1", len(lines))
			}
			l := lines[0]
			want := map[string]any{"id": f.sent[0].ID, "from": "fake:lead", "to": "fake:w1", "thread": "proj-a", "turn": 3.0, "route": "fake", "result": c.result}
			for k, v := range want {
				if l[k] != v {
					t.Errorf("journal %s = %v, want %v", k, l[k], v)
				}
			}
			if _, ok := l["time"].(string); !ok {
				t.Errorf("journal time = %v, want a time string", l["time"])
			}
		})
	}
}

func TestLogOneLineAppendsAcrossSends(t *testing.T) {
	home := setup(t)
	for i := range 3 {
		crewtalk(&fakeRoute{}, "", "send", "--from", "fake:lead", "--to", "fake:w1", fmt.Sprint("message ", i))
	}
	if n := len(journalLines(t, home)); n != 3 {
		t.Fatalf("3 sends wrote %d journal lines, want 3", n)
	}
}

func TestSendJSONPrintsOneObject(t *testing.T) {
	setup(t)
	f := &fakeRoute{err: route.ErrNotListening}
	got := crewtalk(f, "", "send", "--json", "--from", "fake:lead", "--to", "fake:w1", "hello")
	dec := json.NewDecoder(strings.NewReader(got.stdout))
	var report map[string]any
	if err := dec.Decode(&report); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", got.stdout, err)
	}
	if dec.More() {
		t.Fatalf("stdout %q holds more than one JSON object", got.stdout)
	}
	if report["id"] != f.sent[0].ID || report["result"] != "not-listening" || report["error"] == nil {
		t.Fatalf("report = %v, want id, result not-listening and error", report)
	}
	if got.exit != 4 {
		t.Fatalf("exit = %d, want 4", got.exit)
	}
}

func TestSendMessageFields(t *testing.T) {
	setup(t)
	f := &fakeRoute{}
	got := crewtalk(f, "line one\nline two\n", "send", "--from", "fake:6f1c", "--to", "fake:w1", "--turn", "2", "-")
	if got.exit != 0 {
		t.Fatalf("exit = %d; stderr %q", got.exit, got.stderr)
	}
	m := f.sent[0]
	if m.Text != "line one\nline two\n" || m.Turn != 2 || m.To != "fake:w1" || m.Thread != m.ID {
		t.Fatalf("message = %+v", m)
	}
	if f.to[0] != (route.Agent{Address: "w1"}) {
		t.Fatalf("agent = %+v", f.to[0])
	}
	if !strings.Contains(got.stdout, m.ID) || !strings.Contains(got.stdout, m.Thread) {
		t.Fatalf("stdout %q does not name the id and thread", got.stdout)
	}
}

func TestFromDefault(t *testing.T) {
	cases := []struct {
		name, session string
		args          []string
		want          string
	}{
		{"session address", "6f1c", nil, "fake:6f1c"},
		{"flag wins", "6f1c", []string{"--from", "fake:lead"}, "fake:lead"},
		{"flag with no session address", "", []string{"--from", "fake:lead"}, "fake:lead"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setup(t)
			f := &fakeRoute{session: c.session}
			args := append(append([]string{"send"}, c.args...), "--to", "fake:w1", "hello")
			if got := crewtalk(f, "", args...); got.exit != 0 {
				t.Fatalf("exit = %d; stderr %q", got.exit, got.stderr)
			}
			if f.sent[0].From != c.want {
				t.Fatalf("from = %q, want %q", f.sent[0].From, c.want)
			}
		})
	}
}

func TestRealRoutesRefuseBadAddresses(t *testing.T) {
	long := strings.Repeat("n", 90)
	cases := map[string][]string{
		"to claude name over the socket limit":   {"send", "--from", "codex:" + codexThread, "--to", "claude:" + long, "hi"},
		"from claude name over the socket limit": {"send", "--from", "claude:" + long, "--to", "codex:" + codexThread, "hi"},
		"wait name over the socket limit":        {"wait", "--as", long, "--timeout", "1s"},
		"to codex thread with a slash":           {"send", "--from", "claude:lead", "--to", "codex:x/y", "hi"},
		"from codex thread not a uuid":           {"send", "--from", "codex:not-a-uuid", "--to", "claude:lead", "hi"},
		"from claude name with an escape":        {"send", "--from", "claude:a\x1bcb", "--to", "codex:" + codexThread, "hi"},
		"to claude name with an escape":          {"send", "--from", "codex:" + codexThread, "--to", "claude:a\x1bcb", "hi"},
		"wait name with an escape":               {"wait", "--as", "a\x1bcb", "--timeout", "1s"},
		"join name with an escape":               {"join", "--as", "a\x1bcb", "--kind", "codex", "--address", codexThread},
		"join claude address with an escape":     {"join", "--as", "a", "--kind", "claude", "--address", "b\x1bcc"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t)
			home := socketHome(t)
			guard := t.TempDir()
			called := filepath.Join(guard, "called")
			if err := os.WriteFile(filepath.Join(guard, "codex"), []byte("#!/bin/sh\ntouch "+called+"\nexit 99\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", guard+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stdout, stderr bytes.Buffer
			if exit := run(args, strings.NewReader(""), &stdout, &stderr, routeTable()); exit != 64 {
				t.Fatalf("exit = %d, want 64; stderr %q", exit, stderr.String())
			}
			if _, err := os.Lstat(called); err == nil {
				t.Fatal("the codex guard was called")
			}
			entries, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("CREWTALK_HOME holds %v after a usage error, want nothing", entries)
			}
		})
	}
}

func TestRouteTableTakesANewKind(t *testing.T) {
	home := setup(t)
	f := &fakeRoute{session: "w0"}
	routes := routeTable()
	routes[f.Kind()] = f
	var stdout, stderr bytes.Buffer
	if exit := run([]string{"send", "--to", "fake:w1", "hello"}, strings.NewReader(""), &stdout, &stderr, routes); exit != 0 {
		t.Fatalf("exit = %d; stderr %q", exit, stderr.String())
	}
	if len(f.sent) != 1 || f.sent[0].From != "fake:w0" || f.to[0] != (route.Agent{Address: "w1"}) {
		t.Fatalf("sent %+v to %+v, want one message from fake:w0 to w1", f.sent, f.to)
	}
	if lines := journalLines(t, home); len(lines) != 1 || lines[0]["route"] != "fake" {
		t.Fatalf("journal %v, want one line with route fake", lines)
	}
}

func TestSendDashTextUsageErrorNamesBothWays(t *testing.T) {
	for _, text := range []string{"- item one", "---", "-x"} {
		home := setup(t)
		f := &fakeRoute{}
		got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", text)
		reason, _, _ := strings.Cut(got.stderr, "\n")
		if got.exit != 64 || !strings.Contains(reason, "put -- before it") || !strings.Contains(reason, "standard input") {
			t.Errorf("%q: exit %d, first line %q; want 64 and both ways to send it", text, got.exit, reason)
		}
		if len(f.sent) != 0 || len(journalLines(t, home)) != 0 {
			t.Errorf("%q: the route or the log was used on a usage error", text)
		}
	}
}

func TestSendOtherFlagErrorsDoNotMentionDashText(t *testing.T) {
	setup(t)
	got := crewtalk(&fakeRoute{}, "", "send", "--from", "fake:lead", "--to", "fake:w1", "--turn", "two", "hello")
	reason, _, _ := strings.Cut(got.stderr, "\n")
	if got.exit != 64 || strings.Contains(reason, "put -- before it") {
		t.Fatalf("exit %d, first line %q; want 64 and only the --turn error", got.exit, reason)
	}
}

func TestSendDashTextIsDelivered(t *testing.T) {
	cases := map[string]struct {
		stdin string
		last  []string
	}{
		"after --":       {"", []string{"--", "- item"}},
		"standard input": {"- item", []string{"-"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t)
			f := &fakeRoute{}
			args := append([]string{"send", "--from", "fake:lead", "--to", "fake:w1"}, c.last...)
			got := crewtalk(f, c.stdin, args...)
			if got.exit != 0 || len(f.sent) != 1 || f.sent[0].Text != "- item" {
				t.Fatalf("exit %d, sent %+v, stderr %q; want 0 and one message with the text - item", got.exit, f.sent, got.stderr)
			}
		})
	}
}

func TestSendHelpNamesBothWaysForDashText(t *testing.T) {
	setup(t)
	got := crewtalk(&fakeRoute{}, "", "send", "--help")
	for _, want := range []string{`-- "- item"`, "standard input"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("send --help lacks %q", want)
		}
	}
}
