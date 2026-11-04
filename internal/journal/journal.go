package journal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/amrendra18/crewtalk/internal/state"
)

const lockWait = 5 * time.Second

const followPoll = 50 * time.Millisecond

type Result string

const (
	Delivered       Result = "delivered"
	NotListening    Result = "not-listening"
	ReceiverUnknown Result = "receiver-unknown"
	OutcomeUnknown  Result = "outcome-unknown"
	Failed          Result = "failed"
	Held            Result = "held"
	Expired         Result = "expired"
	RefusedLimit    Result = "refused-limit"
	RefusedPaused   Result = "refused-paused"
)

type Entry struct {
	Time    time.Time `json:"time"`
	ID      string    `json:"id"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Thread  string    `json:"thread"`
	Turn    int       `json:"turn"`
	ReplyTo string    `json:"reply_to,omitempty"`
	Route   string    `json:"route"`
	Result  Result    `json:"result"`
	HeldFor float64   `json:"held_for,omitempty"`
}

type Journal struct {
	log  *os.File
	lock string
}

func Open(home string) (*Journal, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(home, "log.jsonl")
	log, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	j := &Journal{log: log, lock: path + ".lock"}
	unlock, err := state.Lock(context.Background(), j.lock, lockWait)
	if err != nil {
		log.Close()
		return nil, err
	}
	unlock()
	return j, nil
}

func (j *Journal) Append(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	unlock, err := state.Lock(context.Background(), j.lock, lockWait)
	if err != nil {
		return err
	}
	defer unlock()
	info, err := j.log.Stat()
	if err != nil {
		return err
	}
	if size := info.Size(); size > 0 {
		last := make([]byte, 1)
		if _, err := j.log.ReadAt(last, size-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	_, err = j.log.Write(append(line, '\n'))
	return err
}

func (j *Journal) Close() error {
	return j.log.Close()
}

func Read(ctx context.Context, home string, follow bool, each func(Entry) error, skip func(error)) error {
	path := filepath.Join(home, "log.jsonl")
	f, err := os.Open(path)
	for follow && errors.Is(err, fs.ErrNotExist) && pause(ctx) {
		f, err = os.Open(path)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var line []byte
	for n := 1; ; {
		chunk, err := r.ReadBytes('\n')
		line = append(line, chunk...)
		if err == io.EOF {
			if !follow || !pause(ctx) {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			skip(notJSON(path, n, err))
		} else if err := each(e); err != nil {
			return err
		}
		line = line[:0]
		n++
	}
}

func Find(home, id string) (Entry, bool, error) {
	errFound := errors.New("found")
	var found Entry
	err := Read(context.Background(), home, false, func(e Entry) error {
		if e.ID != id {
			return nil
		}
		found = e
		return errFound
	}, func(error) {})
	if err == errFound {
		return found, true, nil
	}
	return Entry{}, false, err
}

func pause(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(followPoll):
		return true
	}
}

func PairSince(home, from, to string, since time.Time, skip func(error)) ([]Entry, error) {
	fromKey, toKey := field("from", from), field("to", to)
	path := filepath.Join(home, "log.jsonl")
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var found []Entry
	r := bufio.NewReader(f)
	for n := 1; ; n++ {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return found, nil
		}
		if err != nil {
			return nil, err
		}
		if (!bytes.Contains(line, fromKey) || !bytes.Contains(line, toKey)) && json.Valid(line) {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			skip(notJSON(path, n, err))
		} else if e.From == from && e.To == to && e.Time.After(since) {
			found = append(found, e)
		}
	}
}

func field(name, value string) []byte {
	quoted, _ := json.Marshal(value)
	return append([]byte(`"`+name+`":`), quoted...)
}

func notJSON(path string, n int, err error) error {
	return fmt.Errorf("%s line %d is not one JSON object, so it was skipped: %w; the operator can remove that line from the log file", path, n, err)
}
