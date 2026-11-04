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
	"unicode/utf8"

	"github.com/amrendra18/crewtalk/internal/message"
)

const toolWait = 2 * time.Minute

type toolEnd int

const (
	toolExited toolEnd = iota
	toolNeverRan
	toolInterrupted
)

type toolResult struct {
	stdout   string
	stderr   string
	end      toolEnd
	exitCode int
}

func (r toolResult) exited(code int) bool {
	return r.end == toolExited && r.exitCode == code
}

func (r toolResult) ok() bool {
	return r.exited(0)
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
	r := toolResult{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit) && exit.Exited():
		r.exitCode = exit.ExitCode()
	case errors.As(err, &exit):
		r.end = toolInterrupted
	default:
		r.end = toolInterrupted
		if cmd.ProcessState == nil {
			r.end = toolNeverRan
		}
		if r.stderr == "" {
			r.stderr = err.Error()
		}
	}
	return r
}

func (r toolResult) herdrError() string {
	code, _ := r.parseError()
	return code
}

func (r toolResult) parseError() (code, errorText string) {
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

func (r toolResult) detail() string {
	if code, errorText := r.parseError(); code != "" {
		return printable(code + ": " + errorText)
	}
	for _, text := range []string{r.stderr, r.stdout} {
		for line := range strings.SplitSeq(text, "\n") {
			if strings.TrimSpace(line) != "" {
				return printable(line)
			}
		}
	}
	if r.end != toolExited {
		return "no output, and no exit code"
	}
	return "no output, exit code " + strconv.Itoa(r.exitCode)
}

func printable(s string) string {
	const limit = 400
	s = strings.TrimSpace(oneLine(s))
	if len(s) <= limit {
		return message.Printable(s)
	}
	end := limit
	for i := limit; i > limit-utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			end = i
			break
		}
	}
	return message.Printable(s[:end]) + "..."
}

func oneLine(s string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(s)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+:,./-][A-Za-z0-9_@%+=:,./-]*$`)

func shellQuote(s string) string {
	s = message.Printable(oneLine(s))
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func listeners(names ...string) ([]string, error) {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	r := runTool("pgrep", "-fl", "crewtalk wait --as ("+strings.Join(quoted, "|")+")( |$)")
	switch {
	case r.ok():
		return strings.Split(strings.TrimSpace(r.stdout), "\n"), nil
	case r.exited(1):
		return nil, nil
	}
	return nil, errors.New(r.detail())
}
