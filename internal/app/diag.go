package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
	"github.com/yiyiSmallti/precinct-alcove/internal/diag"
)

// ExportDiagnostics writes the diagnostics bundle (PRD 8, A4) to w: logs,
// settings, workspace and plugin records, plugin settings and a summary of
// what is running, with every stored key and anything that looks like a
// secret taken out. Conversations are not included.
func (a *App) ExportDiagnostics(ctx context.Context, w io.Writer) (*diag.Manifest, error) {
	rel := func(p string) string { r, _ := filepath.Rel(a.Layout.Root, p); return r }
	about := map[string]any{
		"alcoveVersion": a.cfg.AlcoveVersion,
		"dshVersion":    a.Sidecars.DSHVersion,
		"os":            runtime.GOOS,
		"arch":          runtime.GOARCH,
		"goVersion":     runtime.Version(),
		"exportedAt":    time.Now().UTC(),
		"localEnabled":  a.localEnabled(),
	}
	if v, err := a.CLI.Version(ctx); err == nil {
		about["dshVersion"] = v
	}
	var runtimes []map[string]string
	for _, info := range a.Runtimes.List() {
		runtimes = append(runtimes, map[string]string{"profile": info.Profile, "cwd": info.Cwd})
	}
	about["runtimes"] = runtimes
	sidecars := map[string]any{}
	if recs, err := a.Plugins.List(); err == nil {
		for _, r := range recs {
			if st, ok := a.Sidecars.Status(r.Name); ok {
				sidecars[r.Name] = st
			}
		}
	}
	about["sidecars"] = sidecars
	aboutJSON, _ := json.MarshalIndent(about, "", "  ")

	home, _ := os.UserHomeDir()
	return diag.Export(w, diag.Source{
		Root: a.Layout.Root,
		Files: []string{
			rel(a.Layout.Settings()), rel(a.Layout.Workspaces()), rel(a.Layout.Credentials()),
			rel(filepath.Join(a.Layout.Plugins(), "installed.json")), rel(a.pluginSecretsIndex()),
		},
		Dirs:    []string{rel(a.Layout.Logs()), rel(a.Layout.PluginSettings())},
		Extra:   map[string][]byte{"about.json": aboutJSON},
		Secrets: a.secretValues(),
		Home:    home,
	})
}

// secretValues are all values Alcove keeps in the system credential store.
func (a *App) secretValues() [][]byte {
	out := a.Creds.Keys()
	a.mu.Lock()
	idx := map[string][]string{}
	fsutil.ReadJSON(a.pluginSecretsIndex(), &idx)
	a.mu.Unlock()
	for plugin, names := range idx {
		for _, n := range names {
			if v, err := a.cfg.Secrets.Get(pluginSecretKey(plugin, n)); err == nil {
				out = append(out, v)
			}
		}
	}
	return out
}
