package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/amrendra18/crewtalk/internal/book"
	"github.com/amrendra18/crewtalk/internal/held"
	"github.com/amrendra18/crewtalk/internal/journal"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/notice"
	"github.com/amrendra18/crewtalk/internal/route"
	"github.com/amrendra18/crewtalk/internal/state"
)

var sendHelp = fmt.Sprintf(`usage: crewtalk send --to AGENT [--from AGENT] [--thread T | --reply-to ID] [--turn N] [--json] TEXT|-

Delivers TEXT, or standard input when TEXT is "-", to one agent session.
The receiver sees a header line that names the sender before the text.
AGENT is a NAME from "crewtalk who", or KIND:ADDRESS.
TEXT that starts with - needs -- before it, as in
crewtalk send --to AGENT -- "- item", or send it on standard input with -.

  --to AGENT         the receiver: a joined NAME, codex:THREAD_UUID or
                     claude:LISTENER_NAME
  --from AGENT       the sender; default codex:$CODEX_THREAD_ID, else
                     required. A Claude session gives the NAME of its
                     crewtalk wait --as, so a reply reaches it
  --thread T         the conversation; default a new id
  --turn N           the turn in the thread; default 1; a turn over %[1]d is
                     refused
  --reply-to ID      answer message ID: take its thread and the next turn,
                     and name ID in the header; not with --thread or --turn
  --json             print one JSON object with the result

Every send that reaches a route, is refused, or names an unknown --to NAME,
writes one line to CREWTALK_HOME/log.jsonl.

A message to a claude agent in the address book that is not listening is
held in CREWTALK_HOME/held/NAME.jsonl, and its next "crewtalk wait --as NAME"
prints it. A held message expires at the first send, wait or log that runs
%[3]d minutes or more after it was held: the log gets an expired line, the
operator gets a notice, and the sender is told on its next send that is not
refused.

A send is refused while CREWTALK_HOME/PAUSE exists, when its turn is over %[1]d,
or when its sender sent %[2]d messages to its receiver in the last hour. A
refusal says why on stderr, and the operator gets a notice.

A notice is a herdr notification, shown only inside herdr (HERDR_ENV=1).

Exit codes:
  0   delivered, or held for the receiver's next crewtalk wait
  3   refused: PAUSE, the turn limit, or the limit per sender and receiver
  4   receiver unknown (a NAME no agent joined) or not listening
  5   route failed, or the outcome is unknown; crewtalk never retries.
      Or the log is busy or not writable, and nothing was sent
  64  usage error
`, maxTurns, maxPairSends, held.Deadline/time.Minute)

const dashTextNote = "; if that is the message text, put -- before it, or send it on standard input with -"

var routeOutcomes = []struct {
	err    error
	result journal.Result
	exit   int
}{
	{route.ErrNotListening, journal.NotListening, exitReceiver},
	{route.ErrReceiverUnknown, journal.ReceiverUnknown, exitReceiver},
	{route.ErrUnknownOutcome, journal.OutcomeUnknown, exitFailed},
}

type sendReport struct {
	journal.Entry
	Error  string `json:"error,omitempty"`
	Logged *bool  `json:"logged,omitempty"`
}

type sendRequest struct {
	from, to, thread, replyTo, text string
	turn                            int
	replying, asJSON                bool
}

type delivery struct {
	m             message.Message
	known         bool
	kind, address string
}

type sendOutcome struct {
	result journal.Result
	exit   int
	err    error
}

