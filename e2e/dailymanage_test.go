//go:build e2e

// The daily assistant beyond the conversation, in a real Alcove core with
// real dsh and the packed @precinct/daily (DOVETAIL_DAILY_TGZ, as for
// TestDaily): its introduction and pages, memories and reminders removed
// from the pages, the daily briefing, and work handed to a workspace the
// user picks (task.start). Writes e2e/report/daily-manage.md.
//
// Ways this can fail, each checked below:
//   - the sidecar registers no intro or pages, or pages never follow the
//     engine (memory.sync, schedule.sync not handled or not declared);
//   - a removal is undone by the engine's next sync, or never reaches the
//     engine, so the memory is still used or the reminder still fires;
//   - the briefing never comes, or comes at once on being turned on;
//   - a task goes to the in-process worker (task.local not declared), is
//     run without asking, or in a workspace the user did not pick;
//   - the plugin cannot prompt the session it was allowed (no grant), or
//     the report never gets back to the engine and the user.
package e2e

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

func TestDailyManage(t *testing.T) {
	src := os.Getenv("DOVETAIL_DAILY_TGZ")
	if src == "" {
		t.Skip("set DOVETAIL_DAILY_TGZ to a packed @precinct/daily")
	}
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "日常助手：开场、提醒与记忆管理、每日简报、在工作区里干活 e2e", file: "daily-manage.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	bot := &manageModel{t: t}
	model.Respond = bot.respond

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	view := &inboxView{items: map[string]map[string]any{}}
	rec := &recorder{ui: map[string]sidecar.Contribution{}}
	// The user's answer to a task request: the workspace set below.
	var askMu sync.Mutex
	var asked []sidecar.TaskRequest
	var pick string
	dataDir := filepath.Join(work, "data")
	a, err := app.New(app.Config{
		DataDir: dataDir, AlcoveVersion: "0.1.0",
		Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
		TrustedKeys: []ed25519.PublicKey{pub}, Secrets: &memSecrets{m: map[string][]byte{}},
		Answerer: &scriptedAnswerer{outcome: "allowed-once"}, SidecarUI: rec, Inbox: view.post,
		ConfirmTask: func(ctx context.Context, plugin string, req sidecar.TaskRequest) (string, bool) {
			askMu.Lock()
			defer askMu.Unlock()
			asked = append(asked, req)
			return pick, pick != ""
		},
	})
	if err == nil {
		err = a.Start(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Sidecars.Services.Log = rec.log
	a.Sidecars.Backoff = 100 * time.Millisecond
	a.Sidecars.Env = []string{"ALCOVE_DATA_DIR=" + dataDir, "PRECINCT_DAILY_IDLE_AFTER=20s"}
	if _, err := a.Creds.Add("假模型", "deepseek-official", model.URL, "sk-e2e-daily", ""); err != nil {
		t.Fatal(err)
	}

	tgz := filepath.Join(work, filepath.Base(src))
	err = copyFile(src, tgz)
	if err == nil {
		s, serr := sig.Sign(tgz, priv)
		err = serr
		os.WriteFile(tgz+".sig", []byte(s), 0o644)
	}
	if err == nil {
		plan, perr := a.Plugins.Prepare(ctx, tgz)
		err = perr
		if err == nil && !plan.OK() {
			err = fmt.Errorf("problems: %v", plan.Problems)
		}
		if err == nil {
			_, err = a.InstallPlugin(ctx, plan)
		}
	}
	if err == nil {
		err = waitFor(func() bool { st, ok := a.Sidecars.Status(dailyPlugin); return ok && st.State == sidecar.Running }, 30*time.Second)
	}
	r.step("安装 daily 并握手", err, "")
	if err != nil {
		return
	}
	sidecarLog := func() string {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return strings.Join(rec.logs, "\n")
	}
	defer func() {
		if t.Failed() {
			t.Logf("sidecar log:\n%s\ninbox: %s\nreminders: %s\nmemory: %s", sidecarLog(), strings.Join(view.texts(), " | "),
				rec.raw(dailyPlugin+"#reminders"), rec.raw(dailyPlugin+"#memory"))
		}
	}()
	page := func(id string) string { return rec.raw(dailyPlugin + "#" + id) }

	// 1. The introduction and both pages come with the handshake.
	err = nil
	intro := page("intro")
	switch {
	case !strings.Contains(intro, `"type":"inbox_intro"`) || !strings.Contains(intro, "suggestions"):
		err = fmt.Errorf("intro %q", intro)
	case !strings.Contains(page("reminders"), "每日简报"):
		err = fmt.Errorf("reminders page %q", page("reminders"))
	case !strings.Contains(page("memory"), "还没有记住什么"):
		err = fmt.Errorf("memory page %q", page("memory"))
	}
	r.step("握手时登记开场（inbox_intro，带示例）和「提醒与日程」「记忆」两页", err, "")
	if err != nil {
		return
	}

	turn := func(id, text, want string) error {
		err := a.Sidecars.InboxInput(ctx, dailyPlugin, id, text, time.Now())
		if err == nil {
			err = waitFor(func() bool { return view.has(want) }, 90*time.Second)
		}
		return err
	}

	// 2. A memory and a reminder show on the pages.
	err = turn("in-1", "记住我喜欢喝美式", "记住了")
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("memory"), "喜欢喝美式") }, 30*time.Second)
	}
	r.step("对话里记下的记忆出现在「记忆」页（memory.sync）", err, "")
	if err != nil {
		return
	}
	err = turn("in-2", "明天早上 9 点提醒我交周报", "明天早上 9 点提醒你")
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("reminders"), "交周报") }, 30*time.Second)
	}
	r.step("对话里定的提醒出现在「提醒与日程」页", err, firstMatch(page("reminders"), `下次 [^"]*`))
	if err != nil {
		return
	}

	// 3. Both are removed from the pages; the engine is told, and its own
	// syncs do not bring them back.
	forget := firstMatch(page("memory"), `forget:[^"]+`)
	cancel := firstMatch(page("reminders"), `cancel:[^"]+`)
	err = a.Sidecars.UIAction(ctx, dailyPlugin, "memory", forget, json.RawMessage(`{}`))
	if err == nil {
		err = a.Sidecars.UIAction(ctx, dailyPlugin, "reminders", cancel, json.RawMessage(`{}`))
	}
	if err == nil {
		err = waitFor(func() bool {
			return !strings.Contains(page("memory"), "喜欢喝美式") && !strings.Contains(page("reminders"), "交周报")
		}, 10*time.Second)
	}
	r.step("在页上删除记忆、取消提醒，页面立即更新", err, forget+" · "+cancel)
	if err != nil {
		return
	}
	err = turn("in-3", "我喜欢喝什么？", "不记得")
	if err == nil {
		err = waitFor(func() bool { return hostState(dataDir, `"removals"`) == "" }, 30*time.Second)
	}
	if err == nil && (strings.Contains(page("memory"), "喜欢喝美式") || strings.Contains(page("reminders"), "交周报")) {
		err = fmt.Errorf("the engine's sync brought a removed item back")
	}
	if err == nil && strings.Contains(hostState(dataDir, `"schedules"`), "交周报") {
		err = fmt.Errorf("the host still holds the cancelled schedule")
	}
	r.step("引擎收到删除：再问时记忆里已没有「美式」，同步后提醒也没有回来", err, "")
	if err != nil {
		return
	}

	// 4. The daily briefing, set for the next minute.
	at := time.Now().Add(time.Minute).Format("15:04")
	payload, _ := json.Marshal(map[string]any{"values": map[string]any{"on": true, "time": at}})
	err = a.Sidecars.UIAction(ctx, dailyPlugin, "reminders", "briefing", payload)
	if err == nil && view.has("今天的简报") {
		err = fmt.Errorf("the briefing came as soon as it was turned on")
	}
	if err == nil {
		err = waitFor(func() bool { return view.has("今天的简报") }, 150*time.Second)
	}
	r.step("打开每日简报（定在下一分钟），到点在对话里收到简报", err, "时间 "+at)
	if err != nil {
		return
	}

	// 5. Work that needs a workspace: the user is asked, picks one, and the
	// result comes back.
	dir := filepath.Join(work, "proj")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# demo\n\nTODO: 安装说明\n"), 0o644)
	w, err := a.CreateWorkspace(ctx, "演示项目", dir)
	if err != nil {
		r.step("建一个个人工作区", err, "")
		return
	}
	askMu.Lock()
	pick = w.ID
	askMu.Unlock()
	err = turn("in-4", "帮我把项目 README 里的待办整理成清单", "我去整理 README 待办")
	var req sidecar.TaskRequest
	if err == nil {
		err = waitFor(func() bool { askMu.Lock(); defer askMu.Unlock(); return len(asked) > 0 }, 60*time.Second)
	}
	if err == nil {
		askMu.Lock()
		req = asked[0]
		askMu.Unlock()
		if req.Title != "整理 README 待办" || !strings.Contains(req.Prompt, "日常助手") {
			err = fmt.Errorf("asked %+v", req)
		}
	}
	r.step("接下需要工作区的事：Alcove 先问用户在哪个工作区做（task.start）", err, "标题："+req.Title+"；说明："+req.Detail)
	if err != nil {
		return
	}
	sid := app.TaskSessionID(dailyPlugin, req.ID)
	err = waitFor(func() bool { return view.has("已在工作区「演示项目」开始") }, 30*time.Second)
	if err == nil {
		err = waitFor(func() bool { return bot.workerRan() }, 60*time.Second)
	}
	if err == nil && a.SessionSource(w.ID, "personal", sid) != dailyPlugin {
		err = fmt.Errorf("session %s is not listed as the assistant's", sid)
	}
	r.step("用户选了「演示项目」：任务在该工作区作为助手的会话运行，对话里告知已开始", err, sid)
	if err != nil {
		return
	}
	err = waitFor(func() bool { return view.has("做完了：README 待办") }, 90*time.Second)
	r.step("任务的汇报交回引擎，助手在对话里告诉用户结果", err, "")
	if err != nil {
		return
	}
	// The grant covers this session only.
	_, err = a.Sidecars.Services.PromptSession(ctx, dailyPlugin, w.ID, "task-other", "hi")
	if err == nil {
		err = fmt.Errorf("prompting another session of the workspace was allowed")
	} else {
		err = nil
	}
	r.step("授权只限这一个会话：同一工作区的其他会话不让插件用", err, "")
}

