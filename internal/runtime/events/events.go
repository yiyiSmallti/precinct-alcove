// Package events folds a dsh runtime's session notifications into the model
// the conversation view draws (SPEC 2, internal/runtime/events): the user's
// messages, the assistant's replies and reasoning (streamed in through live
// chunks, then replaced by the final message), tool calls (shown while the
// model still writes their arguments, with what a running command prints)
// and their results, context compactions with their summaries, and how each
// turn ended.
//
// What a command prints and the summary a compaction is writing do not come
// with dsh's session events: the alcove-bridge bundle sends them as
// "session.live" notifications on its own connection (bridge/lib/index.js).
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
	// Compaction is a compaction of the conversation's context: Text is its
	// summary, streamed in while it is written; IsError and Result tell a
	// compaction that failed.
	Compaction Kind = "compaction"
)

// Entry is one item of the conversation.
type Entry struct {
	Key  string `json:"-"` // stable across updates, for list keys
	Kind Kind   `json:"kind"`
	Text string `json:"text,omitempty"`
	// Streaming is set while an entry is still built from live chunks: an
	// assistant reply or reasoning (the final assistant/message replaces
	// it), a tool call whose arguments the model is still writing, or a
	// compaction still under way.
	Streaming bool `json:"-"`
	// Tool calls only.
	ToolName string `json:"toolName,omitempty"`
	Args     string `json:"args,omitempty"`
	Result   string `json:"result,omitempty"`
	IsError  bool   `json:"isError,omitempty"`
	Done     bool   `json:"done,omitempty"`
	// Output is the tail of what the call's command has printed so far,
	// while it runs; the result replaces it.
	Output string `json:"-"`
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
	calls map[string]*Entry // tool calls whose arguments are being streamed
	// attempts is the reply attempt each turn/step streams from: a retry
	// starts over. settled holds the turn/steps whose final message came:
	// a chunk still on its way after it is late.
	attempts map[string]string
	settled  map[string]bool
	// compactions are the compactions seen, by id, from their start (or
	// their first summary delta, which may come first). closed holds those
	// whose summary or end came: a delta still on its way after that is
	// late. compactCall is the summary call an open compaction's deltas
	// come from: a retried call writes the summary over.
	compactions map[string]*Entry
	closed      map[string]bool
	compactCall map[string]int
	seq         int // last durable seq applied; -1 before any
	next        int

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
	s.tools, s.live, s.think, s.calls = map[string]*Entry{}, map[string]*Entry{}, map[string]*Entry{}, map[string]*Entry{}
	s.attempts, s.settled = map[string]string{}, map[string]bool{}
	s.compactions, s.closed, s.compactCall = map[string]*Entry{}, map[string]bool{}, map[string]int{}
	s.agents, s.agentCalls = map[string]*Entry{}, map[string]bool{}
}

// Running reports whether a turn is in progress.
func (s *Session) Running() bool { return s.Status == "running" }

