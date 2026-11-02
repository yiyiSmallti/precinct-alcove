package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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
	// (background cap), with env (KEY=value, already checked against the
	// reserved names) added to its environment, and returns the runtime id.
	// The host decides which profiles a plugin may run.
	EnsureRuntime func(ctx context.Context, plugin, profile, cwd string, env []string) (string, error)
	// Attach makes plugin the owner of a workspace and adds the plugin's
	// package to its profile; Detach undoes that (provider_endpoint cap).
	// Detach of an unknown workspace succeeds.
	Attach, Detach func(ctx context.Context, plugin, workspaceID string) error
	// PromptSession queues text on the plugin's own session (task-<id>) in
	// a workspace it owns and returns the message id; the plugin hears
	// onSessionIdle once the session is idle. CancelSession cancels its
	// running turn (workspace_session cap).
	PromptSession func(ctx context.Context, plugin, workspaceID, sessionID, text string) (string, error)
	CancelSession func(ctx context.Context, plugin, workspaceID, sessionID string) (bool, error)
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
	Owner   string `json:"owner"` // "personal" or the attaching plugin
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
	"workspace.attach":           "provider_endpoint",
	"workspace.detach":           "provider_endpoint",
	"session.prompt":             "workspace_session",
	"session.cancel":             "workspace_session",
}

type api struct{ p *plugin }

// ErrNotPermitted is what a Services function wraps to refuse a call; the
// sidecar gets CodeNotGranted.
var ErrNotPermitted = errors.New("not permitted")

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
	case "workspace.attach", "workspace.detach":
		var q struct {
			WorkspaceID string `json:"workspaceId"`
		}
		if err := json.Unmarshal(raw, &q); err != nil || q.WorkspaceID == "" {
			return nil, invalid(fmt.Errorf("%s needs workspaceId", method))
		}
		f := svc.Attach
		if method == "workspace.detach" {
			f = svc.Detach
		}
		if f == nil {
			return nil, unavailable(method)
		}
		err := f(ctx, p.name, q.WorkspaceID)
		if errors.Is(err, ErrNotPermitted) {
			return nil, notGranted("%v", err)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "session.prompt", "session.cancel":
		var q struct {
			WorkspaceID string `json:"workspaceId"`
			SessionID   string `json:"sessionId"`
			Text        string `json:"text"`
		}
		if err := json.Unmarshal(raw, &q); err != nil || q.WorkspaceID == "" || q.SessionID == "" || (method == "session.prompt" && q.Text == "") {
			return nil, invalid(fmt.Errorf("%s needs workspaceId, sessionId%s", method, map[bool]string{true: " and text"}[method == "session.prompt"]))
		}
		var res any
		var err error
		if method == "session.prompt" {
			if svc.PromptSession == nil {
				return nil, unavailable(method)
			}
			var msg string
			msg, err = svc.PromptSession(ctx, p.name, q.WorkspaceID, q.SessionID, q.Text)
			res = map[string]any{"messageId": msg}
		} else {
			if svc.CancelSession == nil {
				return nil, unavailable(method)
			}
			var ok bool
			ok, err = svc.CancelSession(ctx, p.name, q.WorkspaceID, q.SessionID)
			res = map[string]any{"cancelled": ok}
		}
		if errors.Is(err, ErrNotPermitted) {
			return nil, notGranted("%v", err)
		}
		if err != nil {
			return nil, err
		}
		return res, nil
	case "runtime.registerLaunchHook":
		p.mu.Lock()
		p.hook = true
		p.mu.Unlock()
		return map[string]any{}, nil
	case "runtime.ensure":
		var q struct {
			Profile string            `json:"profile"`
			Cwd     string            `json:"cwd"`
			Env     map[string]string `json:"env"`
		}
		if err := json.Unmarshal(raw, &q); err != nil || q.Profile == "" || q.Cwd == "" {
			return nil, invalid(errors.New("runtime.ensure needs profile and cwd"))
		}
		env, err := checkEnv(q.Env)
		if err != nil {
			return nil, invalid(err)
		}
		if svc.EnsureRuntime == nil {
			return nil, unavailable(method)
		}
		id, err := svc.EnsureRuntime(ctx, p.name, q.Profile, q.Cwd, env)
		if errors.Is(err, ErrNotPermitted) {
			return nil, notGranted("%v", err)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"runtimeId": id}, nil
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
		var q struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &q); err != nil || q.ID == "" {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "inbox.post: item needs an id"}
		}
		if err := svc.Inbox(p.name, raw); err != nil {
			return nil, err
		}
		// Card buttons come back through onUiAction with the item id.
		p.mu.Lock()
		if p.ui == nil {
			p.ui = map[string]bool{}
		}
		p.ui[q.ID] = true
		p.mu.Unlock()
		return map[string]any{}, nil
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
	// Providers holds the model routes of plugins with provider_endpoint,
	// by plugin name. The host uses only the one of the workspace's owner.
	Providers map[string]Provider
	// EnvBy is Env split by plugin name, so the host can let one plugin's
	// values win (the owner's, for an attached workspace).
	EnvBy map[string][]string
}

