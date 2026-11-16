package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Tab is a source file left open, and where the cursor was in it.
type Tab struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

// State is how a target's screen was left: what the next start of the same
// target comes back to.
type State struct {
	Tabs    []Tab    `json:"tabs"`
	Tab     int      `json:"tab"`     // the one in front
	Open    []string `json:"open"`    // the variables left opened out
	Watches []string `json:"watches"` // the expressions watched
	History []string `json:"history"` // the commands typed, oldest first
	Side    string   `json:"side"`    // the tab of the side pane in front
}

// Layout is the sizes the panes were dragged to, 0 for the usual. They are
// the same whatever is being debugged.
type Layout struct {
	Side  int `json:"side"`
	Logs  int `json:"logs"`
	Stack int `json:"stack"`
	Split int `json:"split"`
}

// LoadState is how the target was left; nothing, the first time.
func (e Entry) LoadState(home string) State {
	var state State
	read(e.kept(home, "state"), &state)
	return state
}

// SaveState remembers how the target is being left.
func (e Entry) SaveState(home string, state State) error {
	return write(e.kept(home, "state"), state)
}

func layoutPath() string { return filepath.Join(filepath.Dir(Path()), "layout.json") }

func LoadLayout() Layout {
	var layout Layout
	read(layoutPath(), &layout)
	return layout
}

func SaveLayout(layout Layout) error { return write(layoutPath(), layout) }

// read fills into from a file. A missing or broken one leaves it empty:
// nothing here is worth refusing to start over.
func read(path string, into any) {
	if raw, err := os.ReadFile(path); err == nil {
		json.Unmarshal(raw, into)
	}
}

func write(path string, from any) error {
	raw, err := json.MarshalIndent(from, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
