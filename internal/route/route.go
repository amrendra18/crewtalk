package route

import (
	"context"
	"errors"

	"github.com/amrendra18/crewtalk/internal/message"
)

type Agent struct {
	Address string
}

type Presence int

const (
	Unheld Presence = iota
	Absent
	Present
)

type Route interface {
	Kind() string
	CheckAddress(address string) error
	SessionAddress() string
	DefaultAddress(name string) string
	Presence(address string) Presence
	Deliver(ctx context.Context, to Agent, m message.Message) error
}

var (
	ErrNotListening    = errors.New("not listening")
	ErrReceiverUnknown = errors.New("receiver unknown")
	ErrUnknownOutcome  = errors.New("outcome unknown")
)
