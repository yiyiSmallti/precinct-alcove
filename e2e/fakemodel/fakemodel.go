// Package fakemodel is a local stand-in for a model endpoint in e2e runs.
// It speaks the Anthropic Messages API that dsh's DeepSeek adapter uses
// (POST <base>/v1/messages, SSE). While the conversation holds fewer than
// ToolCalls tool results it answers with one tool call (a `write` of
// hello.txt first, then shell commands), then with plain text.
package fakemodel

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server is a running fake model.
type Server struct {
	URL       string
	ToolCalls int
	// ShellCommand overrides the shell tool's command.
	ShellCommand string
	// Script, when set, replaces the default turn: call i answers with
	// Script[i] while i < len(Script), then plain text.
	Script []ToolCall

	mu       sync.Mutex
	requests []string // request paths, in order
	srv      *http.Server
}

// Start listens on 127.0.0.1 on a free port.
func Start(toolCalls int) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{URL: "http://" + ln.Addr().String(), ToolCalls: toolCalls}
	s.srv = &http.Server{Handler: s}
	go s.srv.Serve(ln)
	return s, nil
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Requests returns the paths requested so far.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// ToolCall is one scripted tool call.
type ToolCall struct {
	Name  string
	Input map[string]any
}

type block struct {
	Type string `json:"type"`
}

type request struct {
	Model    string `json:"model"`
	Messages []struct {
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	if !strings.HasSuffix(r.URL.Path, "/messages") {
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"data":[]}`))
		return
	}
	var req request
	json.NewDecoder(r.Body).Decode(&req)
	done := 0
	for _, m := range req.Messages {
		var blocks []block
		if json.Unmarshal(m.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "tool_result" {
					done++
				}
			}
		}
	}
	var shell, write string
	for _, t := range req.Tools {
		switch {
		case t.Name == "write":
			write = t.Name
		case shell == "" && (strings.Contains(t.Name, "bash") || strings.Contains(t.Name, "pwsh") || strings.Contains(t.Name, "shell")):
			shell = t.Name
		}
	}

	w.Header().Set("content-type", "text/event-stream")
	ev := func(name string, data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	}
	ev("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": fmt.Sprintf("msg_%d", time.Now().UnixNano()), "type": "message", "role": "assistant", "model": req.Model,
		"content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 0},
	}})
	stop := "end_turn"
	s.mu.Lock()
	script := s.Script
	s.mu.Unlock()
	var name string
	var input map[string]any
	switch {
	case script != nil && done < len(script):
		name, input = script[done].Name, script[done].Input
	case script == nil && done < s.ToolCalls && (shell != "" || write != ""):
		name, input = shell, map[string]any{"command": s.shellCommand(done), "description": fmt.Sprintf("step %d", done)}
		if done == 0 && write != "" {
			name, input = write, map[string]any{"file_path": "hello.txt", "content": "hello from fake model\n"}
		}
	}
	if name != "" {
		inputJSON, _ := json.Marshal(input)
		ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d_%d", done, time.Now().UnixNano()), "name": name, "input": map[string]any{}}})
		ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(inputJSON)}})
		stop = "tool_use"
	} else {
		ev("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		ev("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": fmt.Sprintf("done after %d tool calls", done)}})
	}
	ev("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	ev("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 5}})
	ev("message_stop", map[string]any{"type": "message_stop"})
}

// SetScript replaces the script for later requests.
func (s *Server) SetScript(calls []ToolCall) {
	s.mu.Lock()
	s.Script = calls
	s.mu.Unlock()
}

func (s *Server) shellCommand(step int) string {
	if s.ShellCommand != "" {
		return s.ShellCommand
	}
	return fmt.Sprintf("echo step %d", step)
}
