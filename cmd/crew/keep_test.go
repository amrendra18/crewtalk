package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func sparseFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	for _, dir := range []string{"inside", "outside"} {
		if err := os.MkdirAll(filepath.Join(f.repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.write(filepath.Join(f.repo, "inside"), "a.txt", "a\n")
	f.write(filepath.Join(f.repo, "outside"), "b.txt", "b\n")
	f.git(f.repo, "add", "inside", "outside")
	f.git(f.repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "more")
	f.git(f.repo, "sparse-checkout", "set", "inside")
	return f
}

func TestStopRemovesACleanSparseFolder(t *testing.T) {
	f := sparseFixture(t)
	id, folder := f.mustStart()
	if exists(filepath.Join(folder, "outside", "b.txt")) {
		t.Fatal("the worktree did not leave out outside/b.txt, so this test shows nothing")
	}
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if exists(folder) {
		t.Fatalf("clean sparse folder %s stayed; stderr %q", folder, r.stderr)
	}
	if branchExists(f, f.row(id).Branch) {
		t.Fatalf("branch %s stayed", f.row(id).Branch)
	}
}

func TestStopKeepsAFileOverALeftOutPath(t *testing.T) {
	f := sparseFixture(t)
	id, folder := f.mustStart()
	if err := os.MkdirAll(filepath.Join(folder, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(folder, "outside"), "b.txt", "written by hand\n")
	r := crew(t, "stop", id)
	wantKept(t, r, folder)
	if got, _ := os.ReadFile(filepath.Join(folder, "outside", "b.txt")); string(got) != "written by hand\n" {
		t.Fatalf("outside/b.txt became %q", got)
	}
}

func TestStopSeesPastAFileMonitor(t *testing.T) {
	f := newFixture(t)
	hook := filepath.Join(filepath.Dir(f.repo), "monitor")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'token\\0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.gitConfig("[core]", "fsmonitor = "+hook)
	id, folder := f.mustStart()
	f.git(folder, "status", "--porcelain")
	time.Sleep(1100 * time.Millisecond)
	f.git(folder, "status", "--porcelain")
	f.write(folder, "tracked.txt", "edited\n")
	if got := f.git(folder, "status", "--porcelain"); got != "" {
		t.Fatalf("the monitor did not hide the edit, so this test shows nothing: %q", got)
	}
	wantKept(t, crew(t, "stop", id), folder)
	if got, _ := os.ReadFile(filepath.Join(folder, "tracked.txt")); string(got) != "edited\n" {
		t.Fatalf("tracked.txt became %q", got)
	}
}

func TestStopSeesASameSizeEditUnderLooseStat(t *testing.T) {
	f := newFixture(t)
	f.gitConfig("[core]", "checkStat = minimal", "trustctime = false")
	id, folder := f.mustStart()
	path := filepath.Join(folder, "tracked.txt")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	f.git(folder, "update-index", "--refresh")
	time.Sleep(1100 * time.Millisecond)
	f.write(folder, "tracked.txt", "fixed\n")
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if got := f.git(folder, "status", "--porcelain"); got != "" {
		t.Fatalf("the settings did not hide the edit, so this test shows nothing: %q", got)
	}
	r := crew(t, "stop", id)
	wantKept(t, r, folder)
	if !strings.Contains(r.stderr, "core.checkStat=default") {
		t.Fatalf("the printed command does not read the folder as crew did: %q", r.stderr)
	}
	if got, _ := os.ReadFile(path); string(got) != "fixed\n" {
		t.Fatalf("tracked.txt became %q", got)
	}
}

func TestStopKeepsFilesUnderANestedGitFolder(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	if err := os.MkdirAll(filepath.Join(folder, "x", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(folder, "x", ".git"), "notes.txt", "keep\n")
	r := crew(t, "stop", id)
	wantKept(t, r, folder)
	if got, _ := os.ReadFile(filepath.Join(folder, "x", ".git", "notes.txt")); string(got) != "keep\n" {
		t.Fatalf("notes.txt became %q", got)
	}
}

func TestStopKeepsAStoppedAm(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	patch := filepath.Join(filepath.Dir(f.repo), "work.patch")
	f.write(filepath.Dir(f.repo), "work.patch", `From 0000000000000000000000000000000000000000 Mon Sep 17 00:00:00 2001
From: Test <test@example.invalid>
Date: Wed, 4 Nov 2026 00:00:00 +0000
Subject: [PATCH] work

---
 missing.txt | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)

diff --git a/missing.txt b/missing.txt
index 0000001..0000002 100644
--- a/missing.txt
+++ b/missing.txt
@@ -1 +1 @@
-old
+new
--
2.39.5
`)
	am := exec.Command("git", "-C", folder, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "am", patch)
	if out, err := am.CombinedOutput(); err == nil {
		t.Fatalf("git am worked, so this test shows nothing: %s", out)
	}
	if got := f.git(folder, "status", "--porcelain"); got != "" {
		t.Fatalf("the tree is not clean, so this test shows nothing: %q", got)
	}
	wantKept(t, crew(t, "stop", id), folder)
}

func TestStopKeepsAnOperationInProgress(t *testing.T) {
	for _, marker := range []string{"rebase-merge", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "sequencer"} {
		t.Run(marker, func(t *testing.T) {
			f := newFixture(t)
			id, folder := f.mustStart()
			gitDir := f.git(folder, "rev-parse", "--absolute-git-dir")
			f.write(gitDir, marker, "")
			r := crew(t, "stop", id)
			wantKept(t, r, folder)
		})
	}
}

func TestStopKeepsAFolderItCannotWalk(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	closed := filepath.Join(folder, "closed")
	if err := os.Mkdir(closed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(closed, 0o700) })
	wantKept(t, crew(t, "stop", id), folder)
}

func TestStopAgainRemovesACleanedFolder(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.write(folder, "new.txt", "new\n")
	wantKept(t, crew(t, "stop", id), folder)
	if !f.row(id).FolderKept {
		t.Fatalf("row %+v does not record the kept folder", f.row(id))
	}
	if err := os.Remove(filepath.Join(folder, "new.txt")); err != nil {
		t.Fatal(err)
	}
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	row := f.row(id)
	if exists(folder) || branchExists(f, row.Branch) || row.FolderKept {
		t.Fatalf("a second stop on a clean folder left it: row %+v, stderr %q", row, r.stderr)
	}
}

func TestStopAgainRepeatsTheKeptLine(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	f.write(folder, "new.txt", "new\n")
	crew(t, "stop", id)
	r := crew(t, "stop", id)
	wantKept(t, r, folder)
	if !strings.Contains(r.stderr, "worktree remove --force") {
		t.Fatalf("a second stop printed no recovery command: %q", r.stderr)
	}
}

func TestStopSaysWhenGitCannotReadTheFolder(t *testing.T) {
	f := newFixture(t)
	id, folder := f.mustStart()
	if err := os.RemoveAll(filepath.Join(f.repo, ".git", "worktrees", filepath.Base(folder))); err != nil {
		t.Fatal(err)
	}
	r := crew(t, "stop", id)
	wantKept(t, r, folder)
	if !strings.Contains(r.stderr, "git cannot read this folder") || strings.Contains(r.stderr, "HEAD is not on") || strings.Contains(r.stderr, "To see it") {
		t.Fatalf("stderr %q does not give the real cause", r.stderr)
	}
}

func TestStopKeepsFolderWhenGitFails(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"symbolic-ref", "status --porcelain", "ls-files --others", "ls-files -v", "ls-files -s", "rev-parse --absolute-git-dir"} {
		t.Run(call, func(t *testing.T) {
			f := newFixture(t)
			id, folder := f.mustStart()
			script := "#!/bin/sh\ncase \" $* \" in\n*\" " + call + " \"*) echo 'git failed on purpose' >&2; exit 1 ;;\nesac\nexec " + realGit + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(f.binDir, "git"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			r := crew(t, "stop", id)
			wantKept(t, r, folder)
			if !strings.Contains(r.stderr, "git failed on purpose") {
				t.Fatalf("stderr %q does not give git's reason", r.stderr)
			}
		})
	}
}

func (f *fixture) addSubmodule() {
	f.t.Helper()
	source := filepath.Join(filepath.Dir(f.repo), "source")
	if err := os.Mkdir(source, 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.git(source, "init", "-q", "-b", "main")
	f.commit(source, "s.txt", "s\n")
	f.git(f.repo, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "sub")
	f.git(f.repo, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "submodule")
}

func TestStopRemovesACleanSubmoduleFolder(t *testing.T) {
	f := newFixture(t)
	f.addSubmodule()
	id, folder := f.mustStart()
	r := crew(t, "stop", id)
	if r.code != exitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if exists(folder) {
		t.Fatalf("clean folder %s stayed; stderr %q", folder, r.stderr)
	}
	if branchExists(f, f.row(id).Branch) {
		t.Fatalf("branch %s stayed", f.row(id).Branch)
	}
}

func TestStopKeepsAFileInAnUninitializedSubmodule(t *testing.T) {
	for _, path := range []string{"notes.txt", filepath.Join("deep", "n.txt")} {
		t.Run(path, func(t *testing.T) {
			f := newFixture(t)
			f.addSubmodule()
			id, folder := f.mustStart()
			file := filepath.Join(folder, "sub", path)
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			r := crew(t, "stop", id)
			wantKept(t, r, folder)
			if got, _ := r.json["kept_paths"].(float64); got != 1 {
				t.Fatalf("stdout %q, want kept_paths 1", r.stdout)
			}
			if got, _ := os.ReadFile(file); string(got) != "keep\n" {
				t.Fatalf("%s became %q", path, got)
			}
		})
	}
}

func TestStopSeesASubmoduleUnderIgnoreSettings(t *testing.T) {
	f := newFixture(t)
	f.gitConfig("[diff]", "ignoreSubmodules = all")
	f.addSubmodule()
	id, folder := f.mustStart()
	f.git(folder, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")
	f.write(filepath.Join(folder, "sub"), "s.txt", "edited\n")
	r := crew(t, "stop", id)
	wantKept(t, r, folder)
	if !strings.Contains(r.stderr, "holds work") {
		t.Fatalf("crew did not see the edit in the submodule itself: %q", r.stderr)
	}
}

func TestStartRefusesATaskFromALongPipe(t *testing.T) {
	f := newFixture(t)
	pipe := filepath.Join(filepath.Dir(f.repo), "task.pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		w, err := os.OpenFile(pipe, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		w.Write([]byte(strings.Repeat("x", 3_000_000)))
	}()
	r := crew(t, "start", "--lead", "lead", "--role", "review", "--folder", f.repo, "--task-file", pipe)
	if r.code != exitUsage {
		t.Fatalf("exit %d, want %d; stderr %q", r.code, exitUsage, r.stderr[:min(len(r.stderr), 200)])
	}
	if f.herdrCalls() != nil {
		t.Fatalf("herdr was called: %q", f.herdrCalls())
	}
	r = crew(t, "start", "--lead", "lead", "--role", "review", "--folder", f.repo, "--task-file", "/dev/zero")
	if r.code != exitUsage {
		t.Fatalf("/dev/zero: exit %d, want %d; stderr %q", r.code, exitUsage, r.stderr[:min(len(r.stderr), 200)])
	}
}

func (f *fixture) refuseCrewtalk(when, words string) {
	f.t.Helper()
	script := "#!/bin/sh\ncase \" $* \" in\n*\" " + when + " \"*) echo '" + words + "' >&2; exit 3 ;;\nesac\nexec " + crewtalkBin + " \"$@\"\n"
	path := filepath.Join(f.binDir, "crewtalk")
	if err := os.Remove(path); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func TestStartExits3WhenCrewtalkRefusesAStep(t *testing.T) {
	for _, tc := range []struct {
		name, when, note, state string
	}{
		{"the ready wait", "wait --as", "no-ready", "starting"},
		{"the task send", "send --from lead", "task-not-sent", "starting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.refuseCrewtalk(tc.when, "refused by a limit")
			r := f.start()
			if r.code != exitRefused {
				t.Fatalf("exit %d, want %d; stderr %q", r.code, exitRefused, r.stderr)
			}
			row := f.onlyRow()
			if row.State != tc.state || !slices.Contains(row.Notes, tc.note) {
				t.Fatalf("row %+v, want %s with the note %s", row, tc.state, tc.note)
			}
			if want := "crew stop " + row.ID + " --force"; !strings.Contains(r.stderr, want) || !strings.Contains(r.stderr, "refused by a limit") {
				t.Fatalf("stderr %q does not give the reason and %q", r.stderr, want)
			}
		})
	}
}

func TestStopForceNoticeSaysWhenNoPaneWasOpen(t *testing.T) {
	notice := func(f *fixture) string {
		for _, call := range f.herdrCalls() {
			if len(call) > 1 && call[0] == "notification" {
				return strings.Join(call, " ")
			}
		}
		return ""
	}
	t.Run("the start failed before the tab", func(t *testing.T) {
		f := newFixture(t)
		t.Setenv("FAKE_HERDR_DENY", "tab create")
		r := f.start()
		if r.code != exitFailed {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		row := f.onlyRow()
		if r := crew(t, "stop", row.ID, "--force"); r.code != exitOK {
			t.Fatalf("stop exit %d, stderr %q", r.code, r.stderr)
		}
		if got := notice(f); !strings.Contains(got, "no open pane") || strings.Contains(got, "closed pane") {
			t.Fatalf("notice %q", got)
		}
	})
	t.Run("the pane was closed by hand", func(t *testing.T) {
		f := newFixture(t)
		id, _ := f.mustStart()
		row := f.row(id)
		if err := os.Remove(filepath.Join(f.herdr, "panes", row.Pane)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(f.herdr, "names", id)); err != nil {
			t.Fatal(err)
		}
		if r := crew(t, "stop", id, "--force"); r.code != exitOK {
			t.Fatalf("stop exit %d, stderr %q", r.code, r.stderr)
		}
		if got := notice(f); !strings.Contains(got, "no open pane") || strings.Contains(got, "closed pane") {
			t.Fatalf("notice %q", got)
		}
	})
	t.Run("the pane was open", func(t *testing.T) {
		f := newFixture(t)
		id, _ := f.mustStart()
		pane := f.row(id).Pane
		if r := crew(t, "stop", id, "--force"); r.code != exitOK {
			t.Fatalf("stop exit %d, stderr %q", r.code, r.stderr)
		}
		if got := notice(f); !strings.Contains(got, "closed pane "+pane) {
			t.Fatalf("notice %q", got)
		}
	})
}

func TestStopForceNoticeOnlyAfterTheClose(t *testing.T) {
	f := newFixture(t)
	id, _ := f.mustStart()
	t.Setenv("FAKE_HERDR_DENY", "pane close")
	r := crew(t, "stop", id, "--force")
	if r.code != exitFailed {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if got := f.countCalls("notification", "show"); got != 0 {
		t.Fatalf("notices %d after a failed close, want 0", got)
	}
}

func TestStartAcceptsNamesTheReplyCommandCanCarry(t *testing.T) {
	for _, lead := range []string{"lead@1", "lead+a", "-lead", "lead=1", "a%b", "a.b_c"} {
		t.Run(lead, func(t *testing.T) {
			f := newFixture(t)
			f.tool("crewtalk", "join", "--as="+lead, "--kind", "claude")
			r := crew(t, "start", "--lead="+lead, "--role", "review", "--folder", f.repo, "--task", "x")
			if r.code != exitOK {
				t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
			}
			data, err := os.ReadFile(f.codexArgs)
			if err != nil {
				t.Fatal(err)
			}
			if want := "crewtalk send --to " + lead + " --reply-to MESSAGE_ID"; !strings.Contains(string(data), want) {
				t.Fatalf("the task message %q does not hold %q", data, want)
			}
		})
	}
}

func TestStartRefusesANameTheReplyCommandCannotCarry(t *testing.T) {
	for _, lead := range []string{"lead$x", "it's", `a\b`, "$(touch${IFS}PWNED)", "`touch${IFS}PWNED`", "a*b", "a\"b", "=lead"} {
		t.Run(lead, func(t *testing.T) {
			f := newFixture(t)
			f.tool("crewtalk", "join", "--as="+lead, "--kind", "claude")
			r := crew(t, "start", "--lead="+lead, "--role", "review", "--folder", f.repo, "--task", "x")
			if r.code != exitUsage || !strings.Contains(r.stderr, "reply command") {
				t.Fatalf("exit %d, stderr %q; want %d and a reason that names the reply command", r.code, r.stderr, exitUsage)
			}
			if f.herdrCalls() != nil {
				t.Fatalf("herdr was called: %q", f.herdrCalls())
			}
			if reason, _, _ := strings.Cut(r.stderr, "\n"); strings.Contains(reason, lead) {
				t.Fatalf("the printed line %q holds the lead's name", reason)
			}
		})
	}
}

func TestStartRefusesANameCrewtalkRefuses(t *testing.T) {
	f := newFixture(t)
	for _, lead := range []string{"a b", "a/b", "a:b", "a,b", "a;b"} {
		r := crew(t, "start", "--lead", lead, "--role", "review", "--folder", f.repo, "--task", "x")
		if r.code != exitUsage {
			t.Fatalf("--lead %q: exit %d, want %d; stderr %q", lead, r.code, exitUsage, r.stderr)
		}
	}
}
