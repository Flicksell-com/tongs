// Package agent lets another program drive the session: a coding agent, a
// script. One line in, one plain-text report out, over a Unix socket:
//
//	echo 'b main' | nc -U /tmp/tongs-501.sock
//
// The line is an lldb command, or one of the words in Help. Everything it
// does goes through the same session the screen shows, so the person there
// sees it happen and can take over at any point.
package agent

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Flicksell-com/tongs/session"
	"github.com/Flicksell-com/tongs/store"
)

const (
	// How long a command that set the program running waits for it to stop
	// again before answering "running", and how long a build may take.
	stepWait  = 15 * time.Second
	buildWait = 15 * time.Minute

	sourceLines = 3 // shown either side of the current line
	valueWidth  = 200
	outputLines = 40
)

// Help is what an agent is told it can send.
const Help = `Each line is an lldb command (b, c, n, s, finish, p, bt, up, frame variable ...)
or one of:
  status              where the program is: source, locals, stack
  wait [seconds]      wait for a running program to stop, then status
  targets             the saved targets
  start <target>      debug a saved name, a Bazel label or a binary; then --
                      and arguments for it. Stops at entry: set breakpoints, c
  restart             rebuild and start again, breakpoints kept
A command that moves the program answers with the new status. Any other
answers with what lldb printed.`

// SocketPath is where a session listens: one per user.
func SocketPath() string {
	return fmt.Sprintf("/tmp/tongs-%d.sock", os.Getuid())
}

// Server answers agents on the socket.
type Server struct {
	session  *session.Session
	entries  func() []store.Entry
	listener net.Listener
	seen     int // program output lines already reported
}

// Listen starts answering. entries gives the saved targets as they now are.
func Listen(s *session.Session, entries func() []store.Entry) (*Server, error) {
	path := SocketPath()
	os.Remove(path) // Left by a run that was killed; the newest run owns it.
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// Anything that can write here can run commands as you, so only you can.
	os.Chmod(path, 0o600)
	server := &Server{session: s, entries: entries, listener: listener}
	go server.accept()
	return server, nil
}

// Close stops answering.
func (a *Server) Close() {
	a.listener.Close()
	os.Remove(SocketPath())
}

func (a *Server) accept() {
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			line, _ := bufio.NewReader(conn).ReadString('\n')
			fmt.Fprintln(conn, strings.TrimRight(a.answer(strings.TrimSpace(line)), "\n"))
		}()
	}
}

func (a *Server) answer(line string) string {
	s := a.session
	word, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	switch {
	case line == "" || word == "help":
		return Help
	case word == "status":
		return a.status()
	case word == "wait":
		wait := stepWait
		if seconds, err := strconv.ParseFloat(rest, 64); err == nil {
			wait = time.Duration(seconds * float64(time.Second))
		}
		s.Settle(wait)
		return a.status()
	case word == "targets":
		var lines []string
		for _, e := range a.entries() {
			lines = append(lines, e.Name+"  "+e.Target+"  in "+store.Tilde(e.Dir))
		}
		if len(lines) == 0 {
			return "no saved targets"
		}
		return strings.Join(lines, "\n")
	case word == "start":
		parts := strings.Fields(rest)
		if len(parts) == 0 {
			return "error: start needs a target"
		}
		var args []string
		for i, part := range parts {
			if part == "--" {
				args = parts[i+1:]
				break
			}
		}
		entry := store.Entry{Name: parts[0], Target: parts[0], Args: args}
		// A saved name means the one in this project before another's.
		found := false
		for _, saved := range a.entries() {
			if saved.Name == parts[0] && (!found || saved.Dir == s.Home) {
				entry, found = saved, true
				if args != nil {
					entry.Args = args
				}
			}
		}
		s.Start(entry, false)
		s.Settle(buildWait)
		return a.status()
	}
	snap := s.Snapshot()
	switch {
	case snap.Entry == nil:
		return "error: nothing is being debugged. `targets` lists what there is; `start <target>` begins."
	case word == "restart":
		s.Restart()
		s.Settle(buildWait)
		return a.status()
	case line == "q" || line == "quit" || line == "exit":
		return "error: the session belongs to the person at the screen; it is not yours to end"
	}
	output, moved := s.Execute(line, "agent")
	if !moved {
		if output == "" {
			return a.stateLine()
		}
		return output + "\n" + a.stateLine()
	}
	s.Settle(stepWait)
	return a.status()
}

func (a *Server) stateLine() string {
	snap := a.session.Snapshot()
	if snap.Detail != "" {
		return fmt.Sprintf("state: %s (%s)", snap.State, snap.Detail)
	}
	return "state: " + snap.State
}

func (a *Server) status() string {
	a.session.Sync()
	snap := a.session.Snapshot()
	lines := []string{a.stateLine()}
	if snap.State == session.Running {
		lines = append(lines, "`wait` waits for it to stop; `process interrupt` stops it now.")
	}
	if view := snap.View; view != nil {
		frame := view.Frames[view.Index]
		name := strings.SplitN(frame.Name, "(", 2)[0]
		if frame.Path != "" {
			lines = append(lines, fmt.Sprintf("at %s:%d in %s  (frame #%d)", Rel(snap.Root, frame.Path), frame.Line, name, view.Index))
			if snap.Newer[frame.Path] {
				lines = append(lines, "note: this file was edited after the program was built; lines are the file's as it is now. `restart` rebuilds.")
			}
			if raw, err := os.ReadFile(frame.Path); err == nil {
				text := strings.Split(string(raw), "\n")
				for n := max(frame.Line-sourceLines, 1); n <= min(frame.Line+sourceLines, len(text)); n++ {
					mark := "  "
					if n == frame.Line {
						mark = "->"
					}
					lines = append(lines, fmt.Sprintf("%s %5d %s", mark, n, text[n-1]))
				}
			}
		} else {
			lines = append(lines, fmt.Sprintf("in %s, which has no source  (frame #%d)", name, view.Index))
		}
		if len(view.Locals) > 0 {
			lines = append(lines, "locals:")
			for _, v := range view.Locals {
				value := v.Value
				if len(value) > valueWidth {
					value = value[:valueWidth] + "..."
				}
				lines = append(lines, fmt.Sprintf("  %s = %s", v.Name, value))
			}
		}
		if len(view.Watches) > 0 {
			lines = append(lines, "watched:")
			for _, v := range view.Watches {
				lines = append(lines, fmt.Sprintf("  %s = %s", v.Name, v.Value))
			}
		}
		lines = append(lines, "stack:")
		for i, f := range view.Frames {
			at := ""
			if f.Path != "" {
				at = fmt.Sprintf("  %s:%d", filepath.Base(f.Path), f.Line)
			}
			lines = append(lines, fmt.Sprintf("  #%d %s%s", i, strings.SplitN(f.Name, "(", 2)[0], at))
		}
	}
	fresh := a.session.Program(a.seen)
	a.seen += len(fresh)
	if len(fresh) > 0 {
		lines = append(lines, "program output since you last looked:")
		if len(fresh) > outputLines {
			lines = append(lines, fmt.Sprintf("  (%d earlier lines left out)", len(fresh)-outputLines))
			fresh = fresh[len(fresh)-outputLines:]
		}
		for _, text := range fresh {
			lines = append(lines, "  "+text)
		}
	}
	return strings.Join(lines, "\n")
}

// Rel is path as shown to a reader: relative to root when inside it.
func Rel(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
