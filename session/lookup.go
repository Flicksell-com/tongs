package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Candidate is one completion: where it starts in the line, what goes there,
// and how it is listed.
type Candidate struct {
	Start      int
	Put        string
	Name, Type string
}

// Function is a function of the program.
type Function struct {
	Name, File string
	Line       int
}

// Commands whose arguments name variables, for completing those names.
var expressionCommands = map[string]bool{
	"p": true, "po": true, "print": true, "expr": true, "expression": true, "call": true,
	"dwim-print": true, "parray": true, "poarray": true, "v": true, "var": true, "vo": true,
	"frame": true, "display": true, "watchpoint": true, "x": true, "memory": true,
}

var (
	nameRe     = regexp.MustCompile(`[A-Za-z_]\w*$`)
	pointerRe  = regexp.MustCompile(`\*\s*(const)?\s*$`)
	functionRe = regexp.MustCompile("(?m)Summary: [^`]*`(.+?)(?: at (\\S+):(\\d+))?$")
)

// expressionStart is where the member-access chain ending at end begins:
// names, `.`, `->`, `::` and `[...]`. It stops at anything else, so a call is
// never part of it.
func expressionStart(text string, end int) int {
	i := end
	for i > 0 {
		c := text[i-1]
		switch {
		case c == '_' || c == '.' || c == ':' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			i--
		case c == '>' && i >= 2 && text[i-2] == '-':
			i -= 2
		case c == ']':
			depth := 0
			for i > 0 {
				i--
				if text[i] == ']' {
					depth++
				} else if text[i] == '[' {
					depth--
				}
				if depth == 0 {
					break
				}
			}
			if depth != 0 {
				return end
			}
		default:
			return i
		}
	}
	return i
}

func (s *Session) frameID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.view == nil || s.state != Stopped {
		return 0
	}
	return s.view.Frames[s.view.Index].ID
}

// Complete offers completions for text, the line left of the cursor.
func (s *Session) Complete(text string) []Candidate {
	client := s.current()
	if client == nil || text == "" {
		return nil
	}
	prefix := nameRe.FindString(text)
	at := len(text) - len(prefix)
	frame := s.frameID()

	access := ""
	for _, a := range []string{"->", "."} {
		if strings.HasSuffix(text[:at], a) {
			access = a
			break
		}
	}
	if access != "" {
		// lldb doesn't complete members. Ask it for the object instead and
		// offer its fields, with the accessor its type calls for.
		end := at - len(access)
		base := text[expressionStart(text, end):end]
		if base == "" || frame == 0 {
			return nil
		}
		value, err := client.Request("evaluate", map[string]any{"expression": base, "context": "watch", "frameId": frame})
		if err != nil || num(value["variablesReference"]) == 0 {
			return nil
		}
		kind, _ := value["type"].(string)
		access = "."
		if pointerRe.MatchString(kind) {
			access = "->"
		}
		var found []Candidate
		for _, field := range variables(client, num(value["variablesReference"])) {
			first := field.Name[:min(1, len(field.Name))]
			if strings.HasPrefix(field.Name, prefix) && first != "" && (first == "_" || nameRe.MatchString(first)) {
				found = append(found, Candidate{end, access + field.Name, field.Name, field.Type})
			}
		}
		return found
	}

	request := map[string]any{"text": text, "column": len(text) + 1}
	if frame != 0 {
		request["frameId"] = frame
	}
	var found []Candidate
	have := map[string]bool{}
	if body, err := client.Request("completions", request); err == nil {
		targets, _ := body["targets"].([]any)
		for _, item := range targets {
			t, _ := item.(map[string]any)
			label, _ := t["label"].(string)
			put, _ := t["text"].(string)
			if put == "" {
				put = label
			}
			start := at
			if _, ok := t["start"]; ok {
				start = num(t["start"])
			}
			found = append(found, Candidate{Start: start, Put: put, Name: label})
			have[put] = true
		}
	}
	// In a command that takes an expression, a name can be a local or a
	// member of `this`, which lldb offers only when spelled this->.
	words := strings.Fields(text[:at])
	if prefix != "" && len(words) > 0 && expressionCommands[words[0]] && frame != 0 {
		names := s.locals(client, frame)
		for _, v := range names {
			if v.Name == "this" && v.Ref != 0 {
				names = append(names, variables(client, v.Ref)...)
			}
		}
		for _, v := range names {
			if strings.HasPrefix(v.Name, prefix) && !have[v.Name] {
				found = append(found, Candidate{at, v.Name, v.Name, v.Type})
				have[v.Name] = true
			}
		}
	}
	return found
}

// Files lists the project's files the program being debugged was built from,
// by its debug info; every file in the project when no program is loaded.
func (s *Session) Files() []string {
	s.mu.Lock()
	files, root, ready := s.files, s.root, s.stoppedOnce && s.client != nil
	s.mu.Unlock()
	if files != nil {
		return files
	}
	if ready {
		files = s.programFiles(root)
	}
	if len(files) == 0 {
		files = projectFiles(root)
	}
	s.mu.Lock()
	s.files = files
	s.mu.Unlock()
	return files
}

func (s *Session) programFiles(root string) []string {
	// What a script prints doesn't come back through the adapter, so it
	// writes its answer to a file.
	tmp, err := os.CreateTemp("", "tongs-files-")
	if err != nil {
		return nil
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	s.talk.Lock()
	s.repl(fmt.Sprintf("script t = lldb.target; open(%q, 'w').write(chr(10).join({str(u.GetSupportFileAtIndex(i))"+
		" for m in t.module_iter() for u in m.compile_unit_iter() for i in range(u.GetNumSupportFiles())}))", tmp.Name()))
	s.talk.Unlock()
	listed, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		real = root
	}
	seen := map[string]bool{}
	var found []string
	for _, line := range strings.Split(string(listed), "\n") {
		if line == "" {
			continue
		}
		// Bazel records paths from the workspace top; others record them
		// whole. Resolved, so a link out to a toolchain or SDK doesn't count.
		path := line
		if !filepath.IsAbs(path) {
			path = filepath.Join(real, path)
		}
		path, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(real, path)
		if err != nil || strings.HasPrefix(rel, "..") || seen[rel] {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			seen[rel] = true
			found = append(found, rel)
		}
	}
	sort.Strings(found)
	return found
}

// projectFiles is every file git knows about under root, tracked or not yet,
// minus the ignored.
func projectFiles(root string) []string {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	}
	// Not a git checkout: walk it, leaving out hidden and Bazel output folders.
	var found []string
	filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := entry.Name()
		if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "bazel-")) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel)
		}
		return nil
	})
	return found
}

// Functions finds the program's functions whose names contain query.
func (s *Session) Functions(query string) []Function {
	s.mu.Lock()
	binary := s.binary
	s.mu.Unlock()
	if len(query) < 2 || binary == "" || s.current() == nil {
		return nil
	}
	s.talk.Lock()
	out := s.repl(fmt.Sprintf(`image lookup -r -n "%s" "%s"`, regexp.QuoteMeta(query), filepath.Base(binary)))
	s.talk.Unlock()
	var found []Function
	for _, m := range functionRe.FindAllStringSubmatch(out, -1) {
		f := Function{Name: strings.SplitN(m[1], "(", 2)[0], File: m[2]}
		fmt.Sscan(m[3], &f.Line)
		found = append(found, f)
	}
	return found
}
