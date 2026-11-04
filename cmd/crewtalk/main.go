package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/amrendra18/crewtalk/internal/held"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
)

const (
	exitOK       = 0
	exitRefused  = 3
	exitReceiver = 4
	exitFailed   = 5
	exitUsage    = 64
)

var usage = fmt.Sprintf(`usage: crewtalk send --to AGENT [--from AGENT] [--thread T | --reply-to ID] [--turn N] [--json] TEXT|-
       crewtalk wait --as NAME [--timeout DURATION] [--json]
       crewtalk join --as NAME [--kind KIND] [--address ADDRESS] [--json]
       crewtalk leave --as NAME [--json]
       crewtalk who [--json]
       crewtalk log [--follow] [--json]

AGENT is a joined NAME or KIND:ADDRESS. State lives in CREWTALK_HOME
(default ~/.crewtalk).

send delivers one message to another agent session and writes one line to
CREWTALK_HOME/log.jsonl. A message to a claude agent in the address book that
is not listening is held. A held message expires at the first send, wait or
log that runs %d minutes or more after it was held. While CREWTALK_HOME/PAUSE
exists, every send is refused and every new wait exits 3.
wait receives one message for a Claude session and prints it, a held one
first.
join adds a name to the address book; leave removes it; who lists the
address book.
log prints the log, one line per message.

Run "crewtalk COMMAND --help" for details.

Exit codes:
  0   send: delivered or held; wait: a message was printed; join, leave,
      who, log: done
  3   send: refused by PAUSE, the turn limit, or the limit per sender and
      receiver; wait: PAUSE exists
  4   send: receiver unknown or not listening; wait: the timeout passed;
      leave: no agent joined as NAME
  5   send: route failed, or the outcome is unknown (never retried);
      or the log is busy or not writable, and nothing was sent;
      wait: the listener could not start, or printing the message failed;
      join, leave, who, log: the state could not be read or written
  64  usage error
`, held.Deadline/time.Minute)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, routeTable()))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, routes map[string]route.Route) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "send":
		return send(args[1:], stdin, stdout, stderr, routes)
	case "wait":
		return wait(args[1:], stdout, stderr)
	case "join":
		return join(args[1:], stdout, stderr, routes)
	case "leave":
		return leave(args[1:], stdout, stderr)
	case "who":
		return who(args[1:], stdout, stderr)
	case "log":
		return printLog(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	fmt.Fprintf(stderr, "crewtalk: unknown command %q\n\n%s", args[0], usage)
	return exitUsage
}

func parseFlags(fs *flag.FlagSet, help string, args []string, stdout, stderr io.Writer) (exit int, done bool) {
	return parseFlagsWithNote(fs, help, "", args, stdout, stderr)
}

func parseFlagsWithNote(fs *flag.FlagSet, help, badFlagNote string, args []string, stdout, stderr io.Writer) (exit int, done bool) {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, help)
		return exitOK, true
	}
	if err != nil {
		reason := err.Error()
		if strings.HasPrefix(reason, "flag provided but not defined") || strings.HasPrefix(reason, "bad flag syntax") {
			reason += badFlagNote
		}
		return usageError(stderr, help, reason), true
	}
	return 0, false
}

func usageError(stderr io.Writer, help, reason string) int {
	fmt.Fprintf(stderr, "crewtalk: %s\n\n%s", message.Printable(reason), help)
	return exitUsage
}

func printJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(message.PrintableJSON(data), '\n'))
	return err
}
