package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
)

// The Model Context Protocol on stdio, for coding agents that take tools that
// way. It offers one tool, `debug`, which takes the same line the socket does
// and passes it there.

const protocol = "2024-11-05"

type rpc struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  any              `json:"result,omitempty"`
	Error   any              `json:"error,omitempty"`
}

// Send passes one line to the session's socket and returns its whole reply.
func Send(line string) (string, error) {
	conn, err := net.Dial("unix", SocketPath())
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, strings.ReplaceAll(line, "\n", " ")+"\n"); err != nil {
		return "", err
	}
	reply, err := io.ReadAll(conn)
	return strings.TrimRight(string(reply), "\n"), err
}

// ServeMCP answers requests on stdin until it closes.
func ServeMCP() {
	tool := map[string]any{
		"name": "debug",
		"description": "Drive an lldb debugging session on a native program (C, C++, Rust). " +
			"Send one line; get back what lldb printed, or where the program now is " +
			"(source, locals, stack) if the line moved it.\n\n" + Help,
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"line"},
			"properties": map[string]any{
				"line": map[string]any{"type": "string", "description": "An lldb command, or one of the words above."},
			},
		},
	}
	in := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for {
		raw, err := in.ReadBytes('\n')
		if len(raw) > 0 {
			var request rpc
			if json.Unmarshal(raw, &request) == nil && request.ID != nil {
				reply := rpc{JSONRPC: "2.0", ID: request.ID}
				switch request.Method {
				case "initialize":
					reply.Result = map[string]any{
						"protocolVersion": protocol,
						"capabilities":    map[string]any{"tools": map[string]any{}},
						"serverInfo":      map[string]any{"name": "tongs", "version": "0.2"},
					}
				case "tools/list":
					reply.Result = map[string]any{"tools": []any{tool}}
				case "ping":
					reply.Result = map[string]any{}
				case "tools/call":
					var params struct {
						Arguments struct {
							Line string `json:"line"`
						} `json:"arguments"`
					}
					json.Unmarshal(request.Params, &params)
					text, err := Send(params.Arguments.Line)
					if err != nil {
						text = "error: the debugging session is gone: " + err.Error()
					}
					reply.Result = map[string]any{
						"content": []any{map[string]any{"type": "text", "text": text}},
						"isError": strings.HasPrefix(text, "error:"),
					}
				default:
					reply.Error = map[string]any{"code": -32601, "message": "unknown method " + request.Method}
				}
				out.Encode(reply)
			}
		}
		if err != nil {
			return
		}
	}
}
