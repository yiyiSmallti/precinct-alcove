//go:build e2e

// End-to-end checks for sidecars and host extension protocol v1 (SPEC 3.2):
// a signed package with a sidecar is installed through dsh, its sidecar runs
// from the store profile, calls are gated by granted capabilities, and
// crashes are rate limited. Writes e2e/report/sidecar.md.
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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

const e2ePlugin = "@precinct/alcove-e2e"

// recorder is the Services the test hands to the manager.
type recorder struct {
	mu   sync.Mutex
	ui   map[string]sidecar.Contribution
	logs []string
}

func (r *recorder) Register(plugin string, c sidecar.Contribution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ui[plugin+"#"+c.ID] = c
	return nil
}

func (r *recorder) Update(plugin, id string, data json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.ui[plugin+"#"+id]; ok {
		c.Raw = data
		r.ui[plugin+"#"+id] = c
	}
	return nil
}

// raw is a registered contribution's current JSON.
func (r *recorder) raw(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.ui[key].Raw)
}

func (r *recorder) Unregister(plugin, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ui, plugin+"#"+id)
}

func (r *recorder) inbox(plugin string, item json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, "inbox "+string(item))
	return nil
}

func (r *recorder) log(plugin, level, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, msg)
}

func (r *recorder) has(prefix string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.logs {
		if strings.HasPrefix(l, prefix) {
			return l, true
		}
	}
	return "", false
}

