// Package app wires Alcove's parts together: data directory, identity,
// credentials, workspaces, profiles, runtimes, the bridge, plugins and their
// sidecars. The UI drives Alcove only through App.
package app

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/catalog"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/identity"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/paths"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/profile"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// Config is what the entry point knows before anything is loaded.
type Config struct {
	DataDir       string // <数据目录>
	AlcoveVersion string
	Node, DSHBin  string   // bundled Node and dsh entry script
	ToolDirs      []string // bundled pnpm etc., prepended to PATH for dsh
	BridgeTgz     string   // the shipped alcove-bridge package
	TrustedKeys   []ed25519.PublicKey
	AllowUnsigned bool
	Secrets       credentials.SecretStore // nil: the system credential store
	Answerer      alrt.Answerer           // approval cards and question forms
	SidecarUI     sidecar.UISink
	// Browser is the window's browser (browser capability); nil: none.
	Browser sidecar.Browser
	Inbox   func(plugin string, item json.RawMessage) error
	// ScratchDir is the default folder of tasks outside a project, whose
	// workspace Alcove adds when it is missing (the window lists its tasks
	// as 最近会话); "" adds none.
	ScratchDir string
	// ConfirmInstall shows a package a plugin market (plugin_install cap)
	// asks to install or update and reports whether the user agreed; nil
	// declines every such request.
	ConfirmInstall func(ctx context.Context, plugin string, plan *host.Plan) bool
	// PluginsChanged is called after plugins were installed or updated
	// without the plugin page's own buttons (a market, a preinstall).
	PluginsChanged func()
	// Preinstall are signed packages shipped with Alcove (the market): each
	// is installed on the first start that finds it, and updated when the
	// shipped version is newer. One the user uninstalled stays uninstalled.
	Preinstall []string
}

// Settings are Alcove's own preferences (settings.json).
type Settings struct {
	DefaultCredential string `json:"defaultCredential,omitempty"`
	// ReasoningEffort is the effort tasks run at (catalog.Efforts); ""
	// and one the model does not take leave the provider's default.
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	// Compaction is when a conversation nearing its window is compacted.
	Compaction Compaction `json:"compaction"`
	// UpdateSource is a mirror of the release files (an https directory);
	// "" is the feed built into the app.
	UpdateSource string `json:"updateSource,omitempty"`
	// ScratchDir is the folder of tasks outside a project (最近会话); ""
	// is Config.ScratchDir.
	ScratchDir string `json:"scratchDir,omitempty"`
	// Appearance is how the window looks; zero is Alcove's default look.
	Appearance Appearance `json:"appearance"`
}

// Compaction tunes dsh's compaction-basic for every task.
type Compaction struct {
	// Off stops automatic compaction; a full window then fails the turn.
	Off bool `json:"off,omitempty"`
	// Threshold is how full the context gets before it is compacted, in
	// percent of the window (or of the model's cap); 0 is DefaultThreshold.
	Threshold int `json:"threshold,omitempty"`
}

// DefaultThreshold is compaction-basic's own thresholdRatio, in percent.
const DefaultThreshold = 80

// Appearance is the look the user chose in settings.
type Appearance struct {
	// Mode is "light" or "dark"; "" follows the system.
	Mode string `json:"mode,omitempty"`
	// Tone tints the neutrals: "zinc" (""), "slate", "linen", "warm",
	// "neutral" or "cool".
	Tone string `json:"tone,omitempty"`
	// Accent colors primary buttons, the chosen row and running tasks: a
	// preset name ("" is the tone's own) or a #rrggbb color.
	Accent string `json:"accent,omitempty"`
	// Font and CodeFont are font families; "" is the system's and its
	// monospace.
	Font     string `json:"font,omitempty"`
	CodeFont string `json:"codeFont,omitempty"`
	// Zoom scales the whole window; 0 is 1.
	Zoom float32 `json:"zoom,omitempty"`
}

