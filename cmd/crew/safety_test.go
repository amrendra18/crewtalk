package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func (f *fixture) gitConfig(lines ...string) {
	f.t.Helper()
	path := filepath.Join(filepath.Dir(f.repo), "gitconfig")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.t.Setenv("GIT_CONFIG_GLOBAL", path)
}

func (f *fixture) commit(dir, name, text string) string {
	f.t.Helper()
	f.write(dir, name, text)
	f.git(dir, "add", name)
	f.git(dir, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "work")
	return f.git(dir, "rev-parse", "HEAD")
}

func wantKept(t *testing.T, r result, folder string) {
	t.Helper()
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if !exists(folder) {
		t.Fatalf("folder %s is gone", folder)
	}
	if kept, _ := r.json["folder_kept"].(bool); !kept {
		t.Fatalf("stdout %q does not say the folder was kept", r.stdout)
	}
	if !strings.Contains(r.stderr, folder) {
		t.Fatalf("stderr %q does not name %s", r.stderr, folder)
	}
}

func TestStopKeepsFilesUnderGitSettings(t *testing.T) {
	for name, lines := range map[string][]string{
		"untracked files hidden": {"[status]", "showUntrackedFiles = no"},
		"everything excluded":    {"[core]", "excludesFile = EXCLUDES"},
		"stat ignored":           {"[core]", "ignoreStat = true"},
		"file mode ignored":      {"[core]", "fileMode = false"},
		"submodules ignored":     {"[diff]", "ignoreSubmodules = all"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			excludes := filepath.Join(filepath.Dir(f.repo), "excludes")
			f.write(filepath.Dir(f.repo), "excludes", "*\n")
			for i, line := range lines {
				lines[i] = strings.Replace(line, "EXCLUDES", excludes, 1)
			}
			f.gitConfig(lines...)
			id, folder := f.mustStart()
			f.write(folder, "new.txt", "new\n")
			if err := os.MkdirAll(filepath.Join(folder, "ignored"), 0o755); err != nil {
				t.Fatal(err)
			}
			f.write(filepath.Join(folder, "ignored"), "notes.txt", "keep\n")
			wantKept(t, crew(t, "stop", id), folder)
			for path, want := range map[string]string{"new.txt": "new\n", "ignored/notes.txt": "keep\n"} {
				got, err := os.ReadFile(filepath.Join(folder, path))
				if err != nil || string(got) != want {
					t.Fatalf("%s: %q, %v", path, got, err)
				}
			}
		})
	}
}

func TestStopKeepsCommitOnDetachedHead(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.git(folder, "checkout", "-q", "--detach")
	hash := f.commit(folder, "tracked.txt", "detached\n")
	r := crew(t, "stop", id)
	wantKept(t, r, folder)
	if got := f.git(folder, "rev-parse", "HEAD"); got != hash {
		t.Fatalf("HEAD %s, want %s", got, hash)
	}
	if !branchExists(f, f.row(id).Branch) {
		t.Fatal("the branch was deleted with a kept folder")
	}
}

func TestStopKeepsFolderOnAnotherBranch(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.git(folder, "checkout", "-q", "-b", "feature")
	wantKept(t, crew(t, "stop", id), folder)
}

func TestStopKeepsHiddenEdits(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			f := newFixture(t)
			id, folder := f.mustStart()
			f.git(folder, "update-index", flag, "tracked.txt")
			f.write(folder, "tracked.txt", "hidden edit\n")
			wantKept(t, crew(t, "stop", id), folder)
			if got, _ := os.ReadFile(filepath.Join(folder, "tracked.txt")); string(got) != "hidden edit\n" {
				t.Fatalf("tracked.txt became %q", got)
			}
		})
	}
}

