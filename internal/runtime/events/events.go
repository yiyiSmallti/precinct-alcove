// Package events folds a dsh runtime's session notifications into the model
// the conversation view draws (SPEC 2, internal/runtime/events): the user's
// messages, the assistant's replies (streamed in through live chunks, then
// replaced by the final message), tool calls with their results, and how
// each turn ended.
//
// It knows nothing about the UI toolkit, so it is tested on recorded event
// streams. A Session is not safe for concurrent use; the UI applies events
// on its own thread.
package events

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Kind is what an Entry shows.
type Kind string

const (
	User      Kind = "user"
	Assistant Kind = "assistant"
	Reasoning Kind = "reasoning"
	Tool      Kind = "tool"
	Notice    Kind = "notice" // a turn that ended other than completed, errors
)

// Entry is one item of the conversation.
type Entry struct {
	Key  string `json:"-"` // stable across updates, for list keys
	Kind Kind   `json:"kind"`
	Text string `json:"text,omitempty"`
	// Streaming is set while an assistant entry is still built from live
	// chunks; the final assistant/message replaces it.
	Streaming bool `json:"-"`
	// Tool calls only.
	ToolName string `json:"toolName,omitempty"`
	Args     string `json:"args,omitempty"`
	Result   string `json:"result,omitempty"`
	IsError  bool   `json:"isError,omitempty"`
	Done     bool   `json:"done,omitempty"`
}

// Session is one dsh session as the conversation view shows it.
type Session struct {
	ID     string
	Title  string
	Status string // running | idle | ...
	// Context is how many tokens the conversation took as of the latest
	// reply (its request and the reply itself, as dsh's token meter counts
	// them); 0 before any. It shrinks with the reply after a compaction.
	Context int
	// Usage is the token count of the latest reply, as it makes Context.
	Usage Usage
	// Entries in display order.
	Entries []*Entry

	tools map[string]*Entry // by call id
	live  map[string]*Entry // assistant text being streamed, by turn/step/index
	think map[string]*Entry // reasoning being streamed
	seq   int               // last durable seq applied; -1 before any
	next  int
}

// Usage splits a reply's tokens: the request's input not read from the
// cache, read from it and written to it, and the reply's output.
type Usage struct {
	Input      int `json:"inputTokens"`
	Output     int `json:"outputTokens"`
	CacheRead  int `json:"cacheReadTokens"`
	CacheWrite int `json:"cacheWriteTokens"`
}

// NewSession returns an empty session view for id.
func NewSession(id string) *Session {
	return &Session{ID: id, seq: -1, tools: map[string]*Entry{}, live: map[string]*Entry{}, think: map[string]*Entry{}}
}

// Running reports whether a turn is in progress.
func (s *Session) Running() bool { return s.Status == "running" }

type envelope struct {
	SessionID string          `json:"sessionId"`
	Status    string          `json:"status"`
	Event     json.RawMessage `json:"event"`
}

type event struct {
	Type string          `json:"type"`
	Seq  float64         `json:"seq"`
	Data json.RawMessage `json:"data"`
}

type block struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type message struct {
	Role       string  `json:"role"`
	Content    []block `json:"content"`
	ToolCallID string  `json:"toolCallId"`
	IsError    bool    `json:"isError"`
	Source     struct {
		Kind string `json:"kind"`
	} `json:"source"`
}

// SessionOf returns the session id a runtime notification is about, or "".
func SessionOf(params json.RawMessage) string {
	var env envelope
	json.Unmarshal(params, &env)
	return env.SessionID
}

// Apply folds one runtime notification (method and params as the runtime
// sent them) into s and reports whether anything visible changed.
// Notifications for other sessions and unknown event types are ignored.
func (s *Session) Apply(method string, params json.RawMessage) bool {
	var env envelope
	if err := json.Unmarshal(params, &env); err != nil || env.SessionID != s.ID {
		return false
	}
	switch method {
	case "session.status":
		if env.Status == s.Status {
			return false
		}
		s.Status = env.Status
		return true
	case "session.event":
		var e event
		if err := json.Unmarshal(env.Event, &e); err != nil {
			return false
		}
		return s.applyEvent(e)
	}
	return false
}

