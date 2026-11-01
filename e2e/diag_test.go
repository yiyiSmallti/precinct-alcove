//go:build e2e

// Acceptance A4 (PRD 8): the diagnostics bundle carries logs and settings
// but no key. A run with a real runtime and a plugin secret is exported and
// the zip scanned for the stored values and for anything shaped like a key.
// Writes e2e/report/diag.md.
package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

func TestDiagnostics(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "A4 诊断包：日志与设置齐全、不含任何 key（PRD 8）", file: "diag.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	const key = "sk-e2e-diag-0123456789abcdef"
	const pluginSecret = "plugin-secret-value-42"
	a, err := app.New(app.Config{
		DataDir: filepath.Join(work, "data"), AlcoveVersion: "0.1.0",
		Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
		Secrets: &memSecrets{m: map[string][]byte{}}, Answerer: &scriptedAnswerer{outcome: "allowed-once"},
	})
	if err == nil {
		err = a.Start(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	model, err := fakemodel.Start(1)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	a.Creds.Add("假模型", "deepseek-official", model.URL, key)
	a.SaveSettings(app.Settings{Model: "deepseek-v4-flash"})

	// A real turn, so there are decisions and runtime logs to export.
	dir := filepath.Join(work, "proj")
	os.MkdirAll(dir, 0o755)
	w, err := a.CreateWorkspace(ctx, "诊断", dir)
	if err == nil {
		proc, oerr := a.OpenWorkspace(ctx, w.ID)
		if err = oerr; err == nil {
			err = turn(ctx, proc, "diag-"+fmt.Sprint(time.Now().UnixNano()))
		}
	}
	r.step("准备：凭据 + 工作区 + 一个真实回合", err, "")

	// Leaks the bundle must catch: the key in a log line, in an env dump,
	// in a plugin's settings, and a plugin secret.
	logs := a.Layout.Logs()
	os.MkdirAll(logs, 0o700)
	f, _ := os.OpenFile(filepath.Join(logs, "alcove.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	fmt.Fprintf(f, "launch env DEEPSEEK_API_KEY=%s\nrequest failed with key %s\n", key, key)
	f.Close()
	a.Sidecars.Settings.Write("@precinct/e2e", json.RawMessage(fmt.Sprintf(`{"apiKey":%q,"note":"%s"}`, key, pluginSecret)))
	_, serr := a.SetPluginSecret("@precinct/e2e", "token", []byte(pluginSecret))

	var buf bytes.Buffer
	m, err := a.ExportDiagnostics(ctx, &buf)
	if err == nil {
		err = serr
	}
	var names []string
	all := map[string]string{}
	if err == nil {
		zr, zerr := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err = zerr; err == nil {
			for _, zf := range zr.File {
				rc, _ := zf.Open()
				b, _ := io.ReadAll(rc)
				rc.Close()
				names = append(names, zf.Name)
				all[zf.Name] = string(b)
			}
		}
	}
	if err == nil {
		for _, want := range []string{"about.json", "manifest.json", "settings.json", "credentials.json", "workspaces.json", "logs/alcove.log"} {
			if _, ok := all[want]; !ok {
				err = fmt.Errorf("missing %s (have %v)", want, names)
				break
			}
		}
	}
	r.step("导出：含 about、manifest、设置、凭据记录（无 key）、工作区、日志", err, strings.Join(names, ", "))

	// The scan: stored values, key shapes, conversation content.
	var leaks []string
	shape := regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)
	for name, body := range all {
		if strings.Contains(body, key) || strings.Contains(body, pluginSecret) {
			leaks = append(leaks, name+": stored value")
		}
		if shape.MatchString(body) {
			leaks = append(leaks, name+": key-shaped "+shape.FindString(body))
		}
		if strings.HasPrefix(name, "dsh/") {
			leaks = append(leaks, name+": dsh home included")
		}
	}
	err = nil
	if len(leaks) > 0 {
		err = errors.New(strings.Join(leaks, "; "))
	}
	detail := ""
	if m != nil {
		detail = fmt.Sprintf("脱敏 %d 处", m.Redactions)
	}
	r.step("扫描：不含存过的 key 与插件密钥、不含 sk- 形状、不含 dsh 会话目录", err, detail)
}
