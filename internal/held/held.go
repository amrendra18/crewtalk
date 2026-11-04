package held

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/amrendra18/crewtalk/internal/message"
	"github.com/amrendra18/crewtalk/internal/state"
)

const Deadline = 30 * time.Minute

const lockWait = 5 * time.Second

type Box struct {
	home string
	skip func(error)
}

type expiredID struct {
	ID   string `json:"id"`
	From string `json:"from"`
}

type lines[T any] struct {
	path   string
	items  []T
	broken [][]byte
}

func Lock(ctx context.Context, home string, skip func(error)) (Box, func(), error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Box{}, nil, err
	}
	unlock, err := state.Lock(ctx, filepath.Join(home, "held.lock"), lockWait)
	if err != nil {
		return Box{}, nil, err
	}
	return Box{home, skip}, unlock, nil
}

func (b Box) Hold(name string, m message.Message) error {
	f, err := load[message.Message](b.file(name), b.skip)
	if err != nil {
		return err
	}
	f.items = append(f.items, m)
	return f.save()
}

func (b Box) Oldest(name string) (message.Message, bool, error) {
	f, err := load[message.Message](b.file(name), b.skip)
	if err != nil || len(f.items) == 0 {
		return message.Message{}, false, err
	}
	return f.items[0], true, nil
}

func Remove(home, name, id string, skip func(error)) (bool, error) {
	b, unlock, err := Lock(context.Background(), home, skip)
	if err != nil {
		return false, err
	}
	defer unlock()
	f, err := load[message.Message](b.file(name), skip)
	if err != nil {
		return false, err
	}
	n := len(f.items)
	f.items = slices.DeleteFunc(f.items, func(m message.Message) bool { return m.ID == id })
	if len(f.items) == n {
		return false, nil
	}
	return true, f.save()
}

func Expire(home string, now time.Time, skip func(error)) ([]message.Message, error) {
	if files, err := heldFiles(home); err != nil || len(files) == 0 {
		return nil, err
	}
	b, unlock, err := Lock(context.Background(), home, skip)
	if err != nil {
		return nil, err
	}
	defer unlock()
	files, err := heldFiles(home)
	if err != nil {
		return nil, err
	}
	var gone []message.Message
	var skipped []error
	for _, path := range files {
		f, err := load[message.Message](path, skip)
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		var kept, old []message.Message
		for _, m := range f.items {
			if now.Sub(m.Time) > Deadline {
				old = append(old, m)
			} else {
				kept = append(kept, m)
			}
		}
		if len(old) == 0 {
			continue
		}
		f.items = kept
		if err := f.save(); err != nil {
			skipped = append(skipped, err)
			continue
		}
		gone = append(gone, old...)
	}
	if len(gone) == 0 {
		return nil, errors.Join(skipped...)
	}
	told, err := load[expiredID](b.expiredFile(), skip)
	if err != nil {
		return gone, errors.Join(append(skipped, err)...)
	}
	for _, m := range gone {
		told.items = append(told.items, expiredID{m.ID, m.From})
	}
	return gone, errors.Join(append(skipped, told.save())...)
}

func Told(home, from string, skip func(error)) ([]string, error) {
	path := filepath.Join(home, "expired.jsonl")
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	b, unlock, err := Lock(context.Background(), home, skip)
	if err != nil {
		return nil, err
	}
	defer unlock()
	f, err := load[expiredID](b.expiredFile(), skip)
	if err != nil {
		return nil, err
	}
	var ids []string
	var kept []expiredID
	for _, e := range f.items {
		if e.From == from {
			ids = append(ids, e.ID)
		} else {
			kept = append(kept, e)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	f.items = kept
	return ids, f.save()
}

func heldFiles(home string) ([]string, error) {
	dir := filepath.Join(home, "held")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	return files, nil
}

func (b Box) file(name string) string {
	return filepath.Join(b.home, "held", name+".jsonl")
}

func (b Box) expiredFile() string {
	return filepath.Join(b.home, "expired.jsonl")
}

func load[T any](path string, skip func(error)) (lines[T], error) {
	f := lines[T]{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	n := 0
	for line := range bytes.Lines(data) {
		n++
		line = bytes.TrimSuffix(line, []byte("\n"))
		var item T
		if err := json.Unmarshal(line, &item); err != nil {
			skip(fmt.Errorf("%s line %d is not one JSON object, so it was skipped: %w; the operator can remove that line", path, n, err))
			f.broken = append(f.broken, line)
			continue
		}
		f.items = append(f.items, item)
	}
	return f, nil
}

func (f lines[T]) save() error {
	if len(f.items) == 0 && len(f.broken) == 0 {
		err := os.Remove(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var data []byte
	for _, item := range f.items {
		line, err := json.Marshal(item)
		if err != nil {
			return err
		}
		data = append(append(data, line...), '\n')
	}
	for _, line := range f.broken {
		data = append(append(data, line...), '\n')
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	return state.Replace(f.path, data)
}