func (s *Session) applyEvent(e event) bool {
	// Live chunks carry fractional seqs between durable ones and are never
	// replayed; durable events are applied once each.
	if e.Type != "assistant/live-chunk" {
		seq := int(e.Seq)
		if float64(seq) == e.Seq {
			if seq <= s.seq {
				return false
			}
			s.seq = seq
		}
	}
	switch e.Type {
	case "user/message":
		var m message
		if json.Unmarshal(e.Data, &m) != nil || m.Source.Kind != "user" {
			return false // runtime context and other injected messages
		}
		s.add(&Entry{Kind: User, Text: textOf(m.Content)})
		return true
	case "assistant/live-chunk":
		var d struct {
			Turn, Step int
			Chunk      struct {
				Type  string `json:"type"`
				Index int    `json:"index"`
				Text  string `json:"text"`
			} `json:"chunk"`
		}
		if json.Unmarshal(e.Data, &d) != nil {
			return false
		}
		key := fmt.Sprintf("%d/%d/%d", d.Turn, d.Step, d.Chunk.Index)
		switch d.Chunk.Type {
		case "text-delta":
			s.stream(s.live, key, Assistant, d.Chunk.Text)
		case "reasoning-delta":
			s.stream(s.think, key, Reasoning, d.Chunk.Text)
		default:
			return false
		}
		return true
	case "assistant/message":
		var d struct {
			Turn, Step int
			Message    message `json:"message"`
			Usage      *Usage  `json:"usage"`
		}
		if json.Unmarshal(e.Data, &d) != nil {
			return false
		}
		if u := d.Usage; u != nil {
			s.Context, s.Usage = u.Input+u.CacheRead+u.CacheWrite+u.Output, *u
		}
		prefix := fmt.Sprintf("%d/%d/", d.Turn, d.Step)
		s.dropStreamed(s.think, prefix)
		live := s.dropStreamed(s.live, prefix)
		// Entries follow the final message's block order: streamed text
		// keeps its place, and every other block goes right after the
		// previous one (or before the first remaining streamed text).
		var prev *Entry
		place := func(en *Entry) {
			switch {
			case prev != nil:
				s.moveAfter(prev, en)
			case len(live) > 0:
				s.moveBefore(live[0], en)
			}
			prev = en
		}
		for _, b := range d.Message.Content {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				if len(live) > 0 {
					// Keep the streamed entry's place and key.
					en := live[0]
					live = live[1:]
					en.Text, en.Streaming = b.Text, false
					prev = en
					continue
				}
				en := &Entry{Kind: Assistant, Text: b.Text}
				s.add(en)
				place(en)
			case "tool-call":
				place(s.tool(b.ID, b.Name, b.Arguments))
			}
		}
		for _, en := range live {
			s.remove(en) // streamed text the final message no longer has
		}
		return true
	case "tool/call":
		var d struct {
			CallID    string `json:"callId"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(e.Data, &d) != nil || d.CallID == "" {
			return false
		}
		s.tool(d.CallID, d.Name, d.Arguments)
		return true
	case "tool/result":
		var d struct {
			Message message `json:"message"`
		}
		if json.Unmarshal(e.Data, &d) != nil || d.Message.ToolCallID == "" {
			return false
		}
		t := s.tool(d.Message.ToolCallID, "", "")
		t.Result, t.IsError, t.Done = textOf(d.Message.Content), d.Message.IsError, true
		return true
	case "turn/end":
		var d struct {
			Reason struct {
				Kind    string `json:"kind"`
				Message string `json:"message"`
			} `json:"reason"`
		}
		json.Unmarshal(e.Data, &d)
		// Anything still streaming is as final as it gets.
		for k, en := range s.live {
			en.Streaming = false
			delete(s.live, k)
		}
		for k, en := range s.think {
			en.Streaming = false
			delete(s.think, k)
		}
		if d.Reason.Kind == "" || d.Reason.Kind == "completed" {
			return true
		}
		text := "回合结束：" + d.Reason.Kind
		if d.Reason.Message != "" {
			text += "（" + d.Reason.Message + "）"
		}
		s.add(&Entry{Kind: Notice, Text: text})
		return true
	case "session/title":
		var d struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(e.Data, &d) != nil || d.Title == s.Title {
			return false
		}
		s.Title = d.Title
		return true
	}
	return false
}

func (s *Session) add(en *Entry) {
	s.next++
	en.Key = fmt.Sprintf("%s#%d", s.ID, s.next)
	s.Entries = append(s.Entries, en)
}

func (s *Session) remove(en *Entry) {
	for i, x := range s.Entries {
		if x == en {
			s.Entries = append(s.Entries[:i], s.Entries[i+1:]...)
			return
		}
	}
}

// moveAfter moves en to just after at.
func (s *Session) moveAfter(at, en *Entry) {
	if at == en {
		return
	}
	s.remove(en)
	for i, x := range s.Entries {
		if x == at {
			s.Entries = append(s.Entries[:i+1], append([]*Entry{en}, s.Entries[i+1:]...)...)
			return
		}
	}
	s.Entries = append(s.Entries, en)
}

// moveBefore moves en to just before at.
func (s *Session) moveBefore(at, en *Entry) {
	if at == en {
		return
	}
	s.remove(en)
	for i, x := range s.Entries {
		if x == at {
			s.Entries = append(s.Entries[:i], append([]*Entry{en}, s.Entries[i:]...)...)
			return
		}
	}
	s.Entries = append(s.Entries, en)
}

func (s *Session) stream(m map[string]*Entry, key string, kind Kind, text string) {
	en := m[key]
	if en == nil {
		en = &Entry{Kind: kind, Streaming: true}
		s.add(en)
		m[key] = en
	}
	en.Text += text
}

// dropStreamed forgets the streamed entries of one turn/step and returns the
// text ones in display order, for the final message to take over.
func (s *Session) dropStreamed(m map[string]*Entry, prefix string) []*Entry {
	var out []*Entry
	for k, en := range m {
		if strings.HasPrefix(k, prefix) {
			delete(m, k)
			en.Streaming = false
			out = append(out, en)
		}
	}
	// Map order is random: restore display order.
	ordered := out[:0:0]
	for _, en := range s.Entries {
		for _, o := range out {
			if o == en {
				ordered = append(ordered, en)
			}
		}
	}
	return ordered
}

func (s *Session) tool(id, name, args string) *Entry {
	t := s.tools[id]
	if t == nil {
		t = &Entry{Kind: Tool}
		s.add(t)
		s.tools[id] = t
	}
	if name != "" {
		t.ToolName = name
	}
	if args != "" {
		t.Args = args
	}
	return t
}

func textOf(blocks []block) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Snapshot is what a session view keeps across restarts: dsh's runtime
// does not replay a resumed session's history, so the UI saves the folded
// entries itself.
type Snapshot struct {
	Title   string  `json:"title,omitempty"`
	Seq     int     `json:"seq"`
	Context int     `json:"context,omitempty"`
	Usage   *Usage  `json:"usage,omitempty"`
	Entries []Entry `json:"entries"`
}

// Snapshot returns the finished part of s: entries still streaming are
// left out.
func (s *Session) Snapshot() Snapshot {
	out := Snapshot{Title: s.Title, Seq: s.seq, Context: s.Context}
	if s.Usage != (Usage{}) {
		u := s.Usage
		out.Usage = &u
	}
	for _, e := range s.Entries {
		if e.Streaming {
			continue
		}
		c := *e
		c.Key = ""
		out.Entries = append(out.Entries, c)
	}
	return out
}

// Restore replaces s's entries with a snapshot's. Tool calls that never
// finished are shown as such.
func (s *Session) Restore(snap Snapshot) {
	s.Entries, s.next = nil, 0
	s.tools, s.live, s.think = map[string]*Entry{}, map[string]*Entry{}, map[string]*Entry{}
	s.Title, s.seq, s.Context = snap.Title, snap.Seq, snap.Context
	if snap.Usage != nil {
		s.Usage = *snap.Usage
	}
	for i := range snap.Entries {
		e := snap.Entries[i]
		if e.Kind == Tool && !e.Done {
			e.Done, e.IsError = true, true
			if e.Result == "" {
				e.Result = "没有结果（运行时已退出）"
			}
		}
		s.add(&e)
	}
}
