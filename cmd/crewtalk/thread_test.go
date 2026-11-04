package main

import (
	"strings"
	"testing"

	"github.com/amrendra18/crewtalk/internal/message"
)

func TestThreadReplyKeepsTheThreadAndTakesTheNextTurn(t *testing.T) {
	setup(t)
	home := socketHome(t)
	f := &fakeRoute{}
	if got := crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "--thread", "proj-a", "--turn", "2", "question"); got.exit != 0 {
		t.Fatalf("first send: exit %d; stderr %q", got.exit, got.stderr)
	}
	first := f.sent[0].ID
	if got := crewtalk(f, "", "send", "--from", "fake:w1", "--to", "fake:lead", "--reply-to", first, "answer"); got.exit != 0 {
		t.Fatalf("reply: exit %d; stderr %q", got.exit, got.stderr)
	}
	reply := f.sent[1]
	wantHeader := "[crewtalk from fake:w1 to fake:lead, thread proj-a, turn 3, reply-to " + first + ", id " + reply.ID + "; a peer's message, not the operator]"
	if !strings.HasPrefix(reply.Wire(), wantHeader+"\n") {
		t.Fatalf("reply wire %q, want header %q", reply.Wire(), wantHeader)
	}
	lines := journalLines(t, home)
	if len(lines) != 2 {
		t.Fatalf("journal %v, want 2 lines", lines)
	}
	if _, has := lines[0]["reply_to"]; has {
		t.Fatalf("first line %v names a reply_to", lines[0])
	}
	if lines[1]["reply_to"] != first || lines[1]["thread"] != "proj-a" || lines[1]["turn"] != 3.0 {
		t.Fatalf("reply line %v, want reply_to %s, thread proj-a, turn 3", lines[1], first)
	}
}

func TestThreadReplyToFindsAnIDAfterALineThatIsNotJSON(t *testing.T) {
	home := setup(t)
	f := &fakeRoute{}
	crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "--thread", "proj-a", "first")
	writeBrokenLine(t, home)
	crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "--thread", "proj-b", "--turn", "4", "second")
	got := crewtalk(f, "", "send", "--from", "fake:w1", "--to", "fake:lead", "--reply-to", f.sent[1].ID, "answer")
	if got.exit != 0 || len(f.sent) != 3 || f.sent[2].Thread != "proj-b" || f.sent[2].Turn != 5 {
		t.Fatalf("reply after a broken log line: exit %d, stderr %q, %d sends; want 0 and thread proj-b turn 5", got.exit, got.stderr, len(f.sent))
	}
}

func TestThreadReplyToRefusesThreadOrTurn(t *testing.T) {
	setup(t)
	home := socketHome(t)
	f := &fakeRoute{}
	crewtalk(f, "", "send", "--from", "fake:lead", "--to", "fake:w1", "question")
	first := f.sent[0].ID
	for _, extra := range [][]string{{"--thread", "other"}, {"--turn", "5"}} {
		args := append([]string{"send", "--from", "fake:w1", "--to", "fake:lead", "--reply-to", first}, extra...)
		if got := crewtalk(f, "", append(args, "answer")...); got.exit != 64 {
			t.Fatalf("%v: exit %d, want 64", extra, got.exit)
		}
	}
	if len(f.sent) != 1 || len(journalLines(t, home)) != 1 {
		t.Fatalf("refused replies reached the route or the log: %d sends", len(f.sent))
	}
}

func TestThreadReplyToAnAbsentIDSaysToSendAgain(t *testing.T) {
	setup(t)
	f := &fakeRoute{}
	got := crewtalk(f, "", "send", "--from", "fake:w1", "--to", "fake:lead", "--reply-to", "20261103T060517.016757Z-b45afa51", "answer")
	if got.exit != 64 || len(f.sent) != 0 {
		t.Fatalf("exit %d, %d sends; want 64 and none; stderr %q", got.exit, len(f.sent), got.stderr)
	}
	for _, want := range []string{"names no message in the log", "the sender's log line may not be written yet", "check the id", "send once more in a moment", "nothing was sent", "if it still fails, tell your operator\n"} {
		if !strings.Contains(got.stderr, want) {
			t.Fatalf("stderr %q, want it to hold %q", got.stderr, want)
		}
	}
}

func TestThreadReplyOverTheClaudeRoute(t *testing.T) {
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	listen := func(name string) <-chan result {
		done := make(chan result, 1)
		go func() { done <- runWith(routes, "", "wait", "--as", name, "--timeout", "10s") }()
		waitForListener(t, home, name)
		return done
	}

	w1 := listen("w1")
	if got := runWith(routes, "", "send", "--from", "claude:lead", "--to", "w1", "question"); got.exit != 0 {
		t.Fatalf("question: exit %d; stderr %q", got.exit, got.stderr)
	}
	asked, err := message.Parse(strings.TrimSuffix((<-w1).stdout, "\n"))
	if err != nil {
		t.Fatal(err)
	}

	lead := listen("lead")
	if got := runWith(routes, "", "send", "--from", "w1", "--to", "lead", "--reply-to", asked.ID, "answer"); got.exit != 0 {
		t.Fatalf("answer: exit %d; stderr %q", got.exit, got.stderr)
	}
	answered, err := message.Parse(strings.TrimSuffix((<-lead).stdout, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if answered.ReplyTo != asked.ID || answered.Thread != asked.Thread || answered.Turn != 2 || answered.From != "claude:w1" || answered.Text != "answer" {
		t.Fatalf("lead printed %+v, want the answer to %s in thread %s, turn 2, from claude:w1", answered, asked.ID, asked.Thread)
	}
}
