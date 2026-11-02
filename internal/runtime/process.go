// Package runtime starts and stops dsh runtime processes and talks to them
// over the SDK JSON-RPC protocol (dsh-sdk-app, stdio).
//
// A process is launched on demand for one profile, is ready once `initialize`
// answers (15 s budget, stderr tail shown on failure), exits after an idle
// period without session events unless pinned, and on stop gets `shutdown`
// followed by a kill after 5 s (SPEC 3.1).
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
	"github.com/yiyiSmallti/precinct-alcove/internal/jsonrpc"
)

// Defaults from SPEC 3.1. The ready timeout allows for a cold first start:
// on a fresh Windows install the virus scanner reads the bundled runtime's
// files on first load, and dsh took over 15 s to answer initialize there
// (Windows retest r3, step 5); later starts took about 3 s.
const (
	DefaultReadyTimeout = 60 * time.Second
	DefaultIdleTimeout  = 10 * time.Minute
	DefaultStopGrace    = 5 * time.Second
)

// LaunchSpec says what to start.
type LaunchSpec struct {
	Profile string
	// Cwd is the process working directory and the default session cwd.
	Cwd string
	// Env holds KEY=VALUE pairs injected into this process only: API keys
	// resolved from the keychain, bridge endpoint and token, and whatever
	// launch hooks add. Never written to disk.
	Env []string
	// Provider and Model select the SDK route at initialize.
	Provider, Model string
	// ReasoningEffort is optional.
	ReasoningEffort string
	// Bridge, when set, lets the alcove-bridge bundle in this process connect
	// back for approvals and questions.
	Bridge       *Bridge
	ReadyTimeout time.Duration
	IdleTimeout  time.Duration // <0 disables idle exit
}

// Event is one notification from the runtime.
type Event struct {
	Method string          // session.event, session.status, subagent.started, subagent.finished
	Params json.RawMessage // raw payload
}

// Process is a running dsh runtime.
type Process struct {
	Spec LaunchSpec
	// ID names this launch (bridge token target, audit records).
	ID string

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	conn   *jsonrpc.Conn
	stderr *tailBuffer
	events chan Event

	mu       sync.Mutex
	pinned   bool
	busy     map[string]bool // sessions currently running
	idle     *time.Timer
	stopping bool
	exited   chan struct{}
	exitErr  error
}

// ErrNotReady wraps a launch that did not answer initialize in time.
var ErrNotReady = errors.New("dsh runtime did not become ready")