// Call is the entry of tool call id, or nil when the session has none: an
// approval names the call it is about.
func (s *Session) Call(id string) *Entry {
	if id == "" {
		return nil
	}
	return s.tools[id]
}

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
	case "session.live":
		return s.applyLive(params)
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
			Chunk      chunk `json:"chunk"`
		}
		if json.Unmarshal(e.Data, &d) != nil {
			return false
		}
		return s.liveChunk(d.Turn, d.Step, d.Chunk)
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
		s.settled[prefix] = true
		delete(s.attempts, prefix)
		thinks := s.dropStreamed(s.think, prefix)
		live := s.dropStreamed(s.live, prefix)
		calls := s.dropStreamed(s.calls, prefix)
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
			case "reasoning":
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				if len(thinks) > 0 {
					en := thinks[0]
					thinks = thinks[1:]
					en.Text = b.Text
					prev = en
					continue
				}
				en := &Entry{Kind: Reasoning, Text: b.Text}
				s.add(en)
				place(en)
			case "tool-call":
				t := s.tool(b.ID, b.Name, b.Arguments)
				t.Streaming = false
				for i, c := range calls {
					if c == t {
						calls = append(calls[:i], calls[i+1:]...)
						break
					}
				}
				place(t)
			}
		}
		for _, en := range live {
			s.remove(en) // streamed text the final message no longer has
		}
		for _, en := range calls {
			s.dropCall(en) // a call the final message does not make
		}
		// Streamed reasoning the final message does not repeat stays.
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
			Reason turnEnd `json:"reason"`
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
		for k, en := range s.calls {
			delete(s.calls, k)
			s.dropCall(en) // never made: the turn ended while it was written
		}
		for id, en := range s.compactions {
			if !s.closed[id] {
				en.Streaming, s.closed[id] = false, true
			}
		}
		if d.Reason.Kind == "" || d.Reason.Kind == "completed" {
			return true
		}
		s.add(&Entry{Kind: Notice, Text: d.Reason.notice(), IsError: d.Reason.Kind == "error"})
		return true
	case "compaction/start":
		var d struct {
			ID string `json:"compactionId"`
		}
		json.Unmarshal(e.Data, &d)
		s.compaction(d.ID)
		return true
	case "compaction/summary":
		var d struct {
			ID      string  `json:"compactionId"`
			Summary []block `json:"summary"`
		}
		if json.Unmarshal(e.Data, &d) != nil {
			return false
		}
		s.compaction(d.ID).Text = textOf(d.Summary)
		s.closed[d.ID] = true
		return true
	case "compaction/end":
		var d struct {
			ID    string          `json:"compactionId"`
			Error json.RawMessage `json:"error"`
		}
		json.Unmarshal(e.Data, &d)
		en := s.compaction(d.ID)
		en.Streaming, s.closed[d.ID] = false, true
		if len(d.Error) > 0 && string(d.Error) != "null" {
			en.IsError = true
			var v any
			json.Unmarshal(d.Error, &v)
			if en.Result = firstMessage(v); en.Result == "" {
				en.Result = string(d.Error)
			}
		}
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

// chunk is one chunk of a reply as it streams.
type chunk struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Text  string `json:"text"`
	// tool-call-delta
	ID             string `json:"id"`
	Name           string `json:"name"`
	ArgumentsDelta string `json:"argumentsDelta"`
}

// liveChunk folds one streamed chunk of turn/step's reply.
func (s *Session) liveChunk(turn, step int, c chunk) bool {
	prefix := fmt.Sprintf("%d/%d/", turn, step)
	if s.settled[prefix] {
		return false
	}
	key := fmt.Sprintf("%s%d", prefix, c.Index)
	switch c.Type {
	case "text-delta":
		s.stream(s.live, key, Assistant, c.Text)
	case "reasoning-delta":
		s.stream(s.think, key, Reasoning, c.Text)
	case "tool-call-delta":
		s.streamCall(key, c.ID, c.Name, c.ArgumentsDelta)
	default:
		return false
	}
	return true
}

// dropAttempt forgets what turn/step's reply has streamed so far: the
// attempt was dropped, or a retry starts over.
func (s *Session) dropAttempt(prefix string) bool {
	changed := false
	for _, m := range []map[string]*Entry{s.live, s.think} {
		for _, en := range s.dropStreamed(m, prefix) {
			s.remove(en)
			changed = true
		}
	}
	for _, en := range s.dropStreamed(s.calls, prefix) {
		s.dropCall(en)
		changed = true
	}
	return changed
}

// streamCall grows the tool call the model is writing at key. Its id may
// come only with a later chunk; once known, the final message's tool-call
// block and the call's result find the same entry by it.
func (s *Session) streamCall(key, id, name, delta string) {
	en := s.calls[key]
	if en == nil {
		if id != "" && s.tools[id] != nil {
			return // already settled
		}
		en = &Entry{Kind: Tool, Streaming: true}
		s.add(en)
		s.calls[key] = en
	}
	if id != "" && s.tools[id] == nil {
		s.tools[id] = en
	}
	if name != "" {
		en.ToolName = name
	}
	en.Args += delta
}

// dropCall removes a streamed tool call that was never made.
func (s *Session) dropCall(en *Entry) {
	for id, t := range s.tools {
		if t == en {
			delete(s.tools, id)
		}
	}
	s.remove(en)
}

// compaction returns the compaction id, starting one if it is not seen yet.
func (s *Session) compaction(id string) *Entry {
	en := s.compactions[id]
	if en == nil {
		en = &Entry{Kind: Compaction, Streaming: true}
		s.add(en)
		s.compactions[id] = en
	}
	return en
}

// outputMax bounds the live output a tool entry keeps: its tail is shown.
const outputMax = 32 << 10

