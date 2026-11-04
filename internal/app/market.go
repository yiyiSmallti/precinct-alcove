package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/pkg"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// market is the host side of a plugin market (plugin_install cap). The
// market only finds and downloads packages; Alcove checks each one like a
// file install and the user confirms it.
type market struct{ a *App }

func (m market) Plugins() ([]sidecar.InstalledPlugin, error) {
	recs, err := m.a.Plugins.List()
	var out []sidecar.InstalledPlugin
	for _, r := range recs {
		out = append(out, sidecar.InstalledPlugin{Name: r.Name, Version: r.Version, Incomplete: r.Incomplete})
	}
	return out, err
}

func (m market) TrustedKeys() []string {
	var out []string
	for _, k := range m.a.cfg.TrustedKeys {
		out = append(out, base64.StdEncoding.EncodeToString(k))
	}
	return out
}

func (m market) Install(ctx context.Context, plugin, path string) (sidecar.InstallResult, error) {
	a := m.a
	plan, err := a.Plugins.Prepare(ctx, path)
	if err != nil {
		return sidecar.InstallResult{}, err
	}
	if !plan.OK() {
		a.Plugins.Discard(plan)
		var msgs []string
		for _, p := range plan.Problems {
			msgs = append(msgs, p.Message)
		}
		return sidecar.InstallResult{}, errors.New(strings.Join(msgs, "; "))
	}
	res := sidecar.InstallResult{Name: plan.Package.Name, Version: plan.Package.Version}
	if a.cfg.ConfirmInstall == nil || !a.cfg.ConfirmInstall(ctx, plugin, plan) {
		a.Plugins.Discard(plan)
		return res, nil
	}
	// A market may update itself: its sidecar stops midway, and the update
	// must not stop with it.
	_, err = a.InstallPlugin(context.WithoutCancel(ctx), plan)
	if a.cfg.PluginsChanged != nil {
		a.cfg.PluginsChanged()
	}
	if err != nil {
		return sidecar.InstallResult{}, err
	}
	res.Installed = true
	return res, nil
}

// preinstall installs the packages shipped with Alcove that are missing,
// and updates installed ones the shipped version is newer than. A package
// the user uninstalled after its first install stays uninstalled.
func (a *App) preinstall(ctx context.Context) {
	changed := false
	for _, path := range requiredFirst(a.cfg.Preinstall) {
		installed, err := a.preinstallOne(ctx, path)
		if err != nil {
			a.pluginLog("host", "warn", fmt.Sprintf("preinstall %s: %v", path, err))
		}
		changed = changed || installed
	}
	if changed && a.cfg.PluginsChanged != nil {
		a.cfg.PluginsChanged()
	}
}

func (a *App) preinstallOne(ctx context.Context, path string) (bool, error) {
	p, err := pkg.Inspect(path)
	if err != nil {
		return false, err
	}
	// The names installed once, so an uninstalled one stays uninstalled.
	var offered []string
	listPath := filepath.Join(a.Layout.Plugins(), "preinstalled.json")
	if err := fsutil.ReadJSON(listPath, &offered); err != nil {
		return false, err
	}
	recs, err := a.Plugins.List()
	if err != nil {
		return false, err
	}
	var cur *host.Installed
	for i := range recs {
		if recs[i].Name == p.Name {
			cur = &recs[i]
		}
	}
	switch {
	case cur == nil && contains(offered, p.Name):
		return false, nil // uninstalled by the user
	case cur != nil && cur.Incomplete:
		return false, nil // the plugin page offers the cleanup
	case cur != nil:
		if newer, err := pkg.Satisfies(cur.Version, "<"+p.Version); err != nil || !newer {
			return false, nil
		}
	}
	plan, err := a.Plugins.Prepare(ctx, path)
	if err != nil {
		return false, err
	}
	if !plan.OK() {
		a.Plugins.Discard(plan)
		return false, fmt.Errorf("%s", plan.Problems[0])
	}
	if _, err := a.InstallPlugin(ctx, plan); err != nil {
		return false, err
	}
	if !contains(offered, p.Name) {
		if err := fsutil.WriteJSON(listPath, append(offered, p.Name), 0o600); err != nil {
			return true, err
		}
	}
	return true, nil
}

// requiredFirst orders shipped packages so that each comes after the shipped
// ones its alcove.requires names (cloister needs daily installed first). A
// package that cannot be read, or a requirement cycle, keeps its place;
// installing it then reports the problem.
func requiredFirst(paths []string) []string {
	needs := map[string][]string{} // path -> names it requires
	names := map[string]string{}   // name -> path
	for _, path := range paths {
		p, err := pkg.Inspect(path)
		if err != nil {
			continue
		}
		names[p.Name] = path
		reqs, _ := p.Requirements()
		for _, r := range reqs {
			needs[path] = append(needs[path], r.Name)
		}
	}
	var out []string
	placed := map[string]bool{}
	var place func(path string, seen map[string]bool)
	place = func(path string, seen map[string]bool) {
		if placed[path] || seen[path] {
			return
		}
		seen[path] = true
		for _, n := range needs[path] {
			if dep, ok := names[n]; ok {
				place(dep, seen)
			}
		}
		placed[path] = true
		out = append(out, path)
	}
	for _, path := range paths {
		place(path, map[string]bool{})
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
