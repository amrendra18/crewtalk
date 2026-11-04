package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/amrendra18/crewtalk/internal/route"
)

func joinAll(t *testing.T, routes map[string]route.Route, joins ...[]string) {
	t.Helper()
	for _, args := range joins {
		if got := runWith(routes, "", append([]string{"join"}, args...)...); got.exit != 0 {
			t.Fatalf("join %v: exit %d; stderr %q", args, got.exit, got.stderr)
		}
	}
}

func whoLines(t *testing.T, routes map[string]route.Route) string {
	t.Helper()
	got := runWith(routes, "", "who")
	if got.exit != 0 {
		t.Fatalf("who: exit %d; stderr %q", got.exit, got.stderr)
	}
	return got.stdout
}

func TestJoinAddsOrReplacesAName(t *testing.T) {
	setup(t)
	socketHome(t)
	routes := map[string]route.Route{"fake": &fakeRoute{}}
	joinAll(t, routes, []string{"--as", "w1", "--kind", "fake", "--address", "a1"})
	if got := whoLines(t, routes); got != "w1 fake:a1\n" {
		t.Fatalf("who after one join = %q", got)
	}
	joinAll(t, routes, []string{"--as", "w1", "--kind", "fake", "--address", "a2"})
	if got := whoLines(t, routes); got != "w1 fake:a2\n" {
		t.Fatalf("who after a second join of w1 = %q, want only the new address", got)
	}
}

func TestJoinDefaultsToTheSessionAddress(t *testing.T) {
	setup(t)
	socketHome(t)
	routes := map[string]route.Route{"fake": &fakeRoute{session: "s1"}}
	joinAll(t, routes, []string{"--as", "w1"}, []string{"--as", "w2", "--kind", "fake"})
	if got := whoLines(t, routes); got != "w1 fake:s1\nw2 fake:s1\n" {
		t.Fatalf("who = %q, want both names at the session address", got)
	}
}

func TestJoinCodexSessionJoinsAsItself(t *testing.T) {
	setup(t)
	socketHome(t)
	t.Setenv("CODEX_THREAD_ID", codexThread)
	joinAll(t, routeTable(), []string{"--as", "w1"})
	if got := whoLines(t, routeTable()); got != "w1 codex:"+codexThread+"\n" {
		t.Fatalf("who = %q, want w1 at this Codex thread", got)
	}
}

func TestJoinClaudeAddressDefaultsToTheName(t *testing.T) {
	setup(t)
	socketHome(t)
	joinAll(t, routeTable(), []string{"--as", "lead", "--kind", "claude"})
	if got := whoLines(t, routeTable()); got != "lead claude:lead\n" {
		t.Fatalf("who = %q, want lead claude:lead", got)
	}
}

func TestJoinSocketPathLimitIsTheClaudeRouteRule(t *testing.T) {
	setup(t)
	name := strings.Repeat("w", 104)
	joinAll(t, routeTable(), []string{"--as", name, "--kind", "codex", "--address", codexThread})
	if got := whoLines(t, routeTable()); got != name+" codex:"+codexThread+"\n" {
		t.Fatalf("who = %q, want %s at the Codex thread", got, name)
	}
	got := runWith(routeTable(), "", "join", "--as", name, "--kind", "claude")
	if got.exit != 64 || !strings.Contains(got.stderr, "socket path") {
		t.Fatalf("claude join of a name past the socket limit: exit %d, stderr %q; want 64 naming the socket path", got.exit, got.stderr)
	}
}

func TestJoinUsage(t *testing.T) {
	cases := map[string][]string{
		"no name":                  {"join"},
		"space in name":            {"join", "--as", "a b", "--kind", "claude"},
		"colon in name":            {"join", "--as", "a:b", "--kind", "claude"},
		"slash in name":            {"join", "--as", "a/b", "--kind", "claude"},
		"unknown kind":             {"join", "--as", "w1", "--kind", "pi", "--address", "x"},
		"no kind and no session":   {"join", "--as", "w1"},
		"codex with no address":    {"join", "--as", "w1", "--kind", "codex"},
		"codex address not a uuid": {"join", "--as", "w1", "--kind", "codex", "--address", "not-a-uuid"},
		"claude address bad":       {"join", "--as", "w1", "--kind", "claude", "--address", "a/b"},
		"extra argument":           {"join", "--as", "w1", "--kind", "claude", "now"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t)
			home := socketHome(t)
			got := runWith(routeTable(), "", args...)
			if got.exit != 64 || got.stderr == "" {
				t.Fatalf("exit %d, stderr %q; want 64 and the reason", got.exit, got.stderr)
			}
			if entries, _ := os.ReadDir(home); len(entries) != 0 {
				t.Fatalf("CREWTALK_HOME holds %v after a usage error, want nothing", entries)
			}
		})
	}
}

