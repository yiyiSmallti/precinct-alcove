package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
)

// PreparePlugin checks a package file for the install dialog. Hand the plan
// to InstallPlugin, or to DiscardPlugin when the user cancels.
func (a *App) PreparePlugin(ctx context.Context, tgz string) (*host.Plan, error) {
	return a.Plugins.Prepare(ctx, tgz)
}

// DiscardPlugin drops a plan the user did not install.
func (a *App) DiscardPlugin(plan *host.Plan) { a.Plugins.Discard(plan) }

// RestartPlugin starts an installed plugin's sidecar again (the plugin
// page's button for a crashed or stopped sidecar); it clears the crash
// count.
func (a *App) RestartPlugin(ctx context.Context, name string) error {
	recs, err := a.Plugins.List()
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Name == name && !r.Incomplete && r.Sidecar != "" {
			return a.Sidecars.Start(ctx, a.sidecarSpec(r))
		}
	}
	return fmt.Errorf("%s has no sidecar to start", name)
}

// rebuildingStore is dsh.CLI.Rebuilding: sidecars run from the store
// profile's node_modules, which a rebuild removes (and Windows will not
// remove while they run), so the running ones stop first and start again
// after. It runs inside the plugin host's install and uninstall, under its
// lock, so it goes by the sidecars' own specs, not the host's records.
func (a *App) rebuildingStore(ctx context.Context, profile string) func() {
	if profile != host.StoreProfile {
		return nil
	}
	return a.Sidecars.StopActive(ctx)
}

// Plugin secrets are what a plugin's settings page collects in secretRef
// fields. The value goes to the system credential store; the plugin only
// ever gets the reference (keychain.ref), never the value.

func pluginSecretKey(plugin, name string) string { return "plugin/" + plugin + "/" + name }

func (a *App) pluginSecretsIndex() string { return filepath.Join(a.Layout.Root, "plugin-secrets.json") }

// SetPluginSecret stores value under the plugin's name and returns its
// reference.
func (a *App) SetPluginSecret(plugin, name string, value []byte) (string, error) {
	if plugin == "" || name == "" {
		return "", errors.New("plugin secret needs a plugin and a name")
	}
	key := pluginSecretKey(plugin, name)
	if err := a.cfg.Secrets.Set(key, value); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := map[string][]string{}
	if err := fsutil.ReadJSON(a.pluginSecretsIndex(), &idx); err != nil {
		return "", err
	}
	for _, n := range idx[plugin] {
		if n == name {
			return key, nil
		}
	}
	idx[plugin] = append(idx[plugin], name)
	sort.Strings(idx[plugin])
	return key, fsutil.WriteJSON(a.pluginSecretsIndex(), idx, 0o600)
}

// keychainRef answers keychain.ref: the reference of a secret the plugin
// stored through its settings page.
func (a *App) keychainRef(plugin, name string) (string, error) {
	key := pluginSecretKey(plugin, name)
	if _, err := a.cfg.Secrets.Get(key); err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return "", fmt.Errorf("no secret %q for %s", name, plugin)
		}
		return "", err
	}
	return key, nil
}

// forgetPluginSecrets deletes everything the plugin stored.
func (a *App) forgetPluginSecrets(plugin string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	idx := map[string][]string{}
	if err := fsutil.ReadJSON(a.pluginSecretsIndex(), &idx); err != nil {
		return err
	}
	var errs []error
	for _, n := range idx[plugin] {
		if err := a.cfg.Secrets.Delete(pluginSecretKey(plugin, n)); err != nil && !errors.Is(err, credentials.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if _, ok := idx[plugin]; !ok {
		return nil
	}
	delete(idx, plugin)
	return fsutil.WriteJSON(a.pluginSecretsIndex(), idx, 0o600)
}
