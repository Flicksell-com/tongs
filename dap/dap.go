// Package dap talks to CodeLLDB: the Debug Adapter Protocol over a local socket.
package dap

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How long the adapter gets to start and connect back.
const connectTimeout = 20 * time.Second

// ErrGone is what a request gets when the adapter has exited.
var ErrGone = errors.New("the debugger exited")

// Client is one running adapter.
type Client struct {
	cmd     *exec.Cmd
	conn    net.Conn
	onEvent func(name string, body map[string]any)

	mu      sync.Mutex
	seq     int
	pending map[int]chan response
	closed  bool
}

type response struct {
	body map[string]any
	err  error
}

type message struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	Command    string          `json:"command,omitempty"`
	Arguments  any             `json:"arguments,omitempty"`
	Event      string          `json:"event,omitempty"`
	RequestSeq int             `json:"request_seq,omitempty"`
	Success    bool            `json:"success,omitempty"`
	Message    string          `json:"message,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

// Start runs the adapter in cwd. The program it launches inherits env. Events
// arrive one at a time on a single goroutine, in order; "adapter-exited" is
// the last. A handler must not make a request itself, only hand off.
func Start(adapter, cwd string, env []string, onEvent func(name string, body map[string]any)) (*Client, error) {
	// The adapter has no stdio mode. It connects to us, so there is no
	// waiting for a port of its own to open.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	cmd := exec.Command(adapter, "--connect", strconv.Itoa(port))
	cmd.Dir = cwd
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the debugger did not start: %w", err)
	}
	listener.(*net.TCPListener).SetDeadline(time.Now().Add(connectTimeout))
	conn, err := listener.Accept()
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, errors.New("the debugger did not start")
	}
	c := &Client{cmd: cmd, conn: conn, onEvent: onEvent, pending: map[int]chan response{}}
	go c.read()
	return c, nil
}

// Alive says whether requests can still be answered.
func (c *Client) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}

// Request sends one request and returns its response body.
func (c *Client) Request(command string, arguments any) (map[string]any, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrGone
	}
	c.seq++
	seq := c.seq
	answer := make(chan response, 1)
	c.pending[seq] = answer
	raw, err := json.Marshal(message{Seq: seq, Type: "request", Command: command, Arguments: arguments})
	if err == nil {
		_, err = fmt.Fprintf(c.conn, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
	}
	if err != nil {
		delete(c.pending, seq)
		c.mu.Unlock()
		return nil, ErrGone
	}
	c.mu.Unlock()
	got := <-answer
	return got.body, got.err
}

func (c *Client) read() {
	reader := bufio.NewReader(c.conn)
	for {
		length := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				c.finish()
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
			c.finish()
			return
		}
		var m message
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		body := map[string]any{}
		if len(m.Body) > 0 {
			json.Unmarshal(m.Body, &body)
		}
		switch m.Type {
		case "response":
			c.mu.Lock()
			answer := c.pending[m.RequestSeq]
			delete(c.pending, m.RequestSeq)
			c.mu.Unlock()
			if answer == nil {
				continue
			}
			if m.Success {
				answer <- response{body: body}
			} else {
				answer <- response{err: errors.New(failure(m, body))}
			}
		case "event":
			c.onEvent(m.Event, body)
		}
	}
}

func failure(m message, body map[string]any) string {
	if detail, ok := body["error"].(map[string]any); ok {
		if text, ok := detail["format"].(string); ok && text != "" {
			return text
		}
	}
	if m.Message != "" {
		return m.Message
	}
	return "request failed"
}

func (c *Client) finish() {
	c.mu.Lock()
	c.closed = true
	for seq, answer := range c.pending {
		answer <- response{err: ErrGone}
		delete(c.pending, seq)
	}
	c.mu.Unlock()
	c.onEvent("adapter-exited", nil)
}

// Close ends the session and the program with it, politely and then not.
func (c *Client) Close() {
	done := make(chan struct{})
	go func() {
		c.Request("disconnect", map[string]any{"terminateDebuggee": true})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	c.conn.Close()
	exited := make(chan struct{})
	go func() { c.cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		c.cmd.Process.Kill()
		<-exited
	}
}