// index is the position of the first log line with prefix, or -1.
func (r *recorder) index(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, l := range r.logs {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

func (r *recorder) count(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.logs {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func newManager(rec *recorder, work string, env ...string) *sidecar.Manager {
	return &sidecar.Manager{
		AlcoveVersion: "0.1.0", DSHVersion: "e2e",
		Services: sidecar.Services{UI: rec, Log: rec.log, Inbox: rec.inbox},
		Settings: &sidecar.Settings{Dir: filepath.Join(work, "alcove", "settings")},
		Env:      env,
		Backoff:  100 * time.Millisecond, HelloTimeout: 2 * time.Second, StopTimeout: 500 * time.Millisecond,
	}
}

func TestSidecar(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	cli := testCLI(t, work)
	ver, _ := cli.Version(ctx)
	r := &report{t: t, title: "边车与宿主扩展协议 v1 e2e（A3 运行部分，SPEC 3.2）", file: "sidecar.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	// 1. Install a signed package whose sidecar is built for this OS.
	pub, priv, _ := ed25519.GenerateKey(nil)
	h := &host.Host{DSH: cli, Dir: filepath.Join(work, "alcove", "plugins"), AlcoveVersion: "0.1.0", GOOS: runtime.GOOS, TrustedKeys: []ed25519.PublicKey{pub}}
	tgz := pack(t, work, "1.0.0", buildFakePlugin(t, work), func(pj map[string]any) {
		a := pj["alcove"].(map[string]any)
		a["capabilities"] = []any{"settings_page", "runtime_launch_hook", "inbox"}
		a["sidecar"] = map[string]any{runtime.GOOS: "bin/alcove-e2e.exe"}
	})
	s, _ := sig.Sign(tgz, priv)
	os.WriteFile(tgz+".sig", []byte(s), 0o644)
	var rec0 *host.Installed
	plan, err := h.Prepare(ctx, tgz)
	if err == nil && !plan.OK() {
		err = fmt.Errorf("problems: %v", plan.Problems)
	}
	if err == nil {
		rec0, err = h.Install(ctx, plan)
	}
	exe := ""
	if err == nil {
		exe = h.SidecarPath(*rec0)
		_, err = os.Stat(exe)
	}
	r.step("安装带边车的签名包，边车在包仓 profile 里", err, filepath.Base(filepath.Dir(filepath.Dir(exe))))
	if err != nil {
		return
	}
	spec := sidecar.Spec{Name: e2ePlugin, Version: "1.0.0", Exe: exe, Caps: rec0.Capabilities}

	// 2. Handshake, capability checks, settings isolation, launch hook.
	calls := []map[string]any{
		// First, right after ready: the host must have registered ready's
		// contributions by the time it handles this.
		{"method": "ui.update", "params": map[string]any{"id": "e2e", "data": map[string]any{"type": "settings_page", "id": "e2e", "sections": []any{}}}},
		{"method": "settings.write", "params": map[string]any{"ns": e2ePlugin, "value": map[string]any{"k": 1}}},
		{"method": "settings.read", "params": map[string]any{"ns": e2ePlugin}},
		{"method": "settings.write", "params": map[string]any{"ns": "@precinct/other", "value": 1}},
		{"method": "settings.write", "params": map[string]any{"ns": "policy", "value": 1}},
		{"method": "settings.read", "params": map[string]any{"ns": "policy"}},
		{"method": "inbox.post", "params": map[string]any{"id": "card-1", "kind": "card", "card": map[string]any{"title": "x", "actions": []any{map[string]any{"id": "done", "label": "完成"}}}}},
		{"method": "ui.register", "params": map[string]any{"type": "sidebar_item", "id": "side"}},
		{"method": "runtime.registerLaunchHook"},
		{"method": "workspace.list"},
		{"method": "inbox.post", "params": map[string]any{"kind": "message", "text": "no id"}},
	}
	cb, _ := json.Marshal(calls)
	ui := `[{"type":"settings_page","id":"e2e","sections":[]}]`
	rec := &recorder{ui: map[string]sidecar.Contribution{}}
	m := newManager(rec, work, "FAKE_PLUGIN_CALLS="+string(cb), "FAKE_PLUGIN_UI="+ui)
	err = m.Start(ctx, spec)
	if err == nil {
		rec.mu.Lock()
		_, ok := rec.ui[e2ePlugin+"#e2e"]
		rec.mu.Unlock()
		if !ok {
			err = errors.New("settings_page from ready was not registered")
		}
	}
	r.step("hello/ready 握手，ready 里的设置页登记到界面", err, "")
	if err != nil {
		st, _ := m.Status(e2ePlugin)
		t.Logf("status %+v", st)
		return
	}
	err = waitFor(func() bool { _, ok := rec.has("calls done"); return ok }, 5*time.Second)
	want := []string{
		`result 0 ok {}`,
		`result 1 ok {}`,
		`result 2 ok {"value":{"k":1}}`,
		`result 3 err -32030`,
		`result 4 err -32030`,
		`result 5 ok {"value":null}`,
		`result 6 ok {}`,
		`result 7 err -32030`,
		`result 8 ok {}`,
		`result 9 err -32031`,
		`result 10 err -32602`,
	}
	for i, w := range want {
		if err != nil {
			break
		}
		got, _ := rec.has(fmt.Sprintf("result %d ", i))
		if got != w {
			err = fmt.Errorf("call %d: got %q, want %q", i, got, w)
		}
	}
	r.step("能力与命名空间把关：ready 之后立刻 ui.update 自己的设置页成功；自己的设置可读写；别人的命名空间、策略段、未授权的侧栏被拒（-32030）；未提供的服务答 -32031；inbox.post 须带 id（-32602）", err, "")

	lr := m.LaunchHooks(ctx, sidecar.LaunchContext{WorkspaceID: "w1", Profile: "ws-1", Cwd: work})
	err = nil
	if strings.Join(lr.Env, ",") != "FAKE_PLUGIN_TOKEN=from-hook" || len(lr.Failed) > 0 {
		err = fmt.Errorf("launch hooks: env %v, failed %v", lr.Env, lr.Failed)
	}
	r.step("拉起前调用 launchHook，取回 env", err, strings.Join(lr.Env, ","))

	err = m.UIAction(ctx, e2ePlugin, "e2e", "save", json.RawMessage(`{"a":1}`))
	if err == nil {
		err = waitFor(func() bool { _, ok := rec.has("uiAction"); return ok }, 2*time.Second)
	}
	r.step("界面操作转给插件（onUiAction）", err, "")

	err = m.InboxInput(ctx, e2ePlugin, "in-1", "你好", time.Now())
	if err == nil {
		err = waitFor(func() bool { l, ok := rec.has("inboxInput"); return ok && strings.Contains(l, `"in-1"`) }, 2*time.Second)
	}
	if err == nil {
		err = m.UIAction(ctx, e2ePlugin, "card-1", "done", json.RawMessage(`{}`))
	}
	if err == nil {
		err = waitFor(func() bool { return hasLine(rec, "card-1") }, 2*time.Second)
	}
	r.step("对话视图输入送达插件（onInboxInput），卡片按钮按 item id 转给插件", err, "")

	pid := m.PID(e2ePlugin)
	err = m.Stop(ctx, e2ePlugin)
	if err == nil && !pidGone(pid) {
		err = fmt.Errorf("sidecar %d still running", pid)
	}
	if err == nil {
		rec.mu.Lock()
		if len(rec.ui) != 0 {
			err = fmt.Errorf("ui left registered: %v", rec.ui)
		}
		rec.mu.Unlock()
	}
	r.step("shutdown 后边车退出，界面登记撤销", err, "")

	// 3. Crash rate limiting.
	var mu sync.Mutex
	var states []string
	m2 := newManager(&recorder{ui: map[string]sidecar.Contribution{}}, work, "FAKE_PLUGIN_MODE=crash")
	m2.OnState = func(name string, st sidecar.Status) {
		mu.Lock()
		states = append(states, string(st.State))
		mu.Unlock()
	}
	start := time.Now()
	err = m2.Start(ctx, spec)
	if err == nil {
		err = waitFor(func() bool { st, _ := m2.Status(e2ePlugin); return st.State == sidecar.Crashed }, 10*time.Second)
	}
	st, _ := m2.Status(e2ePlugin)
	if err == nil && st.Crashes != 3 {
		err = fmt.Errorf("crashes = %d", st.Crashes)
	}
	mu.Lock()
	detail := fmt.Sprintf("%s 内：%s", time.Since(start).Round(100*time.Millisecond), strings.Join(states, " → "))
	mu.Unlock()
	r.step("崩溃退避重启，3 次后停在 crashed 不再拉起", err, detail)
	if err == nil {
		time.Sleep(500 * time.Millisecond)
		if st, _ := m2.Status(e2ePlugin); st.State != sidecar.Crashed {
			err = fmt.Errorf("state moved on to %s", st.State)
		}
		if err == nil {
			err = m2.Start(ctx, spec)
		}
		m2.Stop(ctx, e2ePlugin)
	}
	r.step("用户手动启动会清零崩溃计数并重新拉起", err, "")

	// 4. A sidecar that never answers hello fails within the budget.
	m3 := newManager(&recorder{ui: map[string]sidecar.Contribution{}}, work, "FAKE_PLUGIN_MODE=nohello")
	m3.HelloTimeout, m3.MaxCrashes = 500*time.Millisecond, 1
	start = time.Now()
	err = m3.Start(ctx, spec)
	if errors.Is(err, sidecar.ErrHandshake) {
		err = nil
		if d := time.Since(start); d > 4*time.Second {
			err = fmt.Errorf("took %v", d)
		}
	} else {
		err = fmt.Errorf("want handshake error, got %v", err)
	}
	r.step("不应答 hello 的边车在时限内判失败", err, time.Since(start).Round(100*time.Millisecond).String())

	// 5. A sidecar that ignores shutdown is killed with its children.
	m4 := newManager(&recorder{ui: map[string]sidecar.Contribution{}}, work, "FAKE_PLUGIN_MODE=stubborn")
	err = m4.Start(ctx, spec)
	child := 0
	if err == nil {
		err = waitFor(func() bool {
			st, _ := m4.Status(e2ePlugin)
			m := regexp.MustCompile(`child (\d+)`).FindStringSubmatch(st.Stderr)
			if m != nil {
				fmt.Sscan(m[1], &child)
			}
			return child > 0
		}, 3*time.Second)
	}
	if err == nil {
		start = time.Now()
		m4.Stop(ctx, e2ePlugin)
		if d := time.Since(start); d > 4*time.Second {
			err = fmt.Errorf("stop took %v", d)
		} else if !pidGone(child) {
			err = fmt.Errorf("child %d still running", child)
		}
	}
	r.step("不理 shutdown 的边车超时后连同子进程一起结束", err, "")

	// 6. A sidecar that dies while its child still holds stdout counts as
	// a crash, and the child is ended with it.
	m5 := newManager(&recorder{ui: map[string]sidecar.Contribution{}}, work, "FAKE_PLUGIN_MODE=crash,child")
	m5.MaxCrashes = 1
	err = m5.Start(ctx, spec)
	if err == nil {
		err = waitFor(func() bool { st, _ := m5.Status(e2ePlugin); return st.State == sidecar.Crashed }, 5*time.Second)
	}
	if err == nil {
		st, _ := m5.Status(e2ePlugin)
		child = 0
		if m := regexp.MustCompile(`child (\d+)`).FindStringSubmatch(st.LastError); m != nil {
			fmt.Sscan(m[1], &child)
		}
		if child == 0 {
			err = fmt.Errorf("no child pid in %q", st.LastError)
		} else if !pidGone(child) {
			err = fmt.Errorf("child %d still running", child)
		}
	}
	m5.Stop(ctx, e2ePlugin)
	r.step("边车退出而子进程仍占着 stdout：照样判崩溃并结束子进程", err, "")

	// 7. Stop during the handshake ends the start and leaves no UI behind.
	rec6 := &recorder{ui: map[string]sidecar.Contribution{}}
	m6 := newManager(rec6, work, "FAKE_PLUGIN_MODE=slowhello", "FAKE_PLUGIN_UI="+ui)
	startErr := make(chan error, 1)
	go func() { startErr <- m6.Start(ctx, spec) }()
	time.Sleep(300 * time.Millisecond)
	start = time.Now()
	m6.Stop(ctx, e2ePlugin)
	err = nil
	select {
	case e := <-startErr:
		if e == nil {
			err = errors.New("Start succeeded after Stop")
		}
	case <-time.After(3 * time.Second):
		err = errors.New("Start still waiting after Stop")
	}
	time.Sleep(time.Second)
	rec6.mu.Lock()
	if err == nil && len(rec6.ui) != 0 {
		err = fmt.Errorf("ui left registered: %v", rec6.ui)
	}
	rec6.mu.Unlock()
	if st, _ := m6.Status(e2ePlugin); err == nil && st.State != sidecar.Stopped {
		err = fmt.Errorf("state %s", st.State)
	}
	r.step("握手中途停止：启动随即结束，不留界面登记", err, time.Since(start).Round(100*time.Millisecond).String())

	// 8. Uninstall leaves nothing behind.
	err = h.Uninstall(ctx, e2ePlugin)
	if err == nil {
		if _, serr := os.Stat(exe); serr == nil {
			err = errors.New("sidecar still on disk")
		}
	}
	r.step("卸载后边车文件随包仓移除", err, "")
}

// buildFakePlugin builds e2e/fakeplugin for this OS, or reads a prebuilt
// one from ALCOVE_E2E_FAKEPLUGIN on machines without Go.
func buildFakePlugin(t *testing.T, work string) []byte {
	if p := os.Getenv("ALCOVE_E2E_FAKEPLUGIN"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ALCOVE_E2E_FAKEPLUGIN: %v", err)
		}
		return b
	}
	out := filepath.Join(work, "fakeplugin.exe")
	cmd := exec.Command("go", "build", "-o", out, "./fakeplugin")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakeplugin: %v\n%s", err, b)
	}
	b, _ := os.ReadFile(out)
	return b
}

// pidGone reports whether pid has exited (absent, or a zombie on Linux).
func pidGone(pid int) bool {
	if pid <= 0 {
		return true
	}
	for i := 0; i < 40; i++ {
		if runtime.GOOS == "windows" {
			out, _ := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
			if !strings.Contains(string(out), fmt.Sprint(pid)) {
				return true
			}
		} else {
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if err != nil {
				if runtime.GOOS == "linux" || exec.Command("kill", "-0", fmt.Sprint(pid)).Run() != nil {
					return true
				}
			} else if f := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(f) > 0 && f[0] == "Z" {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func hasLine(r *recorder, sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.logs {
		if strings.HasPrefix(l, "uiAction") && strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
