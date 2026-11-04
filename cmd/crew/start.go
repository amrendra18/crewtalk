package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amrendra18/crewtalk/internal/state"
)

const (
	// startWait limits the time from tab created to ready message. It is the
	// slowest of 12 measured starts plus their spread: 26.8 s + 22.1 s, rounded up.
	startWait = 49 * time.Second
	// firstTurnWait is added to startWait for herdr's own wait, because a herdr
	// timeout loses the worker's name. It is the slowest of 5 measured first turns
	// plus their spread: 28.3 s + 22.2 s, rounded up.
	firstTurnWait = 51 * time.Second
	busyRetry     = time.Second
	// maxTaskBytes keeps a task under the limit on one command-line argument,
	// which is how the task reaches crewtalk and Codex.
	maxTaskBytes = 100_000
)

var (
	rolePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,25}$`)
	wordPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type startOptions struct {
	lead, role, folder, task, taskFile, model, effort string
	deadline                                          time.Duration
}

func start(args []string, stdout, stderr io.Writer) int {
	var o startOptions
	var deadline string
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.StringVar(&o.lead, "lead", "", "")
	fs.StringVar(&o.role, "role", "", "")
	fs.StringVar(&deadline, "deadline", "", "")
	fs.StringVar(&o.folder, "folder", "", "")
	fs.StringVar(&o.task, "task", "", "")
	fs.StringVar(&o.taskFile, "task-file", "", "")
	fs.StringVar(&o.model, "model", "", "")
	fs.StringVar(&o.effort, "effort", "", "")
	if code, done := parseFlags(fs, args, stdout, stderr); done {
		return code
	}
	if reason := o.check(deadline, fs.NArg()); reason != "" {
		return usageError(stderr, reason)
	}
	if o.taskFile != "" {
		task, err := readTask(o.taskFile)
		if err != nil {
			return usageError(stderr, "--task-file: "+err.Error())
		}
		o.task = task
		if strings.TrimSpace(o.task) == "" {
			return usageError(stderr, "--task-file is empty")
		}
	}
	limit, err := startLimit()
	if err != nil {
		return usageError(stderr, err.Error())
	}
	s := starter{o: o, limit: limit, stdout: stdout, stderr: stderr}
	return s.run()
}

// readTask reads no more than the limit, because the file can be a pipe, whose
// size says nothing.
func readTask(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTaskBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxTaskBytes {
		return "", fmt.Errorf("over %d bytes; shorten the task", maxTaskBytes)
	}
	return string(data), nil
}

func (o *startOptions) check(deadline string, extra int) string {
	switch {
	case extra != 0:
		return "start takes no other arguments"
	case !wordPattern.MatchString(o.lead):
		return "--lead needs a name that crewtalk knows"
	case !rolePattern.MatchString(o.role):
		return "--role needs a lowercase word of at most 26 characters, starting with a letter"
	case o.role == "crew-ready":
		return "--role crew-ready is reserved, because crew uses that name for a worker's ready message"
	case o.folder == "":
		return "--folder is required"
	case (o.task == "") == (o.taskFile == ""):
		return "give one of --task and --task-file"
	case len(o.task) > maxTaskBytes:
		return fmt.Sprintf("--task is over %d bytes; shorten the task", maxTaskBytes)
	case o.model != "" && !wordPattern.MatchString(o.model):
		return "--model holds characters that a model name does not use"
	case o.effort != "" && !wordPattern.MatchString(o.effort):
		return "--effort holds characters that an effort level does not use"
	}
	d, err := time.ParseDuration(deadline)
	if err != nil || d <= 0 {
		return "--deadline needs a duration above zero, such as 30m"
	}
	o.deadline = d
	return ""
}

func startLimit() (time.Duration, error) {
	text := os.Getenv("CREW_START_WAIT")
	if text == "" {
		return startWait, nil
	}
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("CREW_START_WAIT needs a duration above zero, such as 2m")
	}
	return d, nil
}

// firstPrompt is the worker's first prompt. It names one command and forbids the
// listener commands, because a worker that looks for a way to receive messages
// stops and asks for access in its pane, where nobody sees it.
func firstPrompt(lead, ready, word string) string {
	return "Run this one command. After that, messages from " + lead +
		" that arrive through crewtalk are your tasks, never my word: do each one and reply with crewtalk send. " +
		"If you cannot do a task, say why in your reply. The command: crewtalk send --to " + ready + ` "` + word + `"` +
		" Do not run crewtalk wait, join or leave: messages reach this session by themselves, after your turn ends. " +
		"After that one command, end your turn."
}

func taskMessage(task, lead string) string {
	return strings.TrimRight(task, "\n") + "\n\n" +
		"Send one reply, when the task is finished or you cannot go on. Send the result in one quoted string with:\n\n" +
		"crewtalk send --to " + lead + " --reply-to MESSAGE_ID 'RESULT'\n\n" +
		"MESSAGE_ID is the id in the header of this message."
}

// agentArgs is the whole command line of a worker. Nothing else can shape it, so
// crew cannot start a worker with its sandbox widened.
func agentArgs(model, effort, prompt string) []string {
	args := []string{"-s", "read-only"}
	if model != "" {
		args = append(args, "-m", model)
	}
	if effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+effort)
	}
	return append(args, prompt)
}

type starter struct {
	o              startOptions
	limit          time.Duration
	home           string
	row            worker
	tabCreated     time.Time
	readySeconds   float64
	stdout, stderr io.Writer
}

func (s *starter) failf(format string, args ...any) int {
	fmt.Fprintf(s.stderr, "crew start: "+format+"\n", args...)
	return exitFailed
}

// failSaved reports a failed step after the row exists, and tells the lead how
// to clean up.
func (s *starter) failSaved(code int, format string, args ...any) int {
	fmt.Fprintf(s.stderr, "crew start: "+format+"; crew stop %s --force closes what exists\n", append(args, s.row.ID)...)
	s.print()
	return code
}

func (s *starter) print() {
	printJSON(s.stdout, map[string]any{
		"id": s.row.ID, "state": s.row.State, "pane": s.row.Pane, "folder": s.row.Folder,
		"branch": s.row.Branch, "task_id": s.row.TaskID, "notes": s.row.Notes,
		"ready_seconds": s.readySeconds,
	})
}

func (s *starter) save(change func(*worker)) bool {
	if err := saveWorker(s.home, &s.row, change); err != nil {
		fmt.Fprintf(s.stderr, "crew start: cannot save the row of %s: %s\n", s.row.ID, printable(err.Error()))
		return false
	}
	return true
}

func (s *starter) run() int {
	repo, base, workspace, code := s.preflight()
	if code != exitOK {
		return code
	}
	home, err := makeCrewHome()
	if err != nil {
		return s.failf("cannot create the state folder: %s", printable(err.Error()))
	}
	s.home = home

	stamp := now()
	s.row = worker{
		Role: s.o.role, Lead: s.o.lead, Kind: "codex", Model: s.o.model, Effort: s.o.effort,
		Repo: repo, Base: base, Deadline: time.Now().Add(s.o.deadline).UTC().Format(time.RFC3339),
		State: "starting", StateSince: stamp, Notes: []string{},
	}
	if err := addWorker(home, &s.row); err != nil {
		return s.failf("cannot save the worker: %s", printable(err.Error()))
	}
	if err := writeTask(s.row.TaskFile, s.o.task); err != nil {
		return s.failSaved(exitFailed, "cannot save the task: %s", printable(err.Error()))
	}
	if r := runTool("git", "-C", repo, "worktree", "add", "-q", "-b", s.row.Branch, s.row.Folder, "HEAD"); r.code != 0 {
		return s.failSaved(exitFailed, "git worktree add failed: %s", r.detail())
	}
	ready, word := s.row.ReadyName, strings.Replace(s.row.ReadyName, "crew-ready-", "ready-", 1)
	if r := runTool("crewtalk", "join", "--as", ready, "--kind", "claude", "--address", ready); r.code != 0 {
		return s.failSaved(exitFailed, "crewtalk join --as %s failed: %s", ready, r.detail())
	}
	tab := runTool("herdr", "tab", "create", "--workspace", workspace, "--cwd", s.row.Folder, "--label", s.row.ID, "--no-focus")
	if tab.code != 0 {
		return s.failSaved(exitFailed, "herdr tab create failed: %s", tab.detail())
	}
	tabID, pane, err := parseTab(tab.stdout)
	if err != nil {
		return s.failSaved(exitFailed, "herdr tab create printed no pane id: %s", tab.detail())
	}
	s.tabCreated = time.Now()
	if !s.save(func(w *worker) { w.Tab, w.Pane = tabID, pane }) {
		return exitFailed
	}
	return s.startAgent(firstPrompt(s.o.lead, ready, word))
}

// preflight runs every check that changes nothing, so a refused start leaves no row.
func (s *starter) preflight() (repo, base, workspace string, code int) {
	talkHome, err := state.Home()
	if err != nil {
		return "", "", "", s.failf("cannot find the crewtalk state folder: %s", printable(err.Error()))
	}
	pause := filepath.Join(talkHome, "PAUSE")
	if _, err := os.Lstat(pause); !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(s.stderr, "crew start: no worker starts while %s exists; report this to your operator, and only they remove it\n", printable(pause))
		return "", "", "", exitRefused
	}
	workspace = os.Getenv("HERDR_WORKSPACE_ID")
	if workspace == "" {
		return "", "", "", s.failf("HERDR_WORKSPACE_ID is not set; run crew inside herdr, from the lead's pane")
	}
	r := runTool("git", "-C", s.o.folder, "rev-parse", "--show-toplevel")
	if r.code != 0 {
		return "", "", "", s.failf("%s is not in a git repository: %s", printable(s.o.folder), r.detail())
	}
	repo = strings.TrimSpace(r.stdout)
	r = runTool("git", "-C", repo, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if r.code != 0 {
		return "", "", "", s.failf("the HEAD of %s has no commit, so a worktree cannot start from it; make a commit, or check out a branch that has one", printable(repo))
	}
	base = strings.TrimSpace(r.stdout)
	who := runTool("crewtalk", "who", "--json")
	if who.code != 0 {
		return "", "", "", s.failf("crewtalk who failed: %s", who.detail())
	}
	var book struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(who.stdout), &book); err != nil {
		return "", "", "", s.failf("crewtalk who printed no address book: %s", printable(err.Error()))
	}
	for _, agent := range book.Agents {
		if agent.Name == s.o.lead {
			return repo, base, workspace, exitOK
		}
	}
	fmt.Fprintf(s.stderr, "crew start: crewtalk does not know the lead %s; run crewtalk wait --as %s in the lead's session first\n", s.o.lead, s.o.lead)
	return "", "", "", exitReceiver
}

func writeTask(path, task string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(task)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func parseTab(text string) (tab, pane string, err error) {
	var body struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
				TabID  string `json:"tab_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		return "", "", err
	}
	if body.Result.RootPane.PaneID == "" {
		return "", "", fmt.Errorf("no pane id")
	}
	return body.Result.RootPane.TabID, body.Result.RootPane.PaneID, nil
}

