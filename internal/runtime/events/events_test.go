package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// replay applies a recorded stream: one "method params" line per
// notification, as the runtime sent them (testdata/turn.txt is a real dsh
// rc.5 turn against the e2e fake model: a write and a bash call, then a
// text reply).
func replay(t *testing.T, s *Session, file string) {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		method, params, _ := strings.Cut(sc.Text(), " ")
		s.Apply(method, json.RawMessage(params))
	}
}

func kinds(s *Session) string {
	var out []string
	for _, e := range s.Entries {
		out = append(out, string(e.Kind))
	}
	return strings.Join(out, ",")
}

func TestRecordedTurn(t *testing.T) {
	s := NewSession("s1")
	replay(t, s, "testdata/turn.txt")
	if got := kinds(s); got != "user,tool,tool,assistant" {
		t.Fatalf("entries %s", got)
	}
	if s.Entries[0].Text != "do it" {
		t.Errorf("user text %q", s.Entries[0].Text)
	}
	w, b := s.Entries[1], s.Entries[2]
	if w.ToolName != "write" || !w.Done || w.IsError {
		t.Errorf("write %+v", w)
	}
	if b.ToolName != "bash" || b.Result != "step 1\n" || !strings.Contains(b.Args, "echo step 1") {
		t.Errorf("bash %+v", b)
	}
	if s.Entries[3].Text != "done after 2 tool calls" || s.Entries[3].Streaming {
		t.Errorf("reply %+v", s.Entries[3])
	}
	if s.Context != 15 { // the last reply's input and output tokens
		t.Errorf("context %d", s.Context)
	}
	if s.Title != "do it" || s.Status != "idle" || s.Running() {
		t.Errorf("title %q status %q", s.Title, s.Status)
	}
	// Replaying the same stream (a reconnect) changes nothing.
	n := len(s.Entries)
	replay(t, s, "testdata/turn.txt")
	if len(s.Entries) != n {
		t.Errorf("replay added entries: %s", kinds(s))
	}
}

func ev(session, typ string, seq float64, data string) (string, json.RawMessage) {
	return "session.event", json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"event":{"type":%q,"seq":%v,"data":%s}}`, session, typ, seq, data))
}

func TestStreamingThenFinal(t *testing.T) {
	s := NewSession("x")
	apply := func(m string, p json.RawMessage) { s.Apply(m, p) }
	apply(ev("x", "assistant/live-chunk", 0.5, `{"turn":1,"step":1,"chunk":{"type":"reasoning-delta","index":0,"text":"think"}}`))
	apply(ev("x", "assistant/live-chunk", 0.6, `{"turn":1,"step":1,"chunk":{"type":"text-delta","index":1,"text":"Hel"}}`))
	apply(ev("x", "assistant/live-chunk", 0.7, `{"turn":1,"step":1,"chunk":{"type":"text-delta","index":1,"text":"lo"}}`))
	if kinds(s) != "reasoning,assistant" || s.Entries[1].Text != "Hello" || !s.Entries[1].Streaming {
		t.Fatalf("streaming: %s %+v", kinds(s), s.Entries)
	}
	key := s.Entries[1].Key
	apply(ev("x", "assistant/message", 1, `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"Hello, world"},{"type":"tool-call","id":"c1","name":"bash","arguments":"{}"}]}}`))
	if kinds(s) != "reasoning,assistant,tool" {
		t.Fatalf("final: %s", kinds(s))
	}
	if a := s.Entries[1]; a.Text != "Hello, world" || a.Streaming || a.Key != key {
		t.Errorf("final reply %+v (key %s)", a, key)
	}
	// Another session's events are ignored.
	if s.Apply(ev("y", "user/message", 2, `{"role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"hi"}]}`)) {
		t.Error("applied another session's event")
	}
	apply(ev("x", "turn/end", 3, `{"turn":1,"reason":{"kind":"error","message":"model unavailable"}}`))
	if last := s.Entries[len(s.Entries)-1]; last.Kind != Notice || !strings.Contains(last.Text, "model unavailable") {
		t.Errorf("turn end notice %+v", last)
	}
}

func TestFinalKeepsBlockOrder(t *testing.T) {
	s := NewSession("x")
	apply := func(m string, p json.RawMessage) { s.Apply(m, p) }
	apply(ev("x", "assistant/live-chunk", 0.5, `{"turn":1,"step":1,"chunk":{"type":"text-delta","index":0,"text":"before"}}`))
	apply(ev("x", "assistant/live-chunk", 0.6, `{"turn":1,"step":1,"chunk":{"type":"text-delta","index":2,"text":"after"}}`))
	apply(ev("x", "assistant/message", 1, `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"before"},{"type":"tool-call","id":"c1","name":"bash","arguments":"{}"},{"type":"text","text":"after"}]}}`))
	if kinds(s) != "assistant,tool,assistant" || s.Entries[0].Text != "before" || s.Entries[2].Text != "after" {
		t.Fatalf("order: %s", kinds(s))
	}
	// A tool call first, before streamed text.
	s = NewSession("x")
	apply(ev("x", "assistant/live-chunk", 0.5, `{"turn":1,"step":1,"chunk":{"type":"text-delta","index":1,"text":"done"}}`))
	apply(ev("x", "assistant/message", 1, `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"tool-call","id":"c1","name":"bash","arguments":"{}"},{"type":"text","text":"done"}]}}`))
	if kinds(s) != "tool,assistant" {
		t.Fatalf("tool first: %s", kinds(s))
	}
}

func TestSnapshotRestore(t *testing.T) {
	s := NewSession("s1")
	s.add(&Entry{Kind: User, Text: "hi"})
	s.add(&Entry{Kind: Tool, ToolName: "bash", Args: "{}"})
	s.add(&Entry{Kind: Assistant, Text: "part", Streaming: true})
	s.seq, s.Title = 7, "t"
	r := NewSession("s1")
	r.Restore(s.Snapshot())
	if len(r.Entries) != 2 || r.Title != "t" || r.seq != 7 {
		t.Fatalf("restored %+v", r)
	}
	if e := r.Entries[1]; !e.Done || !e.IsError || e.Key == "" {
		t.Errorf("unfinished tool %+v", e)
	}
	// Durable events up to the snapshot's seq are not applied again.
	if r.applyEvent(event{Type: "user/message", Seq: 7, Data: []byte(`{"role":"user","content":[{"type":"text","text":"hi"}],"source":{"kind":"user"}}`)}) {
		t.Error("old event applied")
	}
}