func TestStopIgnoresGitLocationVariables(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	if err := os.MkdirAll(filepath.Join(folder, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(folder, "ignored"), "notes.txt", "keep\n")
	t.Setenv("GIT_DIR", filepath.Join(f.repo, ".git"))
	t.Setenv("GIT_WORK_TREE", f.repo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(f.repo, ".git", "index"))
	wantKept(t, crew(t, "stop", id), folder)
}

func TestStopReadsTheFolderItNames(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	t.Setenv("GIT_DIR", filepath.Join(f.repo, ".git"))
	t.Setenv("GIT_WORK_TREE", f.repo)
	if r := crew(t, "stop", id); r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if exists(folder) {
		t.Fatalf("clean folder %s stayed, because git read another repository", folder)
	}
}

func TestStopKeepsALockedFolder(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.git(f.repo, "worktree", "lock", folder)
	r := crew(t, "stop", id)
	wantKept(t, r, folder)
	if !strings.Contains(r.stderr, "git worktree remove failed") || !strings.Contains(r.stderr, "worktree remove --force") {
		t.Fatalf("stderr %q gives no reason and no recovery command", r.stderr)
	}
	if !f.row(id).FolderKept {
		t.Fatalf("row %+v does not record the kept folder", f.row(id))
	}
}

func TestStopRunsOnlyPlainGitRemoval(t *testing.T) {
	f := newFixture(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(f.binDir, "git-calls")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >>%q\nexec %q \"$@\"\n", log, realGit)
	if err := os.WriteFile(filepath.Join(f.binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	id, folder := f.mustStart()
	row := f.row(id)
	if r := crew(t, "stop", id); r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	if !strings.Contains(calls, "-C "+f.repo+" worktree remove "+folder+"\n") {
		t.Fatalf("no plain git worktree remove in:\n%s", calls)
	}
	if !strings.Contains(calls, "-C "+f.repo+" branch -q -d "+row.Branch+"\n") {
		t.Fatalf("no plain git branch -d in:\n%s", calls)
	}
	for _, word := range []string{"--force", " -f ", " -D ", " clean", " reset", "rm "} {
		if strings.Contains(calls, word) {
			t.Fatalf("git calls hold %q:\n%s", word, calls)
		}
	}
}

func TestStopGoesOnWhenIdleOrDone(t *testing.T) {
	for _, status := range []string{"idle", "done"} {
		t.Run(status, func(t *testing.T) {
			f := newFixture(t)
			id, _ := f.mustStart()
			t.Setenv("FAKE_HERDR_STATUS", status)
			if r := crew(t, "stop", id); r.code != exitOK {
				t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
			}
		})
	}
}

func TestStopRefusesAnotherAgentInPane(t *testing.T) {
	for _, extra := range [][]string{nil, {"--force"}} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			f := newFixture(t)
			id, folder := f.mustStart()
			t.Setenv("FAKE_HERDR_NAME", "someone-else")
			r := crew(t, append([]string{"stop", id}, extra...)...)
			if r.code != exitFailed || !strings.Contains(r.stderr, "someone-else") || !strings.Contains(r.stderr, "then run crew stop again") {
				t.Fatalf("exit %d, stderr %q; want %d and a line naming the other agent and the next step", r.code, r.stderr, exitFailed)
			}
			if got := f.countCalls("pane", "close"); got != 0 {
				t.Fatalf("pane close calls %d, want 0", got)
			}
			if !exists(folder) || f.row(id).State == "stopped" {
				t.Fatal("a refused stop changed something")
			}
		})
	}
}

func TestStopAgentWithNoNameGoesOn(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	t.Setenv("FAKE_HERDR_NAME", "none")
	if r := crew(t, "stop", id); r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestStopFailedAgentReadChangesNothing(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	t.Setenv("FAKE_HERDR_BROKEN", "agent get")
	r := crew(t, "stop", id, "--force")
	if r.code != exitFailed {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitFailed, r.stderr)
	}
	if got := f.countCalls("pane", "close"); got != 0 {
		t.Fatalf("pane close calls %d, want 0", got)
	}
	if !exists(folder) || f.row(id).State == "stopped" {
		t.Fatal("a stop that could not read herdr changed something")
	}
}

func TestStopFailedReadByNameIsNotGone(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	if err := os.Remove(filepath.Join(f.herdr, "panes", f.row(id).Pane)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HERDR_BROKEN", "agent get")
	r := crew(t, "stop", id)
	if r.code != exitFailed {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitFailed, r.stderr)
	}
	if !exists(folder) || f.row(id).State == "stopped" {
		t.Fatal("a stop that could not read herdr changed something")
	}
}

func TestCrewPauseRefusesStart(t *testing.T) {
	f := newFixture(t)
	f.write(f.talkHome, "PAUSE", "")
	r := f.start()
	if r.code != exitRefused || !strings.Contains(r.stderr, "PAUSE") {
		t.Fatalf("exit %d, stderr %q; want %d and a line naming PAUSE", r.code, r.stderr, exitRefused)
	}
	if f.herdrCalls() != nil {
		t.Fatalf("herdr was called: %q", f.herdrCalls())
	}
	if exists(filepath.Join(f.crewHome, "workers.json")) {
		t.Fatal("a row was written while PAUSE exists")
	}
}

func TestCrewPauseStopStillWorks(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.write(f.talkHome, "PAUSE", "")
	if r := crew(t, "stop", id); r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if exists(folder) {
		t.Fatalf("clean folder %s stayed", folder)
	}
}

