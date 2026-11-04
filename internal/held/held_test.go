package held

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amrendra18/crewtalk/internal/message"
)

func noSkip(t *testing.T) func(error) {
	return func(err error) { t.Errorf("skipped: %v", err) }
}

func holdAll(t *testing.T, home, name string, ms ...message.Message) {
	t.Helper()
	box, unlock, err := Lock(context.Background(), home, noSkip(t))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, m := range ms {
		if err := box.Hold(name, m); err != nil {
			t.Fatal(err)
		}
	}
}

func oldest(t *testing.T, home, name string) (message.Message, bool) {
	t.Helper()
	box, unlock, err := Lock(context.Background(), home, noSkip(t))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	m, found, err := box.Oldest(name)
	if err != nil {
		t.Fatal(err)
	}
	return m, found
}

func aged(m message.Message, age time.Duration) message.Message {
	m.Time = time.Now().UTC().Add(-age)
	return m
}

func ids(ms []message.Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func TestHeldOldestComesFirstAndRemoveTakesIt(t *testing.T) {
	home := t.TempDir()
	first := message.New("codex:a", "claude:lead", "", "first", 1)
	second := message.New("codex:b", "claude:lead", "", "second", 1)
	holdAll(t, home, "lead", first, second)

	if got, found := oldest(t, home, "lead"); !found || got.ID != first.ID || got.Text != "first" || !got.Time.Equal(first.Time) {
		t.Fatalf("Oldest = %+v, %v; want the first message", got, found)
	}
	if removed, err := Remove(home, "lead", first.ID, noSkip(t)); err != nil || !removed {
		t.Fatalf("Remove = %v, %v; want true", removed, err)
	}
	if got, found := oldest(t, home, "lead"); !found || got.ID != second.ID {
		t.Fatalf("Oldest after Remove = %+v, %v; want the second message", got, found)
	}
	if removed, err := Remove(home, "lead", first.ID, noSkip(t)); err != nil || removed {
		t.Fatalf("second Remove = %v, %v; want false", removed, err)
	}
}

func TestHeldFileIsPrivateAndGoesWhenEmpty(t *testing.T) {
	home := t.TempDir()
	m := message.New("codex:a", "claude:lead", "", "secret text", 1)
	holdAll(t, home, "lead", m)
	path := filepath.Join(home, "held", "lead.jsonl")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("held file %s: %v, %v; want mode 0600", path, info, err)
	}
	if dir, err := os.Stat(filepath.Dir(path)); err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("held folder: %v, %v; want mode 0700", dir, err)
	}
	if _, err := Remove(home, "lead", m.ID, noSkip(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("held file after its last message left: %v, want gone", err)
	}
}

func TestHeldNamesAreSeparate(t *testing.T) {
	home := t.TempDir()
	holdAll(t, home, "lead", message.New("codex:a", "claude:lead", "", "to lead", 1))
	if _, found := oldest(t, home, "w1"); found {
		t.Fatal("w1 has a held message sent to lead")
	}
}

func TestExpiryTakesOnlyMessagesPastTheDeadline(t *testing.T) {
	home := t.TempDir()
	old := aged(message.New("codex:a", "claude:lead", "", "old", 1), Deadline+time.Second)
	fresh := aged(message.New("codex:a", "claude:lead", "", "fresh", 1), Deadline-time.Minute)
	otherOld := aged(message.New("claude:w1", "claude:w2", "", "old too", 1), 2*Deadline)
	holdAll(t, home, "lead", old, fresh)
	holdAll(t, home, "w2", otherOld)

	gone, err := Expire(home, time.Now(), noSkip(t))
	if err != nil {
		t.Fatal(err)
	}
	got := ids(gone)
	slices.Sort(got)
	want := []string{old.ID, otherOld.ID}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("expired %v, want %v", got, want)
	}
	if m, found := oldest(t, home, "lead"); !found || m.ID != fresh.ID {
		t.Fatalf("lead keeps %+v, %v; want only the fresh message", m, found)
	}
	if _, found := oldest(t, home, "w2"); found {
		t.Fatal("w2 still holds its expired message")
	}
	if again, err := Expire(home, time.Now(), noSkip(t)); err != nil || len(again) != 0 {
		t.Fatalf("second Expire = %v, %v; want nothing", ids(again), err)
	}
}

