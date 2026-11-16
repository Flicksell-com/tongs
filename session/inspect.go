package session

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Flicksell-com/tongs/dap"
)

// What the session reads from the stopped program beyond its stack and
// variables - watched expressions, threads, where variables are declared -
// and what it knows of breakpoints beyond where they are.

var (
	// "* thread #1: tid = 0x1, 0x0000000100000518 app`main at main.c:23:32, queue = '…', stop reason = breakpoint 1.1"
	threadRe = regexp.MustCompile("^([* ]) thread #(\\d+): tid = \\S+, (?:0x[0-9a-fA-F]+ )?(.*)$")
	reasonRe = regexp.MustCompile(`, stop reason = (.*)$`)
	// "main.c:16: (point) here = {...}"
	declRe = regexp.MustCompile(`^[^:]+:(\d+): \(.*?\) (\w+) = `)
	// What lldb says is wrong with an expression, without where in it.
	errorRe = regexp.MustCompile(`error: (?:<[^>]*>:\d+:\d+: )?(.+)`)
)

// ---- watches ----

// evaluate works an expression out in a frame.
func evaluate(client *dap.Client, expression, context string, frame int) Var {
	body, err := client.Request("evaluate", map[string]any{"expression": expression, "context": context, "frameId": frame})
	if err != nil {
		why := strings.TrimSpace(err.Error())
		if found := errorRe.FindAllStringSubmatch(why, -1); found != nil {
			why = found[len(found)-1][1]
		}
		return Var{Name: expression, Value: strings.SplitN(why, "\n", 2)[0], Err: true}
	}
	v := Var{Name: expression, Ref: num(body["variablesReference"])}
	v.Value, _ = body["result"].(string)
	v.Type, _ = body["type"].(string)
	return v
}

func (s *Session) watched(client *dap.Client, frame int) []Var {
	s.mu.Lock()
	expressions := append([]string(nil), s.watches...)
	s.mu.Unlock()
	vars := make([]Var, len(expressions))
	for i, expression := range expressions {
		vars[i] = evaluate(client, expression, "watch", frame)
	}
	return vars
}

// SetWatches replaces the expressions watched: each is worked out again at
// every stop, in the frame selected.
func (s *Session) SetWatches(expressions []string) {
	s.mu.Lock()
	s.watches = append([]string(nil), expressions...)
	s.mu.Unlock()
	s.Refresh()
}

// AddWatch watches one more expression. One already watched stays as it is.
func (s *Session) AddWatch(expression string) {
	expression = strings.TrimSpace(expression)
	watches := s.Snapshot().Watches
	for _, have := range watches {
		if have == expression {
			return
		}
	}
	if expression != "" {
		s.SetWatches(append(watches, expression))
	}
}

// RemoveWatch stops watching the expression at index.
func (s *Session) RemoveWatch(index int) {
	watches := s.Snapshot().Watches
	if index >= 0 && index < len(watches) {
		s.SetWatches(append(watches[:index], watches[index+1:]...))
	}
}

// Refresh reads the stopped program again where it stands.
func (s *Session) Refresh() {
	s.talk.Lock()
	s.mu.Lock()
	at := s.shown
	s.mu.Unlock()
	if at != nil {
		s.look(at)
	}
	s.talk.Unlock()
	s.changed()
}

// Value works an expression out in the selected frame, for showing beside
// the source. It is false where there is no value to show.
func (s *Session) Value(expression string) (Var, bool) {
	client, frame := s.current(), s.frameID()
	if client == nil || frame == 0 || expression == "" {
		return Var{}, false
	}
	v := evaluate(client, expression, "hover", frame)
	return v, !v.Err
}

// Chain is the expression that ends at end in text and can be worked out
// without running anything: names joined by `.`, `->`, `::` and `[...]`.
func Chain(text string, end int) string {
	if end < 0 || end > len(text) {
		return ""
	}
	return text[expressionStart(text, end):end]
}

// ---- threads and declarations ----

// threads lists the program's threads. Callers hold talk.
func (s *Session) threads() []Thread {
	var threads []Thread
	for _, line := range strings.Split(s.repl("thread list"), "\n") {
		found := threadRe.FindStringSubmatch(line)
		if found == nil {
			continue
		}
		thread := Thread{Selected: found[1] == "*", Where: found[3]}
		thread.Index, _ = strconv.Atoi(found[2])
		if reason := reasonRe.FindStringSubmatch(thread.Where); reason != nil {
			thread.Reason = reason[1]
		}
		// The module, the queue and the rest say less than they take.
		for _, cut := range []string{", queue = ", ", name = ", ", stop reason = ", ", activity = "} {
			thread.Where, _, _ = strings.Cut(thread.Where, cut)
		}
		if _, function, ok := strings.Cut(thread.Where, "`"); ok {
			thread.Where = function
		}
		// "add(p=0x16fd, n=3) at main.c:7:10": the arguments are in the locals.
		if open, shut := strings.Index(thread.Where, "("), strings.LastIndex(thread.Where, ")"); open >= 0 && shut > open {
			thread.Where = thread.Where[:open] + thread.Where[shut+1:]
		}
		threads = append(threads, thread)
	}
	return threads
}

// declared is the line each variable of the selected frame is declared on.
// Callers hold talk.
func (s *Session) declared() map[string]int {
	lines := map[string]int{}
	for _, line := range strings.Split(s.repl("frame variable --show-declaration --depth 0"), "\n") {
		if found := declRe.FindStringSubmatch(line); found != nil {
			lines[found[2]], _ = strconv.Atoi(found[1])
		}
	}
	return lines
}

// ---- breakpoints ----

// quote writes text as one argument of an lldb command.
func quote(text string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text) + `"`
}

// quiet runs a command of the session's own, saying only what went wrong.
func (s *Session) quiet(command string) bool {
	s.talk.Lock()
	output := s.repl(command)
	s.sync()
	s.talk.Unlock()
	if strings.HasPrefix(output, "error:") {
		s.Note("error", output)
		return false
	}
	s.changed()
	return true
}

// logScript is the breakpoint callback, in lldb's Python, that prints a
// message and has the program run on. An expression in {braces} is replaced
// by its value. The message goes in as its bytes, so that nothing in it needs
// quoting on the way through lldb's command line, and comes back out of a
// saved breakpoint the same way.
func logScript(message string) string {
	codes := make([]string, len(message))
	for i := 0; i < len(message); i++ {
		codes[i] = strconv.Itoa(int(message[i]))
	}
	return "print(__import__('re').sub('[{]([^}]*)[}]', " +
		"lambda m: (lambda v: str(v.GetSummary() or v.GetValue() or v))(frame.EvaluateExpression(m.group(1))), " +
		"bytes([" + strings.Join(codes, ",") + "]).decode())); return False"
}

// Stop ends the program, leaving the debugger with its breakpoints.
func (s *Session) Stop() {
	if state := s.Snapshot().State; state != Running && state != Stopped {
		return
	}
	if s.quiet("process kill") {
		s.Note("note", "The program was stopped. ctrl+r builds and starts it again.")
	}
}
