package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/yiyiSmallti/precinct-alcove/internal/jsonrpc"
)

// Error codes the host answers sidecar calls with (SPEC 3.2).
const (
	CodeNotGranted  = -32030 // the capability or namespace was not granted
	CodeUnavailable = -32031 // the host does not offer this service (yet)
)

// Services are the parts of Alcove the sidecar API reaches. A nil field makes
// the matching methods answer CodeUnavailable.
type Services struct {
	Workspaces WorkspaceSource
	UI         UISink
	// Inbox shows an item in the general conversation view (inbox cap).
	Inbox func(plugin string, item json.RawMessage) error
	// EnsureRuntime starts dsh for profile in cwd if it is not running
	// (background cap).
	EnsureRuntime func(ctx context.Context, plugin, profile, cwd string) error
	// KeychainRef returns an opaque reference to a stored credential.
	KeychainRef func(plugin, name string) (string, error)
	// Log writes a line to the plugin's log.
	Log func(plugin, level, msg string)
}

// Workspace is what sidecars see of a workspace.
type Workspace struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Profile string `json:"profile"`
}

// WorkspaceSource lists workspaces.
type WorkspaceSource interface {
	List(ctx context.Context) ([]Workspace, error)
	Get(ctx context.Context, id string) (*Workspace, error)
}

// UISink receives declarative UI contributions (schema v1). It renders them;
// the sidecar package only checks ownership and capabilities.
type UISink interface {
	Register(plugin string, c Contribution) error
	Update(plugin, id string, data json.RawMessage) error
	Unregister(plugin, id string)
}

// Contribution is one declarative UI element. Raw is the whole object.
type Contribution struct {
	ID   string `json:"id"`
	Type string `json:"type"` // settings_page | sidebar_item | card
	Raw  json.RawMessage
}

// contributionCaps is the capability each contribution type needs.
var contributionCaps = map[string]string{
	"settings_page": "settings_page",
	"sidebar_item":  "sidebar_item",
	"card":          "inbox",
}

// methodCaps gates host methods; methods not listed need no capability.
var methodCaps = map[string]string{
	"runtime.registerLaunchHook": "runtime_launch_hook",
	"runtime.ensure":             "background",
	"inbox.post":                 "inbox",
}

type api struct{ p *plugin }

func notGranted(format string, a ...any) error {
	return &jsonrpc.Error{Code: CodeNotGranted, Message: fmt.Sprintf(format, a...)}
}

func unavailable(method string) error {
	return &jsonrpc.Error{Code: CodeUnavailable, Message: method + " is not available in this host"}
}

func invalid(err error) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
}

