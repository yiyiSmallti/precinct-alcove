package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
)

// ErrLocalDisabled is returned while policy executors.local.enabled is false
// (PRD F7): no entry point may start a runtime.
var ErrLocalDisabled = errors.New("local execution is disabled by policy")

// Key identifies one runtime: a profile run in a directory. Workspaces have
// one each (PRD 3.5); plugins with background ensure their own.
type Key struct {
	Profile string
	Cwd     string
}

// Manager starts runtimes on demand and keeps at most one per Key.
type Manager struct {
	CLI    *dsh.CLI
	Bridge *Bridge
	// LocalEnabled reports policy executors.local.enabled; nil means enabled.
	LocalEnabled func() bool
	// OnEvent receives every runtime notification; nil drops them.
	OnEvent func(runtimeID string, e Event)
	// OnExit is called when a runtime exits, idle or not.
	OnExit func(k Key, runtimeID string, err error)

	mu       sync.Mutex
	procs    map[Key]*Process
	starting map[Key]chan struct{}
	pinned   map[Key]bool
}

// Info describes a running runtime for the runtime panel.
type Info struct {
	Key
	ID      string
	Started time.Time
	Pinned  bool
	// Busy is set while a session (or a subagent's) is running.
	Busy bool
	// IdleExit is when the runtime exits if left idle; zero while it is
	// pinned or busy.
	IdleExit time.Time
}

// Pin keeps the runtime for k up however long it is idle, now and each
// time it is started again, until unpinned (PRD 3.5).
func (m *Manager) Pin(k Key, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pinned == nil {
		m.pinned = map[Key]bool{}
	}
	if on {
		m.pinned[k] = true
	} else {
		delete(m.pinned, k)
	}
	if p := m.procs[k]; p != nil {
		p.Pin(on)
	}
}

// Ensure returns the runtime for spec's profile and cwd, starting it if
// needed. Concurrent calls for the same Key share one start.
func (m *Manager) Ensure(ctx context.Context, spec LaunchSpec) (*Process, error) {
	if m.LocalEnabled != nil && !m.LocalEnabled() {
		return nil, ErrLocalDisabled
	}
	k := Key{spec.Profile, spec.Cwd}
	for {
		m.mu.Lock()
		if m.procs == nil {
			m.procs, m.starting = map[Key]*Process{}, map[Key]chan struct{}{}
		}
		if p := m.procs[k]; p != nil {
			select {
			case <-p.Exited():
				// Stopped or crashed, and watch has not evicted it yet.
				delete(m.procs, k)
			default:
				m.mu.Unlock()
				return p, nil
			}
		}
		wait, busy := m.starting[k]
		if !busy {
			done := make(chan struct{})
			m.starting[k] = done
			m.mu.Unlock()
			if spec.Bridge == nil {
				spec.Bridge = m.Bridge
			}
			p, err := Launch(ctx, m.CLI, spec)
			m.mu.Lock()
			delete(m.starting, k)
			if err == nil {
				m.procs[k] = p
				if m.pinned[k] {
					p.Pin(true)
				}
			}
			m.mu.Unlock()
			close(done)
			if err != nil {
				return nil, err
			}
			go m.watch(k, p)
			return p, nil
		}
		m.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (m *Manager) watch(k Key, p *Process) {
	for e := range p.Events() {
		if m.OnEvent != nil {
			m.OnEvent(p.ID, e)
		}
	}
	<-p.Exited()
	m.mu.Lock()
	if m.procs[k] == p {
		delete(m.procs, k)
	}
	m.mu.Unlock()
	if m.OnExit != nil {
		m.OnExit(k, p.ID, p.ExitErr())
	}
}

// Get returns the running runtime for k, or nil.
func (m *Manager) Get(k Key) *Process {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.procs[k]
}

// List returns running runtimes ordered by profile, then directory.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.procs))
	for k, p := range m.procs {
		out = append(out, Info{Key: k, ID: p.ID, Started: p.Started, Pinned: p.Pinned(), Busy: p.Busy(), IdleExit: p.IdleExit()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].Cwd < out[j].Cwd
	})
	return out
}

// Keys returns the keys of running runtimes and of those still starting.
func (m *Manager) Keys() []Key {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]Key, 0, len(m.procs)+len(m.starting))
	for k := range m.procs {
		keys = append(keys, k)
	}
	for k := range m.starting {
		if m.procs[k] == nil {
			keys = append(keys, k)
		}
	}
	return keys
}

// Stop stops the runtime for k, if any. A start still in progress for k is
// waited for and then stopped, so nothing for k is left running on return.
func (m *Manager) Stop(k Key) error {
	m.mu.Lock()
	for {
		wait, busy := m.starting[k]
		if !busy {
			break
		}
		m.mu.Unlock()
		<-wait
		m.mu.Lock()
	}
	p := m.procs[k]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	err := p.Stop(DefaultStopGrace)
	<-p.Exited()
	m.mu.Lock()
	if m.procs[k] == p {
		delete(m.procs, k)
	}
	m.mu.Unlock()
	return err
}

// StopAll stops every runtime, in parallel; used on quit and when policy
// turns local execution off.
func (m *Manager) StopAll() {
	keys := m.Keys()
	var wg sync.WaitGroup
	for _, k := range keys {
		wg.Add(1)
		go func(k Key) { defer wg.Done(); m.Stop(k) }(k)
	}
	wg.Wait()
}

// String is for logs.
func (k Key) String() string { return fmt.Sprintf("%s@%s", k.Profile, k.Cwd) }