// App is one running Alcove.
type App struct {
	Layout     paths.Layout
	Identity   *identity.Identity
	CLI        *dsh.CLI
	Creds      *credentials.Store
	Workspaces *workspace.Store
	Tasks      *tasks.Store
	Plugins    *host.Host
	Profiles   *profile.Assembler
	Bridge     *alrt.Bridge
	Runtimes   *alrt.Manager
	Sidecars   *sidecar.Manager
	// Catalog is what the bundled runtime knows of each model.
	Catalog *catalog.Catalog

	// OnRuntimeExit is called after a runtime exits (the UI's hook); set it
	// before Start.
	OnRuntimeExit func(k alrt.Key, runtimeID string, err error)
	// OnEvent receives every runtime notification (the UI's hook); set it
	// before Start.
	OnEvent func(runtimeID string, e alrt.Event)

	cfg Config
	mu  sync.Mutex
	// Workspace lifecycle (opened/closed), by runtime id: each started
	// workspace runtime gets one opened and one closed, in that order.
	lifeMu sync.Mutex
	life   map[string]*lifecycle
	// ownMu serializes workspace.attach and workspace.detach, so an owner
	// check and the change it allows cannot interleave with another.
	ownMu  sync.Mutex
	cancel context.CancelFunc
	// sessions are plugin sessions (session.prompt), by workspace and id.
	sessions pluginSessions
}

// New loads Alcove's state from cfg.DataDir, creating it on first start.
func New(cfg Config) (*App, error) {
	l := paths.Layout{Root: cfg.DataDir}
	for _, d := range []string{l.Root, l.Logs(), l.Plugins(), l.DSHHome()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	id, err := identity.LoadOrCreate(l.Identity())
	if err != nil {
		return nil, err
	}
	if cfg.Secrets == nil {
		cfg.Secrets = credentials.MygoSecrets{}
	}
	cli := &dsh.CLI{Node: cfg.Node, Bin: cfg.DSHBin, Home: l.DSHHome(), ToolDirs: cfg.ToolDirs}
	a := &App{Layout: l, Identity: id, CLI: cli, cfg: cfg, Catalog: catalog.FromDSHBin(cfg.DSHBin)}
	a.Creds = &credentials.Store{Path: l.Credentials(), Secrets: cfg.Secrets}
	if err := a.migrateModel(); err != nil {
		a.pluginLog("host", "warn", fmt.Sprintf("moving the saved model onto its credential: %v", err))
	}
	a.Workspaces = &workspace.Store{Path: l.Workspaces(), DataDir: l.Root}
	a.Tasks = &tasks.Store{Path: l.Tasks(), Dir: l.Transcripts()}
	a.Plugins = &host.Host{
		DSH: cli, Dir: l.Plugins(), AlcoveVersion: cfg.AlcoveVersion, GOOS: runtime.GOOS,
		TrustedKeys: cfg.TrustedKeys, AllowUnsigned: cfg.AllowUnsigned,
		ResolveProfiles: a.resolveProfiles,
	}
	a.Profiles = &profile.Assembler{DSH: cli, Bridge: cfg.BridgeTgz, Plugins: a.Plugins}
	a.Plugins.PrepareProfile = a.Profiles.Base
	a.Bridge = &alrt.Bridge{Answerer: cfg.Answerer, AuditLog: filepath.Join(l.Logs(), "decisions.jsonl")}
	a.Runtimes = &alrt.Manager{CLI: cli, Bridge: a.Bridge, LocalEnabled: a.localEnabled, OnExit: a.runtimeExited}
	a.Runtimes.OnEvent = func(id string, e alrt.Event) {
		a.tapEvent(id, e)
		if a.OnEvent != nil {
			a.OnEvent(id, e)
		}
	}
	a.Sidecars = &sidecar.Manager{
		AlcoveVersion: cfg.AlcoveVersion,
		Settings:      &sidecar.Settings{Dir: l.PluginSettings()},
		Services: sidecar.Services{
			Workspaces:    workspace.Source{Store: a.Workspaces},
			UI:            cfg.SidecarUI,
			Inbox:         cfg.Inbox,
			EnsureRuntime: a.ensureForPlugin,
			Attach:        a.attachWorkspace,
			Detach:        a.detachWorkspace,
			PromptSession: a.promptForPlugin,
			CancelSession: a.cancelForPlugin,
			KeychainRef:   a.keychainRef,
			Log:           a.pluginLog,
			Market:        market{a},
			Browser:       cfg.Browser,
		},
	}
	return a, nil
}

// Start opens the bridge endpoint and starts the sidecars of installed
// plugins. A sidecar that fails to start is reported through its status and
// does not stop Alcove.
func (a *App) Start(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)
	if v, err := a.CLI.Version(ctx); err == nil {
		a.Sidecars.DSHVersion = v
	}
	// A short name: the endpoint lands in the temp dir (macOS caps socket
	// paths at 104 bytes) or becomes \\.\pipe\<name> on Windows.
	if err := a.Bridge.Start(ctx, "alcove-"+a.Identity.ID[:8]); err != nil {
		return err
	}
	recs, err := a.Plugins.List()
	if err != nil {
		return err
	}
	for _, r := range recs {
		if !r.Incomplete && r.Sidecar != "" {
			a.Sidecars.Start(ctx, a.sidecarSpec(r))
		}
	}
	if len(a.cfg.Preinstall) > 0 {
		go a.preinstall(ctx)
	}
	return nil
}

