package shell

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/pkg"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// fakeCalls records what the shell sends to sidecars.
type fakeCalls struct {
	mu      sync.Mutex
	inputs  []string // id|text
	actions []string // contribution|action|payload
	failIn  int      // InboxInput calls to fail
}

func (f *fakeCalls) UIAction(_ context.Context, plugin, id, action string, payload json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, plugin+"|"+id+"|"+action+"|"+string(payload))
	return nil
}

func (f *fakeCalls) InboxInput(_ context.Context, plugin, id, text string, ts time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, id+"|"+text)
	if f.failIn > 0 {
		f.failIn--
		return sidecar.ErrNotRunning
	}
	return nil
}

func (f *fakeCalls) got() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...), append([]string(nil), f.actions...)
}

func withPlugin(s *Shell, caps ...string) *fakeCalls {
	f := &fakeCalls{}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sidecarCalls = f
	s.plugins = []host.Installed{{Name: "@precinct/daily", Version: "0.1.0", Capabilities: caps, Sidecar: "bin/daily"}}
	s.sidecars["@precinct/daily"] = sidecar.Status{State: sidecar.Running}
	return f
}

func TestPluginInboxConversation(t *testing.T) {
	s, tt := newShell(t)
	f := withPlugin(s, "inbox", "settings_page")
	tt.Frame()
	// A plugin holding inbox gets a conversation view in the sidebar.
	if err := tt.Click("daily"); err != nil {
		t.Fatalf("no sidebar entry: %v (%v)", err, tt.Texts())
	}
	tt.Frame()
	s.Inbox("@precinct/daily", json.RawMessage(`{"id":"m1","kind":"message","text":"早上好，**今天**有 2 个提醒","ts":"2026-11-01T08:00:00Z"}`))
	s.Inbox("@precinct/daily", json.RawMessage(`{"id":"c1","kind":"card","card":{"title":"提醒","body":"十点开会","actions":[{"id":"done","label":"知道了","style":"primary"},{"id":"snooze","label":"稍后"}]}}`))
	// The same id again replaces the item in place.
	s.Inbox("@precinct/daily", json.RawMessage(`{"id":"m1","kind":"message","text":"早上好，今天有 3 个提醒","ts":"2026-11-01T08:00:00Z"}`))
	tt.Frame()
	if !tt.HasText("早上好，今天有 3 个提醒") || !tt.HasText("十点开会") {
		t.Fatalf("items not shown: %v", tt.Texts())
	}
	if n := len(s.chats["@precinct/daily"].entries); n != 2 {
		t.Errorf("%d entries, want 2", n)
	}
	if err := tt.Click("知道了"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { _, a := f.got(); return len(a) == 1 })
	if _, a := f.got(); a[0] != "@precinct/daily|c1|done|{}" {
		t.Errorf("card action %q", a[0])
	}

	// Input that does not get through is marked and resent, under the same
	// id, once the sidecar is running again.
	f.mu.Lock()
	f.failIn = 1
	f.mu.Unlock()
	s.mu.Lock()
	s.chats["@precinct/daily"].input = "明天九点提醒我交周报"
	s.mu.Unlock()
	tt.Frame()
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { return tt.HasText("未送达") })
	s.OnSidecarState("@precinct/daily", sidecar.Status{State: sidecar.Running})
	until(t, tt, s, func() bool { in, _ := f.got(); return len(in) == 2 })
	in, _ := f.got()
	if in[0] != in[1] || !strings.HasSuffix(in[0], "|明天九点提醒我交周报") {
		t.Errorf("resend %v", in)
	}
	until(t, tt, s, func() bool { return !tt.HasText("未送达") && !tt.HasText("发送中…") })
}

func TestInboxRejectsBadItems(t *testing.T) {
	s, _ := newShell(t)
	if err := s.Inbox("p", json.RawMessage(`{"id":"x","kind":"video"}`)); err == nil {
		t.Error("unknown kind accepted")
	}
	if err := s.Inbox("p", json.RawMessage(`{"id":"x","kind":"card"}`)); err == nil {
		t.Error("card without card accepted")
	}
}

