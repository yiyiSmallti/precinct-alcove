//go:build e2e

// Learning about the person: the daily assistant's idle tidy also reads
// what the person did in their personal workspaces (personal.history) and
// crystallizes it: work preferences go straight into the personal
// context's 关于我 (personal.setProfile), which every personal workspace's
// agent reads, and can be taken out again; a change of how the bot talks
// and a timed task wait for the person to take them from a card; a skill
// taken is also given to the personal workspaces. Real Alcove core, real
// dsh and the packed @precinct/daily (DOVETAIL_DAILY_TGZ); the model is
// scripted. Writes e2e/report/daily-learn.md.
//
// Ways this can fail, each checked below:
//   - the tidy only runs after the person talked to the bot, so work done
//     only in personal workspaces is never learned from;
//   - the tidy hands the engine a plugin's tasks, or nothing of the
//     workspaces at all;
//   - profile entries stay in the engine and never reach 关于我, or a
//     removed one stays there, or one brought back does not return;
//   - a persona change or a timed task applies without the person taking
//     it, or does nothing when taken;
//   - a taken skill stays the bot's own and the workspaces never get it;
//   - the learning turn posts a message in the conversation.
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
	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

func TestDailyLearn(t *testing.T) {
	src := os.Getenv("DOVETAIL_DAILY_TGZ")
	if src == "" {
		t.Skip("set DOVETAIL_DAILY_TGZ to a packed @precinct/daily")
	}
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "日常助手：从个人工作区学习用户并结晶（画像、人设、定时任务、技能）e2e", file: "daily-learn.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	bot := &learnModel{}
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

	// What the person did in a personal workspace before: two conversations
	// they started, and one a plugin ran there.
	wsDir := filepath.Join(work, "proj")
	os.MkdirAll(wsDir, 0o755)
	w, err := a.CreateWorkspace(ctx, "proj", wsDir)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(id, source string, at time.Time, user, answer string) {
		if err := a.Tasks.Put(w.ID, tasks.Task{ID: id, Title: id, Source: source, CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
		snap := events.Snapshot{Title: id, Entries: []events.Entry{{Kind: events.User, Text: user}, {Kind: events.Assistant, Text: answer}}}
		if err := a.Tasks.SaveTranscript(w.ID, id, snap); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now().Add(-time.Hour)
	seed("chat-1", "", t0, "LEARN-SEED-1 别铺垫，先给结论再说细节", "好，先说结论")
	seed("chat-2", "", t0.Add(time.Minute), "LEARN-SEED-2 又来了，先给结论；代码注释用英文", "明白")
	seed("chat-plugin", "@someone/else", t0.Add(2*time.Minute), "PLUGIN-RAN", "x")
	// 关于我 the person wrote before installing: learning adds its own
	// section and leaves this as it is.
	const ownRule = "USER-OWN-RULE 提交信息用英文"
	if err := a.Personal.SetProfile("# 我的约定\n- " + ownRule); err != nil {
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
	page := func(id string) string { return rec.raw(dailyPlugin + "#" + id) }
	if err == nil {
		err = waitFor(func() bool {
			return strings.Contains(page("memory"), `"label":"学习我的习惯","type":"toggle","value":true`) &&
				strings.Contains(page("memory"), `"label":"也读个人工作区里的对话","type":"toggle","value":true`)
		}, 10*time.Second)
	}
	r.step("安装 daily（声明 personal_context、personal_history）并握手；「记忆」页有「学习你的习惯」，两个开关默认打开", err, "")
	if err != nil {
		return
	}
	defer func() {
		if t.Failed() {
			rec.mu.Lock()
			logs := strings.Join(rec.logs, "\n")
			rec.mu.Unlock()
			t.Logf("sidecar log:\n%s\ninbox: %s\nmemory: %s\ntool errors: %s", logs, strings.Join(view.texts(), " | "), page("memory"), bot.errors())
		}
	}()
	action := func(contribution, act string, values map[string]any) error {
		payload, _ := json.Marshal(map[string]any{"values": values})
		return a.Sidecars.UIAction(ctx, dailyPlugin, contribution, act, payload)
	}
	profileFile := filepath.Join(a.Layout.PersonalHome(), "AGENTS.md")
	profile := func() string { b, _ := os.ReadFile(profileFile); return string(b) }

	// 1. Never talked to the bot: the tidy still comes, for the workspaces.
	start := time.Now()
	var trigger string
	err = waitFor(func() bool { trigger = bot.trigger(); return trigger != "" }, quiet+60*time.Second)
	waited := time.Since(start).Round(time.Second)
	switch {
	case err != nil:
	case !strings.Contains(trigger, "（学习）") || !strings.Contains(trigger, "个人工作区对话摘录") || !strings.Contains(trigger, "LEARN-SEED-1") || !strings.Contains(trigger, "LEARN-SEED-2"):
		err = fmt.Errorf("the tidy has no learning step or no conversations: %s", trigger)
	case strings.Contains(trigger, "PLUGIN-RAN"):
		err = fmt.Errorf("a plugin's task was handed over: %s", trigger)
	case strings.Index(trigger, "LEARN-SEED-1") > strings.Index(trigger, "LEARN-SEED-2"):
		err = fmt.Errorf("conversations not oldest first")
	case bot.model() != "deepseek-v4-flash":
		err = fmt.Errorf("with no tool model the tidy asked for %q, want the main model", bot.model())
	}
	r.step("没和助手聊过天，只在个人工作区里干过活：安静一段时间后照样整理，交给引擎的是这两段对话（旧的在前），插件跑的任务不在里面；没选工具模型时整理用主模型", err, fmt.Sprintf("安静 %s 后开始，整理用的模型 %s", waited, bot.model()))
	if err != nil {
		return
	}

	// 2. Profile entries apply at once: the page lists them, 关于我 has them.
	err = waitFor(func() bool {
		m := page("memory")
		return strings.Contains(m, "关于我 · 回答先给结论，再说细节") && strings.Contains(m, "关于我 · 代码注释用英文")
	}, 60*time.Second)
	if err == nil {
		err = waitFor(func() bool {
			p := profile()
			return strings.Contains(p, "回答先给结论，再说细节") && strings.Contains(p, "代码注释用英文") && strings.Contains(p, ownRule)
		}, 20*time.Second)
		if err != nil {
			err = fmt.Errorf("关于我 is %q: %v", profile(), err)
		}
	}
	if err == nil && view.has("学习完成") {
		err = fmt.Errorf("the learning turn posted a message")
	}
	r.step("学到的两条工作偏好立即生效：「记忆」页列为「关于我」条目，个人上下文的 AGENTS.md 里有这两条，用户自己原来写的内容还在；对话里不出消息", err, profile())
	if err != nil {
		return
	}

	// 3. Taking an entry out, and bringing it back.
	off := pageButton(page("memory"), "关于我 · 代码注释用英文", "profile-off:")
	err = action("memory", off, nil)
	if err == nil {
		err = waitFor(func() bool {
			return !strings.Contains(profile(), "代码注释用英文") && strings.Contains(profile(), "先给结论") && strings.Contains(profile(), ownRule) &&
				pageButton(page("memory"), "已删除 · 代码注释用英文", "profile-on:") != ""
		}, 20*time.Second)
	}
	if err == nil {
		// The engine took it: a profile.sync after that still has it removed.
		err = waitFor(func() bool { return hostState(dataDir, `"removals"`) == "" }, 20*time.Second)
	}
	removed := profile()
	if err == nil {
		err = action("memory", pageButton(page("memory"), "已删除 · 代码注释用英文", "profile-on:"), nil)
	}
	if err == nil {
		err = waitFor(func() bool {
			return strings.Contains(profile(), "代码注释用英文") && pageButton(page("memory"), "关于我 · 代码注释用英文", "profile-off:") != ""
		}, 20*time.Second)
	}
	r.step("在「记忆」页删掉一条：AGENTS.md 里随即没有它、引擎也收到了，页面上留「恢复」；点恢复后又回到 AGENTS.md", err, removed)
	if err != nil {
		return
	}

	// 4. A persona change waits for 收下, then goes into SOUL.md.
	soulFile := filepath.Join(dataDir, "bot", "SOUL.md")
	var persona string
	err = waitFor(func() bool {
		persona = view.cardWith("💡 说话方式建议", "少用感叹号")
		return persona != ""
	}, 10*time.Second)
	if err == nil && view.actions(persona) != "take,decline" {
		err = fmt.Errorf("card actions %q", view.actions(persona))
	}
	if err == nil {
		if b, _ := os.ReadFile(soulFile); strings.Contains(string(b), "少用感叹号") {
			err = fmt.Errorf("SOUL.md changed before it was taken")
		}
	}
	if err == nil {
		err = a.Sidecars.UIAction(ctx, dailyPlugin, persona, "take", json.RawMessage(`{}`))
	}
	if err == nil {
		err = waitFor(func() bool { return view.cardHas(persona, "已收下") && view.actions(persona) == "" }, 10*time.Second)
	}
	soul, _ := os.ReadFile(soulFile)
	if err == nil && !strings.Contains(string(soul), "## 从相处中学到的说话方式\n- 少用感叹号") {
		err = fmt.Errorf("SOUL.md: %q", soul)
	}
	r.step("说话方式建议卡片（收下 / 不要）：收下前 SOUL.md 不变，收下后加进「从相处中学到的说话方式」", err, "")
	if err != nil {
		return
	}

	// 5. A timed task waits too; taken from the 记忆 page it is a schedule.
	var take string
	err = waitFor(func() bool {
		take = pageButton(page("memory"), "💡 定时任务建议", "learn-take:")
		return take != ""
	}, 10*time.Second)
	if err == nil && strings.Contains(page("reminders"), "整理昨天的提交") {
		err = fmt.Errorf("the schedule exists before it was taken")
	}
	if err == nil {
		err = action("memory", take, nil)
	}
	if err == nil {
		err = waitFor(func() bool {
			rem := page("reminders")
			return strings.Contains(rem, "从使用习惯里学到、经你确认的定时任务") && strings.Contains(rem, "每天") &&
				pageButton(rem, "从使用习惯里学到", "cancel:sch-auto-") != ""
		}, 30*time.Second)
	}
	if err == nil && pageButton(page("memory"), "💡 定时任务建议", "learn-take:") != "" {
		err = fmt.Errorf("the proposal is still on the 记忆 page")
	}
	r.step("定时任务建议：收下前没有日程；在「记忆」页收下后，「提醒与日程」页出现每天重复的「到点让我处理」任务，可以取消", err, "")
	if err != nil {
		return
	}

	// 6. A skill learned from the workspaces: taken, both the bot and the
	// personal workspaces have it.
	var card string
	err = waitFor(func() bool { card = view.cardWith("💡 技能建议", "release-notes"); return card != "" }, 10*time.Second)
	if err == nil {
		err = a.Sidecars.UIAction(ctx, dailyPlugin, card, "take", json.RawMessage(`{}`))
	}
	shared := filepath.Join(a.Layout.PersonalHome(), "skills", "release-notes", "SKILL.md")
	if err == nil {
		err = waitFor(func() bool {
			b, _ := os.ReadFile(shared)
			_, own := os.Stat(filepath.Join(dataDir, "bot", ".dsh", "skills", "release-notes", "SKILL.md"))
			return strings.Contains(string(b), "按新增、修复、注意三段") && own == nil
		}, 20*time.Second)
	}
	r.step("从个人工作区学到的技能：收下后助手自己有，个人上下文的 skills/release-notes 里也有，个人工作区都能用", err, "")
	if err != nil {
		return
	}

	// 7. Deleted on the 技能 page: the personal context's copy goes too,
	// and a skill daily did not write there stays.
	other := filepath.Join(a.Layout.PersonalHome(), "skills", "my-own", "SKILL.md")
	os.MkdirAll(filepath.Dir(other), 0o755)
	err = os.WriteFile(other, []byte("---\nname: my-own\ndescription: mine\n---\nmine\n"), 0o644)
	if err == nil {
		err = a.Sidecars.UIAction(ctx, dailyPlugin, "skills", "delete:release-notes", json.RawMessage(`{"values":{}}`))
	}
	if err == nil {
		err = waitFor(func() bool {
			_, shared := os.Stat(filepath.Join(a.Layout.PersonalHome(), "skills", "release-notes"))
			_, own := os.Stat(filepath.Join(dataDir, "bot", ".dsh", "skills", "release-notes"))
			return os.IsNotExist(shared) && os.IsNotExist(own)
		}, 20*time.Second)
	}
	if _, serr := os.Stat(other); err == nil && serr != nil {
		err = fmt.Errorf("a skill daily did not write was removed: %v", serr)
	}
	r.step("在「技能」页删掉学到的技能：助手自己的和个人上下文里的副本都删掉，别的个人技能不动", err, "")

	err = nil
	if e := bot.errors(); e != "" {
		err = fmt.Errorf("tool errors: %s", e)
	}
	r.step("整个过程中引擎的学习工具没有报错", err, "")
}

// learnModel scripts the housekeeping Session of TestDailyLearn; the main
// Session only answers.
type learnModel struct {
	mu    sync.Mutex
	tidy  string   // the tidy's text, once it came
	asked string   // the model the tidy asked for
	errs  []string // tool errors seen
}

func (m *learnModel) trigger() string { m.mu.Lock(); defer m.mu.Unlock(); return m.tidy }
func (m *learnModel) model() string   { m.mu.Lock(); defer m.mu.Unlock(); return m.asked }
func (m *learnModel) errors() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.errs, " | ")
}

func (m *learnModel) respond(body []byte) (*fakemodel.ToolCall, string) {
	job := housekeepingJob(body)
	if job == "" {
		return nil, "好"
	}
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &req)
	calls := 0
	for _, msg := range req.Messages {
		var blocks []msgBlock
		json.Unmarshal(msg.Content, &blocks)
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				calls++
			case "tool_result":
				if b.IsError {
					m.mu.Lock()
					m.errs = append(m.errs, string(b.Content))
					m.mu.Unlock()
				}
			}
		}
	}
	m.mu.Lock()
	if m.tidy == "" {
		m.tidy, m.asked = job, req.Model
	}
	m.mu.Unlock()
	at := time.Now().Add(24 * time.Hour).Truncate(time.Minute).Format(time.RFC3339)
	plan := []fakemodel.ToolCall{
		{Name: "profile_note", Input: map[string]any{"add": []any{
			map[string]any{"text": "回答先给结论，再说细节", "why": "个人工作区里两次要求先给结论"},
			map[string]any{"text": "代码注释用英文", "why": "个人工作区里要求过"},
		}}},
		{Name: "persona_propose", Input: map[string]any{"change": "少用感叹号，语气平实", "why": "你几次说我太夸张"}},
		{Name: "automation_propose", Input: map[string]any{"text": "整理昨天的提交，列出要跟进的事", "at": at, "timezone": "Asia/Shanghai",
			"recurrence": "FREQ=DAILY", "why": "你每天早上都让我整理昨天的提交"}},
		{Name: "skill_propose", Input: map[string]any{"name": "release-notes", "description": "我让你写发版说明时",
			"body": "按新增、修复、注意三段写，每条一句。", "why": "你在个人工作区里两次这样写发版说明"}},
	}
	if calls < len(plan) {
		c := plan[calls]
		return &c, ""
	}
	return nil, "学习完成：画像 2 条、人设 1、定时任务 1、技能 1"
}
