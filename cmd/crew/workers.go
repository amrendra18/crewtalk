package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/amrendra18/crewtalk/internal/state"
)

const (
	schemaVersion = 1
	lockWait      = 5 * time.Second
	idTries       = 5
)

type worker struct {
	ID         string   `json:"id"`
	Role       string   `json:"role"`
	Lead       string   `json:"lead"`
	Kind       string   `json:"kind"`
	Model      string   `json:"model"`
	Effort     string   `json:"effort"`
	Address    string   `json:"address"`
	ReadyName  string   `json:"ready_name"`
	Tab        string   `json:"tab"`
	Pane       string   `json:"pane"`
	Repo       string   `json:"repo"`
	Folder     string   `json:"folder"`
	Branch     string   `json:"branch"`
	Base       string   `json:"base"`
	TaskFile   string   `json:"task_file"`
	TaskID     string   `json:"task_id"`
	Deadline   string   `json:"deadline"`
	State      string   `json:"state"`
	StateSince string   `json:"state_since"`
	IdleSince  string   `json:"idle_since"`
	ForcedAt   string   `json:"forced_at,omitempty"`
	FolderKept bool     `json:"folder_kept"`
	Notes      []string `json:"notes"`
}

type workerFile struct {
	Schema  int      `json:"schema"`
	Workers []worker `json:"workers"`
}

func (w *worker) setState(s string) {
	if w.State != s {
		w.State = s
		w.StateSince = now()
	}
}

func (w *worker) addNote(note string) {
	if !slices.Contains(w.Notes, note) {
		w.Notes = append(w.Notes, note)
	}
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// crewHomePath returns the absolute path of the state folder. Rows keep paths
// from it, and a later command can run from another folder.
func crewHomePath() (string, error) {
	home := os.Getenv("CREW_HOME")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		home = filepath.Join(user, ".crew")
	}
	return filepath.Abs(home)
}

// makeCrewHome returns the state folder, and creates it and its parts when missing.
func makeCrewHome() (string, error) {
	home, err := crewHomePath()
	if err != nil {
		return "", err
	}
	for _, dir := range []string{home, filepath.Join(home, "tasks"), filepath.Join(home, "worktrees")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	return home, nil
}

func readFile(path string) (workerFile, error) {
	var file workerFile
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return workerFile{Schema: schemaVersion, Workers: []worker{}}, nil
	}
	if err != nil {
		return file, err
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return file, fmt.Errorf("%s is not valid: %w; move it away to start a new one", path, err)
	}
	if file.Schema != schemaVersion {
		return file, fmt.Errorf("%s has schema %d and this crew reads %d; use a matching crew, or move it away to start a new one", path, file.Schema, schemaVersion)
	}
	return file, nil
}

// changeWorkers rewrites the workers file under its lock. A change that returns
// an error writes nothing.
func changeWorkers(home string, change func(*workerFile) error) error {
	path := filepath.Join(home, "workers.json")
	unlock, err := state.Lock(context.Background(), path+".lock", lockWait)
	if err != nil {
		return err
	}
	defer unlock()
	file, err := readFile(path)
	if err != nil {
		return err
	}
	if err := change(&file); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return state.Replace(path, append(data, '\n'))
}

// addWorker gives the row a new id and saves it. Ids are never reused, and rows
// are never deleted, so a used id stays taken.
func addWorker(home string, row *worker) error {
	return changeWorkers(home, func(file *workerFile) error {
		for range idTries {
			suffix, err := newSuffix()
			if err != nil {
				return err
			}
			id, ready := row.Role+"-"+suffix, "crew-ready-"+suffix
			taken := slices.ContainsFunc(file.Workers, func(w worker) bool {
				return w.ID == id || w.ReadyName == ready
			})
			if taken {
				continue
			}
			row.ID, row.ReadyName = id, ready
			row.Branch = "crew/" + id
			row.Folder = filepath.Join(home, "worktrees", id)
			row.TaskFile = filepath.Join(home, "tasks", id+".txt")
			file.Workers = append(file.Workers, *row)
			return nil
		}
		return fmt.Errorf("no free id after %d tries", idTries)
	})
}

var newSuffix = func() (string, error) {
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// saveWorker changes one saved row and updates row to match it.
func saveWorker(home string, row *worker, change func(*worker)) error {
	return changeWorkers(home, func(file *workerFile) error {
		for i := range file.Workers {
			if file.Workers[i].ID == row.ID {
				change(&file.Workers[i])
				*row = file.Workers[i]
				return nil
			}
		}
		return fmt.Errorf("no row for %s in workers.json", row.ID)
	})
}

func findWorker(home, id string) (worker, bool, error) {
	path := filepath.Join(home, "workers.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return worker{}, false, nil
	}
	unlock, err := state.Lock(context.Background(), path+".lock", lockWait)
	if err != nil {
		return worker{}, false, err
	}
	defer unlock()
	file, err := readFile(path)
	if err != nil {
		return worker{}, false, err
	}
	i := slices.IndexFunc(file.Workers, func(w worker) bool { return w.ID == id })
	if i < 0 {
		return worker{}, false, nil
	}
	return file.Workers[i], true, nil
}
