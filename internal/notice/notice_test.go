package notice

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func fakeHerdr(t *testing.T, mode string) string {
	t.Helper()
	bin, err := filepath.Abs("testdata/herdr")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(bin, filepath.Join(dir, "herdr")); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_HERDR_CALLS", calls)
	t.Setenv("FAKE_HERDR_MODE", mode)
	t.Setenv("HERDR_ENV", "1")
	return calls
}

func calls(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		out = append(out, strings.Split(strings.TrimSuffix(line, "\x00"), "\x00"))
	}
	return out
}

func TestNoticeRunsHerdrInsideHerdr(t *testing.T) {
	path := fakeHerdr(t, "")
	Show("crewtalk: send refused", "refused-paused: remove PAUSE")
	got := calls(t, path)
	want := []string{"notification", "show", "crewtalk: send refused", "--body", "refused-paused: remove PAUSE"}
	if len(got) != 1 || !slices.Equal(got[0], want) {
		t.Fatalf("herdr calls %q, want one call %q", got, want)
	}
}

func TestNoticeDoesNothingOutsideHerdr(t *testing.T) {
	path := fakeHerdr(t, "")
	for _, env := range []string{"", "0", "yes"} {
		t.Setenv("HERDR_ENV", env)
		Show("title", "body")
	}
	if got := calls(t, path); len(got) != 0 {
		t.Fatalf("herdr ran %q with HERDR_ENV not 1, want no call", got)
	}
}

func TestNoticeWithNoHerdrOnPathDoesNothing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HERDR_ENV", "1")
	Show("title", "body")
}

func TestNoticeFailingHerdrIsIgnored(t *testing.T) {
	path := fakeHerdr(t, "fail")
	Show("title", "body")
	if got := calls(t, path); len(got) != 1 {
		t.Fatalf("herdr calls %q, want 1", got)
	}
}

func TestNoticeHungHerdrIsStopped(t *testing.T) {
	old := wait
	wait = 100 * time.Millisecond
	t.Cleanup(func() { wait = old })
	fakeHerdr(t, "hang")
	start := time.Now()
	Show("title", "body")
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Show took %v with a hung herdr, want it stopped after %v", took, wait)
	}
}
