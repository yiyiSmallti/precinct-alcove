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
	// Subagent calls only. dsh's subagent tool returns as soon as the
	// subagent starts; these follow the subagent itself: its id, how it is
	// doing (running, ok, error, stopped), how many tool calls it and its
	// own subagents made, the latest of them, and its final answer.
	Agent       string `json:"agent,omitempty"`
	AgentStatus string `json:"agentStatus,omitempty"`
	AgentSteps  int    `json:"agentSteps,omitempty"`
	AgentTool   string `json:"-"`
	AgentArgs   string `json:"-"`
	AgentResult string `json:"agentResult,omitempty"`
}

// Busy reports whether the entry is still at work: a tool call without its
// result, or a subagent that has not finished.
func (e *Entry) Busy() bool {
	return e.Kind == Tool && (!e.Done || e.AgentStatus == "running")
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

	// agents maps the sessions of subagents (and of their own subagents)
	// to the call that started them. Until the call's result names a
	// subagent, it maps to a stand-in that is not shown.
	agents     map[string]*Entry
	agentCalls map[string]bool // subagent tool calls counted, by session/call id
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
	s := &Session{ID: id}
	s.reset()
	return s
}

func (s *Session) reset() {
	s.Entries, s.next, s.seq = nil, 0, -1
	s.tools, s.live, s.think = map[string]*Entry{}, map[string]*Entry{}, map[string]*Entry{}
	s.agents, s.agentCalls = map[string]*Entry{}, map[string]bool{}
}

// Running reports whether a turn is in progress.
func (s *Session) Running() bool { return s.Status == "running" }

// Exited marks the subagents still running as stopped: their runtime is
// gone. It reports whether there were any.
func (s *Session) Exited() bool {
	any := false
	for _, e := range s.Entries {
		if e.Agent != "" && e.AgentStatus == "running" {
			e.AgentStatus, e.AgentTool, e.AgentArgs, any = "stopped", "", "", true
		}
	}
	return any
}

// AgentsRunning counts the subagents of s that have not finished; the
// session's own turn may have ended meanwhile.
func (s *Session) AgentsRunning() int {
	n := 0
	for _, e := range s.Entries {
		if e.Agent != "" && e.AgentStatus == "running" {
			n++
		}
	}
	return n
}

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
	if method == "subagent.started" || method == "subagent.finished" {
		return s.applySubagent(method, params)
	}
	var env envelope
	if err := json.Unmarshal(params, &env); err != nil {
		return false
	}
	if env.SessionID != s.ID {
		if a := s.agents[env.SessionID]; a != nil && method == "session.event" {
			return s.applyAgentEvent(a, env.SessionID, env.Event)
		}
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
		if id := startedAgent(t.Result); id != "" && !t.IsError {
			s.link(t, id)
		}
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

// startedAgent returns the subagent id in the result of dsh's subagent
// tool ("started subagent <id>"), or "".
func startedAgent(result string) string {
	id, ok := strings.CutPrefix(strings.TrimSpace(result), "started subagent ")
	if !ok || strings.ContainsAny(id, " \n") {
		return ""
	}
	return id
}

// link makes call t the entry of subagent id, taking over what its
// stand-in has gathered.
func (s *Session) link(t *Entry, id string) {
	if t.Agent != "" {
		return
	}
	t.Agent, t.AgentStatus = id, "running"
	if p := s.agents[id]; p != nil && p != t {
		t.AgentStatus, t.AgentSteps, t.AgentTool, t.AgentArgs, t.AgentResult = p.AgentStatus, p.AgentSteps, p.AgentTool, p.AgentArgs, p.AgentResult
		for k, v := range s.agents {
			if v == p {
				s.agents[k] = t
			}
		}
	}
	s.agents[id] = t
}

func (s *Session) applySubagent(method string, params json.RawMessage) bool {
	var d struct {
		Parent string  `json:"parentSessionId"`
		Child  string  `json:"childSessionId"`
		Status string  `json:"status"`
		Last   []block `json:"lastAssistantMessage"`
	}
	if json.Unmarshal(params, &d) != nil || d.Child == "" {
		return false
	}
	a := s.agents[d.Child]
	if method == "subagent.started" {
		switch {
		case a != nil:
		case d.Parent == s.ID:
			s.agents[d.Child] = &Entry{Kind: Tool, Agent: d.Child, AgentStatus: "running"}
		case s.agents[d.Parent] != nil:
			// A subagent's own subagents count toward it.
			s.agents[d.Child] = s.agents[d.Parent]
		}
		return false
	}
	if a == nil || a.Agent != d.Child || a.AgentStatus != "running" {
		return false
	}
	a.AgentStatus, a.AgentTool, a.AgentArgs = d.Status, "", ""
	if a.AgentStatus == "" {
		a.AgentStatus = "ok"
	}
	a.AgentResult = textOf(d.Last)
	return a.Key != ""
}

// applyAgentEvent counts the tool calls of a subagent's session toward the
// call that started it.
func (s *Session) applyAgentEvent(a *Entry, session string, raw json.RawMessage) bool {
	var e event
	if json.Unmarshal(raw, &e) != nil || a.AgentStatus != "running" {
		return false
	}
	call := func(id, name, args string) bool {
		k := session + "/" + id
		if id == "" || s.agentCalls[k] {
			return false
		}
		s.agentCalls[k] = true
		a.AgentSteps++
		a.AgentTool, a.AgentArgs = name, args
		return a.Key != ""
	}
	switch e.Type {
	case "tool/call":
		var d struct {
			CallID    string `json:"callId"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		json.Unmarshal(e.Data, &d)
		return call(d.CallID, d.Name, d.Arguments)
	case "assistant/message":
		var d struct {
			Message message `json:"message"`
		}
		json.Unmarshal(e.Data, &d)
		changed := false
		for _, b := range d.Message.Content {
			if b.Type == "tool-call" && call(b.ID, b.Name, b.Arguments) {
				changed = true
			}
		}
		return changed
	}
	return false
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
	s.reset()
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
		if e.AgentStatus == "running" {
			e.AgentStatus = "stopped"
		}
		s.add(&e)
	}
}