// Close stops sidecars and runtimes.
func (a *App) Close() {
	ctx := context.Background()
	a.Sidecars.StopAll(ctx)
	a.Runtimes.StopAll()
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *App) sidecarSpec(r host.Installed) sidecar.Spec {
	return sidecar.Spec{Name: r.Name, Version: r.Version, Exe: a.Plugins.SidecarPath(r), Caps: r.Capabilities}
}

// resolveProfiles maps alcove.profiles entries to dsh profiles: "personal"
// is every workspace profile; any other name is a profile of that name.
func (a *App) resolveProfiles(name string) []string {
	if name != profile.Personal {
		return []string{name}
	}
	all, _ := a.Workspaces.All()
	out := make([]string, 0, len(all))
	for _, w := range all {
		out = append(out, w.Profile)
	}
	return out
}

// CreateWorkspace records a workspace and builds its profile; if the profile
// cannot be built, the record is removed again.
func (a *App) CreateWorkspace(ctx context.Context, name, dir string) (*workspace.Workspace, error) {
	w, err := a.Workspaces.Create(name, dir)
	if err != nil {
		return nil, err
	}
	if err := a.Profiles.Workspace(ctx, w.Profile); err != nil {
		a.Workspaces.Remove(w.ID)
		return nil, err
	}
	a.Sidecars.WorkspaceEvent(ctx, "created", sidecarView(w))
	return w, nil
}

// Scratch is the name of the workspace in ScratchDir, which holds every
// task outside a project; the window lists its tasks as 最近会话 rather
// than as a workspace.
const Scratch = "最近会话"

// EnsureWorkspace adds the workspace of ScratchDir when there is none,
// and returns it; with one there already, or no ScratchDir, it returns nil.
func (a *App) EnsureWorkspace(ctx context.Context) (*workspace.Workspace, error) {
	dir := a.ScratchDir()
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if w, err := a.Workspaces.ByDir(dir); err != nil || w != nil {
		return nil, err
	}
	return a.CreateWorkspace(ctx, Scratch, dir)
}

// ScratchWorkspace is the workspace of ScratchDir, or nil.
func (a *App) ScratchWorkspace() (*workspace.Workspace, error) {
	if dir := a.ScratchDir(); dir != "" {
		return a.Workspaces.ByDir(dir)
	}
	return nil, nil
}

// ScratchDir is the folder of tasks outside a project: the one chosen in
// settings, else Config.ScratchDir.
func (a *App) ScratchDir() string {
	if st, err := a.Settings(); err == nil && st.ScratchDir != "" {
		return st.ScratchDir
	}
	return a.cfg.ScratchDir
}

// DefaultScratchDir is Config.ScratchDir, the folder of tasks outside a
// project when settings choose none.
func (a *App) DefaultScratchDir() string { return a.cfg.ScratchDir }

