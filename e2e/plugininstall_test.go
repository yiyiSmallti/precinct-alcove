//go:build e2e

// End-to-end checks for the plugin-package install path (acceptance A3, the
// install and uninstall half) against the real dsh CLI.
//
//	ALCOVE_E2E_NODE=/path/to/node \
//	ALCOVE_E2E_DSH=/path/to/node_modules/@deepseek-ai/dsh/lib/bin.js \
//	go test -tags e2e ./e2e/ -run PluginInstall -v
//
// pnpm must be on PATH (or in ALCOVE_E2E_TOOLDIR). Each run writes
// e2e/report/plugin-install.md.
package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

type report struct {
	t           *testing.T
	title, file string
	lines       []string
}

func (r *report) step(name string, err error, detail string) {
	r.t.Helper()
	status := "PASS"
	if err != nil {
		status = "FAIL"
		detail = strings.TrimSpace(detail + " " + err.Error())
		r.t.Errorf("%s: %v", name, err)
	}
	r.lines = append(r.lines, fmt.Sprintf("| %s | %s | %s |", name, status, strings.ReplaceAll(detail, "|", "/")))
}

func (r *report) write(env string) {
	os.MkdirAll("report", 0o755)
	body := "# " + r.title + "\n\n" + env + "\n\n| 步骤 | 结果 | 说明 |\n| --- | --- | --- |\n" + strings.Join(r.lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join("report", r.file), []byte(body), 0o644); err != nil {
		r.t.Log(err)
	}
}

