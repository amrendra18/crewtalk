package book

import (
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

const lockWait = 5 * time.Second

type Agent struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Address string `json:"address"`
}

type file struct {
	Agents []Agent `json:"agents"`
}

func CheckName(name string) error {
	if !message.ValidField(name) || strings.ContainsAny(name, "/:") {
		return fmt.Errorf("name %q must not be empty or hold spaces, commas, semicolons, square brackets, slashes, colons, or control or invisible format characters", name)
	}
	return nil
}

func Load(home string) ([]Agent, error) {
	path := filepath.Join(home, "agents.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s is not an address book: %w; to recover, move it aside (mv %s %s.broken) and join each agent again", path, err, path, path)
	}
	return f.Agents, nil
}

func Lookup(home, name string) (Agent, bool, error) {
	agents, err := Load(home)
	if err != nil {
		return Agent{}, false, err
	}
	i := slices.IndexFunc(agents, func(a Agent) bool { return a.Name == name })
	if i < 0 {
		return Agent{}, false, nil
	}
	return agents[i], true, nil
}

func Join(home string, a Agent) (Agent, bool, error) {
	return change(home, a.Name, &a)
}

func Leave(home, name string) (Agent, bool, error) {
	return change(home, name, nil)
}

func change(home, name string, add *Agent) (Agent, bool, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Agent{}, false, err
	}
	unlock, err := state.Lock(context.Background(), filepath.Join(home, "agents.json.lock"), lockWait)
	if err != nil {
		return Agent{}, false, err
	}
	defer unlock()
	agents, err := Load(home)
	if err != nil {
		return Agent{}, false, err
	}
	var old Agent
	i := slices.IndexFunc(agents, func(b Agent) bool { return b.Name == name })
	if i >= 0 {
		old = agents[i]
		agents = slices.Delete(agents, i, i+1)
	}
	if add == nil && i < 0 {
		return Agent{}, false, nil
	}
	if add != nil {
		agents = append(agents, *add)
		slices.SortFunc(agents, func(x, y Agent) int { return strings.Compare(x.Name, y.Name) })
	}
	data, err := json.MarshalIndent(file{Agents: agents}, "", "  ")
	if err != nil {
		return Agent{}, false, err
	}
	return old, i >= 0, state.Replace(filepath.Join(home, "agents.json"), append(data, '\n'))
}