// SetScratchDir makes dir ("" for the default) the folder of tasks outside
// a project. Their workspace moves there, its runtime stopped (and with it
// any task running in it); with none, one is added there. A folder that is
// a project's workspace is refused.
func (a *App) SetScratchDir(ctx context.Context, dir string) error {
	st, err := a.Settings()
	if err != nil {
		return err
	}
	next := dir
	if next == "" {
		next = a.cfg.ScratchDir
	}
	cur, err := a.ScratchWorkspace()
	if err != nil {
		return err
	}
	there, err := a.Workspaces.ByDir(next)
	switch {
	case err != nil:
		return err
	case there != nil && (cur == nil || there.ID != cur.ID):
		return fmt.Errorf("%w: %s", workspace.ErrTaken, there.Name)
	}
	if err := os.MkdirAll(next, 0o755); err != nil {
		return err
	}
	switch {
	case there != nil:
	case cur == nil:
		if _, err := a.CreateWorkspace(ctx, Scratch, next); err != nil {
			return err
		}
	default:
		k := alrt.Key{Profile: cur.Profile, Cwd: cur.Dir}
		a.Runtimes.Stop(k)
		a.closeWorkspace(k)
		if err := a.Workspaces.Move(cur.ID, next); err != nil {
			return err
		}
	}
	st.ScratchDir = dir
	return a.SaveSettings(st)
}

func sidecarView(w *workspace.Workspace) sidecar.Workspace {
	return sidecar.Workspace{ID: w.ID, Name: w.Name, Dir: w.Dir, Profile: w.Profile, Owner: w.OwnerName()}
}

// localEnabled reads policy executors.local.enabled (PRD F7) from the
// protected settings namespace. Absent means enabled; a policy that cannot
// be read or parsed disables local execution rather than ignoring it.
func (a *App) localEnabled() bool {
	raw, err := a.Sidecars.Settings.Read(sidecar.PolicyNS)
	if err != nil {
		return false
	}
	var pol struct {
		Executors struct {
			Local struct {
				Enabled *bool `json:"enabled"`
			} `json:"local"`
		} `json:"executors"`
	}
	if string(raw) != "null" && len(raw) > 0 {
		if err := json.Unmarshal(raw, &pol); err != nil {
			return false
		}
	}
	return pol.Executors.Local.Enabled == nil || *pol.Executors.Local.Enabled
}

// RemoveWorkspace stops its runtime, deletes its profile and forgets it.
// The folder and its files are left alone.
func (a *App) RemoveWorkspace(ctx context.Context, id string) error {
	w, err := a.Workspaces.Find(id)
	if err != nil {
		return err
	}
	a.Runtimes.Stop(alrt.Key{Profile: w.Profile, Cwd: w.Dir})
	a.closeWorkspace(alrt.Key{Profile: w.Profile, Cwd: w.Dir})
	// Delete first: if that fails, the plugin records still list the
	// profile, so a later uninstall can clean it up.
	if err := a.Profiles.Delete(w.Profile); err != nil {
		return err
	}
	if err := a.Plugins.ForgetProfile(w.Profile); err != nil {
		return err
	}
	if err := a.Workspaces.Remove(id); err != nil {
		return err
	}
	a.Tasks.RemoveWorkspace(id)
	a.Sidecars.WorkspaceEvent(ctx, "removed", sidecarView(w))
	return nil
}

// OpenWorkspace returns the workspace's runtime, starting it with the
// default credential and the plugins' launch hooks. An attached workspace
// runs only on its owner's route.
func (a *App) OpenWorkspace(ctx context.Context, id string) (*alrt.Process, error) {
	// attach/detach can change the owner while a launch is in flight (they
	// stop the runtime, but the launch may not have reached the runtime
	// manager yet). Check the runtime against the owner afterwards and
	// start again if they differ.
	for try := 0; ; try++ {
		w, err := a.Workspaces.Find(id)
		if err != nil {
			return nil, err
		}
		p, err := a.openOnce(ctx, w)
		if err != nil {
			return nil, err
		}
		k := alrt.Key{Profile: w.Profile, Cwd: w.Dir}
		now, err := a.Workspaces.Find(id)
		if err != nil {
			a.Runtimes.Stop(k)
			return nil, err
		}
		if p.Spec.Owner == ownerKey(now) {
			a.opened(p, k, sidecarView(now))
			return p, nil
		}
		a.Runtimes.Stop(k)
		if try == 2 {
			return nil, fmt.Errorf("workspace %s changed owner while opening; try again", w.Name)
		}
	}
}

// ownerKey is LaunchSpec.Owner for w: empty when personal.
func ownerKey(w *workspace.Workspace) string {
	if o := w.OwnerName(); o != workspace.Personal {
		return o
	}
	return ""
}