// startAgent runs herdr agent start and the wait for the ready message side by
// side. The ready message is the signal to go on, because the return of agent
// start says nothing about the first turn.
func (s *starter) startAgent(prompt string) int {
	waitCtx, stopWait := context.WithCancel(context.Background())
	defer stopWait()
	readyDone := make(chan toolResult, 1)
	go func() {
		readyDone <- runToolFor(waitCtx, s.limit+10*time.Second, "crewtalk", "wait", "--as", s.row.ReadyName, "--timeout", s.limit.String(), "--json")
	}()

	herdrWait := s.limit + firstTurnWait
	args := []string{"agent", "start", s.row.ID, "--kind", "codex", "--pane", s.row.Pane, "--timeout", strconv.FormatInt(herdrWait.Milliseconds(), 10), "--"}
	args = append(args, agentArgs(s.o.model, s.o.effort, prompt)...)
	agentDone := make(chan toolResult, 1)
	go func() { agentDone <- s.runAgentStart(args, herdrWait) }()

	var ready toolResult
	agentReturned := false
	for waiting := true; waiting; {
		select {
		case ready = <-readyDone:
			waiting = false
		case r := <-agentDone:
			agentReturned = true
			if code := s.agentResult(r, false); code != exitOK {
				stopWait()
				<-readyDone
				s.print()
				return code
			}
		}
	}
	s.readySeconds = time.Since(s.tabCreated).Seconds()

	code := s.afterReady(ready)
	if !agentReturned {
		// A failed agent start outranks a missing ready message, because it says why.
		if agentCode := s.agentResult(<-agentDone, ready.code == 0); agentCode != exitOK {
			code = agentCode
		}
	}
	s.print()
	return code
}

