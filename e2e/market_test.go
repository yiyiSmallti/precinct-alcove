//go:build e2e

// End-to-end check of the plugin market (Dovetail's @precinct/dovetail,
// SPEC 3.2 plugin_install): the market ships as a preinstalled package,
// reads a signed index from a local mirror folder, installs a plugin and
// updates it through plugins.install with the user's confirmation, refuses
// a package that differs from the index, and stays uninstalled once the
// user removed it. Builds the market from a precinct-dovetail checkout
// (ALCOVE_E2E_DOVETAIL, default ../../precinct-dovetail) and signs
// everything with a key of its own. Writes e2e/report/market.md.
package e2e

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

const (
	marketPlugin = "@precinct/dovetail"
	marketTarget = "@precinct/market-e2e"
)

// pages keeps the latest data of every contribution.
type pages struct {
	mu sync.Mutex
	m  map[string]string
}

func (p *pages) Register(plugin string, c sidecar.Contribution) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[plugin+"#"+c.ID] = string(c.Raw)
	return nil
}

func (p *pages) Update(plugin, id string, data json.RawMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[plugin+"#"+id] = string(data)
	return nil
}

func (p *pages) Unregister(plugin, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, plugin+"#"+id)
}

func (p *pages) get(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[key]
}

// confirmer is the user at the install card: it says yes and remembers
// what it was shown.
type confirmer struct {
	mu    sync.Mutex
	shown []string
}

func (c *confirmer) confirm(_ context.Context, plugin string, plan *host.Plan) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := plugin + " asks " + plan.Package.Name + " " + plan.Package.Version
	if plan.Replaces != nil {
		s += " replacing " + plan.Replaces.Version
	}
	c.shown = append(c.shown, s)
	return true
}

func (c *confirmer) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.shown) == 0 {
		return ""
	}
	return c.shown[len(c.shown)-1]
}

func (c *confirmer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.shown)
}