// EnvFor is Env with owner's variables taking precedence: any variable the
// owner sets drops every other plugin's value for it.
func (r LaunchResult) EnvFor(owner string) []string {
	own := r.EnvBy[owner]
	if len(own) == 0 {
		return r.Env
	}
	set := map[string]bool{}
	for _, kv := range own {
		k, _, _ := strings.Cut(kv, "=")
		set[k] = true
	}
	var out []string
	for _, kv := range r.Env {
		if k, _, _ := strings.Cut(kv, "="); !set[k] {
			out = append(out, kv)
		}
	}
	return append(out, own...)
}

// Provider is a model route a launch hook asks for: an SDK provider route
// (mounted by a bundle in the profile) and its model.
type Provider struct {
	Route string `json:"route"`
	Model string `json:"model"`
}

// reservedEnv are variables plugins may not set: the bridge's own endpoint
// and token, and dsh's home.
var reservedEnv = []string{"ALCOVE_", "DSH_HOME"}

// checkEnv validates plugin-supplied variables and returns them as sorted
// KEY=value pairs.
func checkEnv(m map[string]string) ([]string, error) {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		// Windows environment names are case-insensitive.
		uk := strings.ToUpper(k)
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("invalid environment variable %q", k)
		}
		for _, r := range reservedEnv {
			if strings.HasPrefix(uk, r) {
				return nil, fmt.Errorf("plugins may not set %q", k)
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out, nil
}

// LaunchHooks asks every running sidecar that registered a launch hook for
// env and profile patches. A hook that fails or exceeds HookTimeout is
// skipped and reported; it never blocks the launch.
func (m *Manager) LaunchHooks(ctx context.Context, lc LaunchContext) LaunchResult {
	res := LaunchResult{Failed: map[string]error{}, Providers: map[string]Provider{}, EnvBy: map[string][]string{}}
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
			Provider     *Provider         `json:"provider"`
		}
		err := c.Call(hctx, "launchHook", lc, &out)
		cancel()
		var env []string
		if err == nil {
			env, err = checkEnv(out.Env)
		}
		if err != nil {
			res.Failed[p.name] = err
			p.log("warn", "launch hook skipped: "+err.Error())
			continue
		}
		if out.Provider != nil {
			switch {
			case !p.has("provider_endpoint"):
				p.log("warn", "launch hook provider ignored: needs the provider_endpoint capability")
			case out.Provider.Route == "" || out.Provider.Model == "":
				res.Failed[p.name] = errors.New("launch hook provider needs route and model")
				p.log("warn", "launch hook skipped: provider needs route and model")
				continue
			default:
				res.Providers[p.name] = *out.Provider
			}
		}
		res.Env = append(res.Env, env...)
		res.EnvBy[p.name] = env
		if len(out.ProfilePatch) > 0 && string(out.ProfilePatch) != "null" {
			res.ProfilePatch = append(res.ProfilePatch, out.ProfilePatch)
		}
	}
	return res
}

// WorkspaceEvent tells sidecars with workspace_hook that a workspace was
// opened, closed, created or removed. It does not wait for them, and each
// sidecar gets the events in the order WorkspaceEvent was called.
func (m *Manager) WorkspaceEvent(ctx context.Context, kind string, ws Workspace) {
	// Deliveries run off the caller: a sidecar that stops reading its pipe
	// must not hold up the workspace operation. A stuck write ends when the
	// sidecar is stopped and its pipe closes.
	ctx = context.WithoutCancel(ctx)
	for _, p := range m.running("workspace_hook") {
		if c := p.conn(); c != nil {
			p.enqueue(func() { c.Notify(ctx, "onWorkspaceEvent", map[string]any{"kind": kind, "workspace": ws}) })
		}
	}
}

// Notify sends one plugin a notification (e.g. onSessionIdle), in order
// with its other notifications. A plugin that is not running misses it.
func (m *Manager) Notify(plugin, method string, params any) {
	p := m.get(plugin)
	if p == nil {
		return
	}
	if c := p.conn(); c != nil {
		p.enqueue(func() { c.Notify(context.Background(), method, params) })
	}
}

// enqueue runs fn after the notifications queued before it, off the
// caller's goroutine.
func (p *plugin) enqueue(fn func()) {
	p.evMu.Lock()
	p.evq = append(p.evq, fn)
	if p.evBusy {
		p.evMu.Unlock()
		return
	}
	p.evBusy = true
	p.evMu.Unlock()
	go func() {
		for {
			p.evMu.Lock()
			if len(p.evq) == 0 {
				p.evBusy = false
				p.evMu.Unlock()
				return
			}
			next := p.evq[0]
			p.evq = p.evq[1:]
			p.evMu.Unlock()
			next()
		}
	}()
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

// InboxInput delivers what the user typed in a plugin's conversation view
// (inbox capability). The plugin answers once it has stored the input; the
// host resends the same id until then, so the plugin must treat a repeated
// id as already handled.
func (m *Manager) InboxInput(ctx context.Context, plugin, id, text string, ts time.Time) error {
	for _, p := range m.running("inbox") {
		if c := p.conn(); p.name == plugin && c != nil {
			return c.Call(ctx, "onInboxInput", map[string]any{"id": id, "text": text, "ts": ts.UTC().Format(time.RFC3339Nano)}, nil)
		}
	}
	return fmt.Errorf("%w: %s (with inbox)", ErrNotRunning, plugin)
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
