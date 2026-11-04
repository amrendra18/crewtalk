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

	"github.com/amrendra18/crewtalk/internal/book"
	"github.com/amrendra18/crewtalk/internal/notice"
	"github.com/amrendra18/crewtalk/internal/state"
)

const (
	startWait = 49 * time.Second
	// firstTurnWait is added to startWait for herdr's own wait, because a herdr
	// timeout loses the worker's name.
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
}

func start(args []string, stdout, stderr io.Writer) int {
	var o startOptions
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.StringVar(&o.lead, "lead", "", "")
	fs.StringVar(&o.role, "role", "", "")
	fs.StringVar(&o.folder, "folder", "", "")
	fs.StringVar(&o.task, "task", "", "")
	fs.StringVar(&o.taskFile, "task-file", "", "")
	fs.StringVar(&o.model, "model", "", "")
	fs.StringVar(&o.effort, "effort", "", "")
	if code, done := parseFlags(fs, args, stdout, stderr); done {
		return code
	}
	if reason := o.check(fs.NArg()); reason != "" {
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

func (o *startOptions) check(extra int) string {
	switch {
	case extra != 0:
		return "start takes no other arguments"
	case book.CheckName(o.lead) != nil:
		return "--lead needs a name that crewtalk accepts"
	case !shellSafe.MatchString(o.lead):
		return "--lead holds a character that the worker's reply command could not carry; use letters, digits and _ @ % + = . -, and do not start with ="
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

func readOnlyWorkerCommand(model, effort, prompt string) []string {
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

func (s *starter) failNamingCleanup(code int, format string, args ...any) int {
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
	if err := applyToRow(s.home, &s.row, change); err != nil {
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
		Repo: repo, Base: base, State: "starting", StateSince: stamp, Notes: []string{},
	}
	if err := addWorker(home, &s.row); err != nil {
		return s.failf("cannot save the worker: %s", printable(err.Error()))
	}
	if err := writeTask(s.row.TaskFile, s.o.task); err != nil {
		return s.failNamingCleanup(exitFailed, "cannot save the task: %s", printable(err.Error()))
	}
	if r := runTool("git", "-C", repo, "worktree", "add", "-q", "-b", s.row.Branch, s.row.Folder, "HEAD"); !r.ok() {
		return s.failNamingCleanup(exitFailed, "git worktree add failed: %s", r.detail())
	}
	ready, word := s.row.ReadyName, strings.Replace(s.row.ReadyName, "crew-ready-", "ready-", 1)
	if r := runTool("crewtalk", "join", "--as", ready, "--kind", "claude", "--address", ready); !r.ok() {
		return s.failNamingCleanup(exitFailed, "crewtalk join --as %s failed: %s", ready, r.detail())
	}
	tab := runTool("herdr", "tab", "create", "--workspace", workspace, "--cwd", s.row.Folder, "--label", s.row.ID, "--no-focus")
	if !tab.ok() {
		return s.failNamingCleanup(exitFailed, "herdr tab create failed: %s", tab.detail())
	}
	pane, err := parsePane(tab.stdout)
	if err != nil {
		return s.failNamingCleanup(exitFailed, "herdr tab create printed no pane id: %s", tab.detail())
	}
	s.tabCreated = time.Now()
	if !s.save(func(w *worker) { w.Pane = pane }) {
		return exitFailed
	}
	return s.startAgent(ready, firstPrompt(s.o.lead, ready, word))
}

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
	if !r.ok() {
		return "", "", "", s.failf("%s is not in a git repository: %s", printable(s.o.folder), r.detail())
	}
	repo = strings.TrimSpace(r.stdout)
	r = runTool("git", "-C", repo, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if !r.ok() {
		return "", "", "", s.failf("the HEAD of %s has no commit, so a worktree cannot start from it; make a commit, or check out a branch that has one", printable(repo))
	}
	base = strings.TrimSpace(r.stdout)
	who := runTool("crewtalk", "who", "--json")
	if !who.ok() {
		return "", "", "", s.failf("crewtalk who failed: %s", who.detail())
	}
	var addressBook struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(who.stdout), &addressBook); err != nil {
		return "", "", "", s.failf("crewtalk who printed no address book: %s", printable(err.Error()))
	}
	for _, agent := range addressBook.Agents {
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

func parsePane(text string) (string, error) {
	var body struct {
		Result struct {
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		return "", err
	}
	if body.Result.RootPane.PaneID == "" {
		return "", errors.New("no pane id")
	}
	return body.Result.RootPane.PaneID, nil
}

// The ready message is the signal to go on, because the return of agent start
// says nothing about the first turn.
func (s *starter) startAgent(ready, prompt string) int {
	waitCtx, stopWait := context.WithCancel(context.Background())
	defer stopWait()
	readyDone := make(chan toolResult, 1)
	go func() {
		readyDone <- runToolFor(waitCtx, s.limit+10*time.Second, "crewtalk", "wait", "--as", ready, "--timeout", s.limit.String(), "--json")
	}()

	herdrWait := s.limit + firstTurnWait
	args := []string{"agent", "start", s.row.ID, "--kind", "codex", "--pane", s.row.Pane, "--timeout", strconv.FormatInt(herdrWait.Milliseconds(), 10), "--"}
	args = append(args, readOnlyWorkerCommand(s.o.model, s.o.effort, prompt)...)
	agentDone := make(chan toolResult, 1)
	go func() { agentDone <- s.runAgentStart(args, herdrWait) }()

	var readyResult toolResult
	agentReturned := false
	for waiting := true; waiting; {
		select {
		case readyResult = <-readyDone:
			waiting = false
		case r := <-agentDone:
			agentReturned = true
			if code := s.exitCodeAfterAgentStart(r, false); code != exitOK {
				stopWait()
				<-readyDone
				s.print()
				return code
			}
		}
	}
	s.readySeconds = time.Since(s.tabCreated).Seconds()

	code := s.afterReady(readyResult)
	if !agentReturned {
		code = agentFailureWins(code, s.exitCodeAfterAgentStart(<-agentDone, readyResult.ok()))
	}
	s.print()
	return code
}

func agentFailureWins(readyCode, agentCode int) int {
	if agentCode != exitOK {
		return agentCode
	}
	return readyCode
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

func (s *starter) exitCodeAfterAgentStart(r toolResult, readyCame bool) int {
	if r.ok() {
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
		notice.Show("crew: "+s.row.ID+" is blocked", "A dialog is open in pane "+s.row.Pane+". Only you can answer it.")
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

func (s *starter) afterReady(ready toolResult) int {
	if !ready.ok() {
		s.save(func(w *worker) { w.addNote("no-ready") })
		switch {
		case ready.exited(exitReceiver):
			fmt.Fprintf(s.stderr, "crew start: no ready message from %s in %s; the worker may still arrive, so read its pane with herdr pane read %s, or close it with crew stop %s --force\n",
				s.row.ID, s.limit, s.row.Pane, s.row.ID)
			return exitReceiver
		case ready.exited(exitRefused):
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
	if r := runTool("crewtalk", "join", "--as", s.row.ID, "--kind", "codex", "--address", thread); !r.ok() {
		fmt.Fprintf(s.stderr, "crew start: crewtalk join --as %s failed: %s; crew stop %s --force closes what exists\n", s.row.ID, r.detail(), s.row.ID)
		return exitFailed
	}
	if !s.save(func(w *worker) { w.Address = thread }) {
		return exitFailed
	}
	if r := runTool("crewtalk", "leave", "--as", s.row.ReadyName); !r.ok() {
		fmt.Fprintf(s.stderr, "crew start: crewtalk leave --as %s failed: %s; crew goes on\n", s.row.ReadyName, r.detail())
	}
	return s.sendTaskOnce()
}

func (s *starter) sendTaskOnce() int {
	r := runTool("crewtalk", "send", "--from", s.o.lead, "--to", s.row.ID, "--json", "--", taskMessage(s.o.task, s.o.lead))
	if r.end == toolNeverRan {
		s.save(func(w *worker) { w.addNote("task-not-sent") })
		fmt.Fprintf(s.stderr, "crew start: crewtalk send did not run: %s; nothing was delivered, and crew stop %s --force closes what exists\n", r.detail(), s.row.ID)
		return exitFailed
	}
	switch {
	case r.ok():
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
	case r.exited(exitRefused), r.exited(exitReceiver):
		s.save(func(w *worker) { w.addNote("task-not-sent") })
		fmt.Fprintf(s.stderr, "crew start: the task did not reach %s: %s; nothing was delivered, crew does not send it again, and crew stop %s --force closes what exists\n", s.row.ID, r.detail(), s.row.ID)
		return r.exitCode
	case r.exited(exitUsage):
		s.save(func(w *worker) { w.addNote("task-not-sent") })
		fmt.Fprintf(s.stderr, "crew start: crewtalk refused the send to %s: %s; nothing was delivered, and crew stop %s --force closes what exists\n", s.row.ID, r.detail(), s.row.ID)
		return exitFailed
	}
	s.save(func(w *worker) {
		w.addNote("task-unknown")
		w.setState("working")
	})
	fmt.Fprintf(s.stderr, "crew start: the send to %s may have arrived: %s; check crewtalk log before you send again, crew does not send it again, and crew stop %s --force closes what exists\n", s.row.ID, r.detail(), s.row.ID)
	return exitFailed
}