func TestJoinWaitJoinsAsClaude(t *testing.T) {
	setup(t)
	socketHome(t)
	if got := runWith(routeTable(), "", "wait", "--as", "lead", "--timeout", "100ms"); got.exit != 4 {
		t.Fatalf("wait exit %d, want 4; stderr %q", got.exit, got.stderr)
	}
	if got := whoLines(t, routeTable()); got != "lead claude:lead\n" {
		t.Fatalf("who after wait = %q, want lead claude:lead", got)
	}
}

func TestJoinWaitWarnsWhenItReplacesAnotherKind(t *testing.T) {
	setup(t)
	socketHome(t)
	joinAll(t, routeTable(), []string{"--as", "worker-a", "--kind", "codex", "--address", codexThread})
	got := runWith(routeTable(), "", "wait", "--as", "worker-a", "--timeout", "100ms")
	if got.exit != 4 || strings.Count(got.stderr, "\n") != 1 || !strings.Contains(got.stderr, "codex:"+codexThread) || !strings.Contains(got.stderr, "claude:worker-a") {
		t.Fatalf("wait over a codex entry: exit %d, stderr %q; want 4 and one warning naming both entries", got.exit, got.stderr)
	}
	if got := whoLines(t, routeTable()); got != "worker-a claude:worker-a\n" {
		t.Fatalf("who after wait = %q, want worker-a claude:worker-a", got)
	}
	if got := runWith(routeTable(), "", "wait", "--as", "worker-a", "--timeout", "100ms"); got.exit != 4 || got.stderr != "" {
		t.Fatalf("wait over its own claude entry: exit %d, stderr %q; want 4 and no warning", got.exit, got.stderr)
	}
}

func TestJoinJSONPrintsTheEntry(t *testing.T) {
	setup(t)
	socketHome(t)
	routes := map[string]route.Route{"fake": &fakeRoute{}}
	got := runWith(routes, "", "join", "--as", "w1", "--kind", "fake", "--address", "a1", "--json")
	if want := `{"name":"w1","kind":"fake","address":"a1"}`; got.exit != 0 || strings.TrimSpace(got.stdout) != want {
		t.Fatalf("join --json: exit %d, stdout %q, stderr %q; want 0 and %s", got.exit, got.stdout, got.stderr, want)
	}
}

func TestJoinHelpNamesTheRouteKinds(t *testing.T) {
	routes := map[string]route.Route{"fake": &fakeRoute{}, "other": &fakeRoute{}}
	got := runWith(routes, "", "join", "--help")
	if got.exit != 0 || !strings.Contains(got.stdout, "fake, other") {
		t.Fatalf("join --help: exit %d, stdout %q; want 0 and the kinds fake, other", got.exit, got.stdout)
	}
}

func TestJoinNamesAddressSend(t *testing.T) {
	setup(t)
	home := socketHome(t)
	f := &fakeRoute{}
	routes := map[string]route.Route{"fake": f}
	joinAll(t, routes, []string{"--as", "lead", "--kind", "fake", "--address", "c0"}, []string{"--as", "w1", "--kind", "fake", "--address", "a1"})
	if got := crewtalk(f, "", "send", "--from", "lead", "--to", "w1", "hello"); got.exit != 0 {
		t.Fatalf("send by name: exit %d; stderr %q", got.exit, got.stderr)
	}
	if f.to[0] != (route.Agent{Address: "a1"}) || f.sent[0].From != "fake:c0" || f.sent[0].To != "fake:a1" {
		t.Fatalf("sent %+v to %+v, want fake:c0 to fake:a1", f.sent[0], f.to[0])
	}
	if lines := journalLines(t, home); len(lines) != 1 || lines[0]["from"] != "fake:c0" || lines[0]["to"] != "fake:a1" {
		t.Fatalf("journal %v, want one line from fake:c0 to fake:a1", lines)
	}
}

func TestJoinUnknownNameIsReceiverUnknown(t *testing.T) {
	setup(t)
	home := socketHome(t)
	f := &fakeRoute{}
	got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "w9", "--thread", "proj-a", "hello")
	if want := "crewtalk send: receiver-unknown: \"w9\" is no joined name; see crewtalk who\n"; got.exit != 4 || got.stderr != want {
		t.Fatalf("exit %d, stderr %q; want 4 and %q", got.exit, got.stderr, want)
	}
	if len(f.sent) != 0 {
		t.Fatal("a send to an unknown name reached the route")
	}
	lines := journalLines(t, home)
	if len(lines) != 1 || lines[0]["result"] != "receiver-unknown" || lines[0]["to"] != "w9" || lines[0]["thread"] != "proj-a" || lines[0]["route"] != "" {
		t.Fatalf("journal %v, want one receiver-unknown line to w9 with no route", lines)
	}
}

