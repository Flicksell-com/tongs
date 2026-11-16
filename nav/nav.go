// Package nav answers questions about the code itself: where a name is
// defined, where it is used, what its type is. It asks clangd, the C++
// language server, which reads the code the way the compiler does; nothing
// here matches names as text.
package nav

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How long one question may take: the first one waits for the file to be parsed.
const answerWait = 30 * time.Second

// Place is a position in a source file. Line and Col count from 1.
type Place struct {
	Path      string
	Line, Col int
}

// Nav is one clangd, started for one project.
type Nav struct {
	mu      sync.Mutex
	ready   chan struct{} // closed when clangd is up, or has failed to be
	err     error
	cmd     *exec.Cmd
	in      io.WriteCloser
	seq     int
	pending map[int]chan json.RawMessage
	opened  map[string]bool
}

// Start brings clangd up for the project at root, in the background. label is
// the Bazel target being debugged, whose compile commands are asked of Bazel;
// with no label the project's own compile_commands.json is used.
func Start(root, label string) *Nav {
	n := &Nav{ready: make(chan struct{}), pending: map[int]chan json.RawMessage{}, opened: map[string]bool{}}
	go func() {
		n.err = n.start(root, label)
		close(n.ready)
	}()
	return n
}

func (n *Nav) start(root, label string) error {
	clangd, err := exec.LookPath("clangd")
	if err != nil {
		return errors.New("clangd is not installed, so there is no go-to-definition")
	}
	dir, err := commandsDir(root, label)
	if err != nil {
		return err
	}
	cmd := exec.Command(clangd, "--compile-commands-dir="+dir, "--background-index", "--log=error", "--header-insertion=never")
	cmd.Dir = root
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	n.cmd, n.in = cmd, in
	go n.read(bufio.NewReader(out))
	if _, err := n.request("initialize", map[string]any{
		"processId": os.Getpid(), "rootUri": uri(root), "capabilities": map[string]any{},
	}); err != nil {
		return err
	}
	return n.notify("initialized", map[string]any{})
}

