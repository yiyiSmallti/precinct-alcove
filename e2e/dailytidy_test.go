//go:build e2e

// Tidying while idle: after the person talked to the daily assistant and
// things were quiet for a while, the engine is asked to tidy its memories
// and to offer skills drafted from repeated requests. Real Alcove core, real
// dsh and the packed @precinct/daily (DOVETAIL_DAILY_TGZ); the model is
// scripted. Writes e2e/report/daily-tidy.md.
//
// Ways this can fail, each checked below:
//   - a tidy runs with nothing new to tidy (no conversation since);
//   - it never runs, or runs while the person is still talking;
//   - memory_tidy merges or drops nothing, or the host keeps no record of
//     it, so the 记忆 page cannot show or undo it;
//   - an undo does not reach the engine, or brings back the wrong memories;
//   - a proposed skill is written without the person taking it, is not
//     written when taken, or a declined one is offered again;
//   - the tidy turn posts a message in the conversation.
package e2e

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
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
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

func TestDailyTidy(t *testing.T) {
	src := os.Getenv("DOVETAIL_DAILY_TGZ")
	if src == "" {
		t.Skip("set DOVETAIL_DAILY_TGZ to a packed @precinct/daily")
	}
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "日常助手：闲时整理记忆与沉淀技能 e2e", file: "daily-tidy.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	bot := &tidyModel{}
	model.Respond = bot.respond

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	view := &inboxView{items: map[string]map[string]any{}}
	rec := &recorder{ui: map[string]sidecar.Contribution{}}
	dataDir := filepath.Join(work, "data")
	a, err := app.New(app.Config{
		DataDir: dataDir, AlcoveVersion: "0.1.0",
		Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
		TrustedKeys: []ed25519.PublicKey{pub}, Secrets: &memSecrets{m: map[string][]byte{}},
		Answerer: &scriptedAnswerer{outcome: "allowed-once"}, SidecarUI: rec, Inbox: view.post,
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
	const quiet = 8 * time.Second
	a.Sidecars.Env = []string{"ALCOVE_DATA_DIR=" + dataDir, "PRECINCT_DAILY_IDLE_AFTER=60s", "PRECINCT_DAILY_TIDY_IDLE=" + quiet.String()}
	if _, err := a.Creds.Add("假模型", "deepseek-official", model.URL, "sk-e2e-daily", ""); err != nil {
		t.Fatal(err)
	}
	// The tool model: another service (a custom one) with a key of its own,
	// which the daily runtime takes next to the default credential's.
	small, err := a.Creds.AddCustom("小模型", model.URL, "anthropic-messages", "sk-e2e-small", "fake-small")
	if err == nil {
		var st app.Settings
		if st, err = a.Settings(); err == nil {
			st.ToolModel = app.ToolModel{Credential: small.ID}
			err = a.SaveSettings(st)
		}
	}
	if err != nil {
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
	page := func(id string) string { return rec.raw(dailyPlugin + "#" + id) }
	defer func() {
		if t.Failed() {
			rec.mu.Lock()
			logs := strings.Join(rec.logs, "\n")
			rec.mu.Unlock()
			t.Logf("sidecar log:\n%s\ninbox: %s\nmemory: %s\nskills: %s", logs, strings.Join(view.texts(), " | "), page("memory"), page("skills"))
		}
	}()
	action := func(contribution, act string, values map[string]any) error {
		payload, _ := json.Marshal(map[string]any{"values": values})
		return a.Sidecars.UIAction(ctx, dailyPlugin, contribution, act, payload)
	}
	turn := func(id, text, want string) error {
		err := a.Sidecars.InboxInput(ctx, dailyPlugin, id, text, time.Now())
		if err == nil {
			err = waitFor(func() bool { return view.has(want) }, 90*time.Second)
		}
		return err
	}

	// 1. Nothing said yet: no tidy, however quiet.
	time.Sleep(quiet + 4*time.Second)
	err = nil
	if bot.saw("（闲时整理）") || strings.Contains(hostState(dataDir, `"tidy"`), `"last"`) {
		err = fmt.Errorf("a tidy ran with no conversation: %s", hostState(dataDir, `"tidy"`))
	}
	if err == nil && !(strings.Contains(page("memory"), `"label":"闲时整理记忆","type":"toggle","value":true`) &&
		strings.Contains(page("memory"), `"label":"建议沉淀技能","type":"toggle","value":true`)) {
		err = fmt.Errorf("the 记忆 page has no switches on: %s", page("memory"))
	}
	r.step("没聊过天时安静再久也不整理；「记忆」页两个开关默认打开", err, "")
	if err != nil {
		return
	}

	// 2. A conversation: overlapping memories, a one-off, a request made twice.
	steps := [][3]string{
		{"t-1", "记住：咖啡|喜欢美式咖啡", "记下了：喜欢美式咖啡"},
		{"t-2", "记住：咖啡|美式不加糖", "记下了：美式不加糖"},
		{"t-3", "记住：体检|11 月 1 日上午 10 点体检", "记下了：11 月 1 日上午 10 点体检"},
		{"t-4", "写周报：本周修了登录问题", "周报：本周修了登录问题"},
		{"t-5", "写周报：本周上线了导出，记得分三段", "周报：本周上线了导出"},
	}
	for _, s := range steps {
		if err = turn(s[0], s[1], s[2]); err != nil {
			break
		}
	}
	if err == nil && bot.saw("（闲时整理）") {
		err = fmt.Errorf("a tidy ran while the person was talking")
	}
	r.step("对话里记下三条记忆（两条说咖啡、一条已过期的体检），两次让写周报", err, "")
	if err != nil {
		return
	}
	start := time.Now()
	err = waitFor(func() bool { return strings.Contains(page("memory"), "合并为「咖啡」") }, quiet+90*time.Second)
	waited := time.Since(start).Round(time.Second)
	if err == nil && waited < quiet-time.Second {
		err = fmt.Errorf("the tidy came after %s, before %s of quiet", waited, quiet)
	}
	mem := page("memory")
	switch {
	case err != nil:
	case !strings.Contains(mem, "美式咖啡，不加糖") || strings.Contains(mem, `"喜欢美式咖啡`):
		err = fmt.Errorf("memories after the merge: %s", mem)
	case !strings.Contains(mem, "删除 「体检") || strings.Contains(mem, `"title":"体检"`):
		err = fmt.Errorf("the past check-up was not dropped: %s", mem)
	case pageButton(mem, "合并为「咖啡」", "undo:") == "" || pageButton(mem, "删除 「体检", "undo:") == "":
		err = fmt.Errorf("tidy changes have no 撤销: %s", mem)
	case view.has("整理完成"):
		err = fmt.Errorf("the tidy turn posted a message")
	}
	if err == nil {
		err = bot.modelsUsed("fake-small")
	}
	r.step("安静一段时间后自动整理：两条咖啡合并成「美式咖啡，不加糖」、过期体检删掉，「记忆」页列出改动并可撤销，对话里不出消息；整理在单独的会话里用工具模型（另一个服务的小模型）跑，对话仍用主模型", err, fmt.Sprintf("安静 %s 后开始；%s", waited, bot.modelSummary()))
	if err != nil {
		return
	}

	// 3. Undo the merge: the two memories are back, the merged one gone.
	err = action("memory", pageButton(mem, "合并为「咖啡」", "undo:"), nil)
	if err == nil {
		err = waitFor(func() bool {
			m := page("memory")
			return strings.Contains(m, "喜欢美式咖啡") && strings.Contains(m, `美式不加糖`) && strings.Contains(m, "已撤销") &&
				strings.Count(m, `"forget:`) == 2
		}, 60*time.Second)
	}
	if err == nil && !strings.Contains(page("memory"), "删除 「体检") {
		err = fmt.Errorf("undoing the merge touched the drop: %s", page("memory"))
	}
	r.step("撤销合并：原来的两条咖啡记忆回来、合并出的那条消失，删除体检那项不受影响", err, "")
	if err != nil {
		return
	}

	// 4. Skill proposals: one taken from its card, one declined on the page.
	skillsDir := filepath.Join(dataDir, "bot", ".dsh", "skills")
	var card string
	err = waitFor(func() bool { card = view.cardWith("💡 技能建议", "weekly-report"); return card != "" }, 30*time.Second)
	if err == nil && view.actions(card) != "take,decline" {
		err = fmt.Errorf("card actions %q", view.actions(card))
	}
	if err == nil {
		if _, serr := os.Stat(filepath.Join(skillsDir, "weekly-report")); serr == nil {
			err = fmt.Errorf("the skill was written before it was taken")
		}
	}
	if err == nil {
		err = a.Sidecars.UIAction(ctx, dailyPlugin, card, "take", json.RawMessage(`{}`))
	}
	if err == nil {
		err = waitFor(func() bool { return view.cardHas(card, "已收下") && view.actions(card) == "" }, 10*time.Second)
	}
	if err == nil {
		b, rerr := os.ReadFile(filepath.Join(skillsDir, "weekly-report", "SKILL.md"))
		if rerr != nil || !strings.Contains(string(b), "本周完成、进行中、下周计划") {
			err = fmt.Errorf("SKILL.md %q %v", b, rerr)
		}
	}
	r.step("技能建议卡片（收下 / 不要）：收下前不写文件，收下后写进 .dsh/skills/weekly-report/SKILL.md", err, card)
	if err != nil {
		return
	}
	var decline string
	err = waitFor(func() bool { decline = pageButton(page("skills"), "standup-notes", "decline:"); return decline != "" }, 10*time.Second)
	if err == nil {
		err = action("skills", decline, nil)
	}
	if err == nil {
		err = waitFor(func() bool { return !strings.Contains(page("skills"), "standup-notes") }, 10*time.Second)
	}
	if err == nil {
		if _, serr := os.Stat(filepath.Join(skillsDir, "standup-notes")); serr == nil {
			err = fmt.Errorf("a declined skill was written")
		} else if !strings.Contains(hostState(dataDir, `"declined"`), "standup-notes") {
			err = fmt.Errorf("the declined name is not kept")
		}
	}
	if err == nil {
		// Offered again by the engine: the host drops it.
		err = bot.proposeAgain(a, ctx, view)
	}
	r.step("在「技能」页拒绝另一个建议：不写文件、记下名字，引擎再提同名建议时不再出卡片", err, "")
}

// tidyModel scripts the daily router for TestDailyTidy.
type tidyModel struct {
	mu   sync.Mutex
	seen []string
	// models are the models asked, by kind of request: "chat" (the main
	// Session's) and "tidy" (a housekeeping Session's).
	models map[string]map[string]int
}

// housekeepingJob is the job text of a housekeeping Session's request (its
// first message is the job itself, not a candidate), "" for another one.
func housekeepingJob(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Messages) == 0 {
		return ""
	}
	first := req.Messages[0].Content
	var blocks []msgBlock
	if json.Unmarshal(first, &blocks) != nil {
		var s string
		if json.Unmarshal(first, &s) == nil {
			blocks = []msgBlock{{Type: "text", Text: s}}
		}
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.Contains(b.Text, "（闲时整理）") && !strings.Contains(b.Text, `"kind":"candidate"`) {
			return b.Text[strings.Index(b.Text, "（闲时整理）"):]
		}
	}
	return ""
}

// note records the model a request asked for.
func (m *tidyModel) note(body []byte, kind string) {
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.models == nil {
		m.models = map[string]map[string]int{}
	}
	if m.models[kind] == nil {
		m.models[kind] = map[string]int{}
	}
	m.models[kind][req.Model]++
}

// modelsUsed checks the housekeeping requests asked for tool alone and the
// main Session's for another model.
func (m *tidyModel) modelsUsed(tool string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := m.models["tidy"]; len(n) != 1 || n[tool] == 0 {
		return fmt.Errorf("the tidy asked for %v, want only %s", n, tool)
	}
	if n := m.models["chat"]; len(n) == 0 || n[tool] > 0 {
		return fmt.Errorf("the conversation asked for %v, want the main model", n)
	}
	return nil
}

func (m *tidyModel) modelSummary() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("对话请求的模型 %v，整理请求的模型 %v", m.models["chat"], m.models["tidy"])
}

func (m *tidyModel) saw(sub string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.seen {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// proposeAgain asks for standup-notes once more in a conversation turn and
// checks no card comes.
func (m *tidyModel) proposeAgain(a *app.App, ctx context.Context, view *inboxView) error {
	if err := a.Sidecars.InboxInput(ctx, dailyPlugin, "t-6", "再建议一次站会技能", time.Now()); err != nil {
		return err
	}
	if err := waitFor(func() bool { return view.has("又建议了一次") }, 90*time.Second); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	if n := strings.Count(strings.Join(view.texts(), "\n"), "standup-notes"); n > 1 {
		return fmt.Errorf("the declined skill was shown again (%d times)", n)
	}
	return nil
}

func (m *tidyModel) respond(body []byte) (*fakemodel.ToolCall, string) {
	m.mu.Lock()
	m.seen = append(m.seen, string(body))
	m.mu.Unlock()
	if job := housekeepingJob(body); job != "" {
		m.note(body, "tidy")
		return m.tidy(body)
	}
	m.note(body, "chat")
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "好"
	}
	var trigger, text string
	calls := 0
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
			case "tool_use":
				calls++
			}
		}
	}
	if trigger == "" {
		return nil, "好"
	}
	reply := func(s string) fakemodel.ToolCall {
		return fakemodel.ToolCall{Name: "orchestrator_reply", Input: map[string]any{"trigger_id": trigger, "purpose": "answer", "text": s}}
	}
	ignore := fakemodel.ToolCall{Name: "orchestrator_route", Input: map[string]any{"decisions": []any{map[string]any{"kind": "ignore", "trigger_id": trigger, "reason": "已直接回答"}}}}
	var plan []fakemodel.ToolCall
	switch {
	case strings.HasPrefix(text, "记住："):
		label, fact, _ := strings.Cut(strings.TrimPrefix(text, "记住："), "|")
		plan = []fakemodel.ToolCall{{Name: "memory_write", Input: map[string]any{"label": label, "text": fact, "trigger_id": trigger}}, reply("记下了：" + fact), ignore}
	case strings.HasPrefix(text, "写周报："):
		plan = []fakemodel.ToolCall{reply("周报：" + strings.TrimPrefix(text, "写周报：")), ignore}
	case strings.Contains(text, "再建议一次站会技能"):
		plan = []fakemodel.ToolCall{
			{Name: "skill_propose", Input: map[string]any{"name": "standup-notes", "description": "我让你整理站会记录时",
				"body": "按昨天、今天、阻碍三项列。", "why": "你常让我整理站会"}},
			reply("又建议了一次"), ignore}
	default:
		plan = []fakemodel.ToolCall{reply("收到：" + text), ignore}
	}
	if calls < len(plan) {
		c := plan[calls]
		return &c, ""
	}
	return nil, "好"
}

