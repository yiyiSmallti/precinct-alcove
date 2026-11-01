package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/hostipc"
	"github.com/yiyiSmallti/precinct-alcove/internal/jsonrpc"
)

// Bridge is the host end of the @precinct/alcove-bridge bundle (SPEC 3.1):
// approval requests and ask_user_question calls from every runtime arrive
// here and are passed to the Answerer (the UI). Each decision is appended to
// an audit log (PRD F3: every approval decision has a local record).
type Bridge struct {
	Answerer Answerer
	// AuditLog is a JSONL file; empty disables the record.
	AuditLog string

	srv     hostipc.Server
	mu      sync.Mutex
	cancels map[string]pendingReq // requestId → open card
	logMu   sync.Mutex
}

// Approval outcomes the bundle accepts.
const (
	AllowedOnce = "allowed-once"
	Rejected    = "rejected"
	Cancelled   = "cancelled"
)

// ApprovalRequest is one permission question from dsh.
type ApprovalRequest struct {
	RequestID string `json:"requestId"`
	SessionID string `json:"sessionId"`
	ToolName  string `json:"toolName"`
	CallID    string `json:"callId,omitempty"`
	Reason    string `json:"reason"`
	// DisplayReason is the user-facing reason, localized (dsh sends
	// {"en": …, "zh": …}); Reason is the raw text the model sees.
	DisplayReason LocalText `json:"displayReason,omitempty"`
}

// LocalText is dsh localized text: a plain string or a map of language tag to
// text. A plain string is kept under the "" key.
type LocalText map[string]string

func (t *LocalText) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*t = LocalText{"": s}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*t = m
	return nil
}

// Text returns the text for lang ("zh", "en"), falling back to English, then
// to any entry.
func (t LocalText) Text(lang string) string {
	for _, k := range []string{lang, "en", ""} {
		if v, ok := t[k]; ok {
			return v
		}
	}
	for _, v := range t {
		return v
	}
	return ""
}

// Question is one item of an ask_user_question call.
type Question struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Header   string `json:"header,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description,omitempty"`
	} `json:"options,omitempty"`
	MultiSelect bool `json:"multiSelect,omitempty"`
}

// QuestionRequest is one ask_user_question call.
type QuestionRequest struct {
	RequestID string     `json:"requestId"`
	SessionID string     `json:"sessionId"`
	CallID    string     `json:"callId,omitempty"`
	Questions []Question `json:"questions"`
}

// Answer is the user's answer to one question; empty Selected and Custom
// means the user skipped it.
type Answer struct {
	ID       string   `json:"id"`
	Selected []string `json:"selected"`
	Custom   string   `json:"custom,omitempty"`
}

// Answerer shows approval cards and question forms. ctx is cancelled when dsh
// withdraws the request or the runtime goes away; the card must close then.
type Answerer interface {
	Approve(ctx context.Context, runtimeID string, req ApprovalRequest) (outcome string, err error)
	AnswerQuestions(ctx context.Context, runtimeID string, req QuestionRequest) ([]Answer, error)
}

// BridgeProtocol is the protocol name the bundle sends in host.hello.
const BridgeProtocol = "alcove-bridge"

// Start listens on the per-user endpoint name and serves until ctx ends.
func (b *Bridge) Start(ctx context.Context, name string) error {
	b.srv = hostipc.Server{Protocol: BridgeProtocol, Majors: []string{"1"}, Handler: b}
	b.cancels = map[string]pendingReq{}
	if err := b.srv.Listen(name); err != nil {
		return fmt.Errorf("bridge: %w", err)
	}
	go b.srv.Serve(ctx)
	go func() { <-ctx.Done(); b.srv.Close() }()
	return nil
}

// launchEnv issues a token for runtimeID and returns the bundle's env pair.
func (b *Bridge) launchEnv(runtimeID string) []string {
	return []string{
		"ALCOVE_BRIDGE_HOST=" + b.srv.Endpoint(),
		"ALCOVE_BRIDGE_HOST_TOKEN=" + b.srv.Issue(runtimeID),
	}
}

// Endpoint is the address bundles dial.
func (b *Bridge) Endpoint() string { return b.srv.Endpoint() }

// Attached reports whether runtimeID's bundle is connected.
func (b *Bridge) Attached(runtimeID string) bool { return b.srv.Attachment(runtimeID) != nil }

// hostipc.Handler.

func (b *Bridge) OnAttach(a *hostipc.Attachment) {}

func (b *Bridge) Request(ctx context.Context, a *hostipc.Attachment, method string, params json.RawMessage) (any, error) {
	switch method {
	case "approval.ask":
		var req ApprovalRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
		}
		ctx, done := b.track(ctx, a.Target, req.RequestID)
		defer done()
		outcome, err := b.Answerer.Approve(ctx, a.Target, req)
		if ctx.Err() != nil {
			outcome, err = Cancelled, nil
		}
		if err != nil {
			return nil, err
		}
		if outcome != AllowedOnce && outcome != Rejected && outcome != Cancelled {
			outcome = Rejected
		}
		b.audit(map[string]any{"kind": "approval", "runtime": a.Target, "request": req, "outcome": outcome})
		return map[string]string{"outcome": outcome}, nil
	case "questions.ask":
		var req QuestionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
		}
		ctx, done := b.track(ctx, a.Target, req.RequestID)
		defer done()
		answers, err := b.Answerer.AnswerQuestions(ctx, a.Target, req)
		if err != nil {
			return nil, err
		}
		b.audit(map[string]any{"kind": "questions", "runtime": a.Target, "request": req, "answers": answers})
		return map[string]any{"answers": answers}, nil
	}
	return nil, &jsonrpc.Error{Code: hostipc.CodeUnsupported, Message: "unsupported: " + method}
}

func (b *Bridge) Notify(a *hostipc.Attachment, method string, params json.RawMessage) {
	if method != "request.cancel" {
		return
	}
	var p struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(params, &p) == nil {
		b.mu.Lock()
		pr, ok := b.cancels[p.RequestID]
		b.mu.Unlock()
		if ok {
			pr.cancel()
		}
	}
}

// OnDetach cancels the runtime's open cards: its process is gone or going.
func (b *Bridge) OnDetach(a *hostipc.Attachment) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, pr := range b.cancels {
		if pr.runtime == a.Target {
			pr.cancel()
		}
	}
}

type pendingReq struct {
	runtime string
	cancel  context.CancelFunc
}

func (b *Bridge) track(ctx context.Context, runtime, id string) (context.Context, func()) {
	if id == "" {
		id = randomID()
	}
	ctx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.cancels[id] = pendingReq{runtime: runtime, cancel: cancel}
	b.mu.Unlock()
	return ctx, func() {
		b.mu.Lock()
		delete(b.cancels, id)
		b.mu.Unlock()
		cancel()
	}
}

func (b *Bridge) audit(rec map[string]any) {
	if b.AuditLog == "" {
		return
	}
	rec["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	b.logMu.Lock()
	defer b.logMu.Unlock()
	os.MkdirAll(filepath.Dir(b.AuditLog), 0o755)
	f, err := os.OpenFile(b.AuditLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}

func randomID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
