package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
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

	// Without every stored value the value-based redaction cannot be done:
	// no bundle then (a locked keychain, say).
	secrets, err := a.secretValues()
	if err != nil {
		return nil, fmt.Errorf("读不到系统凭据库，无法脱敏：%w", err)
	}
	extra := map[string][]byte{"about.json": aboutJSON}
	audit := filepath.Join(a.Layout.Logs(), "decisions.jsonl")
	if b, err := decisionsSummary(audit); err == nil {
		extra[rel(audit)] = b // replaces the full log, which holds conversation text
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	return diag.Export(w, diag.Source{
		Root: a.Layout.Root,
		Files: []string{
			rel(a.Layout.Settings()), rel(a.Layout.Workspaces()), rel(a.Layout.Credentials()),
			rel(filepath.Join(a.Layout.Plugins(), "installed.json")), rel(a.pluginSecretsIndex()),
		},
		Dirs:    []string{rel(a.Layout.Logs()), rel(a.Layout.PluginSettings())},
		Extra:   extra,
		Secrets: secrets,
		Home:    home,
	})
}

// secretValues are all values Alcove keeps in the system credential store.
func (a *App) secretValues() ([][]byte, error) {
	out, err := a.Creds.Keys()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	idx := map[string][]string{}
	err = fsutil.ReadJSON(a.pluginSecretsIndex(), &idx)
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for plugin, names := range idx {
		for _, n := range names {
			v, err := a.cfg.Secrets.Get(pluginSecretKey(plugin, n))
			switch {
			case errors.Is(err, credentials.ErrNotFound):
			case err != nil:
				return nil, fmt.Errorf("read %s's secret %s: %w", plugin, n, err)
			default:
				out = append(out, v)
			}
		}
	}
	return out, nil
}

// decisionsSummary is the approval and question log without what was
// asked and answered (tool reasons, question text, the user's answers are
// conversation): per record its time, kind, runtime, tool, outcome and how
// many questions.
func decisionsSummary(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec struct {
			Time    string `json:"time"`
			Kind    string `json:"kind"`
			Runtime string `json:"runtime"`
			Outcome any    `json:"outcome"`
			Request struct {
				ToolName  string            `json:"toolName"`
				Questions []json.RawMessage `json:"questions"`
			} `json:"request"`
		}
		sum := map[string]any{}
		if json.Unmarshal(line, &rec) != nil {
			sum["unreadable"] = true
		} else {
			sum["time"], sum["kind"], sum["runtime"] = rec.Time, rec.Kind, rec.Runtime
			if rec.Request.ToolName != "" {
				sum["toolName"] = rec.Request.ToolName
			}
			if rec.Outcome != nil {
				sum["outcome"] = rec.Outcome
			}
			if rec.Kind == "questions" {
				sum["questions"] = len(rec.Request.Questions)
			}
		}
		l, _ := json.Marshal(sum)
		out.Write(append(l, '\n'))
	}
	return out.Bytes(), nil
}