// tidy scripts a housekeeping Session: read the memories, tidy them,
// propose two skills, and end the turn with no message to anyone.
func (m *tidyModel) tidy(body []byte) (*fakemodel.ToolCall, string) {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &req)
	calls, memories := 0, ""
	for _, msg := range req.Messages {
		var blocks []msgBlock
		json.Unmarshal(msg.Content, &blocks)
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use":
				calls++
			case b.Type == "tool_result" && strings.Contains(string(b.Content), `memories`):
				memories = string(b.Content)
			}
		}
	}
	if calls == 0 {
		return &fakemodel.ToolCall{Name: "memory_read", Input: map[string]any{"limit": 50}}, ""
	}
	// A tool result's content is blocks on one route and a string on another.
	var res struct {
		Memories []struct{ ID, Label string } `json:"memories"`
	}
	var blocks []msgBlock
	var text string
	if json.Unmarshal([]byte(memories), &blocks) == nil {
		for _, b := range blocks {
			json.Unmarshal([]byte(b.Text), &res)
		}
	} else if json.Unmarshal([]byte(memories), &text) == nil {
		json.Unmarshal([]byte(text), &res)
	}
	if len(res.Memories) == 0 {
		return nil, "读不出记忆：" + memories
	}
	var coffee []any
	var checkup string
	for _, mm := range res.Memories {
		switch mm.Label {
		case "咖啡":
			coffee = append(coffee, mm.ID)
		case "体检":
			checkup = mm.ID
		}
	}
	plan := []fakemodel.ToolCall{
		{Name: "memory_read", Input: map[string]any{"limit": 50}},
		{Name: "memory_tidy", Input: map[string]any{
			"merges": []any{map[string]any{"ids": coffee, "label": "咖啡", "text": "美式咖啡，不加糖"}},
			"drops":  []any{map[string]any{"id": checkup, "reason": "11 月 1 日的体检已经过去"}},
		}},
		{Name: "skill_propose", Input: map[string]any{"name": "weekly-report", "description": "我让你写周报时",
			"body": "按本周完成、进行中、下周计划三段写，每段不超过 5 条。", "why": "你两次让我写周报，并要求分三段"}},
		{Name: "skill_propose", Input: map[string]any{"name": "standup-notes", "description": "我让你整理站会记录时",
			"body": "按昨天、今天、阻碍三项列。", "why": "你常让我整理站会"}},
	}
	if calls < len(plan) {
		c := plan[calls]
		return &c, ""
	}
	return nil, "整理完成：合并 1 组、删除 1 条、建议 2 个技能"
}
