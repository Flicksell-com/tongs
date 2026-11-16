// Package store keeps the saved targets - every project's, in one file in the
// user's config folder - and finds the project tongs was started in.
package store

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Entry is what to debug and how to start it. Target is a Bazel label, or a
// path to a binary; for a path, Build is the shell command that produces it.
// Dir is the folder of the project it belongs to; a saved one's is absolute.
type Entry struct {
	Name   string            `json:"name"`
	Target string            `json:"target"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
	Build  string            `json:"build"`
	Dir    string            `json:"dir"`
}

type file struct {
	Entries []Entry `json:"entries"`
}

// IsLabel says whether a target is a Bazel label, as against a path.
func IsLabel(target string) bool {
	return strings.HasPrefix(target, "//") || strings.HasPrefix(target, "@") || strings.HasPrefix(target, ":")
}

// Project is the directory to build in, run in and show the files of.
func (e Entry) Project(home string) string {
	if e.Dir == "" {
		return home
	}
	dir := expand(e.Dir)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(home, dir)
	}
	return filepath.Clean(dir)
}

// kept is where something of this target's is remembered between runs.
func (e Entry) kept(home, what string) string {
	sum := sha1.Sum([]byte(e.Project(home) + "\x00" + e.Target))
	cache, err := os.UserHomeDir()
	if err != nil {
		cache = os.TempDir()
	}
	return filepath.Join(cache, ".cache", "tongs", what, hex.EncodeToString(sum[:8])+".json")
}

// BreakpointsFile is where this target's breakpoints are kept between runs.
func (e Entry) BreakpointsFile(home string) string { return e.kept(home, "breakpoints") }

func expand(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// Tilde is a path as it is shown: with ~ for the home folder.
func Tilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(filepath.Separator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

// Inside says whether path is dir or somewhere beneath it.
func Inside(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// FindRoot is the project tongs was started in: the directory asked for,
// else the saved targets' project the cwd is inside, else the nearest
// directory above the cwd that is a Bazel workspace or a git repository, else
// the cwd itself.
func FindRoot(dir string, entries []Entry) (string, error) {
	if dir != "" {
		path, err := filepath.Abs(expand(dir))
		if err != nil {
			return "", err
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return "", fmt.Errorf("%s is not a directory", dir)
		}
		return path, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	known := ""
	for _, e := range entries {
		if e.Dir != "" && Inside(cwd, e.Dir) && len(e.Dir) > len(known) {
			known = e.Dir
		}
	}
	if known != "" {
		return known, nil
	}
	for path := cwd; ; {
		for _, mark := range []string{"MODULE.bazel", ".git"} {
			if _, err := os.Stat(filepath.Join(path, mark)); err == nil {
				return path, nil
			}
		}
		parent := filepath.Dir(path)
		if parent == path {
			return cwd, nil
		}
		path = parent
	}
}

// Path is the file the targets are kept in.
func Path() string {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, "tongs", "targets.json")
}

// Load reads the saved targets. A missing file is no targets; a broken one is
// an error, since starting empty would overwrite it on the next save.
func Load() ([]Entry, error) {
	path := Path()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s is not valid: %w", path, err)
	}
	return f.Entries, nil
}

// Save writes the targets, whole or not at all.
func Save(entries []Entry) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	for i := range entries {
		if entries[i].Args == nil {
			entries[i].Args = []string{}
		}
		if entries[i].Env == nil {
			entries[i].Env = map[string]string{}
		}
	}
	raw, err := json.MarshalIndent(file{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".targets-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