func TestCrewHomeIsMadeAbsolute(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Dir(f.repo)
	t.Chdir(dir)
	t.Setenv("CREW_HOME", "relative")
	f.crewHome = filepath.Join(dir, "relative")
	id, folder := f.mustStart()
	row := f.row(id)
	if row.Folder != folder || row.TaskFile != filepath.Join(f.crewHome, "tasks", id+".txt") {
		t.Fatalf("row %+v, want absolute paths under %s", row, f.crewHome)
	}
	f.write(folder, "new.txt", "new\n")
	wantKept(t, crew(t, "stop", id), folder)
}

func TestRecoveryCommandsAreQuoted(t *testing.T) {
	for _, name := range []string{"my repo; touch PWNED", "it's; touch PWNED; '"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			root := filepath.Dir(f.repo)
			repo := filepath.Join(root, name)
			if err := os.Rename(f.repo, repo); err != nil {
				t.Fatal(err)
			}
			f.repo = repo
			id, folder := f.mustStart()
			f.write(folder, "new.txt", "new\n")
			r := crew(t, "stop", id)
			wantKept(t, r, folder)
			_, command, found := strings.Cut(r.stderr, "with the branch: ")
			if !found {
				t.Fatalf("stderr %q holds no recovery command", r.stderr)
			}
			sh := exec.Command("sh", "-c", strings.TrimSpace(command))
			sh.Dir = root
			if out, err := sh.CombinedOutput(); err != nil {
				t.Fatalf("the printed command failed: %v\n%s\ncommand: %s", err, out, command)
			}
			if exists(filepath.Join(root, "PWNED")) {
				t.Fatal("the printed command ran text from the folder's name")
			}
			if exists(folder) {
				t.Fatalf("the printed command left %s", folder)
			}
		})
	}
}

func TestDialogCommandIsQuoted(t *testing.T) {
	f := newFixture(t)
	repo := filepath.Join(filepath.Dir(f.repo), "my repo; touch PWNED")
	if err := os.Rename(f.repo, repo); err != nil {
		t.Fatal(err)
	}
	f.repo = repo
	t.Setenv("FAKE_HERDR_START", "not-ready")
	t.Setenv("CREW_START_WAIT", "1s")
	r := f.start()
	if want := "cd '" + repo + "' && codex"; !strings.Contains(r.stderr, want) {
		t.Fatalf("stderr %q does not hold %q", r.stderr, want)
	}
}

func TestStartRefusesAHugeTaskFile(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(filepath.Dir(f.repo), "task.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 3_000_000)), 0o600); err != nil {
		t.Fatal(err)
	}
	r := crew(t, "start", "--lead", "lead", "--role", "review", "--folder", f.repo, "--task-file", path)
	if r.code != exitUsage {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitUsage, r.stderr[:min(len(r.stderr), 200)])
	}
	if f.herdrCalls() != nil {
		t.Fatalf("herdr was called: %q", f.herdrCalls())
	}
}

