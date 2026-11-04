package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/amrendra18/crewtalk/internal/book"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/route"
	"github.com/amrendra18/crewtalk/internal/state"
)

const NoTimeout time.Duration = -1

const maxWire = 8 << 20

const maxSocketPath = 104

var exchangeWait = 5 * time.Second

const claimWait = 200 * time.Millisecond

type Route struct{}

func (Route) Kind() string { return "claude" }

func (Route) CheckAddress(name string) error {
	_, err := inboxOf(name)
	return err
}

func (Route) SessionAddress() string { return "" }

func (Route) DefaultAddress(name string) string { return name }

func (Route) Presence(name string) route.Presence {
	in, err := inboxOf(name)
	if err != nil {
		return route.Unheld
	}
	if _, err := os.Lstat(in.sock); errors.Is(err, fs.ErrNotExist) {
		return route.Absent
	}
	f, err := os.Open(in.waitLock)
	if errors.Is(err, fs.ErrNotExist) {
		return route.Absent
	}
	if err != nil {
		return route.Unheld
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		return route.Absent
	}
	return route.Present
}

func (Route) Deliver(ctx context.Context, to route.Agent, m message.Message) error {
	in, err := inboxOf(to.Address)
	if err != nil {
		return fmt.Errorf("%w: %v", route.ErrReceiverUnknown, err)
	}
	wire := m.Wire()
	if len(wire) > maxWire {
		return fmt.Errorf("claude:%s: the message is %d bytes, over the %d byte limit; nothing was sent", to.Address, len(wire), maxWire)
	}
	if _, err := os.Lstat(in.sock); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("claude:%s: %w", to.Address, route.ErrNotListening)
	}
	unlock, err := state.Lock(ctx, in.sendLock, 2*exchangeWait)
	switch {
	case err == nil:
	case errors.Is(err, state.ErrBusy):
		return fmt.Errorf("claude:%s: %w (%v)", to.Address, route.ErrNotListening, err)
	case ctx.Err() != nil:
		return fmt.Errorf("claude:%s: stopped while waiting for the send lock (%v): %w", to.Address, err, route.ErrUnknownOutcome)
	default:
		return err
	}
	defer unlock()
	return exchangeLocked(ctx, in.sock, to.Address, wire)
}

func exchangeLocked(ctx context.Context, sock, address, wire string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("claude:%s: %w", address, route.ErrNotListening)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()

	if err := conn.SetDeadline(time.Now().Add(exchangeWait)); err != nil {
		return err
	}
	_, err = fmt.Fprintf(conn, "len %d\n%s", len(wire), wire)
	switch {
	case err == nil:
	case ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded):
		return fmt.Errorf("claude:%s: the write stopped part way (%v): %w", address, err, route.ErrUnknownOutcome)
	default:
		return fmt.Errorf("claude:%s closed before reading the message: %w", address, route.ErrNotListening)
	}
	reply, err := io.ReadAll(conn)
	if string(reply) == "ok\n" {
		return nil
	}
	return fmt.Errorf("claude:%s replied %q (%v): %w", address, reply, err, route.ErrUnknownOutcome)
}

type Listener struct {
	name   string
	in     inbox
	unlock func()
}

func Claim(name string) (*Listener, error) {
	in, err := inboxOf(name)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(in.sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	unlock, err := lockThenClearStale(name, in)
	if err != nil {
		return nil, err
	}
	return &Listener{name, in, unlock}, nil
}

func (l *Listener) Close() { l.unlock() }

func (l *Listener) Receive(timeout time.Duration, opening func() error, onReady func(), handle func(message.Message) error) error {
	var deadline time.Time
	if timeout != NoTimeout {
		deadline = time.Now().Add(timeout)
	}
	for {
		if err := opening(); err != nil {
			return err
		}
		conn, err := acceptOne(l.in.sock, deadline, onReady)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return fmt.Errorf("no message for %s within %s: %w", l.name, timeout, err)
		}
		if err != nil {
			return err
		}
		connDeadline := time.Now().Add(exchangeWait)
		if !deadline.IsZero() && deadline.Before(connDeadline) {
			connDeadline = deadline
		}
		if done, err := serve(conn, connDeadline, handle); done {
			return err
		}
	}
}

type inbox struct {
	sock     string
	sendLock string
	waitLock string
}

func inboxOf(name string) (inbox, error) {
	if err := book.CheckName(name); err != nil {
		return inbox{}, fmt.Errorf("listener %w", err)
	}
	home, err := state.Home()
	if err != nil {
		return inbox{}, err
	}
	dir := filepath.Join(home, "inbox")
	in := inbox{
		sock:     filepath.Join(dir, name+".sock"),
		sendLock: filepath.Join(dir, name+".send.lock"),
		waitLock: filepath.Join(dir, name+".wait.lock"),
	}
	if len(in.sock) >= maxSocketPath {
		return inbox{}, fmt.Errorf("the socket path %s is %d bytes, and macOS needs a socket path under %d bytes; use a shorter listener name or set CREWTALK_HOME to a shorter folder", in.sock, len(in.sock), maxSocketPath)
	}
	return in, nil
}

func lockThenClearStale(name string, in inbox) (unlock func(), err error) {
	unlock, err = state.Lock(context.Background(), in.waitLock, claimWait)
	if errors.Is(err, state.ErrBusy) {
		return nil, fmt.Errorf("%s already has a listener", name)
	}
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", in.sock)
	switch {
	case err == nil:
		conn.Close()
		err = fmt.Errorf("%s already has a listener", name)
	case errors.Is(err, fs.ErrNotExist):
		err = nil
	case errors.Is(err, syscall.ECONNREFUSED):
		err = os.Remove(in.sock)
	}
	if err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

func acceptOne(sock string, deadline time.Time, onReady func()) (net.Conn, error) {
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return nil, err
	}
	l.SetUnlinkOnClose(false)
	defer func() {
		os.Remove(sock)
		l.Close()
	}()
	if err := os.Chmod(sock, 0o600); err != nil {
		return nil, err
	}
	if err := l.SetDeadline(deadline); err != nil {
		return nil, err
	}
	onReady()
	return l.Accept()
}

func serve(conn net.Conn, deadline time.Time, handle func(message.Message) error) (done bool, err error) {
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return true, err
	}
	m, err := readFrame(bufio.NewReader(conn))
	if err != nil {
		io.WriteString(conn, "bad\n")
		return false, nil
	}
	if err := handle(m); err != nil {
		io.WriteString(conn, "bad\n")
		return true, err
	}
	io.WriteString(conn, "ok\n")
	return true, nil
}

func readFrame(r *bufio.Reader) (message.Message, error) {
	line, err := r.ReadSlice('\n')
	if err != nil {
		return message.Message{}, err
	}
	size, ok := strings.CutPrefix(string(line), "len ")
	n, err := strconv.Atoi(strings.TrimSuffix(size, "\n"))
	if !ok || err != nil || n < 0 || n > maxWire {
		return message.Message{}, fmt.Errorf("bad length line %q", line)
	}
	wire := make([]byte, n)
	if _, err := io.ReadFull(r, wire); err != nil {
		return message.Message{}, err
	}
	return message.Parse(string(wire))
}