func TestPluginInstall(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	cli := testCLI(t, work)
	dshVersion, err := cli.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)

	targets := []string{"ws-e2e-1", "ws-e2e-2"}
	h := &host.Host{
		DSH: cli, Dir: filepath.Join(work, "alcove", "plugins"), AlcoveVersion: "0.1.0", GOOS: "windows",
		TrustedKeys:     []ed25519.PublicKey{pub},
		ResolveProfiles: func(name string) []string { return map[string][]string{"personal": targets}[name] },
	}
	r := &report{t: t, title: "插件包安装链路 e2e（A3 安装与卸载部分）", file: "plugin-install.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", dshVersion, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	sidecar := buildSidecar(t, work)
	mk := func(version string, edit func(pj map[string]any)) string {
		return pack(t, work, version, sidecar, edit)
	}
	signWith := func(tgz string, k ed25519.PrivateKey) {
		s, err := sig.Sign(tgz, k)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(tgz+".sig", []byte(s), 0o644)
	}

	// 1. Unsigned, wrongly signed and tampered packages never reach dsh.
	good := mk("1.0.0", nil)
	_, err = h.Prepare(ctx, good)
	r.step("拒绝无签名的包", expectStep(err, "signature", sig.ErrUnsigned), "")
	signWith(good, otherPriv)
	_, err = h.Prepare(ctx, good)
	r.step("拒绝不受信任的签名", expectStep(err, "signature", sig.ErrBadSignature), "")
	signWith(good, priv)

	// 2. Package rules are checked before anything is installed.
	for _, c := range []struct {
		name, code string
		edit       func(pj map[string]any)
	}{
		{"engines.dsh 不覆盖随包 dsh 时拒绝", "engines", func(pj map[string]any) { pj["engines"] = map[string]any{"dsh": ">=99.0.0"} }},
		{"没有 dsh.bundle.patch 时拒绝", "not-bundle", func(pj map[string]any) { delete(pj, "dsh") }},
		{"alcove.host 不覆盖当前 Alcove 时拒绝", "host-range", func(pj map[string]any) { pj["alcove"].(map[string]any)["host"] = ">=2.0.0" }},
		{"带 postinstall 脚本时拒绝", "scripts", func(pj map[string]any) { pj["scripts"] = map[string]any{"postinstall": "echo hi"} }},
		{"缺依赖插件时提示先装", "requires", func(pj map[string]any) { pj["alcove"].(map[string]any)["requires"] = []any{"@precinct/daily@^1"} }},
	} {
		tgz := mk("0.0."+fmt.Sprint(len(r.lines)), c.edit)
		signWith(tgz, priv)
		plan, err := h.Prepare(ctx, tgz)
		if err == nil {
			err = expectProblem(plan, c.code)
		}
		r.step(c.name, err, "")
	}
	r.step("预检阶段没有建任何 profile", noProfiles(cli.Home), "")

	// 3. A good package installs into the store profile and both targets.
	plan, err := h.Prepare(ctx, good)
	if err == nil && !plan.OK() {
		err = fmt.Errorf("unexpected problems: %v", plan.Problems)
	}
	r.step("合格包预检通过", err, fmt.Sprintf("目标 profile：%s", strings.Join(planProfiles(plan), ", ")))
	if err != nil {
		return
	}
	// A second plan for the same package, prepared before the first install.
	stale := mk("1.0.3", nil)
	signWith(stale, priv)
	stalePlan, staleErr := h.Prepare(ctx, stale)
	rec, err := h.Install(ctx, plan)
	r.step("安装（包仓 + 两个目标 profile + --dump-config）", err, "")
	if err != nil {
		keepPluginLogs(t, cli.Home)
		return
	}
	if err = staleErr; err == nil {
		var se *host.StepError
		_, err = h.Install(ctx, stalePlan)
		switch {
		case err == nil:
			err = errors.New("stale plan installed over the first install")
		case !errors.As(err, &se) || se.Step != "check":
			err = fmt.Errorf("unexpected error: %w", err)
		default:
			err = nil
		}
	}
	r.step("预检后状态变化：执行时重新检查，拒绝过期计划", err, "")
	for _, p := range append([]string{host.StoreProfile}, targets...) {
		m, err := cli.ReadProfile(p)
		if err == nil && !m.HasBundle(rec.Name) {
			err = errors.New("not in dsh.profile.bundles")
		}
		r.step("已选入组合包："+p, err, "")
	}
	exe := h.SidecarPath(*rec)
	_, err = os.Stat(exe)
	r.step("边车可执行文件就位", err, filepath.Base(exe))
	os.Remove(good) // the original download may go away; the stored copy must keep the profile usable
	_, err = cli.PluginAdd(ctx, targets[0], rec.Stored)
	r.step("原始 .tgz 删除后 profile 仍可操作", err, "")

	again := mk("1.0.1", nil)
	signWith(again, priv)
	plan2, err := h.Prepare(ctx, again)
	if err == nil {
		err = expectProblem(plan2, "already-installed")
	}
	r.step("同名包已安装时拒绝重复安装", err, "")

	// An unreadable target manifest is not "already removed": uninstall must
	// fail and keep the archive and the record.
	manifest := filepath.Join(cli.ProfileDir(targets[0]), "package.json")
	orig, _ := os.ReadFile(manifest)
	os.WriteFile(manifest, []byte("{"), 0o644)
	err = h.Uninstall(ctx, rec.Name)
	if err == nil {
		err = errors.New("uninstall succeeded with an unreadable profile")
	} else if list, _ := h.List(); len(list) != 1 || !list[0].Incomplete {
		err = fmt.Errorf("record dropped or not marked incomplete: %+v", list)
	} else if _, serr := os.Stat(rec.Stored); serr != nil {
		err = fmt.Errorf("archive deleted: %w", serr)
	} else {
		err = nil
	}
	os.WriteFile(manifest, orig, 0o644)
	r.step("profile 清单损坏时卸载报错并保留记录与包", err, "")

	// 4. Uninstall leaves nothing behind.
	err = h.Uninstall(ctx, rec.Name)
	r.step("卸载", err, "")
	for _, p := range append([]string{host.StoreProfile}, targets...) {
		r.step("无残留："+p, clean(cli, p, rec.Name), "")
	}
	_, err = os.Stat(rec.Stored)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	} else if err == nil {
		err = errors.New("stored .tgz still there")
	}
	r.step("包目录里的 .tgz 已删除", err, "")

	// 5. A failure in the second target rolls back everything already done.
	broken := targets[1]
	os.WriteFile(filepath.Join(cli.ProfileDir(broken), "cordis.patch.yml"), []byte("- id: [not, a, valid, id\n"), 0o644)
	rb := mk("1.0.2", nil)
	signWith(rb, priv)
	plan3, err := h.Prepare(ctx, rb)
	if err != nil {
		r.step("回滚：预检", err, "")
		return
	}
	_, err = h.Install(ctx, plan3)
	var se *host.StepError
	switch {
	case err == nil:
		err = errors.New("install succeeded on a broken profile")
	case !errors.As(err, &se):
		err = fmt.Errorf("not a StepError: %w", err)
	case se.Profile != broken || !se.RolledBack:
		err = fmt.Errorf("failed at %s (%s), rolled back %v", se.Step, se.Profile, se.RolledBack)
	default:
		err = nil
	}
	detail := ""
	if se != nil {
		detail = "失败步骤 " + se.Step + "：" + strings.Join(se.Diagnostics, "; ")
	}
	r.step("第二个目标 profile 失败时整体回滚", err, detail)
	for _, p := range append([]string{host.StoreProfile}, targets...) {
		r.step("回滚后无残留："+p, clean(cli, p, "@precinct/alcove-e2e"), "")
	}
	list, _ := h.List()
	if len(list) != 0 {
		r.step("回滚后无安装记录", fmt.Errorf("%d records", len(list)), "")
	} else {
		r.step("回滚后无安装记录", nil, "")
	}
}