func (a *App) openOnce(ctx context.Context, w *workspace.Workspace) (*alrt.Process, error) {
	if w.Missing() {
		return nil, fmt.Errorf("workspace folder %s is gone", w.Dir)
	}
	hooks := a.Sidecars.LaunchHooks(ctx, sidecar.LaunchContext{WorkspaceID: w.ID, Profile: w.Profile, Cwd: w.Dir})
	if len(hooks.ProfilePatch) > 0 {
		// Phase one reserves profilePatch (SPEC 3.2): say so rather than
		// drop it silently.
		a.pluginLog("host", "warn", fmt.Sprintf("workspace %s: %d launch-hook profilePatch value(s) ignored; profilePatch is reserved in phase one", w.ID, len(hooks.ProfilePatch)))
	}
	owner := ownerKey(w)
	var route *sidecar.Provider
	if owner != "" {
		// An attached workspace runs only on its owner's route, never on
		// the personal credential: no route, no launch.
		pr, ok := hooks.Providers[owner]
		if !ok {
			why := "its launch hook gave no model route"
			if err := hooks.Failed[owner]; err != nil {
				why = "its launch hook failed: " + err.Error()
			} else if !a.pluginInstalled(owner) {
				// Kept on purpose: the folder may hold the owner's data,
				// which must not reach the personal credential.
				why = "that plugin is no longer installed; reinstall it, or remove the workspace"
			}
			return nil, fmt.Errorf("workspace %s belongs to %s, and %s", w.Name, owner, why)
		}
		route = &pr
	} else if len(hooks.Providers) > 0 {
		a.pluginLog("host", "warn", fmt.Sprintf("workspace %s is personal: launch-hook model routes ignored", w.ID))
	}
	// The owner's variables win over other plugins' (e.g. the same model
	// endpoint variables pointing somewhere else).
	return a.launch(ctx, w.Profile, w.Dir, hooks.EnvFor(owner), route, owner)
}

func (a *App) pluginInstalled(name string) bool {
	recs, err := a.Plugins.List()
	if err != nil {
		return true // unknown: do not claim it is gone
	}
	for _, r := range recs {
		if r.Name == name {
			return true
		}
	}
	return false
}

type lifecycle struct {
	key    alrt.Key
	ws     sidecar.Workspace
	closed bool
}

// opened sends opened the first time a runtime is seen. A runtime that
// already exited (its exit callback may have run before this) is closed
// right after.
func (a *App) opened(p *alrt.Process, k alrt.Key, ws sidecar.Workspace) {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	if a.life == nil {
		a.life = map[string]*lifecycle{}
	}
	if a.life[p.ID] != nil {
		return
	}
	l := &lifecycle{key: k, ws: ws}
	a.life[p.ID] = l
	a.Sidecars.WorkspaceEvent(context.Background(), "opened", ws)
	select {
	case <-p.Exited():
		a.closeLocked(l)
	default:
	}
}

// closeLocked sends closed once per opened.
func (a *App) closeLocked(l *lifecycle) {
	if l.closed {
		return
	}
	l.closed = true
	a.Sidecars.WorkspaceEvent(context.Background(), "closed", l.ws)
}

// closeWorkspace sends closed for every open runtime of k (RemoveWorkspace,
// after stopping it, so closed goes out before removed).
func (a *App) closeWorkspace(k alrt.Key) {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	for _, l := range a.life {
		if l.key == k {
			a.closeLocked(l)
		}
	}
}

// runtimeExited tells workspace_hook plugins that a workspace's runtime is
// gone (closed: idle exit, crash or stop), then the UI.
func (a *App) runtimeExited(k alrt.Key, runtimeID string, err error) {
	a.lifeMu.Lock()
	// A runtime not seen yet exited before OpenWorkspace got it back;
	// opened closes it then.
	if l := a.life[runtimeID]; l != nil {
		a.closeLocked(l)
	}
	a.lifeMu.Unlock()
	if a.OnRuntimeExit != nil {
		a.OnRuntimeExit(k, runtimeID, err)
	}
}