func TestExpiryRunsWhenTheHomeFolderNameHasAGlobCharacter(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h[2]")
	old := aged(message.New("codex:a", "claude:lead", "", "old", 1), 2*Deadline)
	holdAll(t, home, "lead", old)
	gone, err := Expire(home, time.Now(), noSkip(t))
	if err != nil || !slices.Equal(ids(gone), []string{old.ID}) {
		t.Fatalf("Expire = %v, %v; want %s", ids(gone), err, old.ID)
	}
	if _, found := oldest(t, home, "lead"); found {
		t.Fatal("lead still holds its expired message")
	}
}

func TestExpiryTellsEachSenderOnce(t *testing.T) {
	home := t.TempDir()
	a1 := aged(message.New("codex:a", "claude:lead", "", "1", 1), 2*Deadline)
	a2 := aged(message.New("codex:a", "claude:w1", "", "2", 1), 2*Deadline)
	b := aged(message.New("claude:b", "claude:lead", "", "3", 1), 2*Deadline)
	holdAll(t, home, "lead", a1, b)
	holdAll(t, home, "w1", a2)
	if _, err := Expire(home, time.Now(), noSkip(t)); err != nil {
		t.Fatal(err)
	}

	got, err := Told(home, "codex:a", noSkip(t))
	slices.Sort(got)
	want := []string{a1.ID, a2.ID}
	slices.Sort(want)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Told(codex:a) = %v, %v; want %v", got, err, want)
	}
	if again, err := Told(home, "codex:a", noSkip(t)); err != nil || len(again) != 0 {
		t.Fatalf("second Told(codex:a) = %v, %v; want nothing", again, err)
	}
	if got, err := Told(home, "claude:b", noSkip(t)); err != nil || !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("Told(claude:b) = %v, %v; want [%s]", got, err, b.ID)
	}
}

func TestExpiryWithNothingHeldCreatesNothing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if gone, err := Expire(home, time.Now(), noSkip(t)); err != nil || len(gone) != 0 {
		t.Fatalf("Expire = %v, %v; want nothing", ids(gone), err)
	}
	if got, err := Told(home, "codex:a", noSkip(t)); err != nil || len(got) != 0 {
		t.Fatalf("Told = %v, %v; want nothing", got, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("Expire and Told with nothing held made %s: %v", home, err)
	}
}

func TestHeldLineThatIsNotJSONIsSkippedAndKept(t *testing.T) {
	home := t.TempDir()
	first := message.New("codex:a", "claude:lead", "", "first", 1)
	holdAll(t, home, "lead", first)
	path := filepath.Join(home, "held", "lead.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var skipped []error
	skip := func(err error) { skipped = append(skipped, err) }
	box, unlock, err := Lock(context.Background(), home, skip)
	if err != nil {
		t.Fatal(err)
	}
	second := message.New("codex:a", "claude:lead", "", "second", 1)
	if err := box.Hold("lead", second); err != nil {
		t.Fatalf("Hold beside a line that is not JSON: %v", err)
	}
	unlock()
	if len(skipped) != 1 || !strings.Contains(skipped[0].Error(), path+" line 2 ") {
		t.Fatalf("skipped %v, want one error naming %s line 2", skipped, path)
	}
	for _, want := range []message.Message{first, second} {
		if removed, err := Remove(home, "lead", want.ID, skip); err != nil || !removed {
			t.Fatalf("Remove(%s) = %v, %v; want true", want.Text, removed, err)
		}
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "not json\n" {
		t.Fatalf("held file %q, %v; want only the line that is not JSON", data, err)
	}
}

func TestHeldLockIsExclusive(t *testing.T) {
	home := t.TempDir()
	_, unlock, err := Lock(context.Background(), home, noSkip(t))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := Lock(ctx, home, noSkip(t)); err == nil {
		t.Fatal("a second Lock got the held lock while the first held it")
	}
}