func TestWhoListsOneAgentPerLineByName(t *testing.T) {
	setup(t)
	socketHome(t)
	routes := map[string]route.Route{"fake": &fakeRoute{}}
	joinAll(t, routes,
		[]string{"--as", "w2", "--kind", "fake", "--address", "a2"},
		[]string{"--as", "lead", "--kind", "fake", "--address", "c0"},
		[]string{"--as", "w1", "--kind", "fake", "--address", "a1"},
	)
	if got := whoLines(t, routes); got != "lead fake:c0\nw1 fake:a1\nw2 fake:a2\n" {
		t.Fatalf("who = %q", got)
	}
}

func TestWhoJSONPrintsTheBook(t *testing.T) {
	setup(t)
	socketHome(t)
	routes := map[string]route.Route{"fake": &fakeRoute{}}
	for _, c := range []struct {
		joins [][]string
		want  string
	}{
		{nil, `{"agents":[]}`},
		{[][]string{{"--as", "w1", "--kind", "fake", "--address", "a1"}}, `{"agents":[{"name":"w1","kind":"fake","address":"a1"}]}`},
	} {
		joinAll(t, routes, c.joins...)
		got := runWith(routes, "", "who", "--json")
		if got.exit != 0 || strings.TrimSpace(got.stdout) != c.want {
			t.Fatalf("who --json: exit %d, stdout %q; want 0 and %s", got.exit, got.stdout, c.want)
		}
		if !json.Valid([]byte(got.stdout)) {
			t.Fatalf("who --json printed %q, not JSON", got.stdout)
		}
	}
}

func TestWhoWithNoBookPrintsNothing(t *testing.T) {
	setup(t)
	home := socketHome(t)
	if got := whoLines(t, routeTable()); got != "" {
		t.Fatalf("who = %q, want nothing", got)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("who created %v", entries)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("standard output is closed") }

func TestWhoUnwritableOutputExits5(t *testing.T) {
	setup(t)
	socketHome(t)
	routes := map[string]route.Route{"fake": &fakeRoute{}}
	joinAll(t, routes, []string{"--as", "w1", "--kind", "fake", "--address", "a1"})
	for _, args := range [][]string{{"who"}, {"who", "--json"}} {
		var stderr strings.Builder
		if exit := run(args, strings.NewReader(""), brokenWriter{}, &stderr, routes); exit != 5 || !strings.Contains(stderr.String(), "closed") {
			t.Fatalf("%v with a closed output: exit %d, stderr %q; want 5 and the reason", args, exit, stderr.String())
		}
	}
}

func TestLeaveRemovesAName(t *testing.T) {
	setup(t)
	socketHome(t)
	routes := map[string]route.Route{"fake": &fakeRoute{}}
	joinAll(t, routes, []string{"--as", "w1", "--kind", "fake", "--address", "a1"}, []string{"--as", "w2", "--kind", "fake", "--address", "a2"})
	if got := runWith(routes, "", "leave", "--as", "w1"); got.exit != 0 || got.stdout != "left w1, which was fake:a1\n" {
		t.Fatalf("leave: exit %d, stdout %q, stderr %q; want 0 and the removed entry", got.exit, got.stdout, got.stderr)
	}
	if got := whoLines(t, routes); got != "w2 fake:a2\n" {
		t.Fatalf("who after leave = %q, want only w2", got)
	}
	got := runWith(routes, "", "leave", "--as", "w1")
	if got.exit != 4 || got.stdout != "" || !strings.Contains(got.stderr, "w1") {
		t.Fatalf("leave of a name nobody joined: exit %d, stdout %q, stderr %q; want 4 and a reason naming w1", got.exit, got.stdout, got.stderr)
	}
}

func TestLeaveJSONPrintsTheRemovedEntry(t *testing.T) {
	setup(t)
	socketHome(t)
	routes := map[string]route.Route{"fake": &fakeRoute{}}
	joinAll(t, routes, []string{"--as", "w1", "--kind", "fake", "--address", "a1"})
	got := runWith(routes, "", "leave", "--as", "w1", "--json")
	if want := `{"name":"w1","kind":"fake","address":"a1"}`; got.exit != 0 || strings.TrimSpace(got.stdout) != want {
		t.Fatalf("leave --json: exit %d, stdout %q, stderr %q; want 0 and %s", got.exit, got.stdout, got.stderr, want)
	}
}

func TestLeaveUsage(t *testing.T) {
	cases := map[string][]string{
		"no name":        {"leave"},
		"extra argument": {"leave", "--as", "w1", "now"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t)
			home := socketHome(t)
			got := runWith(routeTable(), "", args...)
			if got.exit != 64 || got.stderr == "" {
				t.Fatalf("exit %d, stderr %q; want 64 and the reason", got.exit, got.stderr)
			}
			if entries, _ := os.ReadDir(home); len(entries) != 0 {
				t.Fatalf("CREWTALK_HOME holds %v after a usage error, want nothing", entries)
			}
		})
	}
}
