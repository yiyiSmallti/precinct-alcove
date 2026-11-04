package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/catalog"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/pkg"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// TestScreens draws the main pages with example content into
// $ALCOVE_SCREENS (PNG, light and dark), for reviewing the look.
func TestScreens(t *testing.T) {
	dir := os.Getenv("ALCOVE_SCREENS")
	if dir == "" {
		t.Skip("set ALCOVE_SCREENS to a folder")
	}
	os.MkdirAll(dir, 0o755)
	for _, dark := range []bool{false, true} {
		s, tt := newShell(t)
		tt.SetDark(dark)
		tt.SetScale(2)
		tt.SetSize(1280, 800)
		tt.SetTitleBar(ui.TitleBar{Height: 38, Left: 78})
		v := demo(t, s)
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		shot := func(name string) {
			tt.Frame()
			tt.Frame()
			f, err := os.Create(filepath.Join(dir, name+"-"+suffix+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, tt.Image())
			f.Close()
		}
		s.mu.Lock()
		v.current = "chat-report"
		s.mu.Unlock()
		shot("task")
		s.mu.Lock()
		v.tasks["chat-report"].open[v.tasks["chat-report"].session.Entries[3].Key] = true
		s.mu.Unlock()
		shot("task-open")
		s.mu.Lock()
		s.panels.tab = tabFiles
		s.panels.open = map[string]bool{filepath.Join(v.ws.Dir, "data"): true}
		s.mu.Unlock()
		shot("files")
		time.Sleep(100 * time.Millisecond)
		shot("files")
		s.mu.Lock()
		s.panels.tab = tabWorkspace
		s.mu.Unlock()
		shot("workspace")
		s.mu.Lock()
		s.panels.tab = tabProgress
		v.current = ""
		s.mu.Unlock()
		shot("new")
		s.mu.Lock()
		s.wsMenu = true
		s.mu.Unlock()
		shot("switcher")
		s.mu.Lock()
		s.wsMenu = false
		s.page = PageSettings
		s.mu.Unlock()
		shot("settings")
		time.Sleep(300 * time.Millisecond)
		shot("settings")
		s.mu.Lock()
		s.settings.Appearance = app.Appearance{Tone: "cool", Accent: "violet"}
		s.mu.Unlock()
		shot("settings-violet")
		s.mu.Lock()
		s.page, v.current = pageWS+v.ws.ID, "chat-report"
		s.mu.Unlock()
		shot("task-violet")
		s.mu.Lock()
		s.settings.Appearance = app.Appearance{Accent: "amber"}
		s.mu.Unlock()
		shot("task-amber")
		for _, tn := range []string{"zinc", "slate", "linen", "warm", "neutral", "cool"} {
			s.mu.Lock()
			s.settings.Appearance = app.Appearance{Tone: tn}
			s.mu.Unlock()
			shot("tone-" + tn)
		}
		s.mu.Lock()
		s.page, s.prefs.tab, s.prefs.picking = PageSettings, tabLook, true
		s.prefs.custom = ui.Hex("#7c3aed")
		s.mu.Unlock()
		shot("picker")
		s.mu.Lock()
		s.settings.Appearance = app.Appearance{}
		s.prefs.picking = false
		s.mu.Unlock()
		shot("settings-look")
		s.mu.Lock()
		s.prefs.tab = tabOther
		s.mu.Unlock()
		shot("settings-other")
		s.mu.Lock()
		s.prefs.tab = tabModel
		s.creds = []credentials.Credential{{ID: "c1", Label: "DeepSeek 个人", Provider: "deepseek-official"}, {ID: "c2", Label: "OpenAI 工作", Provider: "openai", Model: "gpt-6.1-sol", Context: map[string]int{"gpt-6.1-sol": 1050000}}}
		s.settings.DefaultCredential = "c2"
		s.settings.ReasoningEffort = "high"
		// Model facts from the runtime's catalog, when one is given.
		s.App.Catalog = catalog.FromDSHBin(os.Getenv("ALCOVE_E2E_DSH"))

		s.form = credForm{open: true, provider: "Kimi（月之暗面）", protocol: credentials.Protocols[0].Name}
		s.mu.Unlock()
		shot("settings-model")
		s.mu.Lock()
		s.form = credForm{}
		for i := range s.creds {
			if s.creds[i].ID == "c2" {
				s.creds[i].Models = []string{"gpt-6.1-sol", "gpt-5.5", "o4-mini"}
			}
		}
		s.picks = map[string]*modelPick{"c1": {remote: []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-pro-thinking", "deepseek-v3.2", "deepseek-coder-v4"}, picked: []string{"deepseek-v4-flash", "deepseek-v4-pro"}}}
		s.mu.Unlock()
		shot("settings-model-pick")
		s.mu.Lock()
		s.picks = nil
		s.page = pageWS + s.scratchID
		if sv := s.view(s.scratchID); sv != nil {
			sv.current = ""
		}
		s.mu.Unlock()
		shot("new-model")
		s.mu.Lock()
		s.runMenu = true
		s.mu.Unlock()
		shot("new-run")
		// A task on the personal credentials that took some context.
		s.mu.Lock()
		s.runMenu = false
		s.page = pageWS + v.ws.ID
		owner := v.ws.Owner
		v.ws.Owner = workspace.Personal
		v.tasks["chat-report"].session.Context = 712400
		s.mu.Unlock()
		shot("task-run")
		s.mu.Lock()
		s.runMenu = true
		s.mu.Unlock()
		shot("task-run-panel")
		s.mu.Lock()
		s.runMenu = false
		v.ws.Owner = owner
		s.mu.Unlock()
		s.mu.Lock()
		s.page = PageSettings
		s.mu.Unlock()
		s.mu.Lock()
		s.creds = append(s.creds, credentials.Credential{ID: "c3", Label: "公司网关", Provider: credentials.Custom, BaseURL: "https://llm.example.com/v1", API: "openai-completions", Model: "qwen3-coder"})
		s.form = credForm{open: true, provider: "自定义（兼容接口）", protocol: credentials.Protocols[0].Name}
		s.mu.Unlock()
		shot("settings-model-custom")
		s.mu.Lock()
		s.creds = nil
		s.mu.Unlock()
		s.mu.Lock()
		s.page = PagePlugins
		s.mu.Unlock()
		shot("plugins")
		market := `{"id":"market","type":"settings_page","title":"插件市场","submit":false,"sections":[
			{"fields":[
				{"id":"index","type":"text","label":"索引地址","help":"3 个插件 · 11月3日 07:15 读取","placeholder":"https://…/index.json 或本机文件夹","value":"https://plugins.example.com/"},
				{"id":"refresh","type":"button","label":"刷新"}]},
			{"title":"dovetail","caption":"@precinct/dovetail\n最新 0.1.1 · 已安装 0.1.0","description":"插件市场：从签名索引浏览、安装和更新 Alcove 插件，也能从本机镜像文件夹离线导入","display":"card","icon":"puzzle","fields":[{"id":"install:@precinct/dovetail","type":"button","label":"更新","style":"primary"}]},
			{"title":"cloister","caption":"@precinct/cloister\n最新 0.1.0-alpha.0","description":"组织功能：加入组织、策略、事件流、组织 bot 回合、组织工作区、同步与租约、云端任务","display":"card","icon":"puzzle","fields":[{"id":"install:@precinct/cloister","type":"button","label":"安装","style":"primary"}]},
			{"title":"daily","caption":"@precinct/daily\n最新 0.1.0-alpha.0","description":"个人日常助手：对话视图、定时提醒和 daily 运行时","display":"card","icon":"puzzle","fields":[{"id":"install:@precinct/daily","type":"button","label":"安装","style":"primary"}]}]}`
		s.Register("@precinct/dovetail", sidecar.Contribution{ID: "market", Type: "settings_page", Raw: json.RawMessage(market)})
		s.Register("@precinct/dovetail", sidecar.Contribution{ID: "market-nav", Type: "sidebar_item", Raw: json.RawMessage(`{"id":"market-nav","type":"sidebar_item","title":"插件市场 · 1 个更新","icon":"puzzle","viewId":"market"}`)})
		s.mu.Lock()
		s.plugins = []host.Installed{
			{Name: "@precinct/dovetail", Version: "0.1.0", Sidecar: "bin/dovetail", Capabilities: []string{"settings_page", "sidebar_item", "plugin_install"}},
			{Name: "@precinct/cloister", Version: "0.1.0-alpha.0", Sidecar: "bin/cloister", Capabilities: []string{"settings_page", "provider_endpoint", "workspace_hook"}},
			{Name: "@precinct/daily", Version: "0.1.0-alpha.0", Sidecar: "bin/daily", Capabilities: []string{"inbox", "background"}},
		}
		s.sidecars = map[string]sidecar.Status{
			"@precinct/dovetail": {State: sidecar.Running},
			"@precinct/cloister": {State: sidecar.Running},
			"@precinct/daily":    {State: sidecar.Crashed, Crashes: 3, LastError: "exit status 2", Stderr: "panic: open bot/state.db: permission denied"},
		}
		s.mu.Unlock()
		shot("plugins-list")
		s.mu.Lock()
		s.page = pagePlugin + "@precinct/dovetail"
		s.mu.Unlock()
		shot("plugin-detail")
		s.mu.Lock()
		s.page = pageView + "@precinct/dovetail#market"
		s.mu.Unlock()
		shot("plugin-market")
		s.mu.Lock()
		s.plan = &host.Plan{Package: &pkg.Package{Name: "@precinct/dovetail", Version: "0.1.1", Description: "插件市场：从签名索引浏览、安装和更新 Alcove 插件",
			Alcove: &pkg.Alcove{Capabilities: []string{"settings_page", "sidebar_item", "plugin_install"}}},
			Profiles: []string{"alcove-plugins"}, Replaces: &host.Installed{Name: "@precinct/dovetail", Version: "0.1.0"}}
		s.planFrom = "@precinct/dovetail"
		s.mu.Unlock()
		shot("plugin-plan")
		s.mu.Lock()
		s.plan, s.planFrom = nil, ""
		s.page = pagePlugin + "@precinct/daily"
		s.mu.Unlock()
		shot("plugin-crashed")
		s.mu.Lock()
		s.plugins, s.sidecars = nil, nil
		s.mu.Unlock()
	}
}

type ev struct {
	sid string
	seq int
}

func (e *ev) emit(s *Shell, typ string, data any) {
	e.seq++
	d, _ := json.Marshal(data)
	p := fmt.Sprintf(`{"sessionId":%q,"event":{"type":%q,"seq":%d,"data":%s}}`, e.sid, typ, e.seq, d)
	s.OnEvent("rt1", alrt.Event{Method: "session.event", Params: json.RawMessage(p)})
}

func (e *ev) status(s *Shell, st string) {
	s.OnEvent("rt1", alrt.Event{Method: "session.status", Params: json.RawMessage(fmt.Sprintf(`{"sessionId":%q,"status":%q}`, e.sid, st))})
}

func (e *ev) user(s *Shell, text string) {
	e.emit(s, "user/message", map[string]any{"role": "user", "source": map[string]string{"kind": "user"}, "content": []map[string]string{{"type": "text", "text": text}}})
}

func (e *ev) say(s *Shell, turn, step int, text string) {
	e.emit(s, "assistant/message", map[string]any{"turn": turn, "step": step, "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": text}}}})
}

