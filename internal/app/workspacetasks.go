package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// Workspace tasks (task.start, workspace_task capability): a plugin asks to
// run a task in one of the user's own workspaces, e.g. the daily assistant
// handing off work that needs files or commands. Alcove asks the user, who
// picks the workspace or declines; the plugin never chooses. An agreed task
// runs as the plugin's session task-<id> there, on the workspace's own
// model and rules, and from then on the plugin may prompt and cancel that
// one session (a grant), as it may in workspaces it owns. The session id
// carries the plugin (TaskSessionID), so two plugins' tasks of the same id
// never share a conversation.

// maxTaskGrants bounds the grants kept; the oldest go first.
const maxTaskGrants = 200

// TaskGrant is one task the user let a plugin run in a workspace of theirs.
type TaskGrant struct {
	Plugin    string    `json:"plugin"`
	Workspace string    `json:"workspace"`
	Session   string    `json:"session"`
	At        time.Time `json:"at"`
	// Unstarted is set until the task's first prompt was taken: a start
	// that failed is tried again when the plugin asks again, without
	// asking the user again.
	Unstarted bool `json:"unstarted,omitempty"`
}

// TaskSessionID is the session a plugin's task id runs as: task-<plugin
// tag>-<id>, the tag a short hash of the plugin's name.
func TaskSessionID(plugin, id string) string {
	sum := sha256.Sum256([]byte(plugin))
	return "task-" + hex.EncodeToString(sum[:4]) + "-" + id
}

func (a *App) loadGrants() {
	a.grantsMu.Lock()
	defer a.grantsMu.Unlock()
	if a.grants != nil {
		return
	}
	a.grants = []TaskGrant{}
	b, err := os.ReadFile(a.Layout.TaskGrants())
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &a.grants); err != nil {
		a.pluginLog("host", "warn", "reading the workspace task grants: "+err.Error())
		a.grants = []TaskGrant{}
	}
}

// grantFor returns plugin's grant for session, if any.
func (a *App) grantFor(plugin, session string) (TaskGrant, bool) {
	a.loadGrants()
	a.grantsMu.Lock()
	defer a.grantsMu.Unlock()
	for _, g := range a.grants {
		if g.Plugin == plugin && g.Session == session {
			return g, true
		}
	}
	return TaskGrant{}, false
}

// addGrant records g, replacing an earlier grant of the same session.
func (a *App) addGrant(g TaskGrant) error {
	a.loadGrants()
	a.grantsMu.Lock()
	defer a.grantsMu.Unlock()
	kept := a.grants[:0]
	for _, o := range a.grants {
		if o.Plugin != g.Plugin || o.Session != g.Session {
			kept = append(kept, o)
		}
	}
	a.grants = append(kept, g)
	if n := len(a.grants) - maxTaskGrants; n > 0 {
		a.grants = a.grants[n:]
	}
	return a.saveGrantsLocked()
}

func (a *App) saveGrantsLocked() error {
	b, err := json.MarshalIndent(a.grants, "", " ")
	if err != nil {
		return err
	}
	path := a.Layout.TaskGrants()
	os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SessionSource is the plugin that started a session of a workspace: the
// workspace's owner for its task- sessions, or the plugin the user let run
// that task there; "" for the user's own.
func (a *App) SessionSource(wsID, ownerName, session string) string {
	if !pluginSessionID.MatchString(session) {
		return ""
	}
	if ownerName != workspace.Personal {
		return ownerName
	}
	a.loadGrants()
	a.grantsMu.Lock()
	defer a.grantsMu.Unlock()
	for _, g := range a.grants {
		if g.Workspace == wsID && g.Session == session {
			return g.Plugin
		}
	}
	return ""
}

// startTask is the sidecars' task.start.
func (a *App) startTask(ctx context.Context, plugin string, req sidecar.TaskRequest) (sidecar.TaskStart, error) {
	sid := TaskSessionID(plugin, req.ID)
	if !pluginSessionID.MatchString(sid) {
		return sidecar.TaskStart{}, fmt.Errorf("%w: a task id may hold letters, digits and . _ : - only", sidecar.ErrNotPermitted)
	}
	// Asked again after the plugin restarted: the answer stands, and a
	// start that failed is tried again.
	if g, ok := a.grantFor(plugin, sid); ok {
		if g.Unstarted {
			return a.firstPrompt(ctx, g, req.Prompt)
		}
		return sidecar.TaskStart{Accepted: true, WorkspaceID: g.Workspace, SessionID: sid}, nil
	}
	if a.cfg.ConfirmTask == nil {
		return sidecar.TaskStart{Reason: "this Alcove cannot ask the user"}, nil
	}
	ws, ok := a.cfg.ConfirmTask(ctx, plugin, req)
	if !ok {
		reason := "the user declined"
		if ctx.Err() != nil {
			reason = "the request was withdrawn"
		}
		return sidecar.TaskStart{Reason: reason}, nil
	}
	w, err := a.Workspaces.Find(ws)
	if err != nil {
		return sidecar.TaskStart{}, err
	}
	if w.OwnerName() != workspace.Personal {
		return sidecar.TaskStart{}, errors.New("tasks run only in the user's own workspaces")
	}
	g := TaskGrant{Plugin: plugin, Workspace: ws, Session: sid, At: time.Now().UTC(), Unstarted: true}
	if err := a.addGrant(g); err != nil {
		return sidecar.TaskStart{}, fmt.Errorf("recording the task: %w", err)
	}
	return a.firstPrompt(ctx, g, req.Prompt)
}

// firstPrompt sends a granted task its prompt and records that it started.
func (a *App) firstPrompt(ctx context.Context, g TaskGrant, prompt string) (sidecar.TaskStart, error) {
	// The plugin's request may end with the user's answer; the prompt is
	// Alcove's to finish.
	if _, err := a.promptForPlugin(context.WithoutCancel(ctx), g.Plugin, g.Workspace, g.Session, prompt); err != nil {
		return sidecar.TaskStart{}, fmt.Errorf("starting the task: %w", err)
	}
	g.Unstarted = false
	if err := a.addGrant(g); err != nil {
		a.pluginLog(g.Plugin, "warn", "recording that task "+g.Session+" started: "+err.Error())
	}
	return sidecar.TaskStart{Accepted: true, WorkspaceID: g.Workspace, SessionID: g.Session}, nil
}
