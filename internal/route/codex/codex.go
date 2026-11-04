package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
)

const defaultTimeout = 30 * time.Second

var threadUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

var queueExitOneErrors = []struct {
	text string
	err  error
}{
	{"No active session found", route.ErrNotListening},
	{"no rollout found for thread id", route.ErrReceiverUnknown},
}

type Route struct {
	Timeout time.Duration
}

func (Route) Kind() string { return "codex" }

func (Route) CheckAddress(thread string) error {
	if !threadUUID.MatchString(thread) {
		return fmt.Errorf("thread %q must be a Codex thread UUID, 8-4-4-4-12 hex digits", thread)
	}
	return nil
}

func (Route) SessionAddress() string { return os.Getenv("CODEX_THREAD_ID") }

func (Route) DefaultAddress(string) string { return "" }

func (Route) Presence(string) route.Presence { return route.Unheld }

func (r Route) Deliver(ctx context.Context, to route.Agent, m message.Message) error {
	timeout := r.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("no answer within %s", timeout))
	defer cancel()

	cmd := exec.CommandContext(ctx, binary(), "queue", "--thread="+to.Address, "--message", m.Wire())
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second

	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("codex queue to %s stopped (%v): %w", to.Address, context.Cause(ctx), route.ErrUnknownOutcome)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return err
	}
	if exit.ExitCode() == 1 {
		for _, e := range queueExitOneErrors {
			if strings.Contains(out.String(), e.text) {
				return fmt.Errorf("codex thread %s: %w", to.Address, e.err)
			}
		}
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	return fmt.Errorf("codex queue exited %d: %s: %w", exit.ExitCode(), first, exit)
}
