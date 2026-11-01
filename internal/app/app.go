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

	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/identity"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/paths"
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
	Inbox         func(plugin string, item json.RawMessage) error
}

// Settings are Alcove's own preferences (settings.json).
type Settings struct {
	DefaultCredential string `json:"defaultCredential,omitempty"`
	Model             string `json:"model,omitempty"`
	ReasoningEffort   string `json:"reasoningEffort,omitempty"`
	// UpdateSource is a mirror of the release files (an https directory);
	// "" is the feed built into the app.
	UpdateSource string `json:"updateSource,omitempty"`
}

// App is one running Alcove.
type App struct {
	Layout     paths.Layout
	Identity   *identity.Identity
	CLI        *dsh.CLI
	Creds      *credentials.Store
	Workspaces *workspace.Store
	Plugins    *host.Host
	Profiles   *profile.Assembler
	Bridge     *alrt.Bridge
	Runtimes   *alrt.Manager
	Sidecars   *sidecar.Manager

	// OnRuntimeExit is called after a runtime exits (the UI's hook); set it
	// before Start.
	OnRuntimeExit func(k alrt.Key, runtimeID string, err error)

	cfg Config
	mu  sync.Mutex
	// Workspace lifecycle (opened/closed), by runtime id: each started
	// workspace runtime gets one opened and one closed, in that order.
	lifeMu sync.Mutex
	life   map[string]*lifecycle
	cancel context.CancelFunc
}

// DefaultModel is used until the user picks one.
const DefaultModel = "deepseek-v4-flash"

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
	a := &App{Layout: l, Identity: id, CLI: cli, cfg: cfg}
	a.Creds = &credentials.Store{Path: l.Credentials(), Secrets: cfg.Secrets}
	a.Workspaces = &workspace.Store{Path: l.Workspaces(), DataDir: l.Root}
	a.Plugins = &host.Host{
		DSH: cli, Dir: l.Plugins(), AlcoveVersion: cfg.AlcoveVersion, GOOS: runtime.GOOS,
		TrustedKeys: cfg.TrustedKeys, AllowUnsigned: cfg.AllowUnsigned,
		ResolveProfiles: a.resolveProfiles,
	}
	a.Profiles = &profile.Assembler{DSH: cli, Bridge: cfg.BridgeTgz, Plugins: a.Plugins}
	a.Plugins.PrepareProfile = a.Profiles.Base
	a.Bridge = &alrt.Bridge{Answerer: cfg.Answerer, AuditLog: filepath.Join(l.Logs(), "decisions.jsonl")}
	a.Runtimes = &alrt.Manager{CLI: cli, Bridge: a.Bridge, LocalEnabled: a.localEnabled, OnExit: a.runtimeExited}
	a.Sidecars = &sidecar.Manager{
		AlcoveVersion: cfg.AlcoveVersion,
		Settings:      &sidecar.Settings{Dir: l.PluginSettings()},
		Services: sidecar.Services{
			Workspaces:    workspace.Source{Store: a.Workspaces},
			UI:            cfg.SidecarUI,
			Inbox:         cfg.Inbox,
			EnsureRuntime: a.ensureForPlugin,
			KeychainRef:   a.keychainRef,
			Log:           a.pluginLog,
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

func sidecarView(w *workspace.Workspace) sidecar.Workspace {
	return sidecar.Workspace{ID: w.ID, Name: w.Name, Dir: w.Dir, Profile: w.Profile}
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
	a.Sidecars.WorkspaceEvent(ctx, "removed", sidecarView(w))
	return nil
}

// OpenWorkspace returns the workspace's runtime, starting it with the
// default credential and the plugins' launch hooks.
func (a *App) OpenWorkspace(ctx context.Context, id string) (*alrt.Process, error) {
	w, err := a.Workspaces.Find(id)
	if err != nil {
		return nil, err
	}
	if w.Missing() {
		return nil, fmt.Errorf("workspace folder %s is gone", w.Dir)
	}
	hooks := a.Sidecars.LaunchHooks(ctx, sidecar.LaunchContext{WorkspaceID: w.ID, Profile: w.Profile, Cwd: w.Dir})
	if len(hooks.ProfilePatch) > 0 {
		// Phase one reserves profilePatch (SPEC 3.2): say so rather than
		// drop it silently.
		a.pluginLog("host", "warn", fmt.Sprintf("workspace %s: %d launch-hook profilePatch value(s) ignored; profilePatch is reserved in phase one", w.ID, len(hooks.ProfilePatch)))
	}
	p, err := a.launch(ctx, w.Profile, w.Dir, hooks.Env)
	if err != nil {
		return nil, err
	}
	a.opened(p, alrt.Key{Profile: w.Profile, Cwd: w.Dir}, sidecarView(w))
	return p, nil
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

func (a *App) launch(ctx context.Context, prof, cwd string, extra []string) (*alrt.Process, error) {
	s, err := a.Settings()
	if err != nil {
		return nil, err
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
	provider, env, err := a.Creds.LaunchEnv(credID)
	if err != nil {
		return nil, err
	}
	model := s.Model
	if model == "" {
		model = DefaultModel
	}
	return a.Runtimes.Ensure(ctx, alrt.LaunchSpec{
		Profile: prof, Cwd: cwd, Provider: provider, Model: model, ReasoningEffort: s.ReasoningEffort,
		// Plugin variables first: the credential always wins.
		Env: append(append([]string(nil), extra...), env...),
	})
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
	p, err := a.launch(ctx, prof, cwd, env)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// InstallPlugin installs a checked plan and starts its sidecar.
func (a *App) InstallPlugin(ctx context.Context, plan *host.Plan) (*host.Installed, error) {
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
	// Stop the runtimes it ran in its own profiles (e.g. daily).
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
	// Secrets go first: if the credential store refuses, the plugin stays
	// installed and uninstalling again retries the cleanup.
	if err := a.forgetPluginSecrets(name); err != nil {
		return fmt.Errorf("remove %s's secrets: %w", name, err)
	}
	return a.Plugins.Uninstall(ctx, name)
}

// Settings reads settings.json.
func (a *App) Settings() (Settings, error) {
	var s Settings
	err := fsutil.ReadJSON(a.Layout.Settings(), &s)
	return s, err
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
