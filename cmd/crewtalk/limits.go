package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/amrendra18/crewtalk/internal/journal"
	"github.com/amrendra18/crewtalk/internal/message"
)

const (
	maxTurns     = 6
	maxPairSends = 20
	pairWindow   = time.Hour
)

var pairCounted = map[journal.Result]bool{
	journal.Delivered:      true,
	journal.Held:           true,
	journal.OutcomeUnknown: true,
}

func checkLimits(home string, m message.Message, now time.Time, warn func(error)) (sendOutcome, bool, error) {
	if pause, stopped := paused(home); stopped {
		return refused(journal.RefusedPaused, "every send is refused while %s exists; report this to your operator", pause), true, nil
	}
	if m.Turn > maxTurns {
		return refused(journal.RefusedLimit, "turn %d is over the limit of %d turns in thread %s; stop this conversation and report this to your operator", m.Turn, maxTurns, m.Thread), true, nil
	}
	sent, err := pairSends(home, m.From, m.To, now.Add(-pairWindow), warn)
	if err != nil {
		return sendOutcome{}, false, fmt.Errorf("cannot count the sends in the log: %w", err)
	}
	if len(sent) >= maxPairSends {
		clears := sent[len(sent)-maxPairSends].Add(pairWindow)
		return refused(journal.RefusedLimit, "%s sent %d messages to %s in the last hour, the limit of %d; the next send is allowed at %s UTC", m.From, len(sent), m.To, maxPairSends, clears.UTC().Format(time.TimeOnly)), true, nil
	}
	return sendOutcome{}, false, nil
}

func paused(home string) (string, bool) {
	pause := filepath.Join(home, "PAUSE")
	_, err := os.Lstat(pause)
	return pause, !errors.Is(err, fs.ErrNotExist)
}

func refused(result journal.Result, format string, args ...any) sendOutcome {
	return sendOutcome{result, exitRefused, fmt.Errorf(format, args...)}
}

func pairSends(home, from, to string, since time.Time, warn func(error)) ([]time.Time, error) {
	entries, err := journal.PairSince(home, from, to, since, warn)
	var times []time.Time
	for _, e := range entries {
		if pairCounted[e.Result] && e.HeldFor == 0 {
			times = append(times, e.Time)
		}
	}
	slices.SortFunc(times, time.Time.Compare)
	return times, err
}
