//go:build e2e

// End-to-end check of workspaces attached to a plugin with
// provider_endpoint (SPEC 3.2): the plugin's package joins the workspace
// profile, the runtime uses the route the owner's launch hook gives and
// never the personal credential, launches fail closed without a route,
// and detach returns the workspace to personal. Writes e2e/report/org.md.
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
)

func TestOrgWorkspace(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "组织工作区 e2e：workspace.attach、owner 的模型路由、无路由拒绝拉起（SPEC 3.2）", file: "org.md"}
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
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Sidecars.Services.Log = rec.log

	personal, err := fakemodel.Start(1)
	if err != nil {
		t.Fatal(err)
	}
	defer personal.Close()
	org, err := fakemodel.Start(1)
	if err != nil {
		t.Fatal(err)
	}
	defer org.Close()
	if _, err := a.Creds.Add("个人", "deepseek-official", personal.URL, "sk-personal"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, "org-ws")
	os.MkdirAll(dir, 0o755)
	w, err := a.CreateWorkspace(ctx, "组织", dir)
	if err != nil {
		t.Fatal(err)
	}

	// The hook answers from a file the test rewrites: first without a route.
	hookFile := filepath.Join(work, "hook.json")
	setHook := func(v any) { raw, _ := json.Marshal(v); os.WriteFile(hookFile, raw, 0o600) }
	orgEnv := map[string]string{"DEEPSEEK_API_KEY": "sk-org", "DEEPSEEK_BASE_URL": org.URL}
	setHook(map[string]any{"env": orgEnv})
	calls, _ := json.Marshal([]map[string]any{
		{"method": "runtime.registerLaunchHook", "params": map[string]any{}},
		{"method": "workspace.attach", "params": map[string]any{"workspaceId": w.ID}},
		{"method": "workspace.detach", "params": map[string]any{"workspaceId": "000000000000"}},
	})
	a.Sidecars.Env = []string{"FAKE_PLUGIN_CALLS=" + string(calls), "FAKE_PLUGIN_HOOK_FILE=" + hookFile}
	tgz := pack(t, work, "1.0.0", buildFakePlugin(t, work), func(pj map[string]any) {
		al := pj["alcove"].(map[string]any)
		al["capabilities"] = []any{"runtime_launch_hook", "provider_endpoint"}
		al["profiles"] = []any{"worker"}
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
		err = waitFor(func() bool { _, ok := rec.has("calls done"); return ok }, 30*time.Second)
	}
	var got []string
	for i := 0; i < 3 && err == nil; i++ {
		l, _ := rec.has(fmt.Sprintf("result %d ", i))
		got = append(got, l)
	}
	if err == nil && (got[1] != "result 1 ok {}" || got[2] != "result 2 ok {}") {
		err = fmt.Errorf("calls: %v", got)
	}
	if err == nil {
		err = owner(a, w.ID, e2ePlugin)
	}
	if err == nil {
		err = profileHas(a, w.Profile, e2ePlugin)
	}
	r.step("workspace.attach：工作区归插件，插件包进入工作区 profile；未知工作区 detach 无害", err, strings.Join(got, " / "))

	_, err = a.OpenWorkspace(ctx, w.ID)
	if err == nil {
		err = errors.New("opened without a route")
	} else if strings.Contains(err.Error(), "belongs to") {
		err = nil
	}
	if err == nil && personal.SawKey("sk-personal") {
		err = errors.New("personal key was used")
	}
	r.step("钩子不给模型路由：拒绝拉起，不退回个人凭据", err, "")

	setHook(map[string]any{"env": orgEnv, "provider": map[string]string{"route": "deepseek-official", "model": "deepseek-v4-flash"}})
	p, err := a.OpenWorkspace(ctx, w.ID)
	if err == nil {
		err = turn(ctx, p, "org-"+fmt.Sprint(time.Now().UnixNano()))
	}
	if err == nil && !org.SawKey("sk-org") {
		err = errors.New("org model did not get the hook's key")
	}
	if err == nil && (personal.SawKey("sk-personal") || org.SawKey("sk-personal")) {
		err = errors.New("personal key was used")
	}
	r.step("钩子给出路由：按 owner 的路由与环境跑完一个回合，个人凭据不注入", err, "")

	// A personal workspace ignores routes from hooks.
	pdir := filepath.Join(work, "personal-ws")
	os.MkdirAll(pdir, 0o755)
	pw, err := a.CreateWorkspace(ctx, "个人", pdir)
	if err == nil {
		p, err = a.OpenWorkspace(ctx, pw.ID)
	}
	if err == nil {
		err = turn(ctx, p, "personal-"+fmt.Sprint(time.Now().UnixNano()))
	}
	if err == nil && !personal.SawKey("sk-personal") {
		err = errors.New("personal workspace did not use the personal key")
	}
	r.step("个人工作区：忽略钩子的路由，用个人凭据", err, "")

	// The RPC path is the same as attach's (checked above); call the host
	// side directly.
	err = a.Sidecars.Services.Detach(ctx, e2ePlugin, w.ID)
	if err == nil {
		err = owner(a, w.ID, "personal")
	}
	if err == nil {
		if m, rerr := a.CLI.ReadProfile(w.Profile); rerr == nil && m.HasBundle(e2ePlugin) {
			err = errors.New("plugin still in the workspace profile")
		}
	}
	for _, info := range a.Runtimes.List() {
		if err == nil && info.Profile == w.Profile {
			err = errors.New("runtime still running after detach")
		}
	}
	r.step("workspace.detach：停掉运行时，工作区回到个人，插件包移出 profile", err, "")
}

func owner(a *app.App, id, want string) error {
	w, err := a.Workspaces.Find(id)
	if err != nil {
		return err
	}
	if w.OwnerName() != want {
		return fmt.Errorf("owner %q, want %q", w.OwnerName(), want)
	}
	return nil
}