// applyLive folds the bridge's session.live notification: a delta of what a
// call's command printed ("tool-output") or of the summary a compaction is
// writing ("compaction"). They come on another connection than the session
// events, so either may arrive before the event it belongs to: output for a
// call not seen yet makes its entry (the final message names it), and a
// summary delta before compaction/start opens the compaction. Either may
// also arrive after: output once the call's result came, or a summary delta
// once the compaction's summary or end came, is dropped.
func (s *Session) applyLive(params json.RawMessage) bool {
	var d struct {
		SessionID  string `json:"sessionId"`
		Kind       string `json:"kind"`
		Turn, Step int
		Attempt    string `json:"attempt"`
		Chunk      chunk  `json:"chunk"`
		CallID     string `json:"callId"`
		Compaction string `json:"compactionId"`
		Call       int    `json:"call"`
		Text       string `json:"text"`
	}
	if json.Unmarshal(params, &d) != nil || d.SessionID != s.ID {
		return false
	}
	prefix := fmt.Sprintf("%d/%d/", d.Turn, d.Step)
	switch d.Kind {
	case "chunk":
		if s.settled[prefix] {
			return false
		}
		changed := false
		if a, ok := s.attempts[prefix]; ok && a != d.Attempt {
			changed = s.dropAttempt(prefix) // a retry starts over
		}
		s.attempts[prefix] = d.Attempt
		return s.liveChunk(d.Turn, d.Step, d.Chunk) || changed
	case "abandon":
		if s.settled[prefix] || s.attempts[prefix] != d.Attempt {
			return false
		}
		delete(s.attempts, prefix)
		return s.dropAttempt(prefix)
	}
	if d.Text == "" {
		return false
	}
	switch d.Kind {
	case "tool-output":
		if d.CallID == "" {
			return false
		}
		t := s.tool(d.CallID, "", "")
		if t.Done {
			return false // the result has it all
		}
		t.Output = tail(t.Output+d.Text, outputMax)
		return true
	case "compaction":
		if d.Compaction == "" || s.closed[d.Compaction] {
			return false
		}
		en := s.compaction(d.Compaction)
		if c, ok := s.compactCall[d.Compaction]; ok && c != d.Call {
			en.Text = "" // the summary call was retried
		}
		s.compactCall[d.Compaction] = d.Call
		en.Text += d.Text
		return true
	}
	return false
}

// tail is the last max bytes of text, cut on a rune boundary.
func tail(text string, max int) string {
	if len(text) <= max {
		return text
	}
	i := len(text) - max
	for i < len(text) && text[i]&0xC0 == 0x80 {
		i++
	}
	return text[i:]
}

// firstMessage finds the first "message" string in a decoded error chain.
func firstMessage(v any) string {
	switch x := v.(type) {
	case map[string]any:
		if m, ok := x["message"].(string); ok && m != "" {
			return m
		}
		for _, y := range x {
			if m := firstMessage(y); m != "" {
				return m
			}
		}
	case []any:
		for _, y := range x {
			if m := firstMessage(y); m != "" {
				return m
			}
		}
	case string:
		return x
	}
	return ""
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

// turnEnd is why a turn ended, as dsh's turn/end gives it: completed;
// error, with the failure; or aborted, with who stopped it (the user, the
// parent task, the session closing, or a hook, with why). Older dsh gave a
// message beside the kind.
type turnEnd struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
	By json.RawMessage `json:"reason"`
}

// notice is what a turn that ended other than completed says: what
// happened and, in "（…）", why, when dsh says. The conversation shows the
// why under what happened; a plugin's session gets the whole line as its
// error.
func (r turnEnd) notice() string {
	head, why := "回合结束："+r.Kind, r.Message
	switch r.Kind {
	case "error":
		head = "这一轮出错了"
		for _, w := range []string{r.Error.Message, r.Error.Code} {
			if why == "" {
				why = w
			}
		}
	case "aborted":
		var by struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		}
		json.Unmarshal(r.By, &by)
		switch by.Kind {
		case "user":
			head = "你停下了这一轮"
		case "parent":
			head = "上级任务停下了这一轮"
		case "disposed":
			head = "会话关闭，这一轮停了"
		case "hook":
			head, why = "插件拦下了这一轮", by.Reason
		default:
			head = "这一轮中途停了"
		}
	}
	if why = strings.TrimSpace(why); why != "" {
		return head + "（" + why + "）"
	}
	return head
}
