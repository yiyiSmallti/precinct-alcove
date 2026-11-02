// Package sidecar runs plugin sidecars and speaks host extension protocol v1
// with them (SPEC 3.2): JSON-RPC 2.0 over the sidecar's stdio, a hello/ready
// handshake, host methods gated by the capabilities the user granted at
// install, and crash rate limiting.
//
// A sidecar that exits on its own is a crash. It is restarted with backoff
// (1 s, 2 s, 4 s ...); after MaxCrashes crashes inside CrashWindow it stays
// down in state Crashed until the user starts it again.
package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
	"github.com/yiyiSmallti/precinct-alcove/internal/jsonrpc"
)

// Protocol is the host extension protocol version this host speaks.
const Protocol = 1

// Defaults for Manager's zero fields.
const (
	DefaultHelloTimeout = 10 * time.Second
	DefaultStopTimeout  = 3 * time.Second
	DefaultHookTimeout  = 3 * time.Second
	DefaultMaxCrashes   = 3
	DefaultCrashWindow  = 10 * time.Minute
	DefaultBackoff      = time.Second
)

// State is a sidecar's lifecycle state.
type State string

const (
	Starting State = "starting"
	Running  State = "running"
	Backoff  State = "backoff" // crashed, waiting to restart
	Crashed  State = "crashed" // crash limit reached; stays down until Start
	Stopped  State = "stopped"
)

// Spec says how to run one plugin's sidecar.
type Spec struct {
	Name    string
	Version string
	Exe     string   // absolute path
	Caps    []string // granted capabilities
}

// Status is what the plugin page shows for a sidecar.
type Status struct {
	State     State
	Crashes   int // inside the crash window
	LastError string
	Stderr    string // last lines the sidecar wrote to stderr
}

// Manager runs sidecars. Fill the exported fields before the first Start.
type Manager struct {
	AlcoveVersion string
	DSHVersion    string
	Services      Services
	// Settings backs settings.read/write; nil makes both unavailable.
	Settings *Settings
	// Env is added to every sidecar's environment.
	Env []string

	HelloTimeout time.Duration
	StopTimeout  time.Duration
	HookTimeout  time.Duration
	MaxCrashes   int
	CrashWindow  time.Duration
	Backoff      time.Duration

	// OnState is called (without locks held) on every state change.
	OnState func(name string, st Status)

	mu      sync.Mutex
	plugins map[string]*plugin
}

type plugin struct {
	m    *Manager
	name string // fixed for the plugin's lifetime

	// spec and caps change only in Start, under mu.
	spec Spec
	caps map[string]bool

	mu         sync.Mutex
	state      State
	crashes    []time.Time
	lastErr    string
	proc       *proc
	hook       bool            // registered a launch hook
	ui         map[string]bool // contribution ids it registered
	stopping   bool
	generation int // bumps on every Start/Stop so stale supervisors give up

	evMu   sync.Mutex
	evq    []func() // ordered notifications waiting to be written
	evBusy bool
}

type proc struct {
	cmd    *exec.Cmd
	conn   *jsonrpc.Conn
	stdin  io.Closer
	stdout io.Closer
	stderr *tail
	exited chan struct{}
}

// Errors.
var (
	ErrNotRunning = errors.New("sidecar is not running")
	ErrHandshake  = errors.New("sidecar handshake failed")
)

func (m *Manager) init() {
	if m.plugins == nil {
		m.plugins = map[string]*plugin{}
	}
}

