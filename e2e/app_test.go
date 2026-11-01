//go:build e2e

// End-to-end checks for the wired-up Alcove core (SPEC 3.3, PRD 3.3, 3.5,
// 3.7): workspace profiles assembled from the sdk template, the bridge and
// installed plugins; a plugin with background running its own "daily"
// profile through runtime.ensure; cleanup on workspace removal and plugin
// uninstall. Writes e2e/report/app.md.
package e2e

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
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
	"github.com/yiyiSmallti/precinct-alcove/internal/profile"
)

func TestApp(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "Alcove 核心装配 e2e：工作区 profile、插件后台运行时、清理（SPEC 3.3）", file: "app.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	rec := &recorder{ui: map[string]sidecar.Contribution{}}
	a, err := app.New(app.Config{
		DataDir: filepath.Join(work, "data"), AlcoveVersion: "0.1.0",
		Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
		TrustedKeys: []ed25519.PublicKey{pub}, Secrets: &memSecrets{m: map[string][]byte{}},
		Answerer: &scriptedAnswerer{outcome: "allowed-once"}, SidecarUI: rec,
	})
	if err == nil {
		err = a.Start(ctx)
	}
	r.step("首次启动：建数据目录、身份，打开桥接端点", err, "")
	if err != nil {
		return
	}
	defer a.Close()
	a.Sidecars.Services.Log = rec.log
	a.Sidecars.Backoff = 100 * time.Millisecond

	model, err := fakemodel.Start(1)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	_, err = a.Creds.Add("假模型", "deepseek-official", model.URL, "sk-e2e-app")
	r.step("录入凭据", err, "")

	// 1. A workspace profile is the sdk template plus the bridge.
	dir1 := filepath.Join(work, "proj-1")
	os.MkdirAll(dir1, 0o755)
	w1, err := a.CreateWorkspace(ctx, "项目一", dir1)
	if err == nil {
		err = profileHas(a, w1.Profile, "@deepseek-ai/dsh-sdk-app", profile.BridgePackage)
	}
	r.step("新建工作区：profile = sdk 模板 + 桥接，--dump-config 通过", err, "")
	if err != nil {
		return
	}

	// 2. Open it: a turn runs with the stored credential.
	p, err := a.OpenWorkspace(ctx, w1.ID)
	if err == nil {
		err = turn(ctx, p, "app-"+fmt.Sprint(time.Now().UnixNano()))
	}
	if err == nil {
		_, err = os.Stat(filepath.Join(dir1, "hello.txt"))
	}
	if err == nil && !model.SawKey("sk-e2e-app") {
		err = errors.New("model did not get the stored key")
	}
	r.step("打开工作区：用默认凭据拉起运行时并完成一个回合", err, "")

	// 3. A plugin for personal workspaces and its own daily profile, with a
	// sidecar that asks for its daily runtime.
	botDir := filepath.Join(work, "bot")
	os.MkdirAll(botDir, 0o755)
	calls, _ := json.Marshal([]map[string]any{
		{"method": "runtime.ensure", "params": map[string]any{"profile": "daily", "cwd": botDir, "env": map[string]string{"DAILY_TOOLS_HOST": "e2e-endpoint"}}},
		{"method": "runtime.ensure", "params": map[string]any{"profile": w1.Profile, "cwd": dir1}},
		{"method": "runtime.ensure", "params": map[string]any{"profile": "daily", "cwd": botDir, "env": map[string]string{"ALCOVE_BRIDGE_HOST": "x"}}},
	})
	a.Sidecars.Env = []string{"FAKE_PLUGIN_CALLS=" + string(calls)}
	tgz := pack(t, work, "1.0.0", buildFakePlugin(t, work), func(pj map[string]any) {
		al := pj["alcove"].(map[string]any)
		al["capabilities"] = []any{"settings_page", "background"}
		al["profiles"] = []any{"personal", "daily"}
		al["sidecar"] = map[string]any{runtime.GOOS: "bin/alcove-e2e.exe"}
	})
	s, _ := sig.Sign(tgz, priv)
	os.WriteFile(tgz+".sig", []byte(s), 0o644)
	plan, err := a.Plugins.Prepare(ctx, tgz)
	if err == nil && !plan.OK() {
		err = fmt.Errorf("problems: %v", plan.Problems)
	}
	if err == nil {
		_, err = a.InstallPlugin(ctx, plan)
	}
	if err == nil {
		err = profileHas(a, w1.Profile, e2ePlugin)
	}
	if err == nil {
		err = profileHas(a, "daily", "@deepseek-ai/dsh-sdk-app", profile.BridgePackage, e2ePlugin)
	}
	r.step("安装插件：进入已有工作区；daily profile 不存在时先按 sdk 模板 + 桥接建好再装", err, "")

	err = waitFor(func() bool { _, ok := rec.has("calls done"); return ok }, 30*time.Second)
	var got []string
	for i := 0; i < 3 && err == nil; i++ {
		l, _ := rec.has(fmt.Sprintf("result %d ", i))
		got = append(got, l)
	}
	if err == nil {
		switch {
		case !strings.HasPrefix(got[0], `result 0 ok {"runtimeId":`):
			err = fmt.Errorf("daily ensure: %s", got[0])
		case got[1] != "result 1 err -32030":
			err = fmt.Errorf("workspace profile: %s", got[1])
		case got[2] != "result 2 err -32602":
			err = fmt.Errorf("reserved env: %s", got[2])
		}
	}
	daily := false
	for _, info := range a.Runtimes.List() {
		if info.Profile == "daily" && info.Cwd == botDir {
			daily = true
		}
	}
	if err == nil && !daily {
		err = errors.New("no daily runtime running")
	}
	r.step("插件 runtime.ensure：能拉起自己的 daily；拒绝工作区 profile（-32030）与保留变量（-32602）", err, strings.Join(got, " / "))

	// 4. A workspace created later gets the plugin too.
	dir2 := filepath.Join(work, "proj-2")
	os.MkdirAll(dir2, 0o755)
	w2, err := a.CreateWorkspace(ctx, "", dir2)
	if err == nil {
		err = profileHas(a, w2.Profile, profile.BridgePackage, e2ePlugin)
	}
	r.step("之后新建的工作区自动装入 personal 插件", err, "")

	// 5. Removing a workspace deletes its profile, keeps its files, and the
	// plugin record forgets the profile.
	err = a.RemoveWorkspace(ctx, w1.ID)
	if err == nil {
		if _, serr := os.Stat(a.CLI.ProfileDir(w1.Profile)); serr == nil {
			err = errors.New("profile still on disk")
		}
	}
	if err == nil {
		if _, serr := os.Stat(filepath.Join(dir1, "hello.txt")); serr != nil {
			err = fmt.Errorf("workspace files gone: %v", serr)
		}
	}
	if err == nil {
		recs, _ := a.Plugins.List()
		for _, x := range recs {
			for _, pr := range x.Profiles {
				if pr == w1.Profile {
					err = errors.New("plugin record still lists the removed profile")
				}
			}
		}
	}
	r.step("移除工作区：停运行时、删 profile、保留文件，插件记录同步", err, "")

	// 6. Uninstalling the plugin stops its sidecar and leaves no copies.
	err = a.UninstallPlugin(ctx, e2ePlugin)
	if err == nil {
		for _, pr := range []string{"daily", w2.Profile} {
			m, rerr := a.CLI.ReadProfile(pr)
			if rerr == nil && m.HasBundle(e2ePlugin) {
				err = fmt.Errorf("%s still has the plugin", pr)
			}
		}
	}
	if st, _ := a.Sidecars.Status(e2ePlugin); err == nil && st.State != sidecar.Stopped {
		err = fmt.Errorf("sidecar %s", st.State)
	}
	for _, info := range a.Runtimes.List() {
		if err == nil && info.Profile == "daily" {
			err = errors.New("daily runtime still running")
		}
	}
	r.step("卸载插件：边车与它的 daily 运行时停止，各 profile 不再有它", err, "")
}

func profileHas(a *app.App, prof string, pkgs ...string) error {
	m, err := a.CLI.ReadProfile(prof)
	if err != nil {
		return err
	}
	for _, p := range pkgs {
		if !m.HasBundle(p) {
			return fmt.Errorf("%s lacks %s", prof, p)
		}
	}
	return nil
}
