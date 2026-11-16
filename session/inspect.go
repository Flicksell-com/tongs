package session

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
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
	// The message inside the script logScript writes.
	loggedRe = regexp.MustCompile(`bytes\(\[([\d,]*)\]\)\.decode\(\)\)\); return False$`)
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

// detail is how a breakpoint is set, beyond where.
type detail struct {
	on        bool
	condition string
	hits      int
}

// Asks lldb's own scripting for what `breakpoint list` only prints as prose.
const breakScript = `script import json; print(json.dumps([[b.GetID(), b.IsEnabled(), b.GetHitCount(), b.GetCondition() or ""] for b in lldb.target.breakpoint_iter()]))`

// breakDetails reads how every breakpoint is set, and returns their ids in
// order. Callers hold talk.
func (s *Session) breakDetails() []int {
	var rows [][]any
	if json.Unmarshal([]byte(s.repl(breakScript)), &rows) != nil {
		return nil
	}
	details := map[int]detail{}
	var ids []int
	for _, row := range rows {
		if len(row) != 4 {
			continue
		}
		d := detail{hits: num(row[2])}
		d.on, _ = row[1].(bool)
		d.condition, _ = row[3].(string)
		details[num(row[0])] = d
		ids = append(ids, num(row[0]))
	}
	sort.Ints(ids)
	s.mu.Lock()
	same := reflect.DeepEqual(details, s.details)
	s.details = details
	for id := range s.logs {
		if _, has := details[id]; !has {
			delete(s.logs, id)
		}
	}
	s.mu.Unlock()
	if !same {
		s.changed()
	}
	return ids
}

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

// SetEnabled turns a breakpoint on or off, leaving it where it is.
func (s *Session) SetEnabled(id int, on bool) {
	verb := map[bool]string{true: "enable", false: "disable"}[on]
	s.quiet(fmt.Sprintf("breakpoint %s %d", verb, id))
}

// SetCondition has a breakpoint stop only when an expression holds; "" has it
// stop every time.
func (s *Session) SetCondition(id int, condition string) {
	condition = strings.TrimSpace(condition)
	if !s.quiet(fmt.Sprintf("breakpoint modify --condition %s %d", quote(condition), id)) {
		return
	}
	if condition == "" {
		s.Note("note", fmt.Sprintf("Breakpoint %d stops every time.", id))
	} else {
		s.Note("note", fmt.Sprintf("Breakpoint %d stops when %s.", id, condition))
	}
}

// logScript is the breakpoint callback, in lldb's Python, that prints a
// message and has the program run on. An expression in {braces} is replaced
// by its value. The message goes in as its bytes, so that nothing in it needs
// quoting on the way through lldb's command line, and comes back out of a
// saved breakpoint the same way (see keptLogs).
func logScript(message string) string {
	codes := make([]string, len(message))
	for i := 0; i < len(message); i++ {
		codes[i] = strconv.Itoa(int(message[i]))
	}
	return "print(__import__('re').sub('[{]([^}]*)[}]', " +
		"lambda m: (lambda v: str(v.GetSummary() or v.GetValue() or v))(frame.EvaluateExpression(m.group(1))), " +
		"bytes([" + strings.Join(codes, ",") + "]).decode())); return False"
}

// SetLog has a breakpoint print a message and run on rather than stop; ""
// has it stop again.
func (s *Session) SetLog(id int, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		s.mu.Lock()
		_, had := s.logs[id]
		delete(s.logs, id)
		s.mu.Unlock()
		if had && s.quiet(fmt.Sprintf("breakpoint command delete %d", id)) {
			s.Note("note", fmt.Sprintf("Breakpoint %d stops again.", id))
		}
		return
	}
	if !s.quiet(fmt.Sprintf(`breakpoint command add --script-type python %d --one-liner "%s"`, id, logScript(message))) {
		return
	}
	s.mu.Lock()
	s.logs[id] = message
	s.mu.Unlock()
	s.Note("note", fmt.Sprintf("Breakpoint %d prints instead of stopping: %s", id, message))
	s.changed()
}

// keptLogs finds, among the breakpoints just read back from a file lldb
// wrote, the ones that print instead of stopping, and remembers what they
// print. lldb reads them back in the order it wrote them. Callers hold talk.
func (s *Session) keptLogs(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var points []struct {
		Breakpoint struct {
			BKPTOptions struct {
				BKPTCMDData struct {
					UserSource []string
				}
			}
		}
	}
	ids := s.breakDetails()
	if json.Unmarshal(raw, &points) != nil || len(points) != len(ids) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, point := range points {
		for _, line := range point.Breakpoint.BKPTOptions.BKPTCMDData.UserSource {
			found := loggedRe.FindStringSubmatch(line)
			if found == nil {
				continue
			}
			var message []byte
			for _, code := range strings.Split(found[1], ",") {
				if b, err := strconv.Atoi(code); err == nil {
					message = append(message, byte(b))
				}
			}
			s.logs[ids[i]] = string(message)
		}
	}
}

// BreakOn is the breakpoint on a line of a file as it is on disk, if any.
func (s *Session) BreakOn(path string, line int) (int, bool) {
	found, has := 0, false
	for id, point := range s.Snapshot().Breaks {
		if point.Path == path && point.Line == line && (!has || id < found) {
			found, has = id, true
		}
	}
	return found, has
}
