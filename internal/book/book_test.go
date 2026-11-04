package book

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestJoinAddsThenReplaces(t *testing.T) {
	home := t.TempDir()
	first := Agent{Name: "w1", Kind: "codex", Address: "019a3c1e-6f2b-7d40-9e8a-3b5c2d1e0f4a"}
	for _, a := range []Agent{first, {Name: "lead", Kind: "claude", Address: "lead"}} {
		if _, replaced, err := Join(home, a); err != nil || replaced {
			t.Fatalf("Join(%v) replaced %v, err %v; want a new entry", a, replaced, err)
		}
	}
	old, replaced, err := Join(home, Agent{Name: "w1", Kind: "codex", Address: "11111111-2222-4333-8444-555555555555"})
	if err != nil || !replaced || old != first {
		t.Fatalf("second Join of w1 = %v, %v, %v; want it to replace %v", old, replaced, err, first)
	}
	got, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{{Name: "lead", Kind: "claude", Address: "lead"}, {Name: "w1", Kind: "codex", Address: "11111111-2222-4333-8444-555555555555"}}
	if !slices.Equal(got, want) {
		t.Fatalf("Load = %v, want %v", got, want)
	}
	a, found, err := Lookup(home, "w1")
	if err != nil || !found || a != want[1] {
		t.Fatalf("Lookup(w1) = %v, %v, %v; want %v", a, found, err, want[1])
	}
	if _, found, err := Lookup(home, "w2"); err != nil || found {
		t.Fatalf("Lookup(w2) found %v, err %v; want not found", found, err)
	}
}

func TestJoinFileShape(t *testing.T) {
	home := t.TempDir()
	if _, _, err := Join(home, Agent{Name: "lead", Kind: "claude", Address: "lead"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string][]map[string]string
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("agents.json %q is not one JSON object: %v", data, err)
	}
	if fmt.Sprint(file) != fmt.Sprint(map[string][]map[string]string{"agents": {{"name": "lead", "kind": "claude", "address": "lead"}}}) {
		t.Fatalf("agents.json = %v", file)
	}
	info, err := os.Stat(filepath.Join(home, "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("agents.json mode = %v, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"agents.json", "agents.json.lock"}) {
		t.Fatalf("home holds %v, want only agents.json and its lock", names)
	}
}

func TestJoinConcurrentJoinsKeepEveryName(t *testing.T) {
	home := t.TempDir()
	const agents, rounds = 20, 5
	var wg sync.WaitGroup
	for i := range agents {
		wg.Go(func() {
			for r := range rounds {
				name := fmt.Sprintf("w%02d", i)
				if _, _, err := Join(home, Agent{Name: name, Kind: "claude", Address: fmt.Sprintf("%s-%d", name, r)}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	got, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != agents {
		t.Fatalf("Load gave %d agents, want %d: %v", len(got), agents, got)
	}
	for i, a := range got {
		if want := fmt.Sprintf("w%02d-%d", i, rounds-1); a.Name != fmt.Sprintf("w%02d", i) || a.Address != want {
			t.Errorf("agent %d = %v, want name w%02d and the last address %s", i, a, i, want)
		}
	}
}

func TestJoinLoadWithNoBookIsEmpty(t *testing.T) {
	home := t.TempDir()
	got, err := Load(home)
	if err != nil || len(got) != 0 {
		t.Fatalf("Load = %v, %v; want nothing", got, err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("Load created %v", entries)
	}
}

func TestJoinLoadRejectsABrokenBook(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "agents.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil || !strings.Contains(err.Error(), "move it aside") {
		t.Fatalf("Load of a broken agents.json = %v, want an error that says to move it aside", err)
	}
	if _, _, err := Join(home, Agent{Name: "lead", Kind: "claude", Address: "lead"}); err == nil {
		t.Fatal("Join over a broken agents.json returned no error")
	}
	if _, _, err := Leave(home, "lead"); err == nil {
		t.Fatal("Leave over a broken agents.json returned no error")
	}
}

func TestLeaveRemovesTheName(t *testing.T) {
	home := t.TempDir()
	w1 := Agent{Name: "w1", Kind: "claude", Address: "w1"}
	lead := Agent{Name: "lead", Kind: "claude", Address: "lead"}
	for _, a := range []Agent{w1, lead} {
		if _, _, err := Join(home, a); err != nil {
			t.Fatal(err)
		}
	}
	if old, found, err := Leave(home, "w1"); err != nil || !found || old != w1 {
		t.Fatalf("Leave(w1) = %v, %v, %v; want it to remove %v", old, found, err, w1)
	}
	if got, err := Load(home); err != nil || !slices.Equal(got, []Agent{lead}) {
		t.Fatalf("Load after Leave(w1) = %v, %v; want only %v", got, err, lead)
	}
	if _, found, err := Leave(home, "w1"); err != nil || found {
		t.Fatalf("second Leave(w1) found %v, err %v; want not found", found, err)
	}
	if got, err := Load(home); err != nil || !slices.Equal(got, []Agent{lead}) {
		t.Fatalf("Load after a second Leave(w1) = %v, %v; want only %v", got, err, lead)
	}
}