// Launch starts dsh for spec and waits until it is ready.
func Launch(ctx context.Context, cli *dsh.CLI, spec LaunchSpec) (*Process, error) {
	if spec.ReadyTimeout == 0 {
		spec.ReadyTimeout = DefaultReadyTimeout
	}
	if spec.IdleTimeout == 0 {
		spec.IdleTimeout = DefaultIdleTimeout
	}
	cmd := exec.Command(cli.Node, cli.Bin, "--profile", spec.Profile)
	cmd.Dir = spec.Cwd
	// Own process group, so a kill also ends tool subprocesses; and bound the
	// wait for stderr a stray descendant may still hold.
	dsh.OwnProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	id := randomID()
	cmd.Env = append(cli.Environ(), spec.Env...)
	if spec.Bridge != nil {
		cmd.Env = append(cmd.Env, spec.Bridge.launchEnv(id)...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &Process{
		Spec: spec, ID: id, cmd: cmd, stdin: stdin, stdout: stdout,
		stderr: &tailBuffer{max: 16 << 10},
		events: make(chan Event, 1024),
		busy:   map[string]bool{},
		exited: make(chan struct{}),
	}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start dsh: %w", err)
	}
	dsh.Contain(cmd)
	p.conn = jsonrpc.New(stdout, stdin, p)
	go p.conn.Serve(context.Background())
	go func() {
		// Wait must follow the last read from stdout; Serve returns at EOF.
		<-p.conn.Done()
		p.exitErr = cmd.Wait()
		// End what dsh left running (tool subprocesses), and on Windows
		// release its job object.
		dsh.KillTree(cmd)
		p.mu.Lock()
		if p.idle != nil {
			p.idle.Stop()
		}
		p.mu.Unlock()
		if spec.Bridge != nil {
			spec.Bridge.srv.Revoke(id)
		}
		close(p.events)
		close(p.exited)
	}()

	rctx, cancel := context.WithTimeout(ctx, spec.ReadyTimeout)
	defer cancel()
	params := map[string]any{"cwd": spec.Cwd, "provider": spec.Provider, "model": spec.Model}
	if spec.ReasoningEffort != "" {
		params["reasoningEffort"] = spec.ReasoningEffort
	}
	var res struct {
		ServerInfo struct{ Name, Version string } `json:"serverInfo"`
	}
	if err := p.conn.Call(rctx, "initialize", params, &res); err != nil {
		p.kill()
		<-p.exited
		return nil, fmt.Errorf("%w: %v; stderr: %s", ErrNotReady, err, p.stderr.Tail(10))
	}
	p.touch()
	return p, nil
}

// Events delivers runtime notifications in order; closed when the process exits.
func (p *Process) Events() <-chan Event { return p.events }

// Exited is closed when the process has exited.
func (p *Process) Exited() <-chan struct{} { return p.exited }

// ExitErr is the process's exit error once Exited is closed.
func (p *Process) ExitErr() error { return p.exitErr }

// Stderr returns the last lines the runtime wrote to stderr.
func (p *Process) Stderr(lines int) string { return p.stderr.Tail(lines) }

// Pin keeps the process alive regardless of idle time.
func (p *Process) Pin(on bool) {
	p.mu.Lock()
	p.pinned = on
	p.mu.Unlock()
	p.touch()
}

// Prompt queues a user message on session id (created on first use).
func (p *Process) Prompt(ctx context.Context, sessionID string, blocks []any) (messageID string, err error) {
	var res struct {
		MessageID string `json:"messageId"`
	}
	err = p.conn.Call(ctx, "session/prompt", map[string]any{"sessionId": sessionID, "contentBlocks": blocks}, &res)
	p.touch()
	return res.MessageID, err
}

// PromptText is Prompt with one text block.
func (p *Process) PromptText(ctx context.Context, sessionID, text string) (string, error) {
	return p.Prompt(ctx, sessionID, []any{map[string]string{"type": "text", "text": text}})
}

// Wait blocks until the session is idle with no progressing descendants.
func (p *Process) Wait(ctx context.Context, sessionID string) error {
	return p.conn.Call(ctx, "session/wait", map[string]any{"sessionId": sessionID}, nil)
}

// Resume restores a persisted session into this process.
func (p *Process) Resume(ctx context.Context, sessionID string) error {
	return p.conn.Call(ctx, "session/resume", map[string]any{"sessionId": sessionID}, nil)
}

// SetWorkingDirectory sets a session's cwd (one process can serve several
// workspaces of the same profile; validation report section 2).
func (p *Process) SetWorkingDirectory(ctx context.Context, sessionID, path string) (string, error) {
	var res struct {
		Cwd string `json:"cwd"`
	}
	err := p.conn.Call(ctx, "session/working-directory/set", map[string]any{"sessionId": sessionID, "path": path}, &res)
	return res.Cwd, err
}

// Stop asks the runtime to shut down and kills it after grace (DefaultStopGrace when 0).
func (p *Process) Stop(grace time.Duration) error {
	if grace == 0 {
		grace = DefaultStopGrace
	}
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		<-p.exited
		return nil
	}
	p.stopping = true
	p.mu.Unlock()

	select {
	case <-p.exited:
		return nil
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	_ = p.conn.Call(ctx, "shutdown", nil, nil)
	select {
	case <-p.exited:
		return nil
	case <-ctx.Done():
		p.kill()
		<-p.exited
		return fmt.Errorf("dsh did not exit within %s; killed", grace)
	}
}

// kill ends the runtime and its descendants, and closes our pipe ends so the
// reader stops even if a descendant that escaped the tree still holds them.
func (p *Process) kill() {
	if p.cmd.Process != nil {
		dsh.KillTree(p.cmd)
	}
	p.stdin.Close()
	p.stdout.Close()
}

// touch restarts the idle timer unless pinned or a session is running.
func (p *Process) touch() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idle != nil {
		p.idle.Stop()
		p.idle = nil
	}
	if p.pinned || p.stopping || len(p.busy) > 0 || p.Spec.IdleTimeout < 0 {
		return
	}
	p.idle = time.AfterFunc(p.Spec.IdleTimeout, func() { p.Stop(0) })
}

// Request implements jsonrpc.Handler: the SDK server sends no requests.
func (p *Process) Request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "alcove answers no runtime requests: " + method}
}

// Notify implements jsonrpc.Handler.
func (p *Process) Notify(method string, params json.RawMessage) {
	if method == "session.status" {
		var st struct {
			SessionID string `json:"sessionId"`
			Status    string `json:"status"`
		}
		if json.Unmarshal(params, &st) == nil {
			p.mu.Lock()
			key := st.SessionID
			if st.Status == "running" {
				p.busy[key] = true
			} else {
				delete(p.busy, key)
			}
			p.mu.Unlock()
		}
	}
	p.touch()
	select {
	case p.events <- Event{Method: method, Params: params}:
	default:
		// A UI that stops reading must not stall the runtime; the session
		// log on disk stays complete.
	}
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(b), nil
}

func (t *tailBuffer) Tail(lines int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ls := strings.Split(strings.TrimRight(string(bytes.TrimSpace(t.buf)), "\n"), "\n")
	if len(ls) > lines {
		ls = ls[len(ls)-lines:]
	}
	return strings.Join(ls, "\n")
}