// commandsDir is a folder holding a compile_commands.json for the project.
func commandsDir(root, label string) (string, error) {
	if label == "" {
		for _, dir := range []string{root, filepath.Join(root, "build")} {
			if _, err := os.Stat(filepath.Join(dir, "compile_commands.json")); err == nil {
				return dir, nil
			}
		}
		return "", errors.New("no compile_commands.json in the project, so there is no go-to-definition")
	}
	// Ask Bazel how it compiles everything the target is built from.
	cmd := exec.Command("bazelisk", "aquery", "-c", "dbg", "--output=jsonproto",
		fmt.Sprintf(`mnemonic("CppCompile", deps(%s))`, label))
	cmd.Dir = root
	raw, err := cmd.Output()
	if err != nil {
		return "", errors.New("could not ask Bazel how the target is compiled")
	}
	var answer struct {
		Actions []struct {
			Arguments []string `json:"arguments"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", err
	}
	type command struct {
		Directory string   `json:"directory"`
		File      string   `json:"file"`
		Arguments []string `json:"arguments"`
	}
	var commands []command
	for _, action := range answer.Actions {
		for i, arg := range action.Arguments {
			if arg == "-c" && i+1 < len(action.Arguments) {
				// The paths are from the workspace top, where bazel-out and
				// external are links into Bazel's own tree.
				commands = append(commands, command{root, action.Arguments[i+1], action.Arguments})
			}
		}
	}
	if len(commands) == 0 {
		return "", errors.New("Bazel reported no C++ compiles for the target")
	}
	sum := sha1.Sum([]byte(root + "\x00" + label))
	cache, err := os.UserHomeDir()
	if err != nil {
		cache = os.TempDir()
	}
	dir := filepath.Join(cache, ".cache", "tongs", "clangd", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out, _ := json.Marshal(commands)
	return dir, os.WriteFile(filepath.Join(dir, "compile_commands.json"), out, 0o644)
}

func uri(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func (n *Nav) send(message map[string]any) error {
	message["jsonrpc"] = "2.0"
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	_, err = fmt.Fprintf(n.in, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
	return err
}

func (n *Nav) notify(method string, params any) error {
	return n.send(map[string]any{"method": method, "params": params})
}

func (n *Nav) request(method string, params any) (json.RawMessage, error) {
	n.mu.Lock()
	n.seq++
	seq := n.seq
	answer := make(chan json.RawMessage, 1)
	n.pending[seq] = answer
	n.mu.Unlock()
	if err := n.send(map[string]any{"id": seq, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case result, ok := <-answer:
		if !ok {
			return nil, errors.New("clangd exited")
		}
		return result, nil
	case <-time.After(answerWait):
		return nil, errors.New("clangd took too long")
	}
}

func (n *Nav) read(reader *bufio.Reader) {
	defer func() {
		n.mu.Lock()
		for seq, answer := range n.pending {
			close(answer)
			delete(n.pending, seq)
		}
		n.mu.Unlock()
	}()
	for {
		length := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(name, "content-length") {
				length, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		raw := make([]byte, length)
		if _, err := io.ReadFull(reader, raw); err != nil {
			return
		}
		var message struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &message) != nil || message.ID == nil || message.Method != "" {
			continue // A notification, or clangd asking us something.
		}
		n.mu.Lock()
		answer := n.pending[*message.ID]
		delete(n.pending, *message.ID)
		n.mu.Unlock()
		if answer != nil {
			answer <- message.Result
		}
	}
}

// ask opens the file with clangd if it hasn't been, then puts the question.
func (n *Nav) ask(method string, at Place, extra map[string]any) (json.RawMessage, error) {
	<-n.ready
	if n.err != nil {
		return nil, n.err
	}
	n.mu.Lock()
	opened := n.opened[at.Path]
	n.opened[at.Path] = true
	n.mu.Unlock()
	if !opened {
		text, err := os.ReadFile(at.Path)
		if err != nil {
			return nil, err
		}
		language := "cpp"
		if strings.HasSuffix(at.Path, ".c") {
			language = "c"
		}
		if err := n.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": uri(at.Path), "languageId": language, "version": 1, "text": string(text),
		}}); err != nil {
			return nil, err
		}
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri(at.Path)},
		"position":     map[string]any{"line": at.Line - 1, "character": at.Col - 1},
	}
	for key, value := range extra {
		params[key] = value
	}
	return n.request(method, params)
}

type location struct {
	URI       string `json:"uri"`
	TargetURI string `json:"targetUri"`
	Range     *span  `json:"range"`
	Target    *span  `json:"targetSelectionRange"`
}

type span struct {
	Start struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"start"`
}

func places(raw json.RawMessage) []Place {
	var many []location
	if json.Unmarshal(raw, &many) != nil {
		var one location
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		many = []location{one}
	}
	var found []Place
	for _, l := range many {
		at, where := l.URI, l.Range
		if at == "" {
			at, where = l.TargetURI, l.Target
		}
		parsed, err := url.Parse(at)
		if err != nil || where == nil || parsed.Path == "" {
			continue
		}
		found = append(found, Place{parsed.Path, where.Start.Line + 1, where.Start.Character + 1})
	}
	return found
}

// Definition is where the name at a position is defined.
func (n *Nav) Definition(at Place) ([]Place, error) {
	raw, err := n.ask("textDocument/definition", at, nil)
	if err != nil {
		return nil, err
	}
	return places(raw), nil
}

// References is everywhere the name at a position is used.
func (n *Nav) References(at Place) ([]Place, error) {
	raw, err := n.ask("textDocument/references", at, map[string]any{"context": map[string]any{"includeDeclaration": true}})
	if err != nil {
		return nil, err
	}
	return places(raw), nil
}

// Hover is what clangd says about the name at a position: its type, its
// declaration, its comment.
func (n *Nav) Hover(at Place) (string, error) {
	raw, err := n.ask("textDocument/hover", at, nil)
	if err != nil {
		return "", err
	}
	var hover struct {
		Contents struct {
			Value string `json:"value"`
		} `json:"contents"`
	}
	json.Unmarshal(raw, &hover)
	return strings.TrimSpace(hover.Contents.Value), nil
}

// Close stops clangd.
func (n *Nav) Close() {
	go func() {
		<-n.ready
		if n.cmd != nil {
			n.in.Close()
			n.cmd.Process.Kill()
			n.cmd.Wait()
		}
	}()
}