func (e *ev) tool(s *Shell, id, name, args, result string, done bool) {
	e.emit(s, "tool/call", map[string]string{"callId": id, "name": name, "arguments": args})
	if done {
		e.emit(s, "tool/result", map[string]any{"message": map[string]any{"toolCallId": id, "content": []map[string]string{{"type": "text", "text": result}}}})
	}
}

// demo fills the shell with a workspace of example tasks.
func demo(t *testing.T, s *Shell) *wsView {
	dir := filepath.Join(t.TempDir(), "quarterly")
	for _, f := range []string{"data/2026-07.csv", "data/2026-08.csv", "data/2026-09.csv", "data/notes.md", "report/Q3.md", "summarize.py", "README.md"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	page := `{"id":"org","type":"settings_page","title":"组织","sections":[
		{"title":"组织","description":"已加入组织 Precinct（liuliu），本设备已批准，已连接。","fields":[]},
		{"title":"组织工作区","description":"内容同步到组织云端，可以派到云端运行。","fields":[
			{"id":"sync","type":"button","label":"同步「季度报表」"},
			{"id":"task","type":"text","label":"派到云端：要完成的任务","placeholder":"例如：把 data/ 下的报表汇总成季度报告"},
			{"id":"run","type":"button","label":"派到云端","style":"primary"}]}]}`
	s.Register("@precinct/cloister", sidecar.Contribution{ID: "org", Type: "settings_page", Raw: json.RawMessage(page)})
	s.mu.Lock()
	w := workspace.Workspace{ID: "w1", Name: "季度报表", Dir: dir, Profile: "ws-w1", Owner: "@precinct/cloister"}
	s.workspaces = []workspace.Workspace{w, {ID: "w2", Name: "precinct-alcove", Dir: "/Users/liuliu/IdeaProjects/precinct-alcove", Profile: "ws-w2", Owner: "personal"},
		{ID: "w0", Name: app.Scratch, Dir: "/Users/liuliu/Alcove", Profile: "ws-w0", Owner: "personal"}}
	s.scratchID = "w0"
	sv := s.view("w0")
	for _, m := range []tasks.Task{
		{ID: "chat-r1", Title: "翻译这段合同条款", UpdatedAt: time.Now().UTC().Add(-3 * time.Hour)},
		{ID: "chat-r2", Title: "把截图里的表格转成 Excel", UpdatedAt: time.Now().UTC().Add(-26 * time.Hour)},
	} {
		s.addTask(sv, m)
	}
	v := s.view("w1")
	v.runtimeID = "rt1"
	s.byRuntime["rt1"] = "w1"
	s.focus("w1")
	now := time.Now().UTC()
	for i, m := range []tasks.Task{
		{ID: "chat-old1", Title: "整理 9 月的发票", UpdatedAt: now.Add(-50 * time.Hour)},
		{ID: "chat-old2", Title: "修复 build.sh 在 macOS 上的路径问题", UpdatedAt: now.Add(-30 * time.Hour)},
		{ID: "task-a2", Title: "总结 README", Source: "@precinct/cloister", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "chat-today", Title: "把会议纪要整理成待办清单", UpdatedAt: now.Add(-40 * time.Minute)},
	} {
		_ = i
		s.addTask(v, m)
	}
	s.mu.Unlock()

	r := &ev{sid: "chat-report"}
	r.status(s, "running")
	r.user(s, "把 data/ 下的报表汇总成季度报告，输出到 report/Q3.md，再转一份 docx 放桌面")
	r.say(s, 1, 1, "好的。我先看一下 data/ 里有哪些报表，再按月份汇总。")
	r.tool(s, "c1", "bash", `{"command":"ls data/","description":"列出报表"}`, "2026-07.csv\n2026-08.csv\n2026-09.csv\nnotes.md", true)
	r.tool(s, "c2", "read", `{"file_path":"data/2026-07.csv"}`, "月份,收入,支出\n7,128400,96200", true)
	r.tool(s, "c3", "write", `{"file_path":"report/Q3.md","content":"# 第三季度报告\n..."}`, "Created file", true)
	r.say(s, 1, 2, "汇总好了：第三季度收入 **41.2 万**，比上季度多 8%。报告写在 `report/Q3.md`。\n\n接下来把它转成 docx。")
	r.tool(s, "c5", "edit", `{"file_path":"summarize.py","old_string":"a","new_string":"b"}`, "ok", true)
	r.tool(s, "c4", "bash", `{"command":"pandoc report/Q3.md -o ~/Desktop/Q3.docx"}`, "", false)

	b := &ev{sid: "task-c"}
	b.status(s, "running")
	b.user(s, "慢慢分析 README.md，等我补充要求")

	go s.Approve(context.Background(), "rt1", alrt.ApprovalRequest{RequestID: "r1", SessionID: "chat-report", ToolName: "bash",
		Reason: "write outside", DisplayReason: alrt.LocalText{"zh": "命令要写工作区外的 ~/Desktop/Q3.docx"}})
	for deadline := time.Now().Add(5 * time.Second); ; {
		s.mu.Lock()
		n := len(v.approvals)
		s.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return v
}

var _ = ui.Transparent
