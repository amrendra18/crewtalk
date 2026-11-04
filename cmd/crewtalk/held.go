package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/amrendra18/crewtalk/internal/book"
	"github.com/amrendra18/crewtalk/internal/held"
	"github.com/amrendra18/crewtalk/internal/journal"
	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/notice"
	"github.com/amrendra18/crewtalk/internal/route"
)

func warnings(stderr io.Writer, command string) func(error) {
	seen := map[string]bool{}
	return func(err error) {
		if text := err.Error(); !seen[text] {
			seen[text] = true
			fmt.Fprintf(stderr, "crewtalk %s: warning: %s\n", command, message.Printable(text))
		}
	}
}

func heldCount(n int) string {
	if n == 1 {
		return "1 held message"
	}
	return fmt.Sprintf("%d held messages", n)
}

func expire(home string, warn func(error)) {
	gone, err := held.Expire(home, time.Now(), warn)
	if err != nil {
		warn(fmt.Errorf("held messages could not be expired: %w", err))
	}
	if len(gone) == 0 {
		return
	}
	var named []string
	for _, m := range gone {
		named = append(named, fmt.Sprintf("%s from %s to %s", m.ID, m.From, m.To))
	}
	notice.Show("crewtalk: held messages expired", fmt.Sprintf("%s waited over %d minutes and expired unread: %s", heldCount(len(gone)), held.Deadline/time.Minute, strings.Join(named, "; ")))
	j, err := journal.Open(home)
	if err == nil {
		defer j.Close()
		for _, m := range gone {
			if err = j.Append(entryFor(m, journal.Expired)); err != nil {
				break
			}
		}
	}
	if err != nil {
		warn(fmt.Errorf("%s expired, but their log lines were not all written: %w", heldCount(len(gone)), err))
	}
}

func tellSender(home, from string, stderr io.Writer, warn func(error)) {
	ids, err := held.Told(home, from, warn)
	if err != nil {
		warn(fmt.Errorf("cannot read which held messages from %s expired: %w", from, err))
	}
	if len(ids) > 0 {
		fmt.Fprint(stderr, message.Printable(fmt.Sprintf("crewtalk send: %s from %s waited over %d minutes for a crewtalk wait and expired: %s\n", heldCount(len(ids)), from, held.Deadline/time.Minute, strings.Join(ids, ", "))))
	}
}

func hold(ctx context.Context, home string, r route.Route, d delivery, warn func(error)) (isHeld, present bool, err error) {
	box, unlock, err := held.Lock(ctx, home, warn)
	if err != nil {
		return false, false, err
	}
	defer unlock()
	switch r.Presence(d.address) {
	case route.Present:
		return false, true, nil
	case route.Unheld:
		return false, false, nil
	}
	agents, err := book.Load(home)
	if err != nil {
		return false, false, err
	}
	if !slices.ContainsFunc(agents, func(a book.Agent) bool { return a.Kind == d.kind && a.Address == d.address }) {
		return false, false, nil
	}
	if err := box.Hold(d.address, d.m); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func entryFor(m message.Message, result journal.Result) journal.Entry {
	kind, _, found := strings.Cut(m.To, ":")
	if !found {
		kind = ""
	}
	return journal.Entry{Time: time.Now().UTC(), ID: m.ID, From: m.From, To: m.To, Thread: m.Thread, Turn: m.Turn, ReplyTo: m.ReplyTo, Route: kind, Result: result}
}
