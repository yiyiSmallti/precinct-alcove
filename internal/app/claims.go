package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// A runtime a plugin starts for one of its own profiles (runtime.ensure)
// runs on the person's own credential, unless another plugin claims it in
// its launch hook: the organisation plugin does for the daily bot of a
// member, whose bot workspace is the organisation's. A claimed runtime runs
// on the claimant's route or not at all, like a workspace attached to a
// plugin. The claim is kept on disk, so that a claimant that cannot be
// asked (stopped, crashed, slow) keeps the runtime from starting rather
// than letting it fall back to the personal credential; it ends when the
// claimant answers without claiming.

type claims struct {
	mu   sync.Mutex
	path string
	m    map[string]string // profile + "\x00" + cwd → plugin
}

func (c *claims) load() error {
	if c.m != nil {
		return nil
	}
	c.m = map[string]string{}
	b, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &c.m)
}

func (c *claims) save() error {
	b, _ := json.MarshalIndent(c.m, "", "  ")
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// runtimeOwner decides whose route the runtime of prof in cwd starts on:
// "" for the person's own credential, or the plugin that holds it.
func (a *App) runtimeOwner(prof, cwd string, hooks sidecar.LaunchResult) (string, error) {
	c := &a.claims
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		c.path = filepath.Join(a.cfg.DataDir, "runtime-claims.json")
	}
	if err := c.load(); err != nil {
		return "", fmt.Errorf("runtime claims: %w", err)
	}
	key := prof + "\x00" + cwd
	held := c.m[key]
	var claimants []string
	for name := range hooks.Claims {
		claimants = append(claimants, name)
	}
	switch {
	case held != "" && !hooks.Answered[held]:
		why := "its launch hook could not be asked"
		if err := hooks.Failed[held]; err != nil {
			why = "its launch hook failed: " + err.Error()
		} else if !a.pluginInstalled(held) {
			why = "that plugin is no longer installed"
		}
		return "", fmt.Errorf("the %s runtime belongs to %s, and %s", prof, held, why)
	case held != "" && !hooks.Claims[held]:
		// The holder gave it back (the organisation is gone).
		delete(c.m, key)
		if err := c.save(); err != nil {
			c.m[key] = held
			return "", fmt.Errorf("runtime claims: %w", err)
		}
		held = ""
	}
	switch {
	case len(claimants) > 1:
		return "", fmt.Errorf("the %s runtime is claimed by more than one plugin: %v", prof, claimants)
	case len(claimants) == 0:
		return "", nil
	}
	owner := claimants[0]
	if held != "" && held != owner {
		return "", fmt.Errorf("the %s runtime belongs to %s; %s may not take it over", prof, held, owner)
	}
	if held == "" {
		c.m[key] = owner
		if err := c.save(); err != nil {
			delete(c.m, key)
			return "", fmt.Errorf("runtime claims: %w", err)
		}
	}
	if _, ok := hooks.Providers[owner]; !ok {
		return "", fmt.Errorf("the %s runtime belongs to %s, and its launch hook gave no model route", prof, owner)
	}
	return owner, nil
}