func expectStep(err error, step string, target error) error {
	var se *host.StepError
	if !errors.As(err, &se) || se.Step != step || !errors.Is(err, target) {
		return fmt.Errorf("want %s error %v, got %v", step, target, err)
	}
	return nil
}

func expectProblem(plan *host.Plan, code string) error {
	for _, p := range plan.Problems {
		if p.Code == code {
			return nil
		}
	}
	return fmt.Errorf("want problem %q, got %v", code, plan.Problems)
}

func planProfiles(p *host.Plan) []string {
	if p == nil {
		return nil
	}
	return p.Profiles
}

func noProfiles(home string) error {
	entries, err := os.ReadDir(filepath.Join(home, "profiles"))
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
		return nil
	}
	return fmt.Errorf("profiles created: %d", len(entries))
}

func clean(cli *dsh.CLI, profile, name string) error {
	m, err := cli.ReadProfile(profile)
	if err != nil {
		return err
	}
	if m.HasDependency(name) || m.HasBundle(name) {
		return errors.New("still declared")
	}
	if _, err := os.Stat(cli.PackageDir(profile, name)); err == nil {
		return errors.New("still in node_modules")
	}
	if scope, _, ok := strings.Cut(name, "/"); ok {
		if _, err := os.Stat(filepath.Join(cli.ProfileDir(profile), "node_modules", scope)); err == nil {
			return errors.New("empty scope directory left")
		}
	}
	return nil
}

// buildSidecar cross-compiles a trivial Windows sidecar, or reads a prebuilt
// one from ALCOVE_E2E_SIDECAR on machines without Go.
func buildSidecar(t *testing.T, work string) []byte {
	if p := os.Getenv("ALCOVE_E2E_SIDECAR"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ALCOVE_E2E_SIDECAR: %v", err)
		}
		return b
	}
	dir := filepath.Join(work, "sidecar-src")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module sidecar\n\ngo 1.23\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport (\"bufio\"; \"os\")\n\nfunc main() { s := bufio.NewScanner(os.Stdin); for s.Scan() { os.Stdout.Write(append(s.Bytes(), '\\n')) } }\n"), 0o644)
	out := filepath.Join(work, "sidecar.exe")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sidecar: %v\n%s", err, b)
	}
	b, _ := os.ReadFile(out)
	return b
}

// pack writes an npm-style tarball (entries under package/).
func pack(t *testing.T, work, version string, sidecar []byte, edit func(map[string]any)) string {
	pj := map[string]any{
		"name":        "@precinct/alcove-e2e",
		"version":     version,
		"description": "Alcove e2e test plugin",
		"engines":     map[string]any{"dsh": ">=0.2.1-rc.5 <0.3.0"},
		"dsh":         map[string]any{"bundle": map[string]any{"patch": "./cordis.patch.yml"}},
		"alcove": map[string]any{
			"protocol": 1, "host": ">=0.1.0", "capabilities": []any{"settings_page"},
			"profiles": []any{"personal"}, "sidecar": map[string]any{"windows": "bin/alcove-e2e.exe"},
			"ui": []any{map[string]any{"type": "settings_page", "id": "e2e", "sections": []any{}}},
		},
	}
	if edit != nil {
		edit(pj)
	}
	pjBytes, _ := json.MarshalIndent(pj, "", "  ")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, data []byte) {
		tw.WriteHeader(&tar.Header{Name: "package/" + name, Mode: mode, Size: int64(len(data)), ModTime: time.Unix(499162500, 0), Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	add("package.json", 0o644, pjBytes)
	add("cordis.patch.yml", 0o644, []byte("[]\n"))
	add("bin/alcove-e2e.exe", 0o755, sidecar)
	tw.Close()
	gz.Close()
	out := filepath.Join(work, fmt.Sprintf("alcove-e2e-%s-%d.tgz", version, time.Now().UnixNano()))
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return out
}

// keepPluginLogs copies dsh's plugin-manager logs out of the temporary dsh
// home into report/plugin-logs, which outlives the test, so a failed install
// on a test machine can be diagnosed afterwards.
func keepPluginLogs(t *testing.T, home string) {
	dst := filepath.Join("report", "plugin-logs")
	os.RemoveAll(dst)
	filepath.WalkDir(filepath.Join(home, "profiles"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(filepath.ToSlash(path), "/.plugin-manager/") {
			return nil
		}
		rel, _ := filepath.Rel(home, path)
		b, err := os.ReadFile(path)
		if err == nil {
			out := filepath.Join(dst, rel)
			os.MkdirAll(filepath.Dir(out), 0o755)
			os.WriteFile(out, b, 0o644)
		}
		return nil
	})
	t.Logf("plugin-manager files copied to %s", dst)
}