// launch starts (or returns) the runtime of prof in cwd with the plugin
// variables extra. With a route, the runtime uses it and gets no personal
// credential; otherwise it uses the default credential.
func (a *App) launch(ctx context.Context, prof, cwd string, extra []string, route *sidecar.Provider, owner string) (*alrt.Process, error) {
	s, err := a.Settings()
	if err != nil {
		return nil, err
	}
	if route != nil {
		effort, tuning := a.tuning(s, route.Route, route.Model, 0)
		return a.ensure(ctx, alrt.LaunchSpec{
			Profile: prof, Cwd: cwd, Provider: route.Route, Model: route.Model, ReasoningEffort: effort,
			Tuning: effort + "\n" + strings.Join(tuning, "\n"), Owner: owner,
			Env: append(append([]string(nil), extra...), tuning...),
		})
	}
	credID := s.DefaultCredential
	if credID == "" {
		list, err := a.Creds.List()
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, errors.New("add a model API credential first")
		}
		credID = list[0].ID
	}
	cr, err := a.Creds.Get(credID)
	if err != nil {
		return nil, err
	}
	provider, model, env, err := a.Creds.LaunchEnv(credID)
	if err != nil {
		return nil, err
	}
	effort, tuning := a.tuning(s, provider, model, cr.Context[model])
	return a.ensure(ctx, alrt.LaunchSpec{
		Profile: prof, Cwd: cwd, Provider: provider, Model: model, ReasoningEffort: effort,
		Tuning: effort + "\n" + strings.Join(tuning, "\n"), Credential: credID,
		// Plugin variables first: the credential always wins.
		Env: append(append(append([]string(nil), extra...), tuning...), env...),
	})
}

// ensure starts (or returns) the runtime spec describes. The user switched
// model or credential, or retuned it, since a runtime of the same owner
// started: an idle one restarts on the new spec (sessions resume from
// disk); a busy one keeps it until its sessions are done.
func (a *App) ensure(ctx context.Context, spec alrt.LaunchSpec) (*alrt.Process, error) {
	if p := a.Runtimes.Get(alrt.Key{Profile: spec.Profile, Cwd: spec.Cwd}); p != nil && p.Spec.Owner == spec.Owner &&
		(p.Spec.Credential != spec.Credential || p.Spec.Provider != spec.Provider || p.Spec.Model != spec.Model || p.Spec.Tuning != spec.Tuning) && !p.Busy() {
		p.Stop(0)
	}
	return a.Runtimes.Ensure(ctx, spec)
}

// tuning is how the settings reach a runtime of model on route: the
// reasoning effort it takes (none when the model takes no such effort),
// and the variables bridge/cordis.patch.yml reads (the compaction settings,
// a route patch, the default model). limit is the context
// chosen for the model in tokens (0: the runtime's window).
func (a *App) tuning(s Settings, route, model string, limit int) (effort string, env []string) {
	m, listed := a.Catalog.Lookup(route, model)
	if m.Supports(s.ReasoningEffort) {
		effort = s.ReasoningEffort
	}
	// A cap past the runtime's window, up to the widest a catalog states,
	// widens it; one below scales the compaction ratios, so a conversation
	// is compacted before it outgrows the cap.
	window, scale := m.Context, 1.0
	switch {
	case limit > window && limit <= m.Max:
		window = limit
	case limit > 0 && window > 0 && limit < window:
		scale = float64(limit) / float64(window)
	}
	// pi-ai serves only the models its catalog lists; another one the
	// service offers is declared for this runtime's route, and a widened
	// window is set on the route's entry for the model.
	var patch map[string]any
	switch {
	case !listed && a.Catalog.Routes(route):
		entry := map[string]any{"id": model}
		if window != m.Context {
			entry["contextWindow"] = window
		}
		patch = map[string]any{"models": []any{entry}}
	case window != m.Context:
		patch = map[string]any{"modelOverrides": map[string]any{model: map[string]any{"contextWindow": window}}}
	}
	if patch != nil {
		b, _ := json.Marshal(map[string]any{route: patch})
		env = append(env, "ALCOVE_ROUTE_PATCH="+string(b))
	}
	pct := s.Compaction.Threshold
	if pct <= 0 || pct >= 100 {
		pct = DefaultThreshold
	}
	cfg, _ := json.Marshal(map[string]any{
		"auto":           !s.Compaction.Off,
		"thresholdRatio": float64(pct) / 100 * scale,
		// compaction-basic's default verbatim tail, kept under the cap.
		"retainRatio": 0.16 * scale,
	})
	// Agents the runtime makes on its own (a plugin's engine, subagents)
	// take dsh's default model; the bridge sets it to this route.
	def := map[string]string{"provider": route, "model": model}
	if effort != "" {
		def["reasoningEffort"] = effort
	}
	b, _ := json.Marshal(def)
	return effort, append(env, "ALCOVE_COMPACTION="+string(cfg), "ALCOVE_DEFAULT_MODEL="+string(b))
}

