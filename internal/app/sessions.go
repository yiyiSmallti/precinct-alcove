package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// Plugin sessions (session.prompt / session.cancel, workspace_session
// capability): a plugin runs its own sessions in a workspace it owns
// (workspace.attach), e.g. the cloister plugin's local tasks from the org
// bot. The workspace's runtime starts the normal way (launch hooks, the
// owner's model route), so approvals and questions reach the user on that
// workspace like any other. Once a session is idle Alcove tells the plugin
// (onSessionIdle) with the last assistant text.

// pluginSessionID is what a plugin may name its sessions: never a
// workspace's own conversation ("main-…").
var pluginSessionID = regexp.MustCompile(`^task-[A-Za-z0-9._:-]{1,120}$`)

type pluginSession struct {
	plugin, ws, id string
	runtime        string
	view           *events.Session
	waiting        bool
	gen            int // prompts sent; a watcher waits again when it moved
}

type pluginSessions struct {
	mu   sync.Mutex
	byID map[string]*pluginSession // ws + "\x00" + session id
}

func (ps *pluginSessions) get(ws, id string) *pluginSession {
	if ps.byID == nil {
		ps.byID = map[string]*pluginSession{}
	}
	return ps.byID[ws+"\x00"+id]
}

// ownedBy returns the workspace if plugin owns it.
func (a *App) ownedBy(plugin, wsID string) error {
	w, err := a.Workspaces.Find(wsID)
	if err != nil {
		return err
	}
	if w.OwnerName() != plugin {
		return fmt.Errorf("%w: workspace %s does not belong to %s", sidecar.ErrNotPermitted, wsID, plugin)
	}
	return nil
}

// promptForPlugin queues text on the plugin's session in its workspace,
// starting the workspace's runtime if needed, and returns the message id.
func (a *App) promptForPlugin(ctx context.Context, plugin, wsID, sessionID, text string) (string, error) {
	if !pluginSessionID.MatchString(sessionID) {
		return "", fmt.Errorf("%w: plugin session ids look like task-<id>", sidecar.ErrNotPermitted)
	}
	if err := a.ownedBy(plugin, wsID); err != nil {
		return "", err
	}
	if w, err := a.Workspaces.Find(wsID); err == nil && a.Runtimes.Get(alrt.Key{Profile: w.Profile, Cwd: w.Dir}) == nil {
		// session.cancel needs bridge 0.1.1; profiles made before it get
		// the shipped bridge while no runtime of theirs is running.
		if _, err := a.Profiles.RefreshBridge(ctx, w.Profile); err != nil {
			a.pluginLog("host", "warn", fmt.Sprintf("workspace %s: updating the bridge: %v", wsID, err))
		}
	}
	p, err := a.OpenWorkspace(ctx, wsID)
	if err != nil {
		return "", err
	}
	// A detach between the check above and the open gives a runtime on
	// another route: never send the plugin's task there.
	if p.Spec.Owner != plugin {
		return "", fmt.Errorf("%w: workspace %s no longer belongs to %s", sidecar.ErrNotPermitted, wsID, plugin)
	}
	a.sessions.mu.Lock()
	s := a.sessions.get(wsID, sessionID)
	fresh := s == nil || s.runtime != p.ID || s.plugin != plugin
	if fresh {
		// A new owner's session of the same id is a different session.
		s = &pluginSession{plugin: plugin, ws: wsID, id: sessionID, runtime: p.ID, view: events.NewSession(sessionID)}
		a.sessions.byID[wsID+"\x00"+sessionID] = s
	}
	a.sessions.mu.Unlock()
	if fresh {
		// A session persisted by an earlier runtime continues; a new one is
		// created by the prompt.
		p.Resume(ctx, sessionID)
	}
	msg, err := p.PromptText(ctx, sessionID, text)
	if err != nil {
		return "", err
	}
	a.sessions.mu.Lock()
	s.gen++
	watch := !s.waiting
	s.waiting = true
	a.sessions.mu.Unlock()
	if watch {
		go a.watchSession(p, s)
	}
	return msg, nil
}

// watchSession waits for the session to go idle and tells the plugin. A
// prompt that arrived while it waited is waited for too.
func (a *App) watchSession(p *alrt.Process, s *pluginSession) {
	ctx := context.Background()
	for {
		a.sessions.mu.Lock()
		gen := s.gen
		a.sessions.mu.Unlock()
		err := p.Wait(ctx, s.id)
		if err == nil {
			a.settle(s)
		}
		a.sessions.mu.Lock()
		if err == nil && s.gen != gen {
			a.sessions.mu.Unlock()
			continue
		}
		s.waiting = false
		text, failed := lastAnswer(s.view)
		a.sessions.mu.Unlock()
		ev := map[string]any{"workspaceId": s.ws, "sessionId": s.id, "text": text}
		switch {
		case err != nil:
			ev["error"] = "the session did not finish: " + err.Error()
		case failed != "":
			ev["error"] = failed
		}
		a.Sidecars.Notify(s.plugin, "onSessionIdle", ev)
		return
	}
}

// settle gives the event goroutine time to apply the turn's last events:
// session/wait is answered on the connection's reader, notifications are
// applied on another goroutine.
func (a *App) settle(s *pluginSession) {
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		a.sessions.mu.Lock()
		running := s.view.Running()
		a.sessions.mu.Unlock()
		if !running {
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
}

// lastAnswer is the last turn's final assistant text, or why it ended
// otherwise (a notice after the last assistant text).
func lastAnswer(v *events.Session) (text, failed string) {
	if v == nil {
		return "", ""
	}
	for i := len(v.Entries) - 1; i >= 0; i-- {
		e := v.Entries[i]
		switch e.Kind {
		case events.Notice:
			if failed == "" {
				failed = e.Text
			}
		case events.Assistant:
			return e.Text, failed
		case events.User:
			return "", failed
		}
	}
	return "", failed
}

// cancelForPlugin cancels the plugin session's running turn (and drops what
// is queued). A session that is not running is fine.
func (a *App) cancelForPlugin(ctx context.Context, plugin, wsID, sessionID string) (bool, error) {
	if err := a.ownedBy(plugin, wsID); err != nil {
		return false, err
	}
	a.sessions.mu.Lock()
	s := a.sessions.get(wsID, sessionID)
	var rt string
	if s != nil && s.plugin == plugin {
		rt = s.runtime
	}
	a.sessions.mu.Unlock()
	if rt == "" || !a.Bridge.Attached(rt) {
		return false, nil
	}
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := a.Bridge.Call(ctx, rt, "session.cancel", map[string]any{"sessionId": sessionID}, &out); err != nil {
		return false, errors.New("cannot cancel the session: " + err.Error())
	}
	return out.Cancelled, nil
}

// tapEvent feeds runtime notifications to the plugin sessions they belong
// to.
func (a *App) tapEvent(runtimeID string, e alrt.Event) {
	id := events.SessionOf(e.Params)
	if id == "" || !pluginSessionID.MatchString(id) {
		return
	}
	a.sessions.mu.Lock()
	defer a.sessions.mu.Unlock()
	for _, s := range a.sessions.byID {
		if s.runtime == runtimeID && s.id == id {
			s.view.Apply(e.Method, e.Params)
		}
	}
}

// RuntimeWorkspace is the workspace a runtime was opened for, if any (the
// UI uses it for approvals of runtimes a plugin started).
func (a *App) RuntimeWorkspace(runtimeID string) string {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	if l := a.life[runtimeID]; l != nil && !l.closed {
		return l.ws.ID
	}
	return ""
}