func (a *api) Request(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	p := a.p
	svc := p.m.Services
	if c, ok := methodCaps[method]; ok && !p.has(c) {
		return nil, notGranted("%s needs the %s capability", method, c)
	}
	switch method {
	case "workspace.list":
		if svc.Workspaces == nil {
			return nil, unavailable(method)
		}
		ws, err := svc.Workspaces.List(ctx)
		if ws == nil {
			ws = []Workspace{}
		}
		return ws, err
	case "workspace.get":
		var q struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &q); err != nil || q.ID == "" {
			return nil, invalid(errors.New("workspace.get needs id"))
		}
		if svc.Workspaces == nil {
			return nil, unavailable(method)
		}
		return svc.Workspaces.Get(ctx, q.ID)
	case "runtime.registerLaunchHook":
		p.mu.Lock()
		p.hook = true
		p.mu.Unlock()
		return map[string]any{}, nil
	case "runtime.ensure":
		var q struct{ Profile, Cwd string }
		if err := json.Unmarshal(raw, &q); err != nil || q.Profile == "" {
			return nil, invalid(errors.New("runtime.ensure needs profile and cwd"))
		}
		if svc.EnsureRuntime == nil {
			return nil, unavailable(method)
		}
		return map[string]any{}, svc.EnsureRuntime(ctx, p.name, q.Profile, q.Cwd)
	case "ui.register":
		return map[string]any{}, p.register(raw)
	case "ui.update":
		var q struct {
			ID   string          `json:"id"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &q); err != nil || q.ID == "" {
			return nil, invalid(errors.New("ui.update needs id"))
		}
		p.mu.Lock()
		own := p.ui[q.ID]
		p.mu.Unlock()
		if !own {
			return nil, notGranted("ui contribution %q was not registered by this plugin", q.ID)
		}
		if svc.UI == nil {
			return nil, unavailable(method)
		}
		return map[string]any{}, svc.UI.Update(p.name, q.ID, q.Data)
	case "settings.read", "settings.write":
		var q struct {
			NS    string          `json:"ns"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(raw, &q); err != nil || q.NS == "" {
			return nil, invalid(errors.New(method + " needs ns"))
		}
		write := method == "settings.write"
		if err := p.mayUse(q.NS, write); err != nil {
			return nil, err
		}
		if p.m.Settings == nil {
			return nil, unavailable(method)
		}
		if write {
			if len(q.Value) == 0 {
				return nil, invalid(errors.New("settings.write needs value"))
			}
			return map[string]any{}, p.m.Settings.Write(q.NS, q.Value)
		}
		v, err := p.m.Settings.Read(q.NS)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v}, nil
	case "keychain.ref":
		var q struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &q); err != nil || q.Name == "" {
			return nil, invalid(errors.New("keychain.ref needs name"))
		}
		if svc.KeychainRef == nil {
			return nil, unavailable(method)
		}
		ref, err := svc.KeychainRef(p.name, q.Name)
		return map[string]any{"ref": ref}, err
	case "keychain.resolveForRuntime":
		return nil, unavailable(method)
	case "inbox.post":
		if svc.Inbox == nil {
			return nil, unavailable(method)
		}
		return map[string]any{}, svc.Inbox(p.name, raw)
	case "log":
		var q struct{ Level, Msg string }
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, invalid(err)
		}
		p.log(q.Level, q.Msg)
		return map[string]any{}, nil
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "unknown method " + method}
}

// Notify accepts log as a notification too; anything else is ignored.
func (a *api) Notify(method string, raw json.RawMessage) {
	if method == "log" {
		var q struct{ Level, Msg string }
		if json.Unmarshal(raw, &q) == nil {
			a.p.log(q.Level, q.Msg)
		}
	}
}

// PolicyNS is the protected settings namespace holding the policy snapshot.
// Every plugin may read it; only plugins with policy_write may write it.
const PolicyNS = "policy"

// mayUse decides settings access: a plugin owns the namespace named after it
// and any "<name>/..." below it.
func (p *plugin) mayUse(ns string, write bool) error {
	name := p.name
	switch {
	case ns == PolicyNS || strings.HasPrefix(ns, PolicyNS+"/"):
		// Checked first: a package named "policy" must not own it.
		if !write || p.has("policy_write") {
			return nil
		}
		return notGranted("writing the policy namespace needs the policy_write capability")
	case ns == name || strings.HasPrefix(ns, name+"/"):
		return nil
	}
	return notGranted("namespace %q belongs to another plugin", ns)
}

// register checks and forwards one contribution.
func (p *plugin) register(raw json.RawMessage) error {
	var c Contribution
	if err := json.Unmarshal(raw, &c); err != nil {
		return invalid(err)
	}
	c.Raw = raw
	if c.ID == "" {
		return invalid(errors.New("ui contribution needs id"))
	}
	need, ok := contributionCaps[c.Type]
	if !ok {
		return invalid(fmt.Errorf("unknown ui contribution type %q", c.Type))
	}
	if !p.has(need) {
		return notGranted("%s contributions need the %s capability", c.Type, need)
	}
	if p.m.Services.UI == nil {
		return unavailable("ui.register")
	}
	if err := p.m.Services.UI.Register(p.name, c); err != nil {
		return err
	}
	p.mu.Lock()
	if p.ui == nil {
		p.ui = map[string]bool{}
	}
	p.ui[c.ID] = true
	p.mu.Unlock()
	return nil
}

// LaunchContext is what plugin.launchHook receives before a runtime starts.
type LaunchContext struct {
	WorkspaceID string `json:"workspaceId"`
	Profile     string `json:"profile"`
	Cwd         string `json:"cwd"`
}

