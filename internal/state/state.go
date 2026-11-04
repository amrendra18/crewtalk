package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var ErrBusy = errors.New("held by another process")

func Home() (string, error) {
	if home := os.Getenv("CREWTALK_HOME"); home != "" {
		return home, nil
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(user, ".crewtalk"), nil
}

func Lock(ctx context.Context, path string, wait time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return func() { f.Close() }, nil
		case err != syscall.EWOULDBLOCK && err != syscall.EINTR:
			f.Close()
			return nil, err
		case !time.Now().Before(deadline):
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, ErrBusy)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func Replace(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