func send(args []string, stdin io.Reader, stdout, stderr io.Writer, routes map[string]route.Route) int {
	req, exit, done := parseSend(args, stdin, stdout, stderr, routes)
	if done {
		return exit
	}
	home, err := state.Home()
	if err != nil {
		return notSent(stderr, err)
	}
	d, exit, done := resolveSend(home, req, stderr, routes)
	if done {
		return exit
	}
	warn := warnings(stderr, "send")
	expire(home, warn)
	j, err := journal.Open(home)
	if err != nil {
		if errors.Is(err, state.ErrBusy) {
			return notSent(stderr, fmt.Errorf("the log is busy: %w; send again in a moment; if it stays busy, tell your operator", err))
		}
		log := filepath.Join(home, "log.jsonl")
		return notSent(stderr, fmt.Errorf("the log is not writable: %w; if a file mode is the cause, the operator fixes it with chmod 600 %s %s.lock; in a Codex session this can mean the shell line missed the command rule: put the text in one quoted string with no backslash (single quotes, or double quotes when the text has an apostrophe; reword when it has an apostrophe and also a double quote, backtick or dollar sign), send again, and do not ask to leave the sandbox; if that also fails, tell your operator", err, log, log))
	}
	defer j.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	o, refused, err := checkLimits(home, d.m, time.Now(), warn)
	if err != nil {
		return notSent(stderr, err)
	}
	if refused {
		notice.Show("crewtalk: send refused", fmt.Sprintf("%s: %v", o.result, o.err))
	} else {
		tellSender(home, d.m.From, stderr, warn)
		o = deliver(ctx, home, d, req.to, routes, warn)
	}
	return record(j, d, o, req.asJSON, stdout, stderr)
}

