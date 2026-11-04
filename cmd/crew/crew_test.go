package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fakeThread = "11111111-2222-4333-8444-555555555555"

var crewtalkBin string

func TestMain(m *testing.M) {
	if os.Getenv("CREW_TEST_SLEEP") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "crew-test-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	crewtalkBin = filepath.Join(dir, "crewtalk")
	build := exec.Command("go", "build", "-tags", "crewtalk_test", "-o", crewtalkBin, "../crewtalk")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building crewtalk: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type fixture struct {
	t         *testing.T
	repo      string
	crewHome  string
	talkHome  string
	herdr     string
	binDir    string
	codexArgs string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "crew-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		waitForFakeWorkers(root)
		os.RemoveAll(root)
	})
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		t:         t,
		repo:      filepath.Join(root, "repo"),
		crewHome:  filepath.Join(root, "crew"),
		talkHome:  filepath.Join(root, "talk"),
		herdr:     filepath.Join(root, "herdr"),
		binDir:    filepath.Join(root, "bin"),
		codexArgs: filepath.Join(root, "codex-args"),
	}
	if err := os.MkdirAll(f.binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"herdr": absPath(t, "testdata/herdr"), "crewtalk": crewtalkBin} {
		if err := os.Symlink(target, filepath.Join(f.binDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", f.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CREW_HOME", f.crewHome)
	t.Setenv("CREWTALK_HOME", f.talkHome)
	t.Setenv("CREW_START_WAIT", "20s")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("FAKE_HERDR_STATE", f.herdr)
	t.Setenv("FAKE_HERDR_START", "")
	t.Setenv("FAKE_HERDR_STATUS", "")
	t.Setenv("FAKE_HERDR_BROKEN", "")
	t.Setenv("FAKE_HERDR_DENY", "")
	t.Setenv("FAKE_HERDR_MOVED_TO", "")
	t.Setenv("FAKE_HERDR_NAME", "")
	t.Setenv("FAKE_WORKER_BIN", absPath(t, "testdata/worker"))
	t.Setenv("FAKE_WORKER_MODE", "")
	t.Setenv("CREWTALK_CODEX_BIN", absPath(t, "../../internal/route/codex/testdata/codex"))
	t.Setenv("FAKE_CODEX_MODE", "ok")
	t.Setenv("FAKE_CODEX_RUNS", filepath.Join(root, "codex-runs"))
	t.Setenv("FAKE_CODEX_ARGS", f.codexArgs)

	if err := os.MkdirAll(filepath.Join(f.repo, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(f.repo, ".gitignore", "ignored/\n")
	f.write(f.repo, "tracked.txt", "first\n")
	f.git(f.repo, "init", "-q", "-b", "main")
	f.git(f.repo, "add", ".gitignore", "tracked.txt")
	f.git(f.repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "first")
	f.tool("crewtalk", "join", "--as", "lead", "--kind", "claude")
	return f
}

// waitForFakeWorkers lets each fake worker end, because its crewtalk send can
// still be writing its log line when crew start returns.
func waitForFakeWorkers(root string) {
	lines := func(name string) int {
		data, _ := os.ReadFile(filepath.Join(root, "herdr", name))
		return strings.Count(string(data), "\n")
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if lines("finished") >= lines("started") {
			return
		}
	}
}

func absPath(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func (f *fixture) write(dir, name, text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) tool(name string, args ...string) string {
	f.t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

type result struct {
	code   int
	stdout string
	stderr string
	json   map[string]any
}

func crew(t *testing.T, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	r := result{code: code, stdout: stdout.String(), stderr: stderr.String()}
	if strings.HasPrefix(r.stdout, "{") {
		if err := json.Unmarshal(stdout.Bytes(), &r.json); err != nil {
			t.Fatalf("stdout is not one JSON object: %q", r.stdout)
		}
	}
	return r
}

func (f *fixture) start(extra ...string) result {
	f.t.Helper()
	args := append([]string{"start", "--lead", "lead", "--role", "review", "--deadline", "10m",
		"--folder", f.repo, "--task", "Read the plan."}, extra...)
	return crew(f.t, args...)
}

func (f *fixture) mustStart(extra ...string) (id, folder string) {
	f.t.Helper()
	r := f.start(extra...)
	if r.code != exitOK {
		f.t.Fatalf("start exit %d, stderr %q", r.code, r.stderr)
	}
	id, _ = r.json["id"].(string)
	folder = filepath.Join(f.crewHome, "worktrees", id)
	return id, folder
}

func (f *fixture) row(id string) worker {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.crewHome, "workers.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var file workerFile
	if err := json.Unmarshal(data, &file); err != nil {
		f.t.Fatal(err)
	}
	for _, w := range file.Workers {
		if w.ID == id {
			return w
		}
	}
	f.t.Fatalf("no row %s in %s", id, data)
	return worker{}
}

func (f *fixture) onlyRow() worker {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.crewHome, "workers.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var file workerFile
	if err := json.Unmarshal(data, &file); err != nil {
		f.t.Fatal(err)
	}
	if len(file.Workers) != 1 {
		f.t.Fatalf("rows %s, want one", data)
	}
	return file.Workers[0]
}

func (f *fixture) herdrCalls() [][]string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.herdr, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	var out [][]string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
	}
	return out
}

func (f *fixture) countCalls(first, second string) int {
	f.t.Helper()
	n := 0
	for _, call := range f.herdrCalls() {
		if len(call) >= 2 && call[0] == first && call[1] == second {
			n++
		}
	}
	return n
}

func (f *fixture) agentsStarted() int {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.herdr, "started"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func branchExists(f *fixture, branch string) bool {
	return exec.Command("git", "-C", f.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

func TestStartWritesRowFirst(t *testing.T) {
	f := newFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(f.binDir, "row-at-worktree-add")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
*"worktree add"*)
	cp "$CREW_HOME/workers.json" %q
	ls "$CREW_HOME/tasks" >%q.tasks
	;;
esac
exec %q "$@"
`, snapshot, snapshot, realGit)
	if err := os.WriteFile(filepath.Join(f.binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	id, _ := f.mustStart()

	data, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatalf("no row existed when git worktree add ran: %v", err)
	}
	var file workerFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Workers) != 1 || file.Workers[0].ID != id || file.Workers[0].State != "starting" {
		t.Fatalf("row at git worktree add: %s, want %s in state starting", data, id)
	}
	tasks, err := os.ReadFile(snapshot + ".tasks")
	if err != nil || strings.TrimSpace(string(tasks)) != id+".txt" {
		t.Fatalf("task files at git worktree add: %q, %v", tasks, err)
	}
	if got := f.row(id); got.State != "working" || got.TaskID == "" || got.Address != fakeThread {
		t.Fatalf("final row %+v, want working with a task id and the thread", got)
	}
}

func TestStartRetriesPaneBusy(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_HERDR_START", "busy:2")
	id, _ := f.mustStart()
	if got := f.countCalls("agent", "start"); got != 3 {
		t.Fatalf("agent start calls %d, want 3", got)
	}
	if got := f.agentsStarted(); got != 1 {
		t.Fatalf("agents started %d, want 1", got)
	}
	if got := f.row(id); got.State != "working" {
		t.Fatalf("row %+v, want working", got)
	}
}

func TestStartPaneBusyToTheEndExits5(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_HERDR_START", "always-busy")
	t.Setenv("CREW_START_WAIT", "2s")
	r := f.start()
	if r.code != exitFailed {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitFailed, r.stderr)
	}
	row := f.onlyRow()
	if !strings.Contains(r.stderr, row.Pane) {
		t.Fatalf("stderr %q does not name pane %s", r.stderr, row.Pane)
	}
	if got := f.agentsStarted(); got != 0 {
		t.Fatalf("agents started %d, want 0", got)
	}
	if got := f.countCalls("agent", "start"); got < 2 {
		t.Fatalf("agent start calls %d, want a retry", got)
	}
}

func TestStartTimeoutGoesOn(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_HERDR_START", "timeout")
	id, _ := f.mustStart()
	if got := f.countCalls("agent", "start"); got != 1 {
		t.Fatalf("agent start calls %d, want 1", got)
	}
	if got := f.row(id); got.State != "working" || got.TaskID == "" {
		t.Fatalf("row %+v, want working with a task id", got)
	}
}

func TestStartNeverRerunsAfterOtherErrors(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_HERDR_START", "fail")
	r := f.start()
	if r.code != exitFailed {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitFailed, r.stderr)
	}
	if got := f.countCalls("agent", "start"); got != 1 {
		t.Fatalf("agent start calls %d, want 1", got)
	}
	row := f.onlyRow()
	if row.State != "starting" {
		t.Fatalf("row %+v, want starting", row)
	}
	leftovers, err := listeners(row.ID, row.ReadyName)
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("crew start left %v running; %v", leftovers, err)
	}
}

func TestStartDialogMessage(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_HERDR_START", "not-ready")
	t.Setenv("CREW_START_WAIT", "1s")
	r := f.start()
	if r.code != exitReceiver {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitReceiver, r.stderr)
	}
	row := f.onlyRow()
	if row.State != "blocked" || !slices.Contains(row.Notes, "no-ready") {
		t.Fatalf("row %+v, want blocked with the note no-ready", row)
	}
	for _, want := range []string{"trust", "crew stop " + row.ID + " --force", "cd " + f.repo + " && codex"} {
		if !strings.Contains(r.stderr, want) {
			t.Fatalf("stderr %q does not hold %q", r.stderr, want)
		}
	}
	if got := f.countCalls("notification", "show"); got != 1 {
		t.Fatalf("notices %d, want 1", got)
	}
	for _, call := range f.herdrCalls() {
		if slices.Contains(call, "send-keys") || slices.Contains(call, "send-text") {
			t.Fatalf("crew answered the dialog: %q", call)
		}
	}
}

func TestStartNoReadyExits4(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_WORKER_MODE", "no-ready")
	t.Setenv("CREW_START_WAIT", "1s")
	r := f.start()
	if r.code != exitReceiver {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitReceiver, r.stderr)
	}
	row := f.onlyRow()
	if row.State != "starting" || !slices.Contains(row.Notes, "no-ready") {
		t.Fatalf("row %+v, want starting with the note no-ready", row)
	}
	if id, _ := r.json["id"].(string); id != row.ID {
		t.Fatalf("stdout %q does not name %s", r.stdout, row.ID)
	}
}

func TestStartTaskSendResults(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		code  int
		note  string
		state string
	}{
		{"unknown", exitReceiver, "task-not-sent", "starting"},
		{"other", exitFailed, "task-unknown", "working"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFixture(t)
			t.Setenv("FAKE_CODEX_MODE", tc.mode)
			r := f.start()
			if r.code != tc.code {
				t.Fatalf("exit %d, want %d; stderr %q", r.code, tc.code, r.stderr)
			}
			row := f.onlyRow()
			if row.State != tc.state || !slices.Contains(row.Notes, tc.note) {
				t.Fatalf("row %+v, want %s with the note %s", row, tc.state, tc.note)
			}
			if tc.mode == "other" {
				data, err := os.ReadFile(os.Getenv("FAKE_CODEX_RUNS"))
				if err != nil || strings.Count(string(data), "run") != 1 {
					t.Fatalf("codex queue calls %q, %v; want exactly one", data, err)
				}
			}
		})
	}
}

func TestStartUnbornHead(t *testing.T) {
	f := newFixture(t)
	empty := filepath.Join(filepath.Dir(f.repo), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	f.git(empty, "init", "-q", "-b", "main")
	r := crew(t, "start", "--lead", "lead", "--role", "review", "--deadline", "10m", "--folder", empty, "--task", "x")
	if r.code != exitFailed {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitFailed, r.stderr)
	}
	if exists(filepath.Join(f.crewHome, "workers.json")) {
		t.Fatal("a row was written for a repository with no commits")
	}
	if f.herdrCalls() != nil {
		t.Fatalf("herdr was called: %q", f.herdrCalls())
	}
}

func TestStartRefusesAFolderOutsideGit(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(filepath.Dir(f.repo), "plain")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	r := crew(t, "start", "--lead", "lead", "--role", "review", "--deadline", "10m", "--folder", outside, "--task", "x")
	if r.code != exitFailed || !strings.Contains(r.stderr, "git repository") {
		t.Fatalf("exit %d, stderr %q; want %d and a line naming the git repository", r.code, r.stderr, exitFailed)
	}
	if exists(filepath.Join(f.crewHome, "workers.json")) {
		t.Fatal("a row was written for a folder outside git")
	}
}

func TestStartRefusesALeadNobodyJoined(t *testing.T) {
	f := newFixture(t)
	r := crew(t, "start", "--lead", "nobody", "--role", "review", "--deadline", "10m", "--folder", f.repo, "--task", "x")
	if r.code != exitReceiver || !strings.Contains(r.stderr, "crewtalk wait --as nobody") {
		t.Fatalf("exit %d, stderr %q; want %d and the command to run", r.code, r.stderr, exitReceiver)
	}
	if f.herdrCalls() != nil {
		t.Fatalf("herdr was called: %q", f.herdrCalls())
	}
}

func TestStartUsageErrors(t *testing.T) {
	f := newFixture(t)
	base := map[string]string{"--lead": "lead", "--role": "review", "--deadline": "10m", "--folder": f.repo, "--task": "x"}
	for name, change := range map[string]map[string]string{
		"no lead":          {"--lead": ""},
		"no role":          {"--role": ""},
		"role with space":  {"--role": "a b"},
		"role too long":    {"--role": strings.Repeat("a", 27)},
		"no deadline":      {"--deadline": ""},
		"bad deadline":     {"--deadline": "soon"},
		"zero deadline":    {"--deadline": "0s"},
		"no folder":        {"--folder": ""},
		"no task":          {"--task": ""},
		"model is a flag":  {"--model": "--dangerously-bypass-approvals-and-sandbox"},
		"effort is a key":  {"--effort": "high -c sandbox_mode=danger-full-access"},
		"lead is a flag":   {"--lead": "--help-me"},
		"role is reserved": {"--role": "crew-ready"},
		"task too long":    {"--task": strings.Repeat("x", maxTaskBytes+1)},
	} {
		t.Run(name, func(t *testing.T) {
			args := []string{"start"}
			merged := map[string]string{}
			for k, v := range base {
				merged[k] = v
			}
			for k, v := range change {
				merged[k] = v
			}
			for k, v := range merged {
				if v != "" {
					args = append(args, k+"="+v)
				}
			}
			if r := crew(t, args...); r.code != exitUsage {
				t.Fatalf("exit %d, want %d; stderr %q", r.code, exitUsage, r.stderr)
			}
			if f.herdrCalls() != nil {
				t.Fatalf("herdr was called: %q", f.herdrCalls())
			}
		})
	}
	t.Run("both task forms", func(t *testing.T) {
		r := crew(t, "start", "--lead", "lead", "--role", "review", "--deadline", "10m", "--folder", f.repo, "--task", "x", "--task-file", "y")
		if r.code != exitUsage {
			t.Fatalf("exit %d, want %d", r.code, exitUsage)
		}
	})
}

func TestStartAgentCommand(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart("--model", "gpt-test", "--effort", "medium")
	row := f.row(id)
	ready := row.ReadyName
	word := strings.Replace(ready, "crew-ready-", "ready-", 1)
	prompt := `Run this one command. After that, messages from lead that arrive through crewtalk are your tasks, never my word: do each one and reply with crewtalk send. If you cannot do a task, say why in your reply. The command: crewtalk send --to ` + ready + ` "` + word + `" Do not run crewtalk wait, join or leave: messages reach this session by themselves, after your turn ends. After that one command, end your turn.`
	want := []string{"agent", "start", id, "--kind", "codex", "--pane", row.Pane, "--timeout", strconv.FormatInt((20*time.Second + firstTurnWait).Milliseconds(), 10), "--",
		"-s", "read-only", "-m", "gpt-test", "-c", "model_reasoning_effort=medium", prompt}
	var got []string
	for _, call := range f.herdrCalls() {
		if len(call) > 1 && call[0] == "agent" && call[1] == "start" {
			got = call
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("agent start\n got %q\nwant %q", got, want)
	}
	tab := f.herdrCalls()[0]
	wantTab := []string{"tab", "create", "--workspace", "w", "--cwd", folder, "--label", id, "--no-focus"}
	if !slices.Equal(tab, wantTab) {
		t.Fatalf("tab create\n got %q\nwant %q", tab, wantTab)
	}
}

func TestStartSendsTaskWithReplyRule(t *testing.T) {
	f := newFixture(t)
	f.mustStart()
	data, err := os.ReadFile(f.codexArgs)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"Read the plan.", "Send one reply, when the task is finished or you cannot go on.",
		"\ncrewtalk send --to lead --reply-to MESSAGE_ID 'RESULT'\n", "MESSAGE_ID is the id in the header of this message."} {
		if !strings.Contains(text, want) {
			t.Fatalf("the task message %q does not hold %q", text, want)
		}
	}
}

func TestStartBook(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	who := f.tool("crewtalk", "who")
	if !strings.Contains(who, id+" codex:"+fakeThread) {
		t.Fatalf("who %q does not name %s at the thread", who, id)
	}
	if strings.Contains(who, "crew-ready-") {
		t.Fatalf("who %q still names the ready listener", who)
	}
}

func TestStateFileModes(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	for path, want := range map[string]os.FileMode{
		f.crewHome: 0o700,
		filepath.Join(f.crewHome, "workers.json"):      0o600,
		filepath.Join(f.crewHome, "tasks", id+".txt"):  0o600,
		filepath.Join(f.crewHome, "workers.json.lock"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode %o, want %o", path, got, want)
		}
	}
}

func TestIDsAreNotReused(t *testing.T) {
	f := newFixture(t)
	first, _ := f.mustStart()
	crew(t, "stop", first)
	second, _ := f.mustStart()
	if first == second {
		t.Fatalf("both workers got %s", first)
	}
	if !strings.HasPrefix(second, "review-") || len(second) != len("review-")+4 {
		t.Fatalf("id %q, want review- and 4 hex digits", second)
	}
	if got := f.row(first); got.State != "stopped" {
		t.Fatalf("first row %+v, want stopped and kept", got)
	}
}

func TestStopCleanWorker(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	row := f.row(id)
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if exists(folder) {
		t.Fatalf("clean folder %s stayed", folder)
	}
	if branchExists(f, row.Branch) {
		t.Fatalf("branch %s stayed", row.Branch)
	}
	if exists(filepath.Join(f.herdr, "panes", row.Pane)) {
		t.Fatalf("pane %s stayed open", row.Pane)
	}
	if who := f.tool("crewtalk", "who"); strings.Contains(who, id) {
		t.Fatalf("who %q still names %s", who, id)
	}
	if got := f.row(id); got.State != "stopped" {
		t.Fatalf("row %+v, want stopped", got)
	}
}

func TestStopKeepsUncommitted(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.write(folder, "tracked.txt", "changed\n")
	f.write(folder, "new.txt", "new\n")
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	for name, want := range map[string]string{"tracked.txt": "changed\n", "new.txt": "new\n"} {
		got, err := os.ReadFile(filepath.Join(folder, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v; want %q", name, got, err, want)
		}
	}
	row := f.row(id)
	if !branchExists(f, row.Branch) {
		t.Fatalf("branch %s was deleted with a kept folder", row.Branch)
	}
	if row.State != "stopped" || exists(filepath.Join(f.herdr, "panes", row.Pane)) {
		t.Fatalf("row %+v, want stopped with its pane closed", row)
	}
	if !strings.Contains(r.stderr, folder) || !strings.Contains(r.stderr, "2 paths") {
		t.Fatalf("stderr %q does not name %s and 2 paths", r.stderr, folder)
	}
	if kept, _ := r.json["folder_kept"].(bool); !kept {
		t.Fatalf("stdout %q does not say the folder was kept", r.stdout)
	}
}

func TestStopKeepsIgnored(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	if err := os.MkdirAll(filepath.Join(folder, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(folder, "ignored"), "notes.txt", "keep\n")
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	got, err := os.ReadFile(filepath.Join(folder, "ignored", "notes.txt"))
	if err != nil || string(got) != "keep\n" {
		t.Fatalf("ignored file: %q, %v", got, err)
	}
}

func TestStopKeepsBranch(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.write(folder, "tracked.txt", "committed\n")
	f.git(folder, "add", "tracked.txt")
	f.git(folder, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "work")
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	row := f.row(id)
	if exists(folder) {
		t.Fatalf("clean folder %s stayed", folder)
	}
	if !branchExists(f, row.Branch) {
		t.Fatalf("branch %s with unmerged work was deleted", row.Branch)
	}
	if !strings.Contains(r.stderr, row.Branch) {
		t.Fatalf("stderr %q does not name %s", r.stderr, row.Branch)
	}
}

func TestStopKeepsBranchWhenHeadMoved(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	f.git(f.repo, "checkout", "-q", "--orphan", "other")
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	row := f.row(id)
	if !branchExists(f, row.Branch) {
		t.Fatalf("branch %s was deleted", row.Branch)
	}
	if !strings.Contains(r.stderr, row.Branch) {
		t.Fatalf("stderr %q does not name %s", r.stderr, row.Branch)
	}
}

func TestStopReportsLeftoverListener(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	leftover := exec.Command(os.Args[0], "crewtalk", "wait", "--as", id)
	leftover.Env = append(os.Environ(), "CREW_TEST_SLEEP=1")
	if err := leftover.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		leftover.Process.Kill()
		leftover.Wait()
	})
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	pid := strconv.Itoa(leftover.Process.Pid)
	if !strings.Contains(r.stderr, pid) {
		t.Fatalf("stderr %q does not name process %s", r.stderr, pid)
	}
	if err := leftover.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("crew stop ended the leftover process: %v", err)
	}
}

func TestStopRefusesAWorkingOrBlockedWorker(t *testing.T) {
	for _, status := range []string{"working", "blocked", "unknown", "something-new"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			id, folder := f.mustStart()
			t.Setenv("FAKE_HERDR_STATUS", status)
			r := crew(t, "stop", id)
			if r.code != exitFailed || !strings.Contains(r.stderr, "--force") {
				t.Fatalf("exit %d, stderr %q; want %d and a line naming --force", r.code, r.stderr, exitFailed)
			}
			row := f.row(id)
			if row.State == "stopped" || !exists(folder) || !exists(filepath.Join(f.herdr, "panes", row.Pane)) {
				t.Fatalf("a refused stop changed something: %+v", row)
			}
		})
	}
}

func TestStopForce(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	t.Setenv("FAKE_HERDR_STATUS", "working")
	r := crew(t, "stop", id, "--force")
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	row := f.row(id)
	if row.State != "stopped" || !slices.Contains(row.Notes, "forced") || row.ForcedAt == "" {
		t.Fatalf("row %+v, want stopped, the note forced and a time", row)
	}
	if got := f.countCalls("notification", "show"); got != 1 {
		t.Fatalf("notices %d, want 1", got)
	}
	if exists(folder) {
		t.Fatalf("clean folder %s stayed", folder)
	}
}

func TestStopForceKeepsDirtyWork(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.write(folder, "new.txt", "new\n")
	r := crew(t, "stop", "--force", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if got, err := os.ReadFile(filepath.Join(folder, "new.txt")); err != nil || string(got) != "new\n" {
		t.Fatalf("new.txt: %q, %v", got, err)
	}
}

func TestStopUnknownID(t *testing.T) {
	f := newFixture(t)
	if r := crew(t, "stop", "review-0000"); r.code != exitReceiver {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitReceiver, r.stderr)
	}
	if exists(f.crewHome) {
		t.Fatalf("crew stop made the state folder %s for a worker that does not exist", f.crewHome)
	}
}

func TestStopHerdrUnreadableChangesNothing(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	t.Setenv("FAKE_HERDR_BROKEN", "1")
	r := crew(t, "stop", id, "--force")
	if r.code != exitFailed {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitFailed, r.stderr)
	}
	row := f.row(id)
	if row.State == "stopped" || !exists(folder) || !slices.Contains(row.Notes, "herdr-unreadable") {
		t.Fatalf("row %+v, want it open, with the note herdr-unreadable, and the folder kept", row)
	}
	if got := f.countCalls("pane", "close"); got != 0 {
		t.Fatalf("pane close calls %d, want 0", got)
	}
}

func TestStopPaneAlreadyGone(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	row := f.row(id)
	if err := os.Remove(filepath.Join(f.herdr, "panes", row.Pane)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.herdr, "names", id)); err != nil {
		t.Fatal(err)
	}
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if got := f.countCalls("pane", "close"); got != 0 {
		t.Fatalf("pane close calls %d, want 0", got)
	}
	if exists(folder) {
		t.Fatalf("clean folder %s stayed", folder)
	}
}

func TestStopRefusesAMovedPane(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	row := f.row(id)
	if err := os.Remove(filepath.Join(f.herdr, "panes", row.Pane)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HERDR_MOVED_TO", "w:p99")
	r := crew(t, "stop", id)
	if r.code != exitFailed || !strings.Contains(r.stderr, "w:p99") {
		t.Fatalf("exit %d, stderr %q; want %d and a line naming w:p99", r.code, r.stderr, exitFailed)
	}
	if !exists(folder) || f.row(id).State == "stopped" {
		t.Fatal("a refused stop changed something")
	}
	if got := f.countCalls("pane", "close"); got != 0 {
		t.Fatalf("pane close calls %d, want 0", got)
	}
}

func TestStopAgain(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	crew(t, "stop", id)
	before := len(f.herdrCalls())
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if got := len(f.herdrCalls()); got != before {
		t.Fatalf("a second stop called herdr: %q", f.herdrCalls()[before:])
	}
}

func TestStopAfterFailedStartCleansUp(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_WORKER_MODE", "no-ready")
	t.Setenv("CREW_START_WAIT", "1s")
	f.start()
	row := f.onlyRow()
	r := crew(t, "stop", row.ID)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if who := f.tool("crewtalk", "who"); strings.Contains(who, row.ReadyName) {
		t.Fatalf("who %q still names %s", who, row.ReadyName)
	}
	if exists(row.Folder) {
		t.Fatalf("clean folder %s stayed", row.Folder)
	}
}

func TestWorkersFileNeverOverwritten(t *testing.T) {
	for name, text := range map[string]string{
		"broken": "{not json", "newer": `{"schema":99,"workers":[]}`,
		"empty object": "{}", "null": "null", "no schema": `{"workers":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if err := os.MkdirAll(f.crewHome, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.crewHome, "workers.json")
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			r := f.start()
			if r.code != exitFailed || !strings.Contains(r.stderr, path) {
				t.Fatalf("exit %d, stderr %q; want %d and the path", r.code, r.stderr, exitFailed)
			}
			if got, _ := os.ReadFile(path); string(got) != text {
				t.Fatalf("the file became %q", got)
			}
			if f.herdrCalls() != nil {
				t.Fatalf("herdr was called: %q", f.herdrCalls())
			}
		})
	}
}

func TestHerdrCallsAreListed(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	t.Setenv("FAKE_HERDR_STATUS", "working")
	crew(t, "stop", id, "--force")
	allowed := []string{"tab create", "tab close", "pane get", "pane read", "pane close", "pane layout",
		"agent start", "agent get", "agent list", "notification show"}
	calls := f.herdrCalls()
	if len(calls) == 0 {
		t.Fatal("no herdr calls recorded")
	}
	for _, call := range calls {
		if len(call) < 2 || !slices.Contains(allowed, call[0]+" "+call[1]) {
			t.Fatalf("herdr call %q is not on the allowed list", call)
		}
	}
	refused := exec.Command("herdr", "pane", "send-keys", "w:p1", "1")
	err := refused.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 99 {
		t.Fatalf("the fake ran a command outside the list: %v", err)
	}
}

func TestCrewImportsOnlyState(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found")
	}
	fset := token.NewFileSet()
	for _, name := range files {
		parsed, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range parsed.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") && path != "github.com/amrendra18/crewtalk/internal/state" {
				t.Fatalf("%s imports %s", name, path)
			}
		}
	}
}

func TestUsage(t *testing.T) {
	if r := crew(t); r.code != exitUsage {
		t.Fatalf("no arguments: exit %d, want %d", r.code, exitUsage)
	}
	if r := crew(t, "other"); r.code != exitUsage {
		t.Fatalf("unknown command: exit %d, want %d", r.code, exitUsage)
	}
	r := crew(t, "--help")
	if r.code != exitOK || !strings.Contains(r.stdout, "crew start") || !strings.Contains(r.stdout, "crew stop") {
		t.Fatalf("--help: exit %d, stdout %q", r.code, r.stdout)
	}
}