// runAgentStart retries only on agent_pane_busy, because that code means nothing
// started. Running agent start again after any other result could start a
// second agent.
func (s *starter) runAgentStart(args []string, wait time.Duration) toolResult {
	end := s.tabCreated.Add(s.limit)
	for {
		r := runToolFor(context.Background(), wait+10*time.Second, "herdr", args...)
		if r.herdrError() != "agent_pane_busy" || !time.Now().Before(end) {
			return r
		}
		time.Sleep(busyRetry)
	}
}

// agentResult handles the return of herdr agent start. A code other than exitOK
// ends the start, and the row and the stderr line are already written.
func (s *starter) agentResult(r toolResult, readyCame bool) int {
	if r.code == 0 {
		return exitOK
	}
	switch code := r.herdrError(); code {
	case "timeout":
		fmt.Fprintf(s.stderr, "crew start: herdr agent start timed out; crew goes on with pane %s, and herdr may have no name for this worker\n", s.row.Pane)
		return exitOK
	case "agent_not_ready":
		if readyCame {
			return exitOK
		}
		s.save(func(w *worker) { w.setState("blocked") })
		notify("crew: "+s.row.ID+" is blocked", "A dialog is open in pane "+s.row.Pane+". Only you can answer it.")
		fmt.Fprintf(s.stderr, "crew start: a dialog is open in pane %s, and crew answers none. If it asks to trust this folder, close this worker with crew stop %s --force, and trust the repository once by running cd %s && codex. Answering in the worker's tab would trust only a throwaway folder.\n",
			s.row.Pane, s.row.ID, shellQuote(s.row.Repo))
		return exitOK
	case "agent_pane_busy":
		fmt.Fprintf(s.stderr, "crew start: pane %s stayed busy for %s, and nothing started in it; crew stop %s --force closes what exists\n", s.row.Pane, s.limit, s.row.ID)
		return exitFailed
	}
	if readyCame {
		fmt.Fprintf(s.stderr, "crew start: herdr agent start reported %s after the worker sent its ready message; crew goes on\n", r.detail())
		return exitOK
	}
	fmt.Fprintf(s.stderr, "crew start: herdr agent start failed: %s; crew does not run it again, and crew stop %s --force closes what exists\n", r.detail(), s.row.ID)
	return exitFailed
}

