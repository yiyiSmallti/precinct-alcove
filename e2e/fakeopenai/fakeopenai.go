// Package fakeopenai is a stand-in for an external model provider behind a
// gateway: an OpenAI Chat Completions endpoint that keeps every request body
// it was sent, word for word, and answers by repeating the last user message.
// What it kept is what left for the outside; what it repeats shows whether
// the gateway puts the original values back on the way in.
package fakeopenai

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server is a running fake provider.
type Server struct {
	// URL is the base address, without /v1.
	URL string
	// Prefix starts every answer, before the repeated message.
	Prefix string
	// Pick chooses what to repeat from the text parts of the request's user
	// messages, in order; nil repeats the last one. A runtime adds parts of
	// its own (reminders, context) around what the user typed.
	Pick func(parts []string) string
	// Tool, when set, may have the provider call a tool instead of answering:
	// it gets the text parts of the user messages and how many tool results
	// the conversation holds so far, and returns the call to make, or nil to
	// answer with text.
	Tool func(parts []string, results int) *ToolCall
	// Turn, when set, is asked before Tool with the whole conversation: the
	// role and text parts of every message, in order. A non-nil call is
	// made; nil leaves the answer to Tool, then to text.
	Turn func(msgs []Message) *ToolCall

	mu     sync.Mutex
	bodies []string
	auth   []string
	srv    *http.Server
}

// Message is one message of a conversation as Turn sees it.
type Message struct {
	Role  string
	Texts []string
}

// ToolCall is a tool the provider asks the runtime to run.
type ToolCall struct {
	Name string
	Args map[string]any
}

// Start listens on 127.0.0.1 on a free port.
func Start() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{URL: "http://" + ln.Addr().String(), Prefix: "收到："}
	s.srv = &http.Server{Handler: s}
	go s.srv.Serve(ln)
	return s, nil
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// Bodies returns the chat request bodies received so far, in order.
func (s *Server) Bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

// Authorizations returns the Authorization header of each chat request.
func (s *Server) Authorizations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.auth...)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[]}`))
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(body))
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	s.mu.Unlock()
	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":{"message":"bad json"}}`, http.StatusBadRequest)
		return
	}
	var parts []string
	var msgs []Message
	results := 0
	for _, m := range req.Messages {
		msgs = append(msgs, Message{Role: m.Role, Texts: texts(m.Content)})
		switch m.Role {
		case "user":
			parts = append(parts, texts(m.Content)...)
		case "tool":
			results++
		}
	}
	var call *ToolCall
	if s.Turn != nil {
		call = s.Turn(msgs)
	}
	if call == nil && s.Tool != nil {
		call = s.Tool(parts, results)
	}
	user := ""
	switch {
	case s.Pick != nil:
		user = s.Pick(parts)
	case len(parts) > 0:
		user = parts[len(parts)-1]
	}
	content := s.Prefix + user
	message := map[string]any{"role": "assistant", "content": content}
	finish := "stop"
	if call != nil {
		args, _ := json.Marshal(call.Args)
		message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
			"index": 0, "id": fmt.Sprintf("call_%d", results), "type": "function",
			"function": map[string]any{"name": call.Name, "arguments": string(args)},
		}}}
		content, finish = "", "tool_calls"
	}
	usage := map[string]any{"prompt_tokens": len(body) / 4, "completion_tokens": len(content) / 4, "total_tokens": (len(body) + len(content)) / 4}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "chatcmpl-e2e", "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		send(chunk(message, nil))
		send(chunk(map[string]any{}, finish))
		send(map[string]any{"id": "chatcmpl-e2e", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{}, "usage": usage})
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-e2e", "object": "chat.completion", "created": time.Now().Unix(), "model": req.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usage,
	})
}

// texts are the text parts of a message's content: the string itself, or
// the text of each part of a content array.
func texts(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var parts []struct {
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return out
}
