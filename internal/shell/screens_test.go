package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"image/png"
	"math"
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
			// Let transitions (row highlights) finish.
			tt.Frame()
			tt.Advance(time.Second)
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
		// The commands that finished, the file read and the edit, opened; then
		// the top of the conversation, where they are.
		s.mu.Lock()
		tk := v.tasks["chat-report"]
		for _, e := range tk.session.Entries {
			if e.ToolName == "edit" || e.ToolName == "read" || e.ToolName == "bash" && !e.Busy() {
				tk.open[e.Key] = true
			}
		}
		s.mu.Unlock()
		shot("task-open")
		s.mu.Lock()
		tk.scroll.Y = 0
		s.mu.Unlock()
		shot("task-open-top")
		s.mu.Lock()
		tk.scroll.Y = math.MaxFloat32
		tk.open = map[string]bool{}
		s.filesMenu = true
		s.mu.Unlock()
		shot("task-files")
		// The browser on its own sheet, in place of the alcove.
		s.mu.Lock()
		s.filesMenu = false
		v.browsing = true
		s.mu.Unlock()
		shot("task-browser")
		s.mu.Lock()
		v.browsing = false
		v.current = ""
		s.mu.Unlock()
		shot("home")
		s.mu.Lock()
		s.panels.open = map[string]bool{filepath.Join(v.ws.Dir, "data"): true}
		s.mu.Unlock()
		shot("home-files")
		time.Sleep(100 * time.Millisecond)
		shot("home-files")
		s.mu.Lock()
		s.panels.hidden = true
		s.mu.Unlock()
		shot("home-band-hidden")
		// A narrow window: the alcove steps aside for the sheet.
		s.mu.Lock()
		s.panels.hidden = false
		v.current = "chat-report"
		s.mu.Unlock()
		tt.SetSize(1000, 720)
		shot("narrow-task")
		tt.SetSize(1280, 800)
		// A turn that ended in an error: the notice and why, in full.
		s.mu.Lock()
		v.current = "chat-old2"
		s.mu.Unlock()
		shot("task-notice")
		s.mu.Lock()
		v.current = "chat-old1"
		s.mu.Unlock()
		shot("task-stopped")
		s.mu.Lock()
		v.current = ""
		s.mu.Unlock()
		s.mu.Lock()
		s.panels.hidden = false
		s.page = PageSettings
		s.mu.Unlock()
		shot("settings")
		time.Sleep(300 * time.Millisecond)
		shot("settings")
		s.mu.Lock()
		s.settings.Appearance = app.Appearance{Tone: "cool", Accent: "violet", Wallpaper: noWallpaper}
		s.mu.Unlock()
		shot("settings-violet")
		s.mu.Lock()
		s.page, v.current = pageWS+v.ws.ID, "chat-report"
		s.mu.Unlock()
		shot("task-violet")
		s.mu.Lock()
		s.settings.Appearance = app.Appearance{Accent: "amber", Wallpaper: noWallpaper}
		s.mu.Unlock()
		shot("task-amber")
		for _, tn := range []string{"zinc", "slate", "linen", "warm", "neutral", "cool", "forest"} {
			s.mu.Lock()
			s.settings.Appearance = app.Appearance{Tone: tn, Wallpaper: noWallpaper}
			s.mu.Unlock()
			shot("tone-" + tn)
		}
		// The default tone and accent without a painting: the window in
		// black and white, on each kind of page.
		s.mu.Lock()
		s.settings.Appearance = app.Appearance{Wallpaper: noWallpaper}
		for _, e := range tk.session.Entries {
			if e.ToolName == "edit" || e.ToolName == "read" {
				tk.open[e.Key] = true
			}
		}
		tk.scroll.Y = 0
		s.mu.Unlock()
		shot("bw-task-open")
		s.mu.Lock()
		tk.open, tk.scroll.Y = map[string]bool{}, math.MaxFloat32
		v.current = "chat-today"
		s.mu.Unlock()
		shot("bw-done")
		s.mu.Lock()
		v.current = ""
		s.mu.Unlock()
		shot("bw-home")
		s.mu.Lock()
		s.page = PageWaiting
		s.mu.Unlock()
		shot("bw-waiting")
		s.mu.Lock()
		s.page, s.prefs.tab = PageSettings, tabLook
		s.mu.Unlock()
		shot("bw-settings")
		s.mu.Lock()
		s.prefs.tab = tabModel
		s.mu.Unlock()
		shot("bw-model")
		s.mu.Lock()
		s.page, v.current = pageWS+v.ws.ID, "chat-report"
		s.mu.Unlock()
		// Each theme's painting on the new-task page, under a task and
		// behind its own settings.
		for _, sc := range scenes {
			s.mu.Lock()
			s.settings.Appearance = withScene(app.Appearance{}, sc)
			s.page, v.current = pageWS+v.ws.ID, ""
			s.mu.Unlock()
			shot("theme-" + sc.id + "-new")
			s.mu.Lock()
			v.current = "chat-report"
			s.mu.Unlock()
			shot("theme-" + sc.id + "-task")
			s.mu.Lock()
			s.page, s.prefs.tab = PageSettings, tabLook
			s.mu.Unlock()
			shot("theme-" + sc.id + "-settings")
		}
		// The user's own picture, kept as a copy the size the window needs.
		photo := filepath.Join(t.TempDir(), "photo.jpg")
		if data, err := paintings.ReadFile("wallpapers/summer-day.jpg"); err != nil {
			t.Fatal(err)
		} else if err := os.WriteFile(photo, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := saveWallpaper(photo, s.customWallpaper()); err != nil {
			t.Fatal(err)
		}
		wallpapers.Delete(customKey)
		thumbs.Delete(customKey)
		s.mu.Lock()
		s.PickImage = func() (string, error) { return "", nil }
		s.settings.Appearance = app.Appearance{Wallpaper: customScene.id}
		s.page, v.current = pageWS+v.ws.ID, "chat-report"
		s.mu.Unlock()
		shot("theme-custom-task")
		s.mu.Lock()
		s.page, s.prefs.tab = PageSettings, tabLook
		s.mu.Unlock()
		shot("theme-custom-settings")
		// Index in the panel, in each mood: ready on the new-task page, at
		// work, waiting on the user, done, and stuck.
		s.mu.Lock()
		s.settings.Appearance = withScene(app.Appearance{}, scenes[0])
		s.page, v.current = pageWS+v.ws.ID, ""
		s.mu.Unlock()
		for _, m := range []struct{ name, task, err string }{
			{"idle", "", ""}, {"working", "task-c", ""}, {"asking", "chat-report", ""},
			{"done", "chat-today", ""}, {"oops", "chat-today", "运行时断开了"},
		} {
			s.mu.Lock()
			v.current = m.task
			if tk := v.tasks[m.task]; tk != nil {
				if got := s.moodOf(v, tk); m.err == "" && string(got) != m.name {
					t.Errorf("%s shows %s", m.task, got)
				}
				tk.err = m.err
			}
			s.mu.Unlock()
			shot("mood-" + m.name)
		}
		s.mu.Lock()
		v.tasks["chat-today"].err = ""
		s.page, v.current = pageWS+v.ws.ID, "chat-report"
		s.mu.Unlock()
		// The agent's questions, on the task at work.
		var asks []alrt.Question
		if err := json.Unmarshal([]byte(`[
			{"id":"scope","header":"范围","question":"README 里的哪些部分要分析？","options":[
				{"label":"全部","description":"从头到尾通读，给出整体评价"},
				{"label":"只看安装和使用","description":"跳过背景介绍和贡献指南"}]},
			{"id":"out","question":"结果写到哪里？","multiSelect":true,"options":[
				{"label":"对话里"},{"label":"notes/readme-review.md","description":"新建这个文件"}]}]`), &asks); err != nil {
			t.Fatal(err)
		}
		qctx, cancelQ := context.WithCancel(context.Background())
		go s.AnswerQuestions(qctx, "rt1", alrt.QuestionRequest{RequestID: "q1", SessionID: "task-c", Questions: asks})
		until(t, tt, s, func() bool { return len(v.questions) == 1 })
		s.mu.Lock()
		v.current = "task-c"
		v.questions[0].multi[1][0] = true
		s.mu.Unlock()
		shot("question")
		cancelQ()
		until(t, tt, s, func() bool { return len(v.questions) == 0 })
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
		// The looks of the effort control, at the foot of 外观.
		tt.Scroll(700, 500, 0, 4000)
		shot("settings-look-effort")
		tt.Scroll(700, 500, 0, -4000)
		s.mu.Lock()
		s.prefs.tab = tabOther
		s.mu.Unlock()
		shot("settings-other")
		s.mu.Lock()
		s.prefs.tab = tabRuntimes
		s.mu.Unlock()
		shot("settings-runtimes")
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
		// DeepSeek's own route takes a reasoning effort: the slider.
		s.mu.Lock()
		s.settings.DefaultCredential = "c1"
		s.mu.Unlock()
		shot("new-run-effort")
		// The control's other looks (外观 › 思考强度).
		for _, look := range []string{"stream", "embers"} {
			s.mu.Lock()
			s.settings.Appearance.EffortStyle = look
			s.mu.Unlock()
			shot("new-run-effort-" + look)
		}
		s.mu.Lock()
		s.settings.Appearance.EffortStyle = ""
		s.mu.Unlock()
		// A task on the personal credentials that took some context.
		s.mu.Lock()
		s.settings.DefaultCredential = "c2"
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
		// The daily assistant: its introduction, a workspace task request
		// and its reminders page.
		logo, _ := os.ReadFile("logo.png")
		face, _ := ui.DecodeBitmap(logo)
		intro := `{"id":"intro","type":"inbox_intro","title":"你好，我是日常助手","body":"我帮你记事、定提醒，每天早上给你一份简报。需要改文件或跑命令的事，我会先问你在哪个工作区里做，你同意了才开始。\n\n已经定好的提醒和我记住的事，随时可以在上方的「提醒与日程」「记忆」里查看和删除。","suggestions":[
			{"label":"定个提醒","text":"明天早上 9 点提醒我交周报"},{"label":"每天重复","text":"每个工作日下午 6 点提醒我写日报"},
			{"label":"记住一件事","text":"记住：我每周三下午固定开组会"},{"label":"今天有什么","text":"今天我有哪些提醒和要跟进的事？"},
			{"label":"在项目里做事","text":"帮我把项目 README 里的待办事项整理成清单"}]}`
		reminders := `{"id":"reminders","type":"settings_page","title":"提醒与日程","submit":false,"sections":[
			{"title":"每日简报","description":"到点后我把今天的提醒、还在进行的事和要跟进的事整理好发到对话里。","fields":[
				{"id":"on","type":"toggle","label":"每天给我简报","value":true},
				{"id":"time","type":"select","label":"时间","value":"08:30","options":[{"value":"08:00","label":"每天 08:00"},{"value":"08:30","label":"每天 08:30"}]},
				{"id":"briefing","type":"button","label":"保存","style":"primary"}]},
			{"title":"交周报","description":"下次 明天 09:00 · 提醒","fields":[{"id":"cancel:s1","type":"button","label":"取消","style":"danger"}]},
			{"title":"写日报","description":"下次 今天 18:00 · 提醒 · 每个工作日","fields":[{"id":"cancel:s2","type":"button","label":"取消","style":"danger"}]}]}`
		memory := `{"id":"memory","type":"settings_page","title":"记忆","submit":false,"sections":[]}`
		todos := `{"id":"todos","type":"settings_page","title":"待办","submit":false,"sections":[
			{"title":"添加待办","fields":[{"id":"text","type":"text","label":"待办","placeholder":"比如：周五前交报销单"},{"id":"add","type":"button","label":"添加","style":"primary"}]},
			{"title":"周五前交报销单","description":"⚠️ 已逾期 · 截止 昨天 18:00","fields":[{"id":"done:t1","type":"button","label":"完成","style":"primary"},{"id":"remove:t1","type":"button","label":"删除","style":"danger"}]},
			{"title":"✓ 买牛奶","description":"已完成 · 今天 09:12","fields":[{"id":"reopen:t2","type":"button","label":"重新打开"},{"id":"remove:t2","type":"button","label":"删除","style":"danger"}]}]}`
		s.Register("@precinct/daily", sidecar.Contribution{ID: "intro", Type: "inbox_intro", Raw: json.RawMessage(intro)})
		s.Register("@precinct/daily", sidecar.Contribution{ID: "todos", Type: "settings_page", Raw: json.RawMessage(todos)})
		s.Register("@precinct/daily", sidecar.Contribution{ID: "reminders", Type: "settings_page", Raw: json.RawMessage(reminders)})
		s.Register("@precinct/daily", sidecar.Contribution{ID: "memory", Type: "settings_page", Raw: json.RawMessage(memory)})
		persona := `{"id":"persona","type":"settings_page","title":"人设与风格","sections":[
			{"title":"它是谁","fields":[{"id":"name","type":"text","label":"它的名字","value":"小月"},{"id":"call","type":"text","label":"怎么称呼你","value":"老王"}]},
			{"title":"人设全文（SOUL.md）","description":"想写得更细就写在这里：性格、口头禅、背景……最多 8000 个字。也可以直接编辑 ~/Library/Application Support/Alcove/bot/SOUL.md。",
			 "fields":[{"id":"soul","type":"textarea","label":"人设全文","value":"你是一个耐心、有点话痨的助手，说话像老朋友。\n遇到我拖延时会温和地催一下，但不说教。\n口头禅：「妥妥的」。"}]}]}`
		s.Register("@precinct/daily", sidecar.Contribution{ID: "persona", Type: "settings_page", Raw: json.RawMessage(persona)})
		for _, pg := range [][2]string{{"skills", "技能"}, {"channels", "消息通道"}} {
			s.Register("@precinct/daily", sidecar.Contribution{ID: pg[0], Type: "settings_page", Raw: json.RawMessage(`{"id":"` + pg[0] + `","type":"settings_page","title":"` + pg[1] + `","sections":[]}`)})
		}
		s.mu.Lock()
		s.plugins[2] = host.Installed{Name: "@precinct/daily", Version: "0.1.0-alpha.2", Title: "日常助手", Sidecar: "bin/daily", Capabilities: []string{"settings_page", "inbox", "background", "workspace_session", "workspace_task"}}
		s.sidecars["@precinct/daily"] = sidecar.Status{State: sidecar.Running}
		s.avatars = map[string]*ui.Bitmap{"@precinct/daily": face}
		s.page = pageInbox + "@precinct/daily"
		s.mu.Unlock()
		shot("daily-intro")
		s.mu.Lock()
		ch := s.chat("@precinct/daily")
		ch.entries = append(ch.entries, &chatEntry{out: &outgoing{id: "o1", text: "帮我把项目 README 里的待办事项整理成清单", ts: time.Now(), state: outDelivered}},
			&chatEntry{item: &inboxItem{ID: "m1", Kind: "message", Text: "好的，我去整理 README 待办。"}})
		s.taskAsks = []*taskAsk{{plugin: "@precinct/daily", answer: make(chan string, 1), req: sidecar.TaskRequest{ID: "a1", Title: "整理 README 待办", Detail: "起因：帮我把项目 README 里的待办事项整理成清单"}}}
		s.mu.Unlock()
		shot("daily-ask")
		s.mu.Lock()
		s.taskAsks = nil
		ch.entries = append(ch.entries, &chatEntry{item: &inboxItem{ID: "r1", Kind: "card", Card: &card{Title: "⏰ 提醒", Body: "交周报",
			Actions: []cardAction{{ID: "done", Label: "完成", Style: "primary"}, {ID: "snooze:10m", Label: "10 分钟后"}, {ID: "snooze:1h", Label: "1 小时后"}, {ID: "snooze:tomorrow", Label: "明天早上"}}}}})
		s.mu.Unlock()
		shot("daily-reminder-card")
		s.mu.Lock()
		s.taskAsks = []*taskAsk{{plugin: "@precinct/daily", answer: make(chan string, 1), req: sidecar.TaskRequest{ID: "a1", Title: "整理 README 待办", Detail: "起因：帮我把项目 README 里的待办事项整理成清单"}}}
		s.mu.Unlock()
		s.mu.Lock()
		s.page = PageWaiting
		s.mu.Unlock()
		shot("waiting-ask")
		s.mu.Lock()
		s.taskAsks = nil
		s.page = pageView + "@precinct/daily#reminders"
		ch.unread = 3
		s.mu.Unlock()
		shot("daily-reminders")
		s.mu.Lock()
		s.page = pageView + "@precinct/daily#todos"
		s.mu.Unlock()
		shot("daily-todos")
		s.mu.Lock()
		s.page = pageView + "@precinct/daily#persona"
		s.mu.Unlock()
		shot("daily-persona")
		s.mu.Lock()
		s.page = pagePlugin + "@precinct/daily"
		s.mu.Unlock()
		shot("daily-detail")
		s.mu.Lock()
		s.plugins, s.sidecars, s.avatars = nil, nil, nil
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
	s.workspaces = []workspace.Workspace{w, {ID: "w2", Name: "precinct-alcove", Dir: t.TempDir(), Profile: "ws-w2", Owner: "personal"},
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
	// Two subagents: one finished with its answer, one still at work.
	agent := func(child, method, extra string) {
		s.OnEvent("rt1", alrt.Event{Method: method, Params: json.RawMessage(`{"parentSessionId":"chat-report","childSessionId":"` + child + `"` + extra + `}`)})
	}
	agent("sub-2", "subagent.started", "")
	r.tool(s, "c7", "subagent", `{"description":"核对 7 月的退款","prompt":"找出 data/2026-07.csv 里的退款"}`, "started subagent sub-2", true)
	(&ev{sid: "sub-2"}).tool(s, "t1", "grep", `{"pattern":"退款","path":"data/2026-07.csv"}`, "3 matches", true)
	agent("sub-2", "subagent.finished", `,"status":"ok","lastAssistantMessage":[{"type":"text","text":"7 月有 3 笔退款，共 4,820 元，已从收入里扣除。"}]`)
	agent("sub-1", "subagent.started", "")
	r.tool(s, "c6", "subagent", `{"description":"核对三个月的支出","prompt":"逐月核对 data/ 下的支出"}`, "started subagent sub-1", true)
	sub := &ev{sid: "sub-1"}
	sub.tool(s, "s1", "read", `{"file_path":"data/2026-08.csv"}`, "月份,收入,支出", true)
	sub.tool(s, "s2", "bash", `{"command":"python3 summarize.py --check"}`, "", false)

	b := &ev{sid: "task-c"}
	b.status(s, "running")
	b.user(s, "慢慢分析 README.md，等我补充要求")

	d := &ev{sid: "chat-today"}
	d.user(s, "把会议纪要整理成待办清单")
	d.tool(s, "d1", "read", `{"file_path":"notes/meeting.md"}`, "# 周会纪要\n- 周五前交 Q3 报告\n- 下周一评审报销流程", true)
	d.tool(s, "d2", "write", `{"file_path":"notes/todo.md","content":"- [ ] 周五前交 Q3 报告\n- [ ] 下周一评审报销流程"}`, "Created file", true)
	d.say(s, 1, 1, "整理好了：5 条待办，写在 [notes/todo.md](notes/todo.md)。")

	f := &ev{sid: "chat-old2"}
	f.user(s, "修一下 build.sh：在 macOS 上找不到 dist 目录")
	f.tool(s, "f1", "bash", `{"command":"./build.sh"}`, "readlink: illegal option -- f\nusage: readlink [-n] [file ...]\nexit status 1", true)
	// As dsh gives them: a failure, and a stop of the user's.
	f.emit(s, "turn/end", map[string]any{"turn": 1, "reason": map[string]any{"kind": "error",
		"error": map[string]any{"message": "529 overloaded: the model provider is busy, retried 3 times", "code": "OVERLOADED", "status": 529}}})
	o := &ev{sid: "chat-old1"}
	o.user(s, "把 9 月的发票按部门汇总")
	o.tool(s, "o1", "glob", `{"pattern":"invoices/2026-09/*.pdf"}`, "42 files", true)
	o.say(s, 1, 1, "找到 42 张发票，先逐张读出金额和部门。")
	o.emit(s, "turn/end", map[string]any{"turn": 1, "reason": map[string]any{"kind": "aborted", "reason": map[string]string{"kind": "user"}}})

	go s.Approve(context.Background(), "rt1", alrt.ApprovalRequest{RequestID: "r1", SessionID: "chat-report", ToolName: "bash", CallID: "c4",
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