func (m *Manager) helloTimeout() time.Duration { return or(m.HelloTimeout, DefaultHelloTimeout) }
func (m *Manager) stopTimeout() time.Duration  { return or(m.StopTimeout, DefaultStopTimeout) }
func (m *Manager) hookTimeout() time.Duration  { return or(m.HookTimeout, DefaultHookTimeout) }
func (m *Manager) crashWindow() time.Duration  { return or(m.CrashWindow, DefaultCrashWindow) }
func (m *Manager) backoff() time.Duration      { return or(m.Backoff, DefaultBackoff) }
func (m *Manager) maxCrashes() int {
	if m.MaxCrashes > 0 {
		return m.MaxCrashes
	}
	return DefaultMaxCrashes
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Start launches the sidecar and waits for its handshake. Starting a crashed
// sidecar clears its crash history (the user asked for it). A failed
// handshake counts as a crash and is retried like one; Start still reports it.
func (m *Manager) Start(ctx context.Context, spec Spec) error {
	m.mu.Lock()
	m.init()
	p := m.plugins[spec.Name]
	if p == nil {
		p = &plugin{m: m, name: spec.Name}
		m.plugins[spec.Name] = p
	}
	m.mu.Unlock()

	p.mu.Lock()
	if p.state == Running || p.state == Starting || p.state == Backoff {
		p.mu.Unlock()
		return fmt.Errorf("%s: sidecar already %s", spec.Name, p.state)
	}
	p.spec = spec
	p.caps = map[string]bool{}
	for _, c := range spec.Caps {
		p.caps[c] = true
	}
	p.crashes = nil
	p.lastErr = ""
	p.stopping = false
	p.generation++
	gen := p.generation
	p.mu.Unlock()
	return p.launch(ctx, gen)
}

// launch runs one attempt; on failure it hands over to the crash path.
func (p *plugin) launch(ctx context.Context, gen int) error {
	p.setState(Starting, "")
	pr, err := p.spawn()
	if err == nil {
		// Published before the handshake so Stop can end a start in progress.
		p.mu.Lock()
		if gen == p.generation && !p.stopping {
			p.proc = pr
		}
		p.mu.Unlock()
		err = p.handshake(ctx, pr)
		if err != nil {
			pr.kill()
		}
	}
	p.mu.Lock()
	if gen != p.generation || p.stopping {
		// Stopped while starting: drop the process and anything the
		// handshake registered after Stop's cleanup.
		if p.proc == pr {
			p.proc = nil
		}
		p.mu.Unlock()
		if pr != nil {
			pr.kill()
			<-pr.exited
		}
		p.cleanup()
		return ErrNotRunning
	}
	if err == nil {
		p.mu.Unlock()
		p.setState(Running, "")
		go p.supervise(pr, gen)
		return nil
	}
	if p.proc == pr {
		p.proc = nil
	}
	p.mu.Unlock()
	if pr != nil {
		<-pr.exited
		err = fmt.Errorf("%w; stderr: %s", err, pr.stderr.Tail(5))
	}
	p.crashed(gen, err)
	return err
}

func (p *plugin) spawn() (*proc, error) {
	spec := p.specCopy()
	cmd := exec.Command(spec.Exe)
	cmd.Dir = filepath.Dir(spec.Exe)
	cmd.Env = append(os.Environ(), p.m.Env...)
	dsh.OwnProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	pr := &proc{cmd: cmd, stdin: stdin, stdout: stdout, stderr: &tail{max: 16 << 10}, exited: make(chan struct{})}
	cmd.Stderr = pr.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sidecar: %w", err)
	}
	dsh.Contain(cmd)
	pr.conn = jsonrpc.New(stdout, stdin, &api{p: p})
	go pr.conn.Serve(context.Background())
	go func() {
		// Watch the process itself, not stdout: a child that inherited
		// stdout would keep the connection open after the sidecar died.
		// Wait closes our end of the stdout pipe once the process exits;
		// WaitDelay bounds the stderr copy.
		cmd.Wait()
		pr.kill() // descendants, and both pipe ends
		<-pr.conn.Done()
		close(pr.exited)
	}()
	return pr, nil
}

type hello struct {
	Protocol      int      `json:"protocol"`
	AlcoveVersion string   `json:"alcoveVersion"`
	DSHVersion    string   `json:"dshVersion"`
	GrantedCaps   []string `json:"grantedCaps"`
}

type ready struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	UIContributions []json.RawMessage `json:"uiContributions"`
}

func (p *plugin) handshake(ctx context.Context, pr *proc) error {
	ctx, cancel := context.WithTimeout(ctx, p.m.helloTimeout())
	defer cancel()
	caps := append([]string(nil), p.specCopy().Caps...)
	sort.Strings(caps)
	var r ready
	err := pr.conn.Call(ctx, "hello", hello{Protocol: Protocol, AlcoveVersion: p.m.AlcoveVersion, DSHVersion: p.m.DSHVersion, GrantedCaps: caps}, &r)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrHandshake, err)
	}
	if r.Name != p.name {
		return fmt.Errorf("%w: sidecar says it is %q, package is %q", ErrHandshake, r.Name, p.name)
	}
	for _, c := range r.UIContributions {
		if err := p.register(c); err != nil {
			return fmt.Errorf("%w: ui contribution: %v", ErrHandshake, err)
		}
	}
	return nil
}

// supervise waits for the process to exit and treats an unrequested exit as
// a crash.
func (p *plugin) supervise(pr *proc, gen int) {
	<-pr.exited
	p.mu.Lock()
	if p.proc == pr {
		p.proc = nil
	}
	expected := p.stopping || gen != p.generation
	p.mu.Unlock()
	p.cleanup()
	if expected {
		return
	}
	err := fmt.Errorf("sidecar exited: %v; stderr: %s", pr.cmd.ProcessState, pr.stderr.Tail(5))
	p.crashed(gen, err)
}

// crashed records a crash and either schedules a restart or gives up.
func (p *plugin) crashed(gen int, cause error) {
	now := time.Now()
	p.mu.Lock()
	if gen != p.generation || p.stopping {
		p.mu.Unlock()
		return
	}
	keep := p.crashes[:0]
	for _, t := range p.crashes {
		if now.Sub(t) < p.m.crashWindow() {
			keep = append(keep, t)
		}
	}
	p.crashes = append(keep, now)
	n := len(p.crashes)
	p.mu.Unlock()
	p.cleanup()
	if n >= p.m.maxCrashes() {
		if !p.setStateFor(gen, Crashed, cause.Error()) {
			return
		}
		p.log("error", fmt.Sprintf("sidecar crashed %d times in %s; not restarting: %v", n, p.m.crashWindow(), cause))
		return
	}
	if !p.setStateFor(gen, Backoff, cause.Error()) {
		return // stopped or restarted during cleanup
	}
	p.log("warn", fmt.Sprintf("sidecar crashed (%d/%d): %v", n, p.m.maxCrashes(), cause))
	delay := p.m.backoff() << (n - 1)
	time.AfterFunc(delay, func() {
		p.mu.Lock()
		ok := gen == p.generation && !p.stopping && p.state == Backoff
		p.mu.Unlock()
		if ok {
			p.launch(context.Background(), gen)
		}
	})
}

