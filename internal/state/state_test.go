package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHome(t *testing.T) {
	t.Setenv("CREWTALK_HOME", "/tmp/given")
	if got, err := Home(); err != nil || got != "/tmp/given" {
		t.Fatalf("Home() = %q, %v; want /tmp/given", got, err)
	}
	t.Setenv("CREWTALK_HOME", "")
	t.Setenv("HOME", "/tmp/user")
	if got, err := Home(); err != nil || got != "/tmp/user/.crewtalk" {
		t.Fatalf("Home() = %q, %v; want /tmp/user/.crewtalk", got, err)
	}
}

func TestLockWaitsThenGivesUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := Lock(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file %v, %v; want mode 0600", info, err)
	}

	start := time.Now()
	if _, err := Lock(context.Background(), path, 200*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("Lock on a held lock: %v, want ErrBusy", err)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("Lock gave up after %v, want about 200ms", elapsed)
	}

	got := make(chan error, 1)
	go func() {
		second, err := Lock(context.Background(), path, 2*time.Second)
		if err == nil {
			second()
		}
		got <- err
	}()
	time.Sleep(100 * time.Millisecond)
	unlock()
	if err := <-got; err != nil {
		t.Fatalf("Lock after the holder released it: %v", err)
	}
}

func TestLockMissingFolder(t *testing.T) {
	_, err := Lock(context.Background(), filepath.Join(t.TempDir(), "absent", "x.lock"), time.Second)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lock in a missing folder: %v, want os.ErrNotExist", err)
	}
}

func TestLockStopsWhenTheContextEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	unlock, err := Lock(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, err := Lock(ctx, path, 10*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lock with a cancelled context: %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Lock returned %v after the cancel at 100ms", elapsed)
	}
}
