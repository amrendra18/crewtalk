package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	toolWait   = 2 * time.Minute
	noticeWait = 2 * time.Second
)

// toolResult holds the outcome of one child process. code is -1 when the
// process did not run or a signal ended it, and notRun says it never started.
type toolResult struct {
	stdout string
	stderr string
	code   int
	notRun bool
}

// gitLocationVariables are the settings that point git at another repository,
// as git rev-parse --local-env-vars lists them. A hook sets some of them, and
// crew must read the folder it names, whatever called it.
var gitLocationVariables = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_GRAFT_FILE", "GIT_IMPLICIT_WORK_TREE", "GIT_INDEX_FILE",
	"GIT_INTERNAL_SUPER_PREFIX", "GIT_NO_REPLACE_OBJECTS", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX",
	"GIT_REPLACE_REF_BASE", "GIT_SHALLOW_FILE", "GIT_WORK_TREE",
}

func gitEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return slices.Contains(gitLocationVariables, name)
	})
}

func runTool(name string, args ...string) toolResult {
	return runToolFor(context.Background(), toolWait, name, args...)
}

func runToolFor(ctx context.Context, wait time.Duration, name string, args ...string) toolResult {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if name == "git" {
		cmd.Env = gitEnv()
	}
	err := cmd.Run()
	r := toolResult{stdout: stdout.String(), stderr: stderr.String(), notRun: cmd.ProcessState == nil}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	default:
		r.code = -1
		if r.stderr == "" {
			r.stderr = err.Error()
		}
	}
	return r
}

// herdrError is the code in a herdr error object, or empty.
func (r toolResult) herdrError() string {
	code, _ := r.parseError()
	return code
}

func (r toolResult) parseError() (code, message string) {
	for _, text := range []string{r.stderr, r.stdout} {
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(text), &body) == nil && body.Error.Code != "" {
			return body.Error.Code, body.Error.Message
		}
	}
	return "", ""
}

// detail is one short printable line that says what the tool reported.
func (r toolResult) detail() string {
	if code, message := r.parseError(); code != "" {
		return printable(code + ": " + message)
	}
	for _, text := range []string{r.stderr, r.stdout} {
		for line := range strings.SplitSeq(text, "\n") {
			if strings.TrimSpace(line) != "" {
				return printable(line)
			}
		}
	}
	return "no output, exit code " + strconv.Itoa(r.code)
}

// printable keeps a tool's text from carrying terminal controls or extra lines
// into crew's own output.
func printable(s string) string {
	const limit = 400
	out := strings.TrimSpace(noControls(s))
	if len(out) > limit {
		out = strings.ToValidUTF8(out[:limit], "") + "..."
	}
	return out
}

func noControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote makes a value safe to paste into a printed command, because a lead
// may run the line as it stands.
func shellQuote(s string) string {
	s = noControls(s)
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// notify raises a herdr notice inside herdr. A notice never fails a command.
func notify(title, body string) {
	if os.Getenv("HERDR_ENV") != "1" {
		return
	}
	path, err := exec.LookPath("herdr")
	if err != nil {
		return
	}
	runToolFor(context.Background(), noticeWait, path, "notification", "show", title, "--body", body)
}

// listeners finds crewtalk wait processes for these names. crew only reports
// them, because it cannot tell its own from a person's by anything but the name.
func listeners(names ...string) ([]string, error) {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	r := runTool("pgrep", "-fl", "crewtalk wait --as ("+strings.Join(quoted, "|")+")( |$)")
	switch r.code {
	case 0:
		return strings.Split(strings.TrimSpace(r.stdout), "\n"), nil
	case 1:
		return nil, nil
	}
	return nil, errors.New(r.detail())
}