func TestMarket(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("Dovetail ships the market sidecar only for Windows and macOS")
	}
	dovetail := os.Getenv("ALCOVE_E2E_DOVETAIL")
	if dovetail == "" {
		dovetail = filepath.Join("..", "..", "precinct-dovetail")
	}
	if _, err := os.Stat(filepath.Join(dovetail, "plugins", "alcove", "dovetail")); err != nil {
		t.Skipf("no precinct-dovetail checkout at %s", dovetail)
	}
	dovetail, _ = filepath.Abs(dovetail)
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "插件市场 e2e：预装、浏览签名索引、安装与更新、篡改拒绝、卸载后不重装（SPEC 3.2 plugin_install）", file: "market.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	// The Dovetail CLI, a signing key, and the market package signed with it.
	run := func(dir string, name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return nil
	}
	cli := filepath.Join(work, "dovetail")
	keyPrefix := filepath.Join(work, "test")
	err := run(dovetail, "go", "build", "-o", cli, "./registry/cli")
	if err == nil {
		err = run(work, cli, "keygen", keyPrefix)
	}
	if err == nil {
		err = run(dovetail, filepath.Join(dovetail, "plugins", "alcove", "dovetail", "build.sh"))
	}
	if err == nil {
		err = run(work, cli, "pack", filepath.Join(dovetail, "plugins", "alcove", "dovetail"), "-o", filepath.Join(work, "market"))
	}
	marketTgz, _ := filepath.Glob(filepath.Join(work, "market", "*.tgz"))
	if err == nil && len(marketTgz) != 1 {
		err = fmt.Errorf("market packages: %v", marketTgz)
	}
	if err == nil {
		err = run(work, cli, "sign", marketTgz[0], "-key", keyPrefix+".key")
	}
	pubRaw, _ := os.ReadFile(keyPrefix + ".pub")
	pub, perr := sig.ParsePublicKey(strings.TrimSpace(string(pubRaw)))
	if err == nil {
		err = perr
	}
	r.step("构建并签名市场插件包（Dovetail 仓库 build.sh + pack + sign）", err, "")
	if err != nil {
		return
	}

	// The index: a local mirror folder the market reads (offline import).
	site := filepath.Join(work, "site")
	publish := func(version string) error {
		tgz := pack(t, work, version, nil, func(pj map[string]any) {
			pj["name"] = marketTarget
			pj["description"] = "市场 e2e 用的插件"
			al := pj["alcove"].(map[string]any)
			delete(al, "sidecar")
			al["host"] = ">=0.1.0 <0.2.0"
		})
		return run(work, cli, "publish", tgz, "-dir", site, "-key", keyPrefix+".key")
	}
	err = publish("1.0.0")
	r.step("发布索引：market-e2e 1.0.0（本机镜像文件夹，测试密钥签名）", err, "")
	if err != nil {
		return
	}

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	ui := &pages{m: map[string]string{}}
	user := &confirmer{}
	var logMu sync.Mutex
	var logs []string
	cfgFor := func() app.Config {
		return app.Config{
			DataDir: filepath.Join(work, "data"), AlcoveVersion: "0.1.0",
			Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
			TrustedKeys: []ed25519.PublicKey{pub}, Secrets: &memSecrets{m: map[string][]byte{}},
			Answerer: &scriptedAnswerer{outcome: "allowed-once"}, SidecarUI: ui,
			ConfirmInstall: user.confirm, Preinstall: marketTgz,
		}
	}
	a, err := app.New(cfgFor())
	if err == nil {
		a.Sidecars.Services.Log = func(plugin, level, msg string) {
			logMu.Lock()
			logs = append(logs, plugin+" "+level+" "+msg)
			logMu.Unlock()
		}
		err = a.Start(ctx)
	}
	if err != nil {
		r.step("启动", err, "")
		return
	}
	defer func() { a.Close() }()
	diag := func() string {
		st, _ := a.Sidecars.Status(marketPlugin)
		logMu.Lock()
		defer logMu.Unlock()
		return fmt.Sprintf("sidecar %s %s %s; logs %v", st.State, st.LastError, st.Stderr, logs)
	}

	// A workspace, so the target plugin has a personal profile to go into.
	dir := filepath.Join(work, "proj")
	os.MkdirAll(dir, 0o755)
	w, err := a.CreateWorkspace(ctx, "项目", dir)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Preinstall: the market is installed on the first start and its
	// sidecar registers the page and the sidebar entry.
	page := func() string { return ui.get(marketPlugin + "#market") }
	nav := func() string { return ui.get(marketPlugin + "#market-nav") }
	err = waitFor(func() bool { return strings.Contains(page(), "还没有设置索引地址") && nav() != "" }, 2*time.Minute)
	if err != nil {
		err = fmt.Errorf("%v: %s", err, diag())
	}
	r.step("首次启动预装市场：边车运行，登记市场页与侧栏入口，提示设置索引", err, "")
	if err != nil {
		return
	}

	action := func(act string, payload any) error {
		raw, _ := json.Marshal(payload)
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return a.Sidecars.UIAction(c, marketPlugin, "market", act, raw)
	}
	installed := func(name string) string {
		recs, _ := a.Plugins.List()
		for _, rec := range recs {
			if rec.Name == name && !rec.Incomplete {
				return rec.Version
			}
		}
		return ""
	}

	// 2. The index: saved in the market's settings, read and verified.
	err = action("submit", map[string]any{"values": map[string]any{"index": site}})
	if err == nil {
		err = waitFor(func() bool {
			return strings.Contains(page(), "最新 1.0.0") && strings.Contains(page(), `"label":"安装"`)
		}, 30*time.Second)
	}
	if err != nil {
		err = fmt.Errorf("%v: page %s", err, page())
	}
	r.step("设置索引地址（本机文件夹）：读取并验证签名，列出可安装的 market-e2e 1.0.0", err, "")
	if err != nil {
		return
	}

	// 3. Install: downloaded, checked, confirmed by the user, installed.
	err = action("install:"+marketTarget, map[string]any{"values": map[string]any{}})
	if err == nil {
		err = waitFor(func() bool { return installed(marketTarget) == "1.0.0" && strings.Contains(page(), "已安装 1.0.0") }, 2*time.Minute)
	}
	if err == nil && user.last() != marketPlugin+" asks "+marketTarget+" 1.0.0" {
		err = fmt.Errorf("confirmation showed %q", user.last())
	}
	if err == nil {
		err = profileHas(a, w.Profile, marketTarget)
	}
	if err != nil {
		err = fmt.Errorf("%v: page %s; %s", err, page(), diag())
	}
	r.step("安装：市场下载并核对 → 宿主校验签名 → 用户确认 → 装进工作区 profile", err, user.last())
	if err != nil {
		return
	}

	// 4. Update: a newer version in the index shows as an update, and
	// installing it swaps the versions in place.
	recs, _ := a.Plugins.List()
	var oldStored string
	for _, rec := range recs {
		if rec.Name == marketTarget {
			oldStored = rec.Stored
		}
	}
	err = publish("1.1.0")
	if err == nil {
		err = action("refresh", map[string]any{"values": map[string]any{"index": site}})
	}
	if err == nil {
		err = waitFor(func() bool {
			return strings.Contains(page(), "最新 1.1.0 · 已安装 1.0.0") && strings.Contains(page(), `"label":"更新"`) && strings.Contains(nav(), "1 个更新")
		}, 30*time.Second)
	}
	if err == nil {
		err = action("install:"+marketTarget, map[string]any{"values": map[string]any{}})
	}
	if err == nil {
		err = waitFor(func() bool {
			return installed(marketTarget) == "1.1.0" && strings.Contains(page(), "已更新到 1.1.0")
		}, 2*time.Minute)
	}
	if err == nil && user.last() != marketPlugin+" asks "+marketTarget+" 1.1.0 replacing 1.0.0" {
		err = fmt.Errorf("confirmation showed %q", user.last())
	}
	if err == nil {
		err = profileHas(a, w.Profile, marketTarget)
	}
	if _, serr := os.Stat(oldStored); err == nil && serr == nil {
		err = errors.New("the replaced version's package is still stored")
	}
	if err == nil && strings.Contains(nav(), "个更新") {
		err = fmt.Errorf("sidebar still counts updates: %s", nav())
	}
	if err != nil {
		err = fmt.Errorf("%v: page %s; %s", err, page(), diag())
	}
	r.step("更新：索引出现 1.1.0 → 侧栏显示 1 个更新 → 确认后原地替换，工作区 profile 保留，旧包删除", err, user.last())
	if err != nil {
		return
	}

	// 5. A package that differs from the index never reaches the host.
	asked := user.count()
	err = publish("1.2.0")
	if err == nil {
		var files []string
		files, err = filepath.Glob(filepath.Join(site, "packages", "*1.2.0*.tgz"))
		if err == nil && len(files) != 1 {
			err = fmt.Errorf("published 1.2.0: %v", files)
		}
		if err == nil {
			var f *os.File
			if f, err = os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0); err == nil {
				f.Write([]byte{0})
				f.Close()
			}
		}
	}
	if err == nil {
		err = action("refresh", map[string]any{"values": map[string]any{"index": site}})
	}
	if err == nil {
		err = waitFor(func() bool {
			return strings.Contains(page(), "最新 1.2.0") && strings.Contains(page(), `"label":"更新"`)
		}, 30*time.Second)
	}
	if err == nil {
		err = action("install:"+marketTarget, map[string]any{"values": map[string]any{}})
	}
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page(), "和索引记录的不一致") }, time.Minute)
	}
	if err == nil && (user.count() != asked || installed(marketTarget) != "1.1.0") {
		err = fmt.Errorf("tampered package went on: asked %d times, installed %s", user.count()-asked, installed(marketTarget))
	}
	if err != nil {
		err = fmt.Errorf("%v: page %s", err, page())
	}
	r.step("篡改：包与索引摘要不符，市场拒绝，不交给宿主、不弹确认", err, "")

	// 6. Uninstalled by the user, the market is not installed again.
	err = a.UninstallPlugin(ctx, marketPlugin)
	a.Close()
	if err == nil {
		a, err = app.New(cfgFor())
	}
	if err == nil {
		err = a.Start(ctx)
	}
	if err == nil {
		time.Sleep(5 * time.Second)
		if v := installed(marketPlugin); v != "" {
			err = fmt.Errorf("market %s was installed again", v)
		}
	}
	r.step("用户卸载市场后重启：不会再次预装", err, "")
}