// hostState is the part of the daily host state file from key on, "" when
// the key is absent or empty.
func hostState(dataDir, key string) string {
	b, _ := os.ReadFile(filepath.Join(dataDir, "bot", "alcove-daily-host.json"))
	s := string(b)
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	if strings.HasPrefix(strings.TrimLeft(rest, ": "), "null") || strings.HasPrefix(strings.TrimLeft(rest, ": "), "[]") || strings.HasPrefix(strings.TrimLeft(rest, ": "), "{}") {
		return ""
	}
	return rest
}

func firstMatch(s, pattern string) string {
	return regexp.MustCompile(pattern).FindString(s)
}

// manageModel scripts the daily router (as dailyModel does) and the worker
// session the task runs as.
type manageModel struct {
	t      *testing.T
	mu     sync.Mutex
	worker bool
}

func (m *manageModel) workerRan() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.worker
}

func (m *manageModel) respond(body []byte) (*fakemodel.ToolCall, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "好"
	}
	if len(req.Messages) > 0 && strings.Contains(string(req.Messages[0].Content), "你在替用户的日常助手执行一个任务") {
		m.worker = true
		return nil, "README 待办已整理：1. 补安装说明"
	}
	var trigger, text, lastResult, report string
	calls, sinceReport := 0, 0
	for _, msg := range req.Messages {
		var blocks []msgBlock
		if json.Unmarshal(msg.Content, &blocks) != nil {
			var s string
			if json.Unmarshal(msg.Content, &s) == nil {
				blocks = []msgBlock{{Type: "text", Text: s}}
			}
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				var raw map[string]any
				if strings.Contains(b.Text, `"kind":"candidate"`) && json.Unmarshal([]byte(b.Text), &raw) == nil {
					id, _ := raw["trigger_id"].(string)
					if id != "" && id != trigger {
						trigger, calls = id, 0
						text, _ = raw["text"].(string)
					}
				}
				if strings.Contains(b.Text, `"kind":"worker_report"`) {
					report, sinceReport = b.Text, 0
				}
			case "tool_use":
				calls++
				sinceReport++
			case "tool_result":
				lastResult = string(b.Content)
			}
		}
	}
	if report != "" {
		if sinceReport > 0 {
			return nil, "好"
		}
		var rep struct {
			Ticket struct {
				ID string `json:"ticket_id"`
			} `json:"ticket"`
			Summary string `json:"summary"`
		}
		json.Unmarshal([]byte(report), &rep)
		return &fakemodel.ToolCall{Name: "orchestrator_reply", Input: map[string]any{"ticket_id": rep.Ticket.ID, "purpose": "result", "text": "做完了：" + rep.Summary}}, ""
	}
	if trigger == "" {
		return nil, "好"
	}
	reply := func(s string) fakemodel.ToolCall {
		return fakemodel.ToolCall{Name: "orchestrator_reply", Input: map[string]any{"trigger_id": trigger, "purpose": "answer", "text": s}}
	}
	ignore := fakemodel.ToolCall{Name: "orchestrator_route", Input: map[string]any{"decisions": []any{map[string]any{"kind": "ignore", "trigger_id": trigger, "reason": "已直接回答，不需要建工单"}}}}
	var plan []fakemodel.ToolCall
	switch {
	case strings.Contains(text, "每日简报"):
		plan = []fakemodel.ToolCall{reply("早上好，这是今天的简报：没有到点的提醒。"), ignore}
	case strings.HasPrefix(text, "记住"):
		plan = []fakemodel.ToolCall{
			{Name: "memory_write", Input: map[string]any{"label": "饮品偏好", "text": "喜欢喝美式", "trigger_id": trigger}},
			reply("记住了：你喜欢喝美式。"), ignore}
	case strings.Contains(text, "提醒我交周报"):
		at := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Hour).Format("2006-01-02T15:04:05+00:00")
		plan = []fakemodel.ToolCall{
			{Name: "schedule_create", Input: map[string]any{"at": at, "timezone": "UTC", "kind": "notify", "text": "交周报", "trigger_id": trigger}},
			reply("好的，明天早上 9 点提醒你交周报。"), ignore}
	case strings.Contains(text, "喜欢喝什么"):
		plan = []fakemodel.ToolCall{{Name: "memory_read", Input: map[string]any{"query": "喜欢喝", "limit": 5}}}
		if calls >= 1 {
			answer := "我不记得你喜欢喝什么。"
			if strings.Contains(lastResult, "美式") {
				answer = "你喜欢喝美式（来自记忆）。"
			}
			plan = append(plan, reply(answer), ignore)
		}
	case strings.Contains(text, "README"):
		plan = []fakemodel.ToolCall{{Name: "orchestrator_route", Input: map[string]any{"decisions": []any{map[string]any{
			"kind": "accept", "trigger_id": trigger, "title": "整理 README 待办", "worktree_name": "readme-todo",
			"acknowledgement": "好的，我去整理 README 待办。"}}}}}
	}
	if calls < len(plan) {
		c := plan[calls]
		return &c, ""
	}
	return nil, "好"
}
