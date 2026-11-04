package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amrendra18/crewtalk/internal/held"
	"github.com/amrendra18/crewtalk/internal/journal"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/state"
)

var logHelp = fmt.Sprintf(`usage: crewtalk log [--follow] [--json]

Prints CREWTALK_HOME/log.jsonl one line per message: time, from, to, thread,
turn, reply-to id when the message answers one, id, result, and for a held
message that was delivered, the seconds it was held. A send writes one line;
each later change of state adds one, such as delivered for a held message or
expired. Held messages older than %d minutes expire first. A line
that is not JSON is skipped with a warning on stderr that names its line
number.

  --follow  keep printing new lines as they are written; stop with Ctrl-C
  --json    print each line as one JSON object

Exit codes:
  0   the log was printed, or --follow was stopped
  5   the log could not be read
  64  usage error
`, held.Deadline/time.Minute)

func printLog(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	follow := fs.Bool("follow", false, "")
	asJSON := fs.Bool("json", false, "")
	if exit, done := parseFlags(fs, logHelp, args, stdout, stderr); done {
		return exit
	}
	if fs.NArg() != 0 {
		return usageError(stderr, logHelp, "log takes no arguments")
	}
	home, err := state.Home()
	if err == nil {
		expire(home, warnings(stderr, "log"))
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = journal.Read(ctx, home, *follow, func(e journal.Entry) error {
			if *asJSON {
				return printJSON(stdout, e)
			}
			replyTo := ""
			if e.ReplyTo != "" {
				replyTo = ", reply-to " + e.ReplyTo
			}
			heldFor := ""
			if e.HeldFor != 0 {
				heldFor = fmt.Sprintf(", held for %g s", e.HeldFor)
			}
			_, err := fmt.Fprint(stdout, message.Printable(fmt.Sprintf("%s %s to %s, thread %s, turn %d%s, id %s, %s%s\n", e.Time.UTC().Format(time.RFC3339), e.From, e.To, e.Thread, e.Turn, replyTo, e.ID, e.Result, heldFor)))
			return err
		}, func(err error) { fmt.Fprintf(stderr, "crewtalk log: warning: %v\n", err) })
	}
	if err != nil {
		fmt.Fprintf(stderr, "crewtalk log: %v\n", err)
		return exitFailed
	}
	return exitOK
}
