package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	zone "github.com/lrstanley/bubblezone"

	"github.com/Flicksell-com/tongs/store"
)

// The fields of a target, in the order they are filled in.
var fields = []struct{ key, title, note string }{
	{"dir", "project folder", "the folder to build and run in; empty for this project"},
	{"target", "what to debug", "a Bazel label, or the binary's path   (f2 browses)"},
	{"build", "build command", "run in the folder before each start; not needed for a label"},
	{"args", "arguments", ""},
	{"env", "environment", "KEY=VALUE, space separated"},
	{"name", "name", "how it is listed; the target if left empty"},
}

// form edits one target.
type form struct {
	home    string
	known   []string // the folders other targets are in, to choose from
	editing int      // index in the saved targets, or -1 for a new one
	inputs  []textinput.Model
	at      int
	message string
	bad     bool
}

// newForm edits a saved target, or with none starts a new one in dir.
func newForm(home string, entry *store.Entry, editing int, dir string, known []string) *form {
	f := &form{home: home, editing: editing, known: known}
	values := map[string]string{"dir": store.Tilde(dir)}
	if entry != nil {
		var env []string
		for k, v := range entry.Env {
			env = append(env, k+"="+v)
		}
		values = map[string]string{
			"dir": store.Tilde(entry.Dir), "target": entry.Target, "build": entry.Build,
			"args": shellJoin(entry.Args), "env": shellJoin(env), "name": entry.Name,
		}
		if entry.Name == entry.Target {
			values["name"] = ""
		}
	}
	for _, field := range fields {
		input := textinput.New()
		input.Prompt = ""
		input.Placeholder = field.note
		input.PlaceholderStyle = styDim
		input.SetValue(values[field.key])
		f.inputs = append(f.inputs, input)
	}
	f.inputs[0].Focus()
	if entry == nil {
		f.folderChanged()
	}
	return f
}

func (f *form) value(key string) string {
	for i, field := range fields {
		if field.key == key {
			return strings.TrimSpace(f.inputs[i].Value())
		}
	}
	return ""
}

func (f *form) set(key, value string) {
	for i, field := range fields {
		if field.key == key {
			f.inputs[i].SetValue(value)
			f.inputs[i].CursorEnd()
		}
	}
}

func (f *form) folder() string {
	return store.Entry{Dir: f.value("dir")}.Project(f.home)
}

func (f *form) focus(at int) {
	if fields[f.at].key == "dir" && at != f.at {
		f.folderChanged()
	}
	f.inputs[f.at].Blur()
	f.at = (at + len(fields)) % len(fields)
	f.inputs[f.at].Focus()
}

var cargoName = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)

// projectKind is "bazel", "cargo", "cmake", "make", or "".
func projectKind(folder string) string {
	for _, mark := range []struct{ file, kind string }{
		{"MODULE.bazel", "bazel"}, {"WORKSPACE", "bazel"}, {"WORKSPACE.bazel", "bazel"},
		{"Cargo.toml", "cargo"}, {"CMakeLists.txt", "cmake"}, {"Makefile", "make"},
	} {
		if info, err := os.Stat(filepath.Join(folder, mark.file)); err == nil && !info.IsDir() {
			return mark.kind
		}
	}
	return ""
}

// folderChanged offers what is known about the folder's kind, in fields
// still empty. Guesses, shown to be corrected.
func (f *form) folderChanged() {
	folder := f.folder()
	f.bad = false
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		f.message, f.bad = folder+" is not a folder", true
		return
	}
	build, binary := "", ""
	kind := projectKind(folder)
	switch kind {
	case "bazel":
		f.message = "Bazel workspace: give a label, such as //package:binary. It is built with -c dbg."
		return
	case "cargo":
		name := filepath.Base(folder)
		if raw, err := os.ReadFile(filepath.Join(folder, "Cargo.toml")); err == nil {
			if m := cargoName.FindSubmatch(raw); m != nil {
				name = string(m[1])
			}
		}
		build, binary = "cargo build", "target/debug/"+name
	case "cmake":
		build = "cmake -S . -B build -DCMAKE_BUILD_TYPE=Debug && cmake --build build"
		binary = "build/" + filepath.Base(folder)
	case "make":
		build = "make"
	default:
		f.message = ""
		return
	}
	filled := false
	if build != "" && f.value("build") == "" {
		f.set("build", build)
		filled = true
	}
	if binary != "" && f.value("target") == "" {
		f.set("target", binary)
		filled = true
	}
	f.message = kind + " project."
	if filled {
		f.message = kind + " project: filled in a build command and binary to check."
	}
}

