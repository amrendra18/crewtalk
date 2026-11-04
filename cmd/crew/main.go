// Command crew starts, tracks and stops worker agent sessions for a lead session.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/amrendra18/crewtalk/internal/message"
)

const (
	exitOK       = 0
	exitRefused  = 3
	exitReceiver = 4
	exitFailed   = 5
	exitUsage    = 64
)

const usage = `usage: crew start --lead NAME --role WORD --folder PATH
                  (--task TEXT | --task-file PATH) [--model M] [--effort E]
       crew stop ID [--force]

crew runs Codex workers for a lead session. State lives in CREW_HOME (default
~/.crew). CREW_START_WAIT sets how long start waits for a worker's ready message
(default 49s).

start makes a worktree of the git repository that holds --folder, under
CREW_HOME/worktrees, opens a tab in the lead's herdr workspace, and starts a
read-only Codex session there. It sends the task through crewtalk once the
session reports ready, and prints one JSON object with the id and the state.
--lead is a name that crewtalk already knows, the one the lead listens on. It
holds only letters, digits and _ @ % + = . -, and does not start with =, because
the worker's reply command carries it.
While CREWTALK_HOME/PAUSE exists, start refuses and changes nothing.

stop closes the worker's pane and removes its crewtalk names. It removes the
folder only when git shows no change and no ignored file in it, no git operation
is in progress there, and its HEAD is on the worker's branch. It deletes the
branch only when git branch -d allows it. When crew is not sure, it keeps them
and prints what it kept. Run stop again to retry, or to see that line again. A
worker that herdr reads as anything but idle or done needs --force, which closes
the pane, raises a notice and records the time. --force does not widen what gets
deleted, and stop never closes a pane that holds another agent.

Limit: stop checks the folder's files and HEAD, and it does not check history. A
commit that no branch or tag holds, such as one dropped by git reset, or one held
only by a ref under refs/worktree, is gone with the folder.

Exit codes:
  0   done
  3   PAUSE exists, or crewtalk refused the step because of a limit
  4   not found, or the worker sent no ready message in time
  5   a step failed; the stderr line names the step and the recovery
  64  usage error
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "start":
		return start(args[1:], stdout, stderr)
	case "stop":
		return stop(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	return usageError(stderr, fmt.Sprintf("unknown command %q", args[0]))
}

func parseFlags(fs *flag.FlagSet, args []string, stdout, stderr io.Writer) (exit int, done bool) {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return exitOK, true
	}
	if err != nil {
		return usageError(stderr, err.Error()), true
	}
	return 0, false
}

func usageError(stderr io.Writer, reason string) int {
	fmt.Fprintf(stderr, "crew: %s\n\n%s", printable(reason), usage)
	return exitUsage
}

func printJSON(w io.Writer, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, "%s\n", message.PrintableJSON(data))
}