func parseSend(args []string, stdin io.Reader, stdout, stderr io.Writer, routes map[string]route.Route) (req sendRequest, exit int, done bool) {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.StringVar(&req.to, "to", "", "")
	fs.StringVar(&req.from, "from", sessionSender(routes), "")
	fs.StringVar(&req.thread, "thread", "", "")
	fs.IntVar(&req.turn, "turn", 1, "")
	fs.StringVar(&req.replyTo, "reply-to", "", "")
	fs.BoolVar(&req.asJSON, "json", false, "")
	if exit, done := parseFlagsWithNote(fs, sendHelp, dashTextNote, args, stdout, stderr); done {
		return req, exit, true
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	req.replying = given["reply-to"]

	if fs.NArg() != 1 {
		return req, usageError(stderr, sendHelp, "send takes exactly one TEXT, or - for standard input"), true
	}
	req.text = fs.Arg(0)
	if req.text == "-" {
		in, err := io.ReadAll(stdin)
		if err != nil {
			return req, usageError(stderr, sendHelp, "reading standard input: "+err.Error()), true
		}
		req.text = string(in)
	}
	reason := ""
	switch {
	case req.text == "":
		reason = "the message text is empty"
	case req.from == "":
		reason = "--from is required, because no route gives this session's address"
	case req.to == "":
		reason = "--to is required"
	case req.turn < 1:
		reason = "--turn must be 1 or more"
	case req.replying && (given["thread"] || given["turn"]):
		reason = "--reply-to sets the thread and the turn; leave out --thread and --turn"
	default:
		return req, 0, false
	}
	return req, usageError(stderr, sendHelp, reason), true
}

func resolveSend(home string, req sendRequest, stderr io.Writer, routes map[string]route.Route) (d delivery, exit int, done bool) {
	sender, known, err := resolve(home, req.from)
	if err != nil {
		return d, notSent(stderr, err), true
	}
	if !known {
		return d, usageError(stderr, sendHelp, fmt.Sprintf("--from %q is no joined name; see crewtalk who", req.from)), true
	}
	if _, _, err := kindAddress(sender, routes); err != nil {
		return d, usageError(stderr, sendHelp, "--from "+err.Error()), true
	}
	receiver, known, err := resolve(home, req.to)
	if err != nil {
		return d, notSent(stderr, err), true
	}
	d.known = known
	if known {
		if d.kind, d.address, err = kindAddress(receiver, routes); err != nil {
			return d, usageError(stderr, sendHelp, "--to "+err.Error()), true
		}
	}
	thread, turn := req.thread, req.turn
	if req.replying {
		answered, found, err := journal.Find(home, req.replyTo)
		if err != nil {
			return d, notSent(stderr, err), true
		}
		if !found {
			return d, usageError(stderr, sendHelp, fmt.Sprintf("--reply-to %q names no message in the log, so nothing was sent; check the id; the sender's log line may not be written yet, so send once more in a moment; if it still fails, tell your operator", req.replyTo)), true
		}
		if !message.ValidField(req.replyTo) || !message.ValidField(answered.Thread) {
			return d, usageError(stderr, sendHelp, fmt.Sprintf("--reply-to %q names a message whose id or thread %q holds characters crewtalk refuses, so nothing was sent; leave out --reply-to to start a new conversation", req.replyTo, answered.Thread)), true
		}
		thread, turn = answered.Thread, answered.Turn+1
	}
	d.m = message.New(sender, receiver, thread, req.text, turn)
	d.m.ReplyTo = req.replyTo
	for _, field := range []string{d.m.From, d.m.To, d.m.Thread} {
		if !message.ValidField(field) {
			return d, usageError(stderr, sendHelp, "--from, --to and --thread cannot hold spaces, commas, semicolons, square brackets, or control or invisible format characters"), true
		}
	}
	return d, 0, false
}

func deliver(ctx context.Context, home string, d delivery, to string, routes map[string]route.Route, warn func(error)) sendOutcome {
	if !d.known {
		return sendOutcome{journal.ReceiverUnknown, exitReceiver, fmt.Errorf("%q is no joined name; see crewtalk who", to)}
	}
	r := routes[d.kind]
	err := r.Deliver(ctx, route.Agent{Address: d.address}, d.m)
	if errors.Is(err, route.ErrNotListening) {
		isHeld, present, holdErr := hold(ctx, home, r, d, warn)
		switch {
		case isHeld:
			return sendOutcome{journal.Held, exitOK, nil}
		case present:
			err = r.Deliver(ctx, route.Agent{Address: d.address}, d.m)
		case holdErr != nil:
			err = fmt.Errorf("%w; the message could not be held either: %v", err, holdErr)
		}
	}
	result, exit := outcome(err)
	return sendOutcome{result, exit, err}
}

func record(j *journal.Journal, d delivery, o sendOutcome, asJSON bool, stdout, stderr io.Writer) int {
	m := d.m
	report := sendReport{Entry: entryFor(m, o.result)}
	if o.err != nil {
		report.Error = o.err.Error()
	}
	if err := j.Append(report.Entry); err != nil {
		logged := false
		report.Logged = &logged
		fmt.Fprintf(stderr, "crewtalk send: the result is %s, but the log line was not written: %v\n", o.result, err)
	}

	switch {
	case asJSON:
		printJSON(stdout, report)
	case o.result == journal.Held:
		fmt.Fprintf(stdout, "held, id %s, thread %s; %s is not listening, and its next crewtalk wait prints it\n", m.ID, m.Thread, d.address)
	case o.err == nil:
		fmt.Fprintf(stdout, "delivered, id %s, thread %s\n", m.ID, m.Thread)
	}
	if o.err != nil {
		fmt.Fprintf(stderr, "crewtalk send: %s: %s\n", o.result, message.Printable(o.err.Error()))
	}
	return o.exit
}

func kindAddress(name string, routes map[string]route.Route) (kind, address string, err error) {
	kind, address, _ = strings.Cut(name, ":")
	r, known := routes[kind]
	if !known || address == "" {
		return "", "", fmt.Errorf("%q must be KIND:ADDRESS with KIND one of %s", name, kinds(routes))
	}
	if err := r.CheckAddress(address); err != nil {
		return "", "", fmt.Errorf("%q: %w", name, err)
	}
	return kind, address, nil
}

func notSent(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "crewtalk send: %v; nothing was sent\n", err)
	return exitFailed
}

func resolve(home, agent string) (string, bool, error) {
	if strings.Contains(agent, ":") {
		return agent, true, nil
	}
	a, found, err := book.Lookup(home, agent)
	if err != nil || !found {
		return agent, false, err
	}
	return a.Kind + ":" + a.Address, true, nil
}

func sessionSender(routes map[string]route.Route) string {
	for _, kind := range slices.Sorted(maps.Keys(routes)) {
		if address := routes[kind].SessionAddress(); address != "" {
			return kind + ":" + address
		}
	}
	return ""
}

func outcome(err error) (journal.Result, int) {
	if err == nil {
		return journal.Delivered, exitOK
	}
	for _, o := range routeOutcomes {
		if errors.Is(err, o.err) {
			return o.result, o.exit
		}
	}
	return journal.Failed, exitFailed
}