// ensureForPlugin is runtime.ensure for sidecars with the background
// capability. A plugin may only run profiles it was installed into that are
// not workspace profiles, e.g. "daily".
func (a *App) ensureForPlugin(ctx context.Context, plugin, prof, cwd string, env []string) (string, error) {
	recs, err := a.Plugins.List()
	if err != nil {
		return "", err
	}
	allowed := false
	for _, r := range recs {
		if r.Name != plugin || r.Incomplete {
			continue
		}
		for _, p := range r.Profiles {
			if p == prof && !strings.HasPrefix(p, "ws-") {
				allowed = true
			}
		}
	}
	if !allowed {
		return "", fmt.Errorf("%w: %s may not run profile %q", sidecar.ErrNotPermitted, plugin, prof)
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() || !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("runtime.ensure: %q is not an existing absolute directory", cwd)
	}
	// As for a workspace (RefreshBridge): a profile made by an older Alcove
	// gets the shipped bridge before its runtime starts.
	if a.Runtimes.Get(alrt.Key{Profile: prof, Cwd: cwd}) == nil {
		if _, err := a.Profiles.RefreshBridge(ctx, prof); err != nil {
			a.pluginLog("host", "warn", fmt.Sprintf("profile %s: updating the bridge: %v", prof, err))
		}
	}
	p, err := a.launch(ctx, prof, cwd, env, nil, "")
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// attachWorkspace is workspace.attach: plugin becomes the workspace's owner
// and its package joins the workspace profile. A runtime already open is
// stopped, so the next open uses the owner's route.
func (a *App) attachWorkspace(ctx context.Context, plugin, id string) error {
	if plugin == workspace.Personal {
		// The owner "personal" means unowned; a plugin by that name could
		// otherwise attach and run on the personal credential.
		return fmt.Errorf("%w: a plugin named %q cannot own workspaces", sidecar.ErrNotPermitted, plugin)
	}
	a.ownMu.Lock()
	defer a.ownMu.Unlock()
	w, err := a.Workspaces.Find(id)
	if err != nil {
		return err
	}
	if o := w.OwnerName(); o != workspace.Personal && o != plugin {
		return fmt.Errorf("%w: workspace %s belongs to %s", sidecar.ErrNotPermitted, w.ID, o)
	}
	// A package of the same name the user put there themselves is not
	// ours to take over (or later remove).
	if err := a.Plugins.CanAdd(plugin, w.Profile); err != nil {
		return err
	}
	a.Runtimes.Stop(alrt.Key{Profile: w.Profile, Cwd: w.Dir})
	// Owner first: if adding the package fails, launches still refuse the
	// personal credential, and detach cleans up.
	if err := a.Workspaces.SetOwner(id, plugin); err != nil {
		return err
	}
	return a.Plugins.AddToProfile(ctx, plugin, w.Profile)
}

// detachWorkspace is workspace.detach: the workspace goes back to personal
// and the package leaves its profile (unless the plugin is installed into
// every personal workspace anyway). Unknown workspaces are fine.
func (a *App) detachWorkspace(ctx context.Context, plugin, id string) error {
	a.ownMu.Lock()
	defer a.ownMu.Unlock()
	w, err := a.Workspaces.Find(id)
	if errors.Is(err, workspace.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if o := w.OwnerName(); o != plugin {
		if o == workspace.Personal {
			return nil
		}
		return fmt.Errorf("%w: workspace %s belongs to %s", sidecar.ErrNotPermitted, w.ID, o)
	}
	a.Runtimes.Stop(alrt.Key{Profile: w.Profile, Cwd: w.Dir})
	personal := false
	recs, err := a.Plugins.List()
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Name == plugin {
			for _, t := range r.Targets {
				personal = personal || t == profile.Personal
			}
		}
	}
	if !personal {
		if err := a.Plugins.RemoveFromProfile(ctx, plugin, w.Profile); err != nil {
			return err
		}
	}
	return a.Workspaces.SetOwner(id, workspace.Personal)
}

// InstallPlugin installs a checked plan and starts its sidecar.
func (a *App) InstallPlugin(ctx context.Context, plan *host.Plan) (*host.Installed, error) {
	if plan.Replaces != nil {
		// An update: the old sidecar and the runtimes of the plugin's own
		// profiles stop while the versions are swapped.
		name := plan.Replaces.Name
		a.Sidecars.Stop(ctx, name)
		a.stopOwnRuntimes(name)
		rec, err := a.Plugins.Install(ctx, plan)
		// Whatever is installed now, new or put back, runs again.
		if recs, lerr := a.Plugins.List(); lerr == nil {
			for _, r := range recs {
				if r.Name == name && !r.Incomplete && r.Sidecar != "" {
					a.Sidecars.Start(context.WithoutCancel(ctx), a.sidecarSpec(r))
				}
			}
		}
		return rec, err
	}
	rec, err := a.Plugins.Install(ctx, plan)
	if err != nil {
		return nil, err
	}
	if rec.Sidecar != "" {
		a.Sidecars.Start(ctx, a.sidecarSpec(*rec))
	}
	return rec, nil
}

// UninstallPlugin stops its sidecar, then removes the package everywhere.
func (a *App) UninstallPlugin(ctx context.Context, name string) error {
	a.Sidecars.Stop(ctx, name)
	a.stopOwnRuntimes(name)
	// Secrets go first: if the credential store refuses, the plugin stays
	// installed and uninstalling again retries the cleanup.
	if err := a.forgetPluginSecrets(name); err != nil {
		return fmt.Errorf("remove %s's secrets: %w", name, err)
	}
	return a.Plugins.Uninstall(ctx, name)
}

// stopOwnRuntimes stops the runtimes plugin name ran in its own profiles
// (e.g. daily), not workspace ones.
func (a *App) stopOwnRuntimes(name string) {
	recs, _ := a.Plugins.List()
	own := map[string]bool{}
	for _, r := range recs {
		if r.Name == name {
			for _, p := range r.Profiles {
				if !strings.HasPrefix(p, "ws-") {
					own[p] = true
				}
			}
		}
	}
	// Keys includes starts still in progress (Stop waits for them).
	for _, k := range a.Runtimes.Keys() {
		if own[k.Profile] {
			a.Runtimes.Stop(k)
		}
	}
}

// Settings reads settings.json.
func (a *App) Settings() (Settings, error) {
	var s Settings
	err := fsutil.ReadJSON(a.Layout.Settings(), &s)
	return s, err
}

// migrateModel moves the model versions before 0.1 kept in settings.json
// onto the credential they ran it with (the default one, else the first):
// credentials now carry their own model.
func (a *App) migrateModel() error {
	var legacy struct {
		Model string `json:"model"`
	}
	if err := fsutil.ReadJSON(a.Layout.Settings(), &legacy); err != nil || legacy.Model == "" {
		return err
	}
	s, err := a.Settings()
	if err != nil {
		return err
	}
	id := s.DefaultCredential
	if id == "" {
		list, err := a.Creds.List()
		if err != nil || len(list) == 0 {
			return err // no credential yet: keep the model for when one exists
		}
		id = list[0].ID
	}
	cr, err := a.Creds.Get(id)
	if err != nil {
		return err
	}
	if cr.Model == "" {
		if err := a.Creds.SetModel(id, legacy.Model); err != nil {
			return err
		}
	}
	return a.SaveSettings(s) // Settings has no model field: this drops it
}

// SaveSettings writes settings.json.
func (a *App) SaveSettings(s Settings) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return fsutil.WriteJSON(a.Layout.Settings(), s, 0o600)
}

func (a *App) pluginLog(plugin, level, msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(a.Layout.Logs(), "plugins.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]string{"plugin": plugin, "level": level, "msg": msg})
	f.Write(append(b, '\n'))
}
