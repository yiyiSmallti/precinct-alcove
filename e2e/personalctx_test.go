//go:build e2e

// End-to-end check of the personal context (SPEC 3.1 个人上下文, 3.2
// personal_*): a plugin with personal_context writes the profile and a
// skill, and a plugin with personal_history reads the user's conversations
// in personal workspaces; a real dsh in a personal workspace then sends
// both to the model, and one in an org workspace sends neither.
//
// Ways this can fail, each checked below:
//   - the bridge rows do not reach dsh (the profile or the skill never
//     shows in the personal workspace's request);
//   - the rows replace dsh-base's config badly and a plugin does not
//     activate (instructions lose maxBytes, skills lose their roots);
//   - the personal context leaks into an org workspace, whose requests go
//     out through the org's route;
//   - a profile edited while the runtime runs is not what the next turn
//     sends;
//   - history returns tasks a plugin ran, an org workspace's tasks, or
//     ignores its limit;
//   - a skill with a name dsh would not take is written anyway.
//
// Writes e2e/report/personal-context.md.
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
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

func TestPersonalContext(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "个人上下文 e2e：画像与全局技能只进个人工作区，插件读取个人对话（SPEC 3.1、3.2）", file: "personal-context.md"}
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

	// Each fake model keeps the bodies it was sent.
	capture := func() (*fakemodel.Server, func() string) {
		m, err := fakemodel.Start(0)
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var last string
		m.Respond = func(body []byte) (*fakemodel.ToolCall, string) {
			mu.Lock()
			last = string(body)
			mu.Unlock()
			return nil, "好的"
		}
		return m, func() string { mu.Lock(); defer mu.Unlock(); return last }
	}
	personalModel, personalLast := capture()
	defer personalModel.Close()
	orgModel, orgLast := capture()
	defer orgModel.Close()
	if _, err := a.Creds.Add("个人", "deepseek-official", personalModel.URL, "sk-personal", ""); err != nil {
		t.Fatal(err)
	}

	mkws := func(name string) string {
		dir := filepath.Join(work, name)
		os.MkdirAll(dir, 0o755)
		w, err := a.CreateWorkspace(ctx, name, dir)
		if err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	pw, ow := mkws("personal-ws"), mkws("org-ws")
	// Conversations as their views saved them: two the user started in the
	// personal workspace, one a plugin ran there, one in the org workspace.
	seed := func(ws, id, source string, at time.Time, user, answer string) {
		if err := a.Tasks.Put(ws, tasks.Task{ID: id, Title: id, Source: source, CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
		snap := events.Snapshot{Title: id, Entries: []events.Entry{
			{Kind: events.User, Text: user}, {Kind: events.Tool, ToolName: "read", Result: "TOOL-OUTPUT"},
			{Kind: events.Reasoning, Text: "REASONING"}, {Kind: events.Assistant, Text: answer},
		}}
		if err := a.Tasks.SaveTranscript(ws, id, snap); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now().Add(-time.Hour)
	seed(pw, "chat-1", "", t0, "周报用三段式：进展、风险、下周", "好，按三段式写")
	seed(pw, "chat-2", "", t0.Add(time.Minute), "代码注释用中文", "好的")
	seed(pw, "task-plugin", e2ePlugin, t0.Add(2*time.Minute), "PLUGIN-RAN", "x")
	seed(ow, "chat-org", "", t0.Add(3*time.Minute), "ORG-SECRET", "x")

	hookFile := filepath.Join(work, "hook.json")
	raw, _ := json.Marshal(map[string]any{
		"env":      map[string]string{"DEEPSEEK_API_KEY": "sk-org", "DEEPSEEK_BASE_URL": orgModel.URL},
		"provider": map[string]string{"route": "deepseek-official", "model": "deepseek-v4-flash"},
	})
	os.WriteFile(hookFile, raw, 0o600)
	calls, _ := json.Marshal([]map[string]any{
		{"method": "runtime.registerLaunchHook", "params": map[string]any{}},
		{"method": "workspace.attach", "params": map[string]any{"workspaceId": ow}},
		{"method": "personal.setProfile", "params": map[string]any{"text": "# 关于我\n- PROFILE-MARK-1 回答用中文，先给结论"}},
		{"method": "personal.putSkill", "params": map[string]any{"skill": map[string]any{
			"name": "weekly-report", "description": "SKILL-MARK 写周报时用：进展、风险、下周三段", "body": "1. 进展\n2. 风险\n3. 下周"}}},
		{"method": "personal.putSkill", "params": map[string]any{"skill": map[string]any{"name": "Bad Name", "description": "x", "body": "x"}}},
		{"method": "personal.get", "params": map[string]any{}},
		{"method": "personal.history", "params": map[string]any{"limit": 1}},
		{"method": "personal.history", "params": map[string]any{}},
	})
	a.Sidecars.Env = []string{"FAKE_PLUGIN_CALLS=" + string(calls), "FAKE_PLUGIN_HOOK_FILE=" + hookFile}
	tgz := pack(t, work, "1.0.0", buildFakePlugin(t, work), func(pj map[string]any) {
		al := pj["alcove"].(map[string]any)
		al["capabilities"] = []any{"runtime_launch_hook", "provider_endpoint", "personal_context", "personal_history"}
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
	if err != nil {
		t.Fatal(err)
	}
	result := func(i int) string { l, _ := rec.has(fmt.Sprintf("result %d ", i)); return l }

	// 1. Writes through the host: the files are where dsh looks, a bad name
	// is refused.
	skillFile := filepath.Join(a.Layout.PersonalHome(), "skills", "weekly-report", "SKILL.md")
	body, ferr := os.ReadFile(skillFile)
	err = nil
	switch {
	case result(2) != `result 2 ok {"ok":true}` || result(3) != `result 3 ok {"ok":true}`:
		err = fmt.Errorf("writes: %s / %s", result(2), result(3))
	case ferr != nil:
		err = ferr
	case !strings.HasPrefix(string(body), "---\nname: \"weekly-report\"\n"):
		err = fmt.Errorf("SKILL.md: %q", body)
	case !strings.HasPrefix(result(4), "result 4 err -32602"):
		err = fmt.Errorf("bad name: %s", result(4))
	case !strings.Contains(result(5), "PROFILE-MARK-1") || !strings.Contains(result(5), `"name":"weekly-report"`):
		err = fmt.Errorf("personal.get: %s", result(5))
	}
	r.step("personal.setProfile / putSkill 写进个人上下文（AGENTS.md、skills/<name>/SKILL.md），dsh 不收的技能名被拒，personal.get 读得回", err, result(5))

	// 2. History: only what the user started in personal workspaces, oldest
	// first, text only, limit honoured.
	h1, h2 := result(6), result(7)
	err = nil
	switch {
	case !strings.Contains(h1, `"taskId":"chat-1"`) || strings.Contains(h1, "chat-2") || !strings.Contains(h1, `"more":true`):
		err = fmt.Errorf("limit 1: %s", h1)
	case !strings.Contains(h2, "chat-1") || !strings.Contains(h2, "chat-2") || !strings.Contains(h2, `"more":false`):
		err = fmt.Errorf("all: %s", h2)
	case strings.Contains(h2, "PLUGIN-RAN") || strings.Contains(h2, "ORG-SECRET"):
		err = fmt.Errorf("a plugin's task or the org workspace's leaked: %s", h2)
	case strings.Contains(h2, "TOOL-OUTPUT") || strings.Contains(h2, "REASONING"):
		err = fmt.Errorf("tool output or reasoning leaked: %s", h2)
	case strings.Index(h2, "chat-1") > strings.Index(h2, "chat-2"):
		err = fmt.Errorf("not oldest first: %s", h2)
	}
	r.step("personal.history：只有个人工作区里用户自己发起的对话，旧的在前，只含双方的文字，limit 生效", err, h2)

	// 3. A personal workspace's request carries the profile and the skill.
	p, err := a.OpenWorkspace(ctx, pw)
	sid := fmt.Sprintf("ctx-%d", time.Now().UnixNano())
	if err == nil {
		err = turn(ctx, p, sid)
	}
	if err == nil && strings.Contains(p.Stderr(400), "did not activate") {
		err = fmt.Errorf("a plugin did not activate: %s", p.Stderr(400))
	}
	got := personalLast()
	if err == nil && (!strings.Contains(got, "PROFILE-MARK-1") || !strings.Contains(got, "SKILL-MARK")) {
		err = fmt.Errorf("profile in request %v, skill %v", strings.Contains(got, "PROFILE-MARK-1"), strings.Contains(got, "SKILL-MARK"))
	}
	r.step("个人工作区：模型请求里有「关于我」和全局技能 weekly-report", err, excerpt(got, "PROFILE-MARK-1"))

	// 4. Edited while the runtime runs: the next turn sends the new text.
	err = a.Personal.SetProfile("# 关于我\n- PROFILE-MARK-2 回答尽量短")
	if err == nil {
		err = turn(ctx, p, sid)
	}
	got = personalLast()
	if err == nil && !strings.Contains(got, "PROFILE-MARK-2") {
		err = errors.New("the edited profile is not in the next request")
	}
	r.step("运行中改了「关于我」：同一会话的下一个回合带上新内容", err, excerpt(got, "PROFILE-MARK-2"))

	// 5. An org workspace's runtime sends neither.
	op, err := a.OpenWorkspace(ctx, ow)
	if err == nil {
		err = turn(ctx, op, fmt.Sprintf("org-%d", time.Now().UnixNano()))
	}
	got = orgLast()
	if err == nil && got == "" {
		err = errors.New("the org model got no request")
	}
	if err == nil && (strings.Contains(got, "PROFILE-MARK") || strings.Contains(got, "SKILL-MARK")) {
		err = errors.New("the personal context went out through the org route")
	}
	r.step("组织工作区：经组织路由出去的请求里没有「关于我」也没有个人技能", err, "")
}

// excerpt is the text around mark in a request body, for the report.
func excerpt(body, mark string) string {
	i := strings.Index(body, mark)
	if i < 0 {
		return ""
	}
	from, to := max(0, i-80), min(len(body), i+120)
	return strings.ToValidUTF8(body[from:to], "")
}