// LaunchResult merges every launch hook's answer.
type LaunchResult struct {
	Env          []string          // KEY=value, sorted by plugin name
	ProfilePatch []json.RawMessage // in plugin name order
	Failed       map[string]error  // plugin name → why its hook was skipped
}

// reservedEnv are variables plugins may not set: the bridge's own endpoint
// and token, and dsh's home.
var reservedEnv = []string{"ALCOVE_", "DSH_HOME"}

// LaunchHooks asks every running sidecar that registered a launch hook for
// env and profile patches. A hook that fails or exceeds HookTimeout is
// skipped and reported; it never blocks the launch.
func (m *Manager) LaunchHooks(ctx context.Context, lc LaunchContext) LaunchResult {
	res := LaunchResult{Failed: map[string]error{}}
	for _, p := range m.running("runtime_launch_hook") {
		p.mu.Lock()
		hook := p.hook
		p.mu.Unlock()
		c := p.conn()
		if !hook || c == nil {
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, m.hookTimeout())
		var out struct {
			Env          map[string]string `json:"env"`
			ProfilePatch json.RawMessage   `json:"profilePatch"`
		}
		err := c.Call(hctx, "launchHook", lc, &out)
		cancel()
		if err == nil {
			for k := range out.Env {
				// Windows environment names are case-insensitive.
				uk := strings.ToUpper(k)
				for _, r := range reservedEnv {
					if strings.HasPrefix(uk, r) || k == "" || strings.Contains(k, "=") {
						err = fmt.Errorf("launch hook may not set %q", k)
					}
				}
			}
		}
		if err != nil {
			res.Failed[p.name] = err
			p.log("warn", "launch hook skipped: "+err.Error())
			continue
		}
		keys := make([]string, 0, len(out.Env))
		for k := range out.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			res.Env = append(res.Env, k+"="+out.Env[k])
		}
		if len(out.ProfilePatch) > 0 && string(out.ProfilePatch) != "null" {
			res.ProfilePatch = append(res.ProfilePatch, out.ProfilePatch)
		}
	}
	return res
}

// WorkspaceEvent tells sidecars with workspace_hook that a workspace was
// opened, closed, created or removed. It does not wait for them.
func (m *Manager) WorkspaceEvent(ctx context.Context, kind string, ws Workspace) {
	for _, p := range m.running("workspace_hook") {
		if c := p.conn(); c != nil {
			c.Notify(ctx, "onWorkspaceEvent", map[string]any{"kind": kind, "workspace": ws})
		}
	}
}

// UIAction forwards a click or form submit on a contribution to its owner.
func (m *Manager) UIAction(ctx context.Context, plugin, contributionID, action string, payload json.RawMessage) error {
	p := m.get(plugin)
	if p == nil {
		return ErrNotRunning
	}
	p.mu.Lock()
	own := p.ui[contributionID]
	p.mu.Unlock()
	c := p.conn()
	if c == nil {
		return ErrNotRunning
	}
	if !own {
		return fmt.Errorf("%s has no ui contribution %q", plugin, contributionID)
	}
	return c.Call(ctx, "onUiAction", map[string]any{"contributionId": contributionID, "action": action, "payload": payload}, nil)
}

// DeepLink routes alcove://plugin/<name>/... to that plugin if it holds
// deep_link.
func (m *Manager) DeepLink(ctx context.Context, url string) error {
	const prefix = "alcove://plugin/"
	if !strings.HasPrefix(url, prefix) {
		return fmt.Errorf("not a plugin link: %s", url)
	}
	rest := strings.TrimPrefix(url, prefix)
	// Scoped names keep their slash: @scope/name/...
	parts := strings.SplitN(rest, "/", 3)
	name := parts[0]
	if strings.HasPrefix(name, "@") && len(parts) > 1 {
		name += "/" + parts[1]
	}
	for _, p := range m.running("deep_link") {
		if c := p.conn(); p.name == name && c != nil {
			return c.Call(ctx, "onDeepLink", map[string]any{"url": url}, nil)
		}
	}
	return fmt.Errorf("no running plugin %s holds deep_link", name)
}