// entry reads the fields into a target, or says what is wrong with them.
func (f *form) entry(taken map[string]bool) (store.Entry, error) {
	args, ok := shellSplit(f.value("args"))
	pairs, ok2 := shellSplit(f.value("env"))
	if !ok || !ok2 {
		return store.Entry{}, errors.New("a quote is left open")
	}
	env := map[string]string{}
	for _, pair := range pairs {
		key, value, found := strings.Cut(pair, "=")
		if !found || key == "" {
			return store.Entry{}, fmt.Errorf("environment: %s is not KEY=VALUE", pair)
		}
		env[key] = value
	}
	if info, err := os.Stat(f.folder()); err != nil || !info.IsDir() {
		return store.Entry{}, fmt.Errorf("%s is not a folder", f.folder())
	}
	target := f.value("target")
	if target == "" {
		return store.Entry{}, errors.New("say what to debug")
	}
	name := f.value("name")
	if name == "" {
		name = target
	}
	if taken[name] {
		return store.Entry{}, fmt.Errorf("there is already a target named %s", name)
	}
	if args == nil {
		args = []string{}
	}
	return store.Entry{Name: name, Target: target, Args: args, Env: env, Build: f.value("build"), Dir: f.folder()}, nil
}

func (f *form) view(width, height int) string {
	title := "New target"
	if f.editing >= 0 {
		title = "Edit target"
	}
	lines := []string{styTitle.Render(title), ""}
	for i, field := range fields {
		label := stySub.Render(fmt.Sprintf("%-16s", field.title))
		if i == f.at {
			label = styAccent.Bold(true).Render(fmt.Sprintf("%-16s", field.title))
		}
		f.inputs[i].Width = max(width-36, 10)
		row := label + f.inputs[i].View()
		if field.key == "dir" || field.key == "target" {
			row = fit(row, width-14) + zone.Mark("browse:"+field.key, styTool.Render("browse…"))
		}
		lines = append(lines, zone.Mark(fmt.Sprintf("field:%d", i), row))
		// The folders already in use, to pick with a click or f3.
		if field.key == "dir" && i == f.at && len(f.known) > 0 {
			for at, dir := range f.known {
				chip := styDim.Render("  " + store.Tilde(dir))
				if dir == f.folder() {
					chip = stySub.Render("● " + store.Tilde(dir))
				}
				lines = append(lines, repeat(" ", 16)+zone.Mark(fmt.Sprintf("known:%d", at), chip))
			}
		}
		lines = append(lines, "")
	}
	message := styDim.Render(f.message)
	if f.bad {
		message = styError.Render(f.message)
	}
	lines = append(lines, message)
	lines = append(lines, "", styDim.Render(truncate("tab next field   ctrl+s saves   f2 file dialog   f3 next known folder   esc cancels", width-4)))
	return pane("", joinLines(lines), width, min(len(lines)+2, height), true)
}

// nextKnown puts the next of the folders already in use into the field.
func (f *form) nextKnown() {
	if len(f.known) == 0 {
		return
	}
	next := 0
	for at, dir := range f.known {
		if dir == f.folder() {
			next = (at + 1) % len(f.known)
		}
	}
	f.set("dir", store.Tilde(f.known[next]))
	f.folderChanged()
}

type pickedMsg struct {
	field string
	path  string
	err   error
}

const appleScript = `
set startFolder to POSIX file "%s"
try
    tell application (path to frontmost application as text)
        set chosen to choose %s with prompt "%s" default location startFolder
    end tell
on error number code
    if code is -128 then error number -128
    set chosen to choose %s with prompt "%s" default location startFolder
end try
POSIX path of chosen
`

// pick asks for a folder, or a file, with the system's own dialog. It gives
// an empty path when cancelled, and an error where there is no dialog.
func pick(field, start string, files bool) tea.Cmd {
	return func() tea.Msg {
		prompt, what := "Choose the project folder", "folder"
		if files {
			prompt, what = "Choose the program to debug", "file"
		}
		var cmd *exec.Cmd
		switch {
		case runtime.GOOS == "darwin":
			// Shown by the terminal app itself, so that it opens in front.
			quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(start)
			cmd = exec.Command("osascript", "-e", fmt.Sprintf(appleScript, quoted, what, prompt, what, prompt))
		case has("zenity"):
			args := []string{"--file-selection", "--title=" + prompt, "--filename=" + start + "/"}
			if !files {
				args = append(args, "--directory")
			}
			cmd = exec.Command("zenity", args...)
		case has("kdialog"):
			mode := "--getexistingdirectory"
			if files {
				mode = "--getopenfilename"
			}
			cmd = exec.Command("kdialog", mode, start)
		default:
			return pickedMsg{field: field, err: errors.New("no file dialog on this system; type the path")}
		}
		out, err := cmd.Output()
		chosen := strings.TrimSpace(string(out))
		if err != nil || chosen == "" {
			var exit *exec.ExitError
			if runtime.GOOS == "darwin" && errors.As(err, &exit) && !strings.Contains(string(exit.Stderr), "-128") {
				return pickedMsg{field: field, err: errors.New("the file dialog could not open; type the path")}
			}
			return pickedMsg{field: field}
		}
		return pickedMsg{field: field, path: strings.TrimRight(chosen, "/")}
	}
}

func has(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}
