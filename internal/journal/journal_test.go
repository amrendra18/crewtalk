package journal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func readLines(t *testing.T, home string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(home, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []map[string]any
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			t.Fatalf("line %q is not one JSON object: %v", scan.Text(), err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestLogOneLineFields(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	j, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	when := time.Date(2026, 11, 2, 22, 57, 1, 0, time.UTC)
	if err := j.Append(Entry{Time: when, ID: "id1", From: "claude:lead", To: "codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", Thread: "t1", Turn: 2, Route: "codex", Result: OutcomeUnknown}); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, home)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	want := map[string]any{"time": "2026-11-02T22:57:01Z", "id": "id1", "from": "claude:lead", "to": "codex:019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a", "thread": "t1", "turn": 2.0, "route": "codex", "result": "outcome-unknown"}
	if fmt.Sprint(lines[0]) != fmt.Sprint(want) {
		t.Fatalf("line = %v, want %v", lines[0], want)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("state folder mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestLogOneLineConcurrentSenders(t *testing.T) {
	home := t.TempDir()
	const senders, each = 8, 50
	text := strings.Repeat("x", 4096)
	var wg sync.WaitGroup
	for s := range senders {
		wg.Go(func() {
			j, err := Open(home)
			if err != nil {
				t.Error(err)
				return
			}
			defer j.Close()
			for i := range each {
				if err := j.Append(Entry{ID: fmt.Sprintf("%d-%d", s, i), Thread: text, Result: Delivered}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	lines := readLines(t, home)
	if len(lines) != senders*each {
		t.Fatalf("got %d lines, want %d", len(lines), senders*each)
	}
	seen := map[any]bool{}
	for _, l := range lines {
		seen[l["id"]] = true
	}
	if len(seen) != senders*each {
		t.Fatalf("got %d distinct ids, want %d", len(seen), senders*each)
	}
}

func TestLogOneLineWaitsForLock(t *testing.T) {
	home := t.TempDir()
	j, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	holder, err := os.OpenFile(filepath.Join(home, "log.jsonl.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() { done <- j.Append(Entry{ID: "after-lock", Result: Delivered}) }()
	select {
	case err := <-done:
		t.Fatalf("Append returned (%v) while another process held log.jsonl.lock", err)
	case <-time.After(200 * time.Millisecond):
	}
	if lines := readLines(t, home); len(lines) != 0 {
		t.Fatalf("got %d lines while the lock was held, want 0", len(lines))
	}
	syscall.Flock(int(holder.Fd()), syscall.LOCK_UN)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if lines := readLines(t, home); len(lines) != 1 {
		t.Fatalf("got %d lines after the lock was released, want 1", len(lines))
	}
}

func appendAll(t *testing.T, home string, entries ...Entry) {
	t.Helper()
	j, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	for _, e := range entries {
		if err := j.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

func readAll(t *testing.T, home string) []Entry {
	t.Helper()
	var got []Entry
	if err := Read(context.Background(), home, false, func(e Entry) error {
		got = append(got, e)
		return nil
	}, func(err error) { t.Errorf("Read skipped a line: %v", err) }); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestLogOneLineReplyTo(t *testing.T) {
	home := t.TempDir()
	appendAll(t, home, Entry{ID: "r1", Thread: "t1", Turn: 2, ReplyTo: "q1", Result: Delivered})
	lines := readLines(t, home)
	if len(lines) != 1 || lines[0]["reply_to"] != "q1" {
		t.Fatalf("lines = %v, want one line with reply_to q1", lines)
	}
}

func TestLogReadGivesEveryEntryInOrder(t *testing.T) {
	home := t.TempDir()
	want := []Entry{
		{Time: time.Date(2026, 11, 3, 6, 5, 17, 0, time.UTC), ID: "a", From: "claude:lead", To: "claude:w1", Thread: "t1", Turn: 1, Route: "claude", Result: Delivered},
		{Time: time.Date(2026, 11, 3, 6, 5, 18, 0, time.UTC), ID: "b", From: "claude:w1", To: "claude:lead", Thread: "t1", Turn: 2, ReplyTo: "a", Route: "claude", Result: NotListening},
		{Time: time.Date(2026, 11, 3, 6, 5, 19, 0, time.UTC), ID: "c", From: "claude:lead", To: "w9", Thread: "t2", Turn: 1, Result: ReceiverUnknown},
	}
	appendAll(t, home, want...)
	if got := readAll(t, home); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Read = %v, want %v", got, want)
	}
}

func TestLogReadMissingLogIsEmpty(t *testing.T) {
	home := t.TempDir()
	if got := readAll(t, home); len(got) != 0 {
		t.Fatalf("Read = %v, want nothing", got)
	}
	if _, err := os.Lstat(filepath.Join(home, "log.jsonl")); err == nil {
		t.Fatal("Read created log.jsonl")
	}
}

func TestLogReadSkipsALineThatIsNotJSON(t *testing.T) {
	home := t.TempDir()
	appendAll(t, home, Entry{ID: "a", Result: Delivered})
	f, err := os.OpenFile(filepath.Join(home, "log.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"id":"broken` + "\n")
	f.Close()
	appendAll(t, home, Entry{ID: "c", Thread: "t3", Turn: 4, Result: Delivered})
	var ids []string
	var skipped []error
	err = Read(context.Background(), home, false, func(e Entry) error {
		ids = append(ids, e.ID)
		return nil
	}, func(err error) { skipped = append(skipped, err) })
	if err != nil || !slices.Equal(ids, []string{"a", "c"}) {
		t.Fatalf("Read gave %v, %v; want a and c", ids, err)
	}
	if len(skipped) != 1 || skipped[0].Error() != filepath.Join(home, "log.jsonl")+" line 2 is not one JSON object, so it was skipped: invalid character '\\n' in string; the operator can remove that line from the log file" {
		t.Fatalf("Read skipped %v, want one error naming the log, line 2, and what the operator can do", skipped)
	}
	if e, found, err := Find(home, "c"); err != nil || !found || e.Thread != "t3" || e.Turn != 4 {
		t.Fatalf("Find(c) after the broken line = %+v, %v, %v; want thread t3 turn 4", e, found, err)
	}
}

func TestLogAppendAfterATornLineStartsANewLine(t *testing.T) {
	home := t.TempDir()
	appendAll(t, home, Entry{ID: "a", Result: Delivered})
	f, err := os.OpenFile(filepath.Join(home, "log.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"id":"torn`)
	f.Close()
	appendAll(t, home, Entry{ID: "c", Thread: "t3", Turn: 4, Result: Delivered})
	var ids []string
	var skipped []error
	err = Read(context.Background(), home, false, func(e Entry) error {
		ids = append(ids, e.ID)
		return nil
	}, func(err error) { skipped = append(skipped, err) })
	if err != nil || !slices.Equal(ids, []string{"a", "c"}) {
		t.Fatalf("Read gave %v, %v; want a and c", ids, err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Error(), "line 2") {
		t.Fatalf("Read skipped %v, want one error naming line 2", skipped)
	}
	if e, found, err := Find(home, "c"); err != nil || !found || e.Thread != "t3" {
		t.Fatalf("Find(c) after the torn line = %+v, %v, %v; want thread t3", e, found, err)
	}
}

func TestLogFollowSeesLaterAppends(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Entry)
	done := make(chan error)
	go func() {
		done <- Read(ctx, home, true, func(e Entry) error {
			got <- e
			return nil
		}, func(err error) { t.Errorf("Read skipped a line: %v", err) })
	}()
	next := func(want string) {
		t.Helper()
		select {
		case e := <-got:
			if e.ID != want {
				t.Fatalf("followed entry %q, want %q", e.ID, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no entry %q within 5s", want)
		}
	}
	time.Sleep(100 * time.Millisecond)
	appendAll(t, home, Entry{ID: "first", Result: Delivered})
	next("first")
	f, err := os.OpenFile(filepath.Join(home, "log.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(`{"id":"sec`)
	time.Sleep(150 * time.Millisecond)
	f.WriteString(`ond","result":"delivered"}` + "\n")
	next("second")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Read after cancel = %v, want nil", err)
	}
}

func TestLogFindGivesTheFirstLineOfAnID(t *testing.T) {
	home := t.TempDir()
	appendAll(t, home,
		Entry{ID: "a", Thread: "t1", Turn: 3, Result: Delivered},
		Entry{ID: "b", Thread: "t2", Turn: 1, Result: Delivered},
		Entry{ID: "a", Thread: "t1", Turn: 3, Result: Failed},
	)
	e, found, err := Find(home, "a")
	if err != nil || !found || e.Thread != "t1" || e.Turn != 3 || e.Result != Delivered {
		t.Fatalf("Find(a) = %+v, %v, %v; want the delivered line of t1 turn 3", e, found, err)
	}
	if _, found, err := Find(home, "zz"); err != nil || found {
		t.Fatalf("Find(zz) found %v, err %v; want not found", found, err)
	}
}

func TestLogPairSinceGivesOnlyThatPairAfterTheTime(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	appendAll(t, home,
		Entry{Time: now.Add(-2 * time.Hour), ID: "old", From: "claude:a", To: "claude:b", Result: Delivered},
		Entry{Time: now, ID: "back", From: "claude:b", To: "claude:a", Result: Delivered},
		Entry{Time: now, ID: "other", From: "claude:a", To: "claude:bb", Result: Delivered},
		Entry{Time: now, ID: "odd", From: "claude:<a&>", To: "claude:b", Result: Delivered},
		Entry{Time: now, ID: "new", From: "claude:a", To: "claude:b", Result: Held},
	)
	writeRaw(t, home, `{"from":"claude:a","to":"claude:b",`+"\n")
	writeRaw(t, home, `{"to":"claude:b","result":"delivered","from":"claude:a","id":"reordered","time":"`+now.Format(time.RFC3339)+`"}`+"\n")
	appendAll(t, home, Entry{Time: now, ID: "last", From: "claude:a", To: "claude:b", Result: Delivered})

	var skipped []error
	got, err := PairSince(home, "claude:a", "claude:b", now.Add(-time.Hour), func(err error) { skipped = append(skipped, err) })
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if !slices.Equal(ids, []string{"new", "reordered", "last"}) {
		t.Fatalf("PairSince gave %v, want new, reordered and last", ids)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Error(), "line 6 is not one JSON object") || !strings.HasSuffix(skipped[0].Error(), "; the operator can remove that line from the log file") {
		t.Fatalf("PairSince skipped %v, want one error naming line 6", skipped)
	}
	quiet := func(error) {}
	if odd, err := PairSince(home, "claude:<a&>", "claude:b", now.Add(-time.Hour), quiet); err != nil || len(odd) != 1 || odd[0].ID != "odd" {
		t.Fatalf("PairSince for a name JSON escapes = %+v, %v; want odd", odd, err)
	}
	if none, err := PairSince(filepath.Join(home, "missing"), "claude:a", "claude:b", now, quiet); err != nil || len(none) != 0 {
		t.Fatalf("PairSince with no log = %+v, %v; want nothing", none, err)
	}
}

func writeRaw(t *testing.T, home, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(home, "log.jsonl"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}