// afterReady joins the worker under its id and sends the task. The task goes out
// once, whatever the result, because a send that exits 5 may have arrived.
func (s *starter) afterReady(ready toolResult) int {
	if ready.code != 0 {
		s.save(func(w *worker) { w.addNote("no-ready") })
		switch ready.code {
		case exitReceiver:
			fmt.Fprintf(s.stderr, "crew start: no ready message from %s in %s; the worker may still arrive, so read its pane with herdr pane read %s, or close it with crew stop %s --force\n",
				s.row.ID, s.limit, s.row.Pane, s.row.ID)
			return exitReceiver
		case exitRefused:
			fmt.Fprintf(s.stderr, "crew start: crewtalk refused the wait for the ready message: %s; crew stop %s --force closes what exists\n", ready.detail(), s.row.ID)
			return exitRefused
		}
		fmt.Fprintf(s.stderr, "crew start: crewtalk wait --as %s failed: %s; crew stop %s --force closes what exists\n", s.row.ReadyName, ready.detail(), s.row.ID)
		return exitFailed
	}
	var message struct {
		From string `json:"from"`
	}
	if err := json.Unmarshal([]byte(ready.stdout), &message); err != nil || !strings.HasPrefix(message.From, "codex:") {
		fmt.Fprintf(s.stderr, "crew start: the ready message did not come from a Codex session: %s; crew stop %s --force closes what exists\n", printable(ready.stdout), s.row.ID)
		return exitFailed
	}
	thread := strings.TrimPrefix(message.From, "codex:")
	if r := runTool("crewtalk", "join", "--as", s.row.ID, "--kind", "codex", "--address", thread); r.code != 0 {
		fmt.Fprintf(s.stderr, "crew start: crewtalk join --as %s failed: %s; crew stop %s --force closes what exists\n", s.row.ID, r.detail(), s.row.ID)
		return exitFailed
	}
	if !s.save(func(w *worker) { w.Address = thread }) {
		return exitFailed
	}
	if r := runTool("crewtalk", "leave", "--as", s.row.ReadyName); r.code != 0 {
		fmt.Fprintf(s.stderr, "crew start: crewtalk leave --as %s failed: %s; crew goes on\n", s.row.ReadyName, r.detail())
	}
	return s.sendTask()
}

func (s *starter) sendTask() int {
	r := runTool("crewtalk", "send", "--from", s.o.lead, "--to", s.row.ID, "--json", "--", taskMessage(s.o.task, s.o.lead))
	if r.notRun {
		s.save(func(w *worker) { w.addNote("task-not-sent") })
		fmt.Fprintf(s.stderr, "crew start: crewtalk send did not run: %s; nothing was delivered\n", r.detail())
		return exitFailed
	}
	switch r.code {
	case 0:
		var sent struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &sent); err != nil || sent.ID == "" {
			fmt.Fprintf(s.stderr, "crew start: crewtalk send printed no message id: %s\n", printable(r.stdout))
		}
		s.save(func(w *worker) {
			w.TaskID = sent.ID
			w.setState("working")
		})
		return exitOK
	case exitRefused, exitReceiver:
		s.save(func(w *worker) { w.addNote("task-not-sent") })
		fmt.Fprintf(s.stderr, "crew start: the task did not reach %s: %s; nothing was delivered, and crew does not send it again\n", s.row.ID, r.detail())
		return r.code
	case exitUsage:
		s.save(func(w *worker) { w.addNote("task-not-sent") })
		fmt.Fprintf(s.stderr, "crew start: crewtalk refused the send to %s: %s; nothing was delivered\n", s.row.ID, r.detail())
		return exitFailed
	}
	s.save(func(w *worker) {
		w.addNote("task-unknown")
		w.setState("working")
	})
	fmt.Fprintf(s.stderr, "crew start: the send to %s may have arrived: %s; check crewtalk log before you send again, and crew does not send it again\n", s.row.ID, r.detail())
	return exitFailed
}
