package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/amrendra18/crewtalk/internal/notice"
)

type paneKind int

const (
	paneHasWorker paneKind = iota
	paneHasNoAgent
	paneGone
	paneMoved
	paneHasOtherAgent
	paneUnreadable
)

type paneRead struct {
	kind   paneKind
	status string
	moved  string
	other  string
	detail string
}

func stop(args []string, stdout, stderr io.Writer) int {
	var force bool
	var positional []string
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.BoolVar(&force, "force", false, "")
	for rest := args; len(rest) > 0; {
		if code, done := parseFlags(fs, rest, stdout, stderr); done {
			return code
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) != 1 {
		return usageError(stderr, "stop takes one worker id")
	}
	id := positional[0]

	home, err := crewHomePath()
	if err != nil {
		fmt.Fprintf(stderr, "crew stop: cannot find the state folder: %s\n", printable(err.Error()))
		return exitFailed
	}
	row, found, err := findWorker(home, id)
	if err != nil {
		fmt.Fprintf(stderr, "crew stop: %s\n", printable(err.Error()))
		return exitFailed
	}
	if !found {
		fmt.Fprintf(stderr, "crew stop: no worker %s\n", printable(id))
		return exitReceiver
	}
	if row.State == "stopped" {
		return finishStop(home, &row, stdout, stderr)
	}

	read := paneRead{kind: paneGone}
	if row.Pane != "" {
		read = readPane(row)
	}
	switch {
	case read.kind == paneUnreadable:
		if err := applyToRow(home, &row, func(w *worker) { w.addNote("herdr-unreadable") }); err != nil {
			fmt.Fprintf(stderr, "crew stop: cannot save the row: %s\n", printable(err.Error()))
		}
		fmt.Fprintf(stderr, "crew stop: cannot read pane %s, so nothing was closed: %s; run crew stop again when herdr answers\n", row.Pane, read.detail)
		return exitFailed
	case read.kind == paneMoved:
		fmt.Fprintf(stderr, "crew stop: worker %s moved from pane %s to pane %s, so nothing was closed; close pane %s yourself, then run crew stop again\n", row.ID, row.Pane, read.moved, read.moved)
		return exitFailed
	case read.kind == paneHasOtherAgent:
		fmt.Fprintf(stderr, "crew stop: pane %s holds the agent %s, which is not worker %s, so nothing was closed, and --force does not change this; close that agent or the pane, then run crew stop again\n", row.Pane, read.other, row.ID)
		return exitFailed
	case read.kind == paneHasWorker && read.status != "idle" && read.status != "done" && !force:
		fmt.Fprintf(stderr, "crew stop: herdr reads %s as %s, so nothing was closed; wait for it, or use --force to close it\n", row.ID, read.status)
		return exitFailed
	}

	if force {
		if err := applyToRow(home, &row, func(w *worker) {
			w.ForcedAt = now()
			w.addNote("forced")
		}); err != nil {
			fmt.Fprintf(stderr, "crew stop: cannot save the row: %s\n", printable(err.Error()))
			return exitFailed
		}
	}
	forceNotice := "crew stop --force found no open pane for this worker."
	if read.kind != paneGone {
		if r := runTool("herdr", "pane", "close", row.Pane); !r.ok() {
			fmt.Fprintf(stderr, "crew stop: herdr pane close %s failed: %s; run crew stop again\n", row.Pane, r.detail())
			return exitFailed
		}
		forceNotice = "crew stop --force closed pane " + row.Pane + "."
	}
	if force {
		notice.Show("crew: "+row.ID+" was force-stopped", forceNotice)
	}
	for _, name := range []string{row.ID, row.ReadyName} {
		if r := runTool("crewtalk", "leave", "--as", name); !r.ok() && !r.exited(exitReceiver) {
			fmt.Fprintf(stderr, "crew stop: crewtalk leave --as %s failed: %s; run crew stop again\n", name, r.detail())
			return exitFailed
		}
	}
	return finishStop(home, &row, stdout, stderr)
}

func finishStop(home string, row *worker, stdout, stderr io.Writer) int {
	folderKept, paths := keepOrRemoveFolder(*row, stderr)
	branchKept := folderKept
	if !folderKept {
		branchKept = keepOrDeleteBranch(*row, stderr)
	}

	left, err := listeners(row.ID, row.ReadyName)
	if err != nil {
		fmt.Fprintf(stderr, "crew stop: could not check for a leftover crewtalk wait: %s\n", printable(err.Error()))
	}
	for _, line := range left {
		fmt.Fprintf(stderr, "crew stop: a crewtalk wait still runs for this worker, and crew sends it no signal: %s\n", printable(line))
	}

	if err := applyToRow(home, row, func(w *worker) {
		w.FolderKept = folderKept
		w.setState("stopped")
	}); err != nil {
		fmt.Fprintf(stderr, "crew stop: cannot save the row: %s; run crew stop again\n", printable(err.Error()))
		return exitFailed
	}
	printJSON(stdout, map[string]any{
		"id": row.ID, "state": row.State, "folder": row.Folder, "folder_kept": folderKept, "kept_paths": paths,
		"branch": row.Branch, "branch_kept": branchKept, "leftover_listeners": len(left),
	})
	return exitOK
}

func readPane(row worker) paneRead {
	pane := runTool("herdr", "pane", "get", row.Pane)
	if !pane.ok() {
		if pane.herdrError() != "pane_not_found" {
			return paneRead{kind: paneUnreadable, detail: pane.detail()}
		}
		byName := runTool("herdr", "agent", "get", row.ID)
		switch {
		case byName.ok():
			_, _, moved, err := parseAgent(byName.stdout)
			if err != nil {
				return paneRead{kind: paneUnreadable, detail: printable(err.Error())}
			}
			return paneRead{kind: paneMoved, moved: printable(moved)}
		case byName.herdrError() == "agent_not_found":
			return paneRead{kind: paneGone}
		}
		return paneRead{kind: paneUnreadable, detail: byName.detail()}
	}
	agent := runTool("herdr", "agent", "get", row.Pane)
	switch {
	case agent.ok():
		status, name, _, err := parseAgent(agent.stdout)
		if err != nil {
			return paneRead{kind: paneUnreadable, detail: printable(err.Error())}
		}
		// herdr can have no name for a worker after a start timeout, so only a
		// different name counts.
		if name != "" && name != row.ID {
			return paneRead{kind: paneHasOtherAgent, other: printable(name)}
		}
		return paneRead{kind: paneHasWorker, status: printable(status)}
	case agent.herdrError() == "agent_not_found":
		return paneRead{kind: paneHasNoAgent}
	}
	return paneRead{kind: paneUnreadable, detail: agent.detail()}
}

func parseAgent(text string) (status, name, pane string, err error) {
	var body struct {
		Result struct {
			Agent struct {
				Status string `json:"agent_status"`
				Name   string `json:"name"`
				Pane   string `json:"pane_id"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		return "", "", "", fmt.Errorf("herdr agent get printed no agent: %w", err)
	}
	if body.Result.Agent.Status == "" {
		return "", "", "", errors.New("herdr agent get printed no status")
	}
	return body.Result.Agent.Status, body.Result.Agent.Name, body.Result.Agent.Pane, nil
}

func keepOrRemoveFolder(row worker, stderr io.Writer) (kept bool, paths int) {
	if _, err := os.Lstat(row.Folder); errors.Is(err, os.ErrNotExist) {
		return false, 0
	}
	work := folderWork(row)
	switch {
	case work.gitError != "":
		fmt.Fprintf(stderr, "crew stop: kept %s and branch %s: git cannot read this folder: %s. The repository may have moved, or its worktree record may be gone. Look in the folder, and delete it yourself when it holds nothing you need\n",
			printable(row.Folder), printable(row.Branch), work.gitError)
		return true, 0
	case work.keepReason != "":
		printKept(row, work.keepReason, stderr)
		return true, work.paths
	}
	if r := runTool("git", "-C", row.Repo, "worktree", "remove", row.Folder); !r.ok() {
		printKept(row, "git worktree remove failed: "+r.detail(), stderr)
		return true, 0
	}
	return false, 0
}

func printKept(row worker, why string, stderr io.Writer) {
	folder, repo, branch := shellQuote(row.Folder), shellQuote(row.Repo), shellQuote(row.Branch)
	see := "git " + strings.Join(strictFileCheckSettings, " ") + " -C " + folder + " " + strings.Join(statusArgs, " ")
	fmt.Fprintf(stderr, "crew stop: kept %s and branch %s: %s. To see it: %s. To delete the folder and all that it holds, with the branch: git -C %s worktree remove --force %s && git -C %s branch -D %s\n",
		printable(row.Folder), printable(row.Branch), why, see, repo, folder, repo, branch)
}

var (
	strictFileCheckSettings = []string{"-c", "core.fsmonitor=false", "-c", "core.checkStat=default", "-c", "core.trustctime=true"}
	statusArgs              = []string{"status", "--porcelain", "--ignored", "--untracked-files=normal", "--ignore-submodules=none"}
)

// inProgress names what a stopped git operation leaves in a worktree's git
// folder. That state is gone with the folder, even when the files look clean.
var inProgress = []string{"rebase-apply", "rebase-merge", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "sequencer"}

type folderState struct {
	paths      int
	keepReason string
	gitError   string
}

// The checks do not rest on one view: git worktree remove deletes ignored files, and
// git settings can hide files from git status and from that command's own check.
func folderWork(row worker) folderState {
	// symbolic-ref exits 1 with no message on a detached HEAD.
	head := runTool("git", "-C", row.Folder, "symbolic-ref", "-q", "HEAD")
	if !head.ok() && (!head.exited(1) || head.stderr != "") {
		return folderState{gitError: head.detail()}
	}
	if strings.TrimSpace(head.stdout) != "refs/heads/"+row.Branch {
		return folderState{keepReason: "its HEAD is not on that branch, so a commit may exist only there"}
	}
	gitDir := runTool("git", "-C", row.Folder, "rev-parse", "--absolute-git-dir")
	if !gitDir.ok() {
		return folderState{gitError: gitDir.detail()}
	}
	for _, name := range inProgress {
		if _, err := os.Lstat(filepath.Join(strings.TrimSpace(gitDir.stdout), name)); !errors.Is(err, os.ErrNotExist) {
			return folderState{keepReason: "a git operation is in progress there, and its saved state (" + name + ") would be lost"}
		}
	}
	args := append(append([]string{}, strictFileCheckSettings...), "-C", row.Folder)
	status := runTool("git", append(args, statusArgs...)...)
	if !status.ok() {
		return folderState{gitError: status.detail()}
	}
	// With no exclude rule, this lists every untracked and ignored file.
	others := runTool("git", "-C", row.Folder, "ls-files", "--others")
	if !others.ok() {
		return folderState{gitError: others.detail()}
	}
	flags := runTool("git", "-C", row.Folder, "ls-files", "-v", "-z")
	if !flags.ok() {
		return folderState{gitError: flags.detail()}
	}
	stages := runTool("git", "-C", row.Folder, "ls-files", "-s", "-z")
	if !stages.ok() {
		return folderState{gitError: stages.detail()}
	}
	paths := countLines(status.stdout)
	if paths == 0 {
		paths = countLines(others.stdout)
	}
	paths += hiddenFiles(row.Folder, flags.stdout) + filledSubmodules(row.Folder, stages.stdout)
	switch paths {
	case 0:
	case 1:
		return folderState{paths: 1, keepReason: "1 path holds work"}
	default:
		return folderState{paths: paths, keepReason: fmt.Sprintf("%d paths hold work", paths)}
	}
	nested, err := nestedGit(row.Folder)
	switch {
	case err != nil:
		return folderState{keepReason: "crew could not read all of it: " + printable(err.Error())}
	case nested != "":
		return folderState{keepReason: "git does not look inside " + printable(nested) + ", so a file there may be work"}
	}
	return folderState{}
}

// hiddenFiles counts the files that git status may not compare. In git ls-files
// -v, tag H is a plain file. Tag S is a skip-worktree file, which a sparse
// checkout sets on every file it leaves out, so it counts only when a file is on
// disk. Any other tag counts, such as a lowercase one for assume-unchanged.
func hiddenFiles(folder, listing string) int {
	n := 0
	for entry := range strings.SplitSeq(listing, "\x00") {
		tag, path, found := strings.Cut(entry, " ")
		if !found || tag == "H" {
			continue
		}
		if tag == "S" {
			if _, err := os.Lstat(filepath.Join(folder, path)); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		n++
	}
	return n
}

// filledSubmodules counts the submodule folders that hold files but were never
// initialized. git treats such a folder as the submodule's place and does not
// look inside, so git status and git ls-files list nothing there. In git ls-files
// -s, mode 160000 is a submodule.
func filledSubmodules(folder, listing string) int {
	n := 0
	for entry := range strings.SplitSeq(listing, "\x00") {
		meta, path, found := strings.Cut(entry, "\t")
		if !found || !strings.HasPrefix(meta, "160000 ") {
			continue
		}
		items, err := os.ReadDir(filepath.Join(folder, path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		initialized := slices.ContainsFunc(items, func(item fs.DirEntry) bool {
			return strings.EqualFold(item.Name(), ".git")
		})
		if err != nil || (len(items) > 0 && !initialized) {
			n++
		}
	}
	return n
}

// nestedGit finds a .git entry below the top of the folder. git skips everything
// under one, so neither git status nor git ls-files lists those files.
func nestedGit(folder string) (string, error) {
	nested := ""
	err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !strings.EqualFold(d.Name(), ".git") {
			return nil
		}
		if filepath.Dir(path) == folder {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		nested = path
		return filepath.SkipAll
	})
	return nested, err
}

func countLines(text string) int {
	if text = strings.TrimRight(text, "\n"); text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func keepOrDeleteBranch(row worker, stderr io.Writer) (kept bool) {
	if !runTool("git", "-C", row.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+row.Branch).ok() {
		return false
	}
	r := runTool("git", "-C", row.Repo, "branch", "-q", "-d", row.Branch)
	if r.ok() {
		return false
	}
	repo, branch := shellQuote(row.Repo), shellQuote(row.Branch)
	fmt.Fprintf(stderr, "crew stop: kept branch %s, because git branch -d refused it: %s. To read it: git -C %s log %s..%s. To delete it: git -C %s branch -D %s\n",
		printable(row.Branch), r.detail(), repo, shellQuote(row.Base), branch, repo, branch)
	return true
}