func TestStartSendThatNeverRanIsNotSent(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	row := f.row(id)
	if err := os.Remove(filepath.Join(f.binDir, "crewtalk")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", f.binDir)
	var stdout, stderr strings.Builder
	s := starter{o: startOptions{lead: "lead", task: "x"}, home: f.crewHome, row: row, stdout: &stdout, stderr: &stderr}
	if err := applyToRow(f.crewHome, &s.row, func(w *worker) { w.State, w.Notes = "starting", []string{} }); err != nil {
		t.Fatal(err)
	}
	if code := s.sendTaskOnce(); code != exitFailed {
		t.Fatalf("exit %d, want %d; stderr %q", code, exitFailed, stderr.String())
	}
	got := f.row(id)
	if !slices.Contains(got.Notes, "task-not-sent") || slices.Contains(got.Notes, "task-unknown") || got.State != "starting" {
		t.Fatalf("row %+v, want starting with the note task-not-sent", got)
	}
}

func TestStartRefusesANonCodexReady(t *testing.T) {
	f := newFixture(t)
	t.Setenv("FAKE_WORKER_MODE", "wrong-sender")
	r := f.start()
	if r.code != exitFailed || !strings.Contains(r.stderr, "did not come from a Codex session") {
		t.Fatalf("exit %d, stderr %q; want %d and a line saying the sender is not a Codex session", r.code, r.stderr, exitFailed)
	}
	row := f.onlyRow()
	if row.Address != "" || row.TaskID != "" || row.State != "starting" {
		t.Fatalf("row %+v, want no address, no task and state starting", row)
	}
	if who := f.tool("crewtalk", "who"); strings.Contains(who, row.ID+" ") {
		t.Fatalf("who %q names %s", who, row.ID)
	}
}

func TestNameCollisionWithARow(t *testing.T) {
	f := newFixture(t)
	suffixes := []string{"aaaa", "aaaa", "bbbb"}
	real := newSuffix
	t.Cleanup(func() { newSuffix = real })
	newSuffix = func() (string, error) {
		next := suffixes[0]
		suffixes = suffixes[1:]
		return next, nil
	}
	first, _ := f.mustStart()
	if first != "review-aaaa" {
		t.Fatalf("first id %s", first)
	}
	r := crew(t, "start", "--lead", "lead", "--role", "other", "--folder", f.repo, "--task", "x")
	if id, _ := r.json["id"].(string); r.code != exitOK || id != "other-bbbb" {
		t.Fatalf("exit %d, id %q, stderr %q; want other-bbbb, because the ready name crew-ready-aaaa is taken", r.code, id, r.stderr)
	}
}

func TestNameCollisionGivesUp(t *testing.T) {
	f := newFixture(t)
	real := newSuffix
	t.Cleanup(func() { newSuffix = real })
	calls := 0
	newSuffix = func() (string, error) {
		calls++
		return "aaaa", nil
	}
	f.mustStart()
	calls = 0
	r := f.start()
	if r.code != exitFailed || calls != idTries {
		t.Fatalf("exit %d after %d tries, stderr %q; want %d after %d", r.code, calls, r.stderr, exitFailed, idTries)
	}
}

func TestPrintJSONEscapesControls(t *testing.T) {
	var out strings.Builder
	printJSON(&out, map[string]string{"folder": "a\u009bb"})
	if strings.Contains(out.String(), "\u009b") || !strings.Contains(out.String(), `\u009b`) {
		t.Fatalf("printJSON printed %q", out.String())
	}
}

func TestPrintableKeepsTextOfInvalidBytes(t *testing.T) {
	got := printable(strings.Repeat("\x80", 1000))
	if !strings.HasPrefix(got, `\x80\x80`) || !strings.HasSuffix(got, "...") {
		t.Fatalf("printable kept %q", got[:min(len(got), 40)])
	}
}

func TestShellQuoteQuotesALeadingEquals(t *testing.T) {
	if got := shellQuote("=lead"); got != "'=lead'" {
		t.Fatalf("shellQuote(=lead) = %q", got)
	}
	if got := shellQuote("a=b"); got != "a=b" {
		t.Fatalf("shellQuote(a=b) = %q", got)
	}
}

func TestPrintableKeepsEscapesWhole(t *testing.T) {
	for pad := 390; pad < 402; pad++ {
		got := printable(strings.Repeat("x", pad) + strings.Repeat("\x1b", 20))
		tail := strings.TrimSuffix(got, "...")
		if tail == got {
			t.Fatalf("pad %d: a long text has no mark that it was cut: %q", pad, got)
		}
		if i := strings.LastIndex(tail, `\`); i >= 0 && len(tail)-i != len(`\x1b`) {
			t.Fatalf("pad %d: the text ends in a cut escape: %q", pad, got[max(0, len(got)-12):])
		}
	}
}

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"plain":             "plain",
		"a\x1b[31mb":        `a\x1b[31mb`,
		"one\ntwo\r\tthree": `one two\x0d three`,
		"\u009b31m":         `\u009b31m`,
		"caf\u00e9":         "caf\u00e9",
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
	long := printable("x" + strings.Repeat("\u00e9", 1000))
	if !strings.HasSuffix(long, "...") || !strings.Contains(long, "\u00e9") || strings.ContainsRune(long, '\ufffd') {
		t.Errorf("a long text became %q", long)
	}
	r := toolResult{stderr: `{"error":{"code":"x","message":"bad` + "\\u001b" + `[31m"}}`}
	if got := r.detail(); strings.ContainsRune(got, 0x1b) || !strings.Contains(got, `\x1b`) {
		t.Errorf("detail() did not show the control character as an escape: %q", got)
	}
}

func TestWorkersTwoWriters(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.crewHome, 0o700); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Go(func() {
			row := worker{Role: "review", State: "starting", Notes: []string{}}
			errs <- addWorker(f.crewHome, &row)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	file, err := readFile(filepath.Join(f.crewHome, "workers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Workers) != writers {
		t.Fatalf("%d rows, want %d", len(file.Workers), writers)
	}
}
