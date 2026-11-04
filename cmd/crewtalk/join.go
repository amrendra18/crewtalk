package main

import (
	"cmp"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/amrendra18/crewtalk/internal/book"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
	"github.com/amrendra18/crewtalk/internal/state"
)

const joinHelp = `usage: crewtalk join --as NAME [--kind KIND] [--address ADDRESS] [--json]

Adds NAME to the address book, CREWTALK_HOME/agents.json, or replaces the
entry NAME has. Other agents then reach it with "crewtalk send --to NAME".

  --as NAME          the agent name; no spaces, commas, semicolons, square
                     brackets, slashes, colons, or control or invisible
                     format characters (a name may hold U+200C and U+200D)
  --kind KIND        a route kind: %s; default the kind of this
                     session, so a Codex session run outside its sandbox
                     joins as its own thread
  --address ADDRESS  codex: the thread UUID; claude: the listener name.
                     Default this session's address; for claude, NAME
  --json             print the joined entry as one JSON object

"crewtalk wait --as NAME" joins NAME as a claude agent by itself.

Exit codes:
  0   joined
  5   the address book could not be read or written
  64  usage error
`

const leaveHelp = `usage: crewtalk leave --as NAME [--json]

Removes NAME from the address book, CREWTALK_HOME/agents.json.

  --as NAME  the agent name
  --json     print the removed entry as one JSON object

Exit codes:
  0   removed
  4   no agent joined as NAME
  5   the address book could not be read or written
  64  usage error
`

const whoHelp = `usage: crewtalk who [--json]

Lists the agents in the address book, CREWTALK_HOME/agents.json, one per
line: NAME KIND:ADDRESS.

  --json  print the address book as one JSON object

Exit codes:
  0   listed, or the address book is empty
  5   the address book could not be read, or the list could not be printed
  64  usage error
`

func join(args []string, stdout, stderr io.Writer, routes map[string]route.Route) int {
	joinHelp := fmt.Sprintf(joinHelp, kinds(routes))
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	as := fs.String("as", "", "")
	kind := fs.String("kind", "", "")
	address := fs.String("address", "", "")
	asJSON := fs.Bool("json", false, "")
	if exit, done := parseFlags(fs, joinHelp, args, stdout, stderr); done {
		return exit
	}
	if *as == "" || fs.NArg() != 0 {
		return usageError(stderr, joinHelp, "join takes --as NAME and no other arguments")
	}
	if err := book.CheckName(*as); err != nil {
		return usageError(stderr, joinHelp, "--as "+err.Error())
	}
	if *kind == "" {
		*kind, _, _ = strings.Cut(sessionSender(routes), ":")
	}
	if *kind == "" {
		return usageError(stderr, joinHelp, "--kind is required, because no route gives this session's address")
	}
	r, known := routes[*kind]
	if !known {
		return usageError(stderr, joinHelp, fmt.Sprintf("--kind %q must be one of %s", *kind, kinds(routes)))
	}
	*address = cmp.Or(*address, r.SessionAddress(), r.DefaultAddress(*as))
	if *address == "" {
		return usageError(stderr, joinHelp, "--address is required for kind "+*kind)
	}
	if err := r.CheckAddress(*address); err != nil {
		return usageError(stderr, joinHelp, "--address "+err.Error())
	}
	joined := book.Agent{Name: *as, Kind: *kind, Address: *address}
	home, err := state.Home()
	if err == nil {
		_, _, err = book.Join(home, joined)
	}
	if err != nil {
		fmt.Fprintf(stderr, "crewtalk join: %v\n", err)
		return exitFailed
	}
	if *asJSON {
		printJSON(stdout, joined)
		return exitOK
	}
	fmt.Fprintf(stdout, "joined %s as %s:%s\n", *as, *kind, *address)
	return exitOK
}

func leave(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("leave", flag.ContinueOnError)
	as := fs.String("as", "", "")
	asJSON := fs.Bool("json", false, "")
	if exit, done := parseFlags(fs, leaveHelp, args, stdout, stderr); done {
		return exit
	}
	if *as == "" || fs.NArg() != 0 {
		return usageError(stderr, leaveHelp, "leave takes --as NAME and no other arguments")
	}
	home, err := state.Home()
	var left book.Agent
	found := false
	if err == nil {
		left, found, err = book.Leave(home, *as)
	}
	if err != nil {
		fmt.Fprintf(stderr, "crewtalk leave: %v\n", err)
		return exitFailed
	}
	if !found {
		fmt.Fprintf(stderr, "crewtalk leave: no agent joined as %q; see crewtalk who\n", *as)
		return exitReceiver
	}
	if *asJSON {
		printJSON(stdout, left)
		return exitOK
	}
	fmt.Fprint(stdout, message.Printable(fmt.Sprintf("left %s, which was %s:%s\n", left.Name, left.Kind, left.Address)))
	return exitOK
}

func who(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("who", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	if exit, done := parseFlags(fs, whoHelp, args, stdout, stderr); done {
		return exit
	}
	if fs.NArg() != 0 {
		return usageError(stderr, whoHelp, "who takes no arguments")
	}
	home, err := state.Home()
	var agents []book.Agent
	if err == nil {
		agents, err = book.Load(home)
	}
	if err == nil {
		err = printAgents(stdout, agents, *asJSON)
	}
	if err != nil {
		fmt.Fprintf(stderr, "crewtalk who: %v\n", err)
		return exitFailed
	}
	return exitOK
}

func printAgents(stdout io.Writer, agents []book.Agent, asJSON bool) error {
	if asJSON {
		if agents == nil {
			agents = []book.Agent{}
		}
		return printJSON(stdout, map[string][]book.Agent{"agents": agents})
	}
	for _, a := range agents {
		if _, err := fmt.Fprint(stdout, message.Printable(fmt.Sprintf("%s %s:%s\n", a.Name, a.Kind, a.Address))); err != nil {
			return err
		}
	}
	return nil
}

func kinds(routes map[string]route.Route) string {
	return strings.Join(slices.Sorted(maps.Keys(routes)), ", ")
}