func TestPluginSettingsPage(t *testing.T) {
	s, tt := newShell(t)
	f := withPlugin(s, "settings_page", "sidebar_item")
	page := `{"id":"main","type":"settings_page","title":"日常助手","sections":[{"title":"通用","fields":[
		{"id":"name","type":"text","label":"称呼","value":"小李"},
		{"id":"quiet","type":"toggle","label":"夜间免打扰","value":true},
		{"id":"tone","type":"select","label":"语气","value":"brief","options":[{"value":"brief","label":"简洁"},{"value":"warm","label":"亲切"}]},
		{"id":"token","type":"secretRef","label":"日历 token"},
		{"id":"test","type":"button","label":"测试连接"}]}]}`
	s.Register("@precinct/daily", sidecar.Contribution{ID: "main", Type: "settings_page", Raw: json.RawMessage(page)})
	s.Register("@precinct/daily", sidecar.Contribution{ID: "nav", Type: "sidebar_item", Raw: json.RawMessage(`{"id":"nav","type":"sidebar_item","title":"日常"}`)})
	tt.Frame()
	if err := tt.Click("日常"); err != nil {
		t.Fatalf("sidebar item: %v (%v)", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("日常助手") || !tt.HasText("添加设置页") {
		t.Fatalf("plugin page: %v", tt.Texts())
	}
	s.mu.Lock()
	form := s.forms["@precinct/daily#main"]
	form.values["tone"].text = "亲切"
	form.values["token"].text = "cal-secret"
	form.dirty = true
	s.mu.Unlock()
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { _, a := f.got(); return len(a) == 1 && !form.busy })
	_, a := f.got()
	parts := strings.SplitN(a[0], "|", 4)
	if parts[1] != "main" || parts[2] != "submit" {
		t.Fatalf("action %q", a[0])
	}
	var p struct {
		Values struct {
			Name  string            `json:"name"`
			Quiet bool              `json:"quiet"`
			Tone  string            `json:"tone"`
			Token map[string]string `json:"token"`
		} `json:"values"`
	}
	json.Unmarshal([]byte(parts[3]), &p)
	if p.Values.Name != "小李" || !p.Values.Quiet || p.Values.Tone != "warm" || p.Values.Token["ref"] == "" {
		t.Errorf("payload %s", parts[3])
	}
	if strings.Contains(parts[3], "cal-secret") {
		t.Error("secret value sent to the plugin")
	}
	if v, err := s.App.Creds.Secrets.Get(p.Values.Token["ref"]); err != nil || string(v) != "cal-secret" {
		t.Errorf("secret store: %q %v", v, err)
	}
	s.mu.Lock()
	tok := *form.values["token"]
	s.mu.Unlock()
	if !tt.HasText("已保存") || !tok.hasRef || tok.text != "" {
		t.Errorf("after save: %+v %v", tok, tt.Texts())
	}
	// A button field sends its own action.
	if err := tt.Click("测试连接"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { _, a := f.got(); return len(a) == 2 && !form.busy })
	if _, a := f.got(); !strings.HasPrefix(a[1], "@precinct/daily|main|test|") {
		t.Errorf("button action %q", a[1])
	}
}

func TestPluginsPagePlan(t *testing.T) {
	s, tt := newShell(t)
	withPlugin(s, "inbox")
	s.mu.Lock()
	s.page = PagePlugins
	s.sidecars["@precinct/daily"] = sidecar.Status{State: sidecar.Crashed, LastError: "exit status 2", Stderr: "panic: boom"}
	p := &pkg.Package{Name: "@precinct/dovetail", Version: "0.1.0", Description: "插件市场"}
	p.Alcove = &pkg.Alcove{Capabilities: []string{"background"}}
	s.plan = &host.Plan{Package: p, SignedBy: 0, Problems: []pkg.Problem{{Code: "host-range", Message: "需要 Alcove 0.2 以上"}}}
	s.mu.Unlock()
	tt.Frame()
	for _, want := range []string{"安装 @precinct/dovetail 0.1.0？", "签名有效", "· 在后台运行，并按需启动自己的运行时", "需要 Alcove 0.2 以上",
		"崩溃次数过多，已停止", "exit status 2", "panic: boom", "重新启动"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q: %v", want, tt.Texts())
		}
	}
	// A plan with problems cannot be installed.
	tt.Click("安装")
	tt.Frame()
	s.mu.Lock()
	busy := s.planBusy
	s.mu.Unlock()
	if busy {
		t.Error("installing a plan with problems")
	}
	// Uninstall asks once more.
	if err := tt.Click("卸载"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("确认卸载") {
		t.Error("no confirmation")
	}
	tt.Click("不卸载")
	tt.Frame()
	tt.Click("取消")
	tt.Frame()
	if s.plan != nil {
		t.Error("plan kept after cancel")
	}
}

func TestSelectWithRepeatedLabels(t *testing.T) {
	s, tt := newShell(t)
	f := withPlugin(s, "settings_page")
	s.Register("@precinct/daily", sidecar.Contribution{ID: "p", Type: "settings_page", Raw: json.RawMessage(`{"sections":[{"fields":[
		{"id":"m","type":"select","label":"模型","value":"a","options":[{"value":"a","label":"快速"},{"value":"b","label":"快速"}]}]}]}`)})
	s.mu.Lock()
	s.page = pagePlugin + "@precinct/daily"
	s.mu.Unlock()
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { _, a := f.got(); return len(a) == 1 })
	if _, a := f.got(); !strings.HasSuffix(a[0], `{"values":{"m":"a"}}`) {
		t.Errorf("untouched select changed: %q", a[0])
	}
}