// cleanup drops what the sidecar registered while it ran.
func (p *plugin) cleanup() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.ui))
	for id := range p.ui {
		ids = append(ids, id)
	}
	p.ui = nil
	p.hook = false
	p.mu.Unlock()
	if s := p.m.Services.UI; s != nil {
		for _, id := range ids {
			s.Unregister(p.name, id)
		}
	}
}

func (p *plugin) setState(st State, errText string) {
	p.setStateFor(-1, st, errText)
}

// setStateFor sets the state only while generation gen is current and not
// stopping (gen -1: always), checked under the same lock as the update.
func (p *plugin) setStateFor(gen int, st State, errText string) bool {
	p.mu.Lock()
	if gen >= 0 && (gen != p.generation || p.stopping) {
		p.mu.Unlock()
		return false
	}
	p.state = st
	if errText != "" {
		p.lastErr = errText
	}
	status := p.statusLocked()
	p.mu.Unlock()
	if p.m.OnState != nil {
		p.m.OnState(p.name, status)
	}
	return true
}

func (p *plugin) statusLocked() Status {
	s := Status{State: p.state, Crashes: len(p.crashes), LastError: p.lastErr}
	if p.proc != nil {
		s.Stderr = p.proc.stderr.Tail(10)
	}
	return s
}

// Stop asks the sidecar to shut down, then ends its process tree after
// StopTimeout. It also cancels a pending restart.
func (m *Manager) Stop(ctx context.Context, name string) error {
	p := m.get(name)
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.stopping = true
	p.generation++
	pr := p.proc
	p.proc = nil
	p.mu.Unlock()
	if pr != nil {
		sctx, cancel := context.WithTimeout(ctx, m.stopTimeout())
		pr.conn.Call(sctx, "shutdown", nil, nil)
		select {
		case <-pr.exited:
		case <-sctx.Done():
		}
		cancel()
		pr.kill()
		<-pr.exited
	}
	p.cleanup()
	p.setState(Stopped, "")
	return nil
}

// StopAll stops every sidecar, in parallel.
func (m *Manager) StopAll(ctx context.Context) {
	m.mu.Lock()
	var names []string
	for n := range m.plugins {
		names = append(names, n)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Add(1)
		go func(n string) { defer wg.Done(); m.Stop(ctx, n) }(n)
	}
	wg.Wait()
}

// Status reports a sidecar's state; ok is false for an unknown name.
func (m *Manager) Status(name string) (Status, bool) {
	p := m.get(name)
	if p == nil {
		return Status{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statusLocked(), true
}

// PID is the running sidecar's process id, or 0.
func (m *Manager) PID(name string) int {
	p := m.get(name)
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc == nil || p.proc.cmd.Process == nil {
		return 0
	}
	return p.proc.cmd.Process.Pid
}

func (m *Manager) get(name string) *plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.plugins == nil {
		return nil
	}
	return m.plugins[name]
}

// running returns running plugins holding cap (any when cap is ""), by name.
func (m *Manager) running(cap string) []*plugin {
	m.mu.Lock()
	var out []*plugin
	for _, p := range m.plugins {
		out = append(out, p)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].spec.Name < out[j].spec.Name })
	keep := out[:0]
	for _, p := range out {
		p.mu.Lock()
		ok := p.state == Running && p.proc != nil && (cap == "" || p.caps[cap])
		p.mu.Unlock()
		if ok {
			keep = append(keep, p)
		}
	}
	return keep
}

func (p *plugin) specCopy() Spec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spec
}

// has reports whether cap was granted.
func (p *plugin) has(cap string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caps[cap]
}

func (p *plugin) conn() *jsonrpc.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc == nil {
		return nil
	}
	return p.proc.conn
}

func (pr *proc) kill() {
	if pr.cmd.Process != nil {
		dsh.KillTree(pr.cmd)
	}
	pr.stdin.Close()
	pr.stdout.Close()
}

func (p *plugin) log(level, msg string) {
	if l := p.m.Services.Log; l != nil {
		l(p.name, level, msg)
	}
}

// tail keeps the last max bytes written to it.
type tail struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, b...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(b), nil
}

func (t *tail) Tail(lines int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := strings.Split(strings.TrimRight(string(t.b), "\n"), "\n")
	if len(l) > lines {
		l = l[len(l)-lines:]
	}
	return strings.Join(l, "\n")
}
