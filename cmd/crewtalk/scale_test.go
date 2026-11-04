package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/message"
)

func listening(home, name string) bool {
	sock := filepath.Join(home, "inbox", name+".sock")
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if info, err := os.Lstat(sock); err == nil && info.Mode().Perm() == 0o600 {
			return true
		}
	}
	return false
}

func TestScale(t *testing.T) {
	const agents, sends = 10, 200
	setup(t)
	home := socketHome(t)
	routes := routeTable()
	name := func(i int) string { return fmt.Sprintf("agent-%02d", i%agents) }
	for i := range agents {
		joinAll(t, routes, []string{"--as", name(i), "--kind", "claude"})
	}

	printed := make(chan string, sends)
	var listeners sync.WaitGroup
	for i := range agents {
		listeners.Go(func() {
			for range sends / agents {
				got := runWith(routes, "", "wait", "--as", name(i), "--timeout", "30s")
				if got.exit != 0 {
					t.Errorf("wait --as %s: exit %d; stderr %q", name(i), got.exit, got.stderr)
					return
				}
				printed <- strings.TrimSuffix(got.stdout, "\n")
			}
		})
	}

	turns := make([]sync.Mutex, agents)
	took := make([]time.Duration, sends)
	var senders sync.WaitGroup
	for k := range sends {
		senders.Go(func() {
			to := k % agents
			turns[to].Lock()
			defer turns[to].Unlock()
			if !listening(home, name(to)) {
				t.Errorf("message %03d: %s did not listen within 10s", k, name(to))
				return
			}
			start := time.Now()
			got := runWith(routes, "", "send", "--from", name(to+1), "--to", name(to), fmt.Sprintf("message %03d", k))
			took[k] = time.Since(start)
			if got.exit != 0 {
				t.Errorf("message %03d to %s: exit %d; stderr %q", k, name(to), got.exit, got.stderr)
			}
		})
	}
	senders.Wait()
	listeners.Wait()
	close(printed)

	seen := map[string]int{}
	ids := map[string]bool{}
	for wire := range printed {
		m, err := message.Parse(wire)
		if err != nil {
			t.Fatalf("printed %q: %v", wire, err)
		}
		var k int
		if _, err := fmt.Sscanf(m.Text, "message %03d", &k); err != nil {
			t.Fatalf("printed text %q: %v", m.Text, err)
		}
		if m.To != "claude:"+name(k) {
			t.Errorf("message %03d reached %s, want claude:%s", k, m.To, name(k))
		}
		seen[m.Text]++
		ids[m.ID] = true
	}
	for k := range sends {
		if n := seen[fmt.Sprintf("message %03d", k)]; n != 1 {
			t.Errorf("message %03d printed %d times, want 1; sends to one listener take turns here, and TestHeldSendsToABusyListenerAreAllPrintedOnce covers sends at once", k, n)
		}
	}

	lines := journalLines(t, home)
	if len(lines) != sends {
		t.Fatalf("journal holds %d lines, want %d", len(lines), sends)
	}
	for _, l := range lines {
		if l["result"] != "delivered" || !ids[l["id"].(string)] {
			t.Errorf("journal line %v is not a delivered, printed message", l)
		}
	}
	if got := strings.Count(whoLines(t, routes), "\n"); got != agents {
		t.Errorf("who lists %d agents, want %d", got, agents)
	}
	slices.Sort(took)
	t.Logf("%d sends to %d listeners, taking turns per listener: median %v, slowest %v", sends, agents, took[sends/2], took[sends-1])
	if took[sends-1] >= 100*time.Millisecond {
		t.Errorf("the slowest send took %v; the Claude route bar is under 100ms", took[sends-1])
	}
}
