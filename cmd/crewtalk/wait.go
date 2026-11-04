package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/amrendra18/crewtalk/internal/book"
	"github.com/amrendra18/crewtalk/internal/held"
	"github.com/amrendra18/crewtalk/internal/journal"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route/claude"
	"github.com/amrendra18/crewtalk/internal/state"
)

var waitHelp = fmt.Sprintf(`usage: crewtalk wait --as NAME [--timeout DURATION] [--json]

Joins NAME as a claude agent, receives one message for the Claude session
that listens as NAME, prints it with its header line, and exits. The sender
is told "delivered" only after the print succeeded. Run it in the background;
its exit wakes the session. A sender reaches it with
"crewtalk send --to NAME TEXT". If NAME was joined as another kind, wait
prints a warning on stderr, because sends to NAME now come here.

A message to a claude agent in the address book that is not listening is
held. wait then prints the oldest held message at once, removes it from
CREWTALK_HOME/held/NAME.jsonl, and exits; the next wait prints the next one.
A held message expires at the first send, wait or log that runs %d minutes
or more after it was held.

While CREWTALK_HOME/PAUSE exists, wait takes and prints nothing, a held
message stays held, and wait exits 3. A wait that was already listening keeps
listening.

  --as NAME           the listener name
  --timeout DURATION  stop waiting after this long, such as 30m; default never
  --json              print the message as one JSON object

Exit codes:
  0   a message was printed
  3   CREWTALK_HOME/PAUSE exists; nothing is printed
  4   the timeout passed; nothing is printed
  5   the listener could not start, or printing the message failed
  64  usage error
`, held.Deadline/time.Minute)

func wait(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	as := fs.String("as", "", "")
	timeout := fs.Duration("timeout", 0, "")
	asJSON := fs.Bool("json", false, "")
	if exit, done := parseFlags(fs, waitHelp, args, stdout, stderr); done {
		return exit
	}
	if *as == "" || fs.NArg() != 0 {
		return usageError(stderr, waitHelp, "wait takes --as NAME and no other arguments")
	}
	r := claude.Route{}
	if err := r.CheckAddress(*as); err != nil {
		return usageError(stderr, waitHelp, err.Error())
	}
	given := false
	fs.Visit(func(f *flag.Flag) { given = given || f.Name == "timeout" })
	limit := claude.NoTimeout
	if given {
		if *timeout <= 0 {
			return usageError(stderr, waitHelp, "--timeout must be a positive duration")
		}
		limit = *timeout
	}

	signal.Ignore(syscall.SIGPIPE)
	joined := book.Agent{Name: *as, Kind: r.Kind(), Address: r.DefaultAddress(*as)}
	var old book.Agent
	replaced := false
	warn := warnings(stderr, "wait")
	home, err := state.Home()
	if err == nil {
		expire(home, warn)
		if pause, stopped := paused(home); stopped {
			fmt.Fprintf(stderr, "crewtalk wait: %s: every message is stopped while %s exists; report this to your operator\n", journal.RefusedPaused, pause)
			return exitRefused
		}
		old, replaced, err = book.Join(home, joined)
	}
	if err != nil {
		fmt.Fprintf(stderr, "crewtalk wait: cannot join %s: %v\n", *as, err)
		return exitFailed
	}
	if replaced && old.Kind != joined.Kind {
		fmt.Fprint(stderr, message.Printable(fmt.Sprintf("crewtalk wait: warning: %s was %s:%s and is now %s:%s, so sends to %s come to this listener\n", *as, old.Kind, old.Address, joined.Kind, joined.Address, *as)))
	}
	err = receive(home, *as, limit, func(m message.Message) error {
		if *asJSON {
			return printJSON(stdout, m)
		}
		_, err := fmt.Fprintln(stdout, message.Printable(m.Wire()))
		return err
	}, warn)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return exitReceiver
	}
	if err != nil {
		fmt.Fprintf(stderr, "crewtalk wait: %v\n", err)
		return exitFailed
	}
	return exitOK
}

var errHeldFirst = errors.New("a held message comes first")

func receive(home, name string, timeout time.Duration, print func(message.Message) error, warn func(error)) error {
	l, err := claude.Claim(name)
	if err != nil {
		return err
	}
	defer l.Close()
	opening, ready, waiting := heldGate(home, name, warn)
	err = l.Receive(timeout, opening, ready, print)
	ready()
	if errors.Is(err, errHeldFirst) {
		return printHeld(home, name, *waiting, print, warn)
	}
	return err
}

func heldGate(home, name string, warn func(error)) (opening func() error, ready func(), waiting *message.Message) {
	waiting = &message.Message{}
	release := func() {}
	opening = func() error {
		box, unlock, err := held.Lock(context.Background(), home, warn)
		if err != nil {
			return err
		}
		release = sync.OnceFunc(unlock)
		m, found, err := box.Oldest(name)
		if err == nil && found {
			*waiting, err = m, errHeldFirst
		}
		if err != nil {
			release()
		}
		return err
	}
	return opening, func() { release() }, waiting
}

func printHeld(home, name string, m message.Message, print func(message.Message) error, warn func(error)) error {
	if err := print(m); err != nil {
		return err
	}
	if _, err := held.Remove(home, name, m.ID, warn); err != nil {
		warn(fmt.Errorf("%s was printed, but it is still held, so the next wait prints it again: %w", m.ID, err))
	}
	e := entryFor(m, journal.Delivered)
	e.HeldFor = time.Since(m.Time).Seconds()
	j, err := journal.Open(home)
	if err == nil {
		defer j.Close()
		err = j.Append(e)
	}
	if err != nil {
		warn(fmt.Errorf("%s was printed, but its log line was not written: %w", m.ID, err))
	}
	return nil
}
