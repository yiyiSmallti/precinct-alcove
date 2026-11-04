//go:build e2e

// The daily assistant's to-dos, reminder cards, persona, skills, web tools
// and Feishu channel, in a real Alcove core with real dsh and the packed
// @precinct/daily (DOVETAIL_DAILY_TGZ, as for TestDaily). Feishu is a fake
// lark-cli script: events are lines appended to a file, what the plugin
// sends is logged. Writes e2e/report/daily-more.md.
//
// Ways this can fail, each checked below:
//   - the new pages are not registered, or do not follow the engine
//     (todo.sync not declared or not handled);
//   - a to-do added or ticked off on the page is lost to the engine's next
//     sync, or never reaches the engine, so the model sees the old list;
//   - a due reminder is plain text without buttons, a snooze is not kept
//     (or not listed, or cannot be cancelled), or the card keeps its buttons;
//   - the persona never reaches the prompt, or a change made while the
//     engine runs (persona.set) waits for the next start;
//   - a new bot starts without the default persona, or it cannot be had
//     back after being cleared;
//   - SOUL.md saved on the page or edited by hand does not reach the next
//     turn, or an over-long one is taken (or cut) silently;
//   - a skill made on the page is not where the engine looks for skills;
//   - the main session has no web tools;
//   - Feishu takes messages from anyone, binds without the code, does not
//     answer in Feishu, or does not mirror reminders there.
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

// fakeLark is lark-cli for the Feishu channel: signed in as a bot in strict
// bot mode; event consume follows $LARK_EVENTS; im commands are logged to
// $LARK_SENT, one line of arguments each.
const fakeLark = `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"appId":"cli_e2e","identities":{"bot":{"status":"ready"}}}' ;;
"config strict-mode") echo bot ;;
"event consume")
  echo "[event] ready" >&2
  exec tail -n +1 -f "$LARK_EVENTS" ;;
im*) echo "$*" >> "$LARK_SENT"; echo '{"ok":true}' ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`

func TestDailyMore(t *testing.T) {
	src := os.Getenv("DOVETAIL_DAILY_TGZ")
	if src == "" {
		t.Skip("set DOVETAIL_DAILY_TGZ to a packed @precinct/daily")
	}
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "日常助手：待办、提醒卡片与稍后提醒、人设、技能、联网工具、飞书通道 e2e", file: "daily-more.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	bot := &moreModel{}
	model.Respond = bot.respond

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	view := &inboxView{items: map[string]map[string]any{}}
	rec := &recorder{ui: map[string]sidecar.Contribution{}}
	dataDir := filepath.Join(work, "data")
	events, sent := filepath.Join(work, "lark-events.ndjson"), filepath.Join(work, "lark-sent.log")
	os.WriteFile(events, nil, 0o644)
	cli := filepath.Join(work, "lark-cli")
	os.WriteFile(cli, []byte(fakeLark), 0o755)
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
	a.Sidecars.Env = []string{"ALCOVE_DATA_DIR=" + dataDir, "PRECINCT_DAILY_IDLE_AFTER=60s", "LARK_EVENTS=" + events, "LARK_SENT=" + sent}
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
	page := func(id string) string { return rec.raw(dailyPlugin + "#" + id) }
	larkSent := func() string { b, _ := os.ReadFile(sent); return string(b) }
	defer func() {
		if t.Failed() {
			rec.mu.Lock()
			logs := strings.Join(rec.logs, "\n")
			rec.mu.Unlock()
			t.Logf("sidecar log:\n%s\ninbox: %s\ntodos: %s\nreminders: %s\nchannels: %s\nlark sent:\n%s", logs, strings.Join(view.texts(), " | "),
				page("todos"), page("reminders"), page("channels"), larkSent())
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

	// 1. The new pages come with the handshake.
	err = nil
	for _, id := range []string{"todos", "persona", "skills", "channels"} {
		if !strings.Contains(page(id), `"type":"settings_page"`) {
			err = fmt.Errorf("page %s: %q", id, page(id))
		}
	}
	r.step("握手时登记「待办」「人设与风格」「技能」「消息通道」四页", err, "")
	if err != nil {
		return
	}
	soulFile := filepath.Join(dataDir, "bot", "SOUL.md")
	if b, rerr := os.ReadFile(soulFile); rerr != nil || !strings.Contains(string(b), "这是默认人设") {
		err = fmt.Errorf("SOUL.md %q %v", b, rerr)
	} else if !strings.Contains(page("persona"), "这是默认人设") || !strings.Contains(page("persona"), "恢复默认人设") {
		err = fmt.Errorf("the persona page: %q", page("persona"))
	}
	r.step("新 bot 带默认人设：首次启动写出 SOUL.md，「人设与风格」页显示它和「恢复默认人设」按钮", err, "")
	if err != nil {
		return
	}

	// 2. Before the engine first starts: a persona and a skill.
	err = action("persona", "submit", map[string]any{"name": "小日", "call": "老王", "tone": "warm", "length": "short", "emoji": false, "extra": ""})
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("intro"), "你好，我是小日") }, 10*time.Second)
	}
	r.step("在「人设与风格」页设名字和称呼，开场标题随之改成「你好，我是小日」", err, "")
	if err != nil {
		return
	}
	err = action("skills", "create", map[string]any{"name": "weekly-report", "when": "用于: 我让你写周报时", "how": "按本周完成、进行中、下周计划三段写", "path": ""})
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("skills"), "delete:weekly-report") }, 10*time.Second)
	}
	skillFile := filepath.Join(dataDir, "bot", ".dsh", "skills", "weekly-report", "SKILL.md")
	if err == nil {
		if b, rerr := os.ReadFile(skillFile); rerr != nil || !strings.HasPrefix(string(b), "---\nname: weekly-report\ndescription: \"用于: 我让你写周报时\"\n---") {
			err = fmt.Errorf("SKILL.md %q %v", b, rerr)
		}
	}
	r.step("在「技能」页新建技能：写成 bot 工作区 .dsh/skills/weekly-report/SKILL.md 并列在页上", err, "")
	if err != nil {
		return
	}

	// 3. The first turn: persona, skill and web tools reach the model.
	err = turn("in-1", "你好", "收到：你好")
	if err == nil {
		switch {
		case !bot.saw("称呼用户为「老王」"):
			err = fmt.Errorf("the persona is not in the prompt")
		case !bot.saw("这是默认人设"):
			err = fmt.Errorf("the default persona is not in the prompt")
		case !bot.saw("weekly-report") || !bot.saw("用于: 我让你写周报时"):
			err = fmt.Errorf("the skill (with its description) is not offered to the model")
		case !bot.saw(`"web_fetch"`) && !bot.saw(`"web_search"`):
			err = fmt.Errorf("the main session has no web tools")
		}
	}
	r.step("首个回合：模型请求里有人设（## Persona，含默认人设全文）、新技能 weekly-report 和联网工具", err, "")
	if err != nil {
		return
	}
	// Changed while the engine runs: persona.set.
	err = action("persona", "submit", map[string]any{"name": "小月", "call": "老王", "tone": "warm", "length": "short", "emoji": false, "extra": ""})
	if err == nil {
		err = waitFor(func() bool { return hostState(dataDir, `"personaPending"`) == "" }, 30*time.Second)
	}
	if err == nil {
		err = turn("in-2", "你好呀", "收到：你好呀")
	}
	if err == nil && !bot.sawLast("你叫「小月」") {
		err = fmt.Errorf("the changed persona did not reach the running engine")
	}
	r.step("引擎在跑时改人设（persona.set），下一个回合就用新名字「小月」", err, "")
	if err != nil {
		return
	}

	// The long persona, SOUL.md: saved on the page, then edited by hand.
	persona := map[string]any{"name": "小月", "call": "老王", "tone": "warm", "length": "short", "emoji": false, "extra": ""}
	withSoul := func(text string) map[string]any {
		m := map[string]any{"soul": text}
		for k, v := range persona {
			m[k] = v
		}
		return m
	}
	err = action("persona", "submit", withSoul("你说话带一点东北味儿，口头禅是「整挺好」。"))
	if err == nil {
		err = waitFor(func() bool { return hostState(dataDir, `"personaPending"`) == "" }, 30*time.Second)
	}
	if err == nil {
		if b, rerr := os.ReadFile(soulFile); rerr != nil || !strings.Contains(string(b), "整挺好") {
			err = fmt.Errorf("SOUL.md %q %v", b, rerr)
		}
	}
	if err == nil {
		err = turn("in-2s", "在吗", "收到：在吗")
	}
	if err == nil && !(bot.sawLast("SOUL.md") && bot.sawLast("整挺好") && bot.sawLast("你叫「小月」")) {
		err = fmt.Errorf("the page's SOUL.md is not in the next turn's prompt")
	}
	r.step("在「人设与风格」页写人设全文：存成 bot 工作区 SOUL.md，下一个回合的提示词里有它（和名字一起）", err, "")
	if err != nil {
		return
	}
	err = os.WriteFile(soulFile, []byte("你说话干脆利落，口头禅是「妥妥的」。\n"), 0o600)
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("persona"), "妥妥的") }, 15*time.Second)
	}
	if err == nil {
		err = waitFor(func() bool { return hostState(dataDir, `"personaPending"`) == "" }, 30*time.Second)
	}
	if err == nil {
		err = turn("in-2h", "还在吗", "收到：还在吗")
	}
	if err == nil && !(bot.sawLast("妥妥的") && !bot.sawLast("整挺好")) {
		err = fmt.Errorf("the hand-edited SOUL.md did not replace the old one in the prompt")
	}
	r.step("手改 SOUL.md：几秒内页面显示新内容，下一个回合的提示词换成新人设", err, "")
	if err != nil {
		return
	}
	err = action("persona", "submit", withSoul(strings.Repeat("长", 8001)))
	switch {
	case err == nil:
		err = fmt.Errorf("an 8001-character SOUL.md was accepted")
	case !strings.Contains(err.Error(), "最多 8000 个字"):
		err = fmt.Errorf("unexpected refusal: %v", err)
	default:
		err = nil
		if b, _ := os.ReadFile(soulFile); !strings.Contains(string(b), "妥妥的") {
			err = fmt.Errorf("SOUL.md changed by a refused save: %q", b)
		}
	}
	if err == nil {
		os.WriteFile(soulFile, []byte(strings.Repeat("长", 8001)), 0o600)
		err = waitFor(func() bool { return strings.Contains(page("persona"), "超过 8000 字上限") }, 15*time.Second)
	}
	if err == nil {
		err = action("persona", "submit", withSoul(""))
	}
	if err == nil {
		if _, serr := os.Stat(soulFile); !os.IsNotExist(serr) {
			err = fmt.Errorf("an empty save left SOUL.md: %v", serr)
		}
	}
	r.step("超过 8000 字：页面保存被拒、文件不变；手写超长时页面提示只用了前 8000 字；清空后删除 SOUL.md", err, "")
	if err != nil {
		return
	}
	err = action("persona", "default", withSoul(""))
	if err == nil {
		if b, rerr := os.ReadFile(soulFile); rerr != nil || !strings.Contains(string(b), "这是默认人设") {
			err = fmt.Errorf("SOUL.md %q %v", b, rerr)
		}
	}
	if err == nil {
		err = waitFor(func() bool { return hostState(dataDir, `"personaPending"`) == "" }, 30*time.Second)
	}
	if err == nil {
		err = turn("in-2d", "回来了", "收到：回来了")
	}
	if err == nil && !(bot.sawLast("这是默认人设") && bot.sawLast("你叫「小月」")) {
		err = fmt.Errorf("the restored default persona is not in the next turn's prompt")
	}
	r.step("「恢复默认人设」：SOUL.md 写回默认人设，下一个回合的提示词里有它（名字仍是「小月」）", err, "")
	if err != nil {
		return
	}

	// 4. To-dos: from the conversation and from the page.
	err = turn("in-3", "加个待办：周五前交报销单", "加好了")
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("todos"), "周五前交报销单") }, 30*time.Second)
	}
	r.step("对话里加的待办出现在「待办」页（todo_add → todo.sync）", err, "")
	if err != nil {
		return
	}
	err = action("todos", "add", map[string]any{"text": "买牛奶"})
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("todos"), "买牛奶") }, 10*time.Second)
	}
	var milk string
	if err == nil {
		milk = pageButton(page("todos"), "买牛奶", "done:")
		if milk == "" {
			err = fmt.Errorf("no done button for 买牛奶: %s", page("todos"))
		}
	}
	if err == nil {
		err = action("todos", milk, nil)
	}
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("todos"), "✓ 买牛奶") }, 10*time.Second)
	}
	if err == nil {
		err = waitFor(func() bool { return hostState(dataDir, `"removals"`) == "" }, 30*time.Second)
	}
	if err == nil && !strings.Contains(page("todos"), "✓ 买牛奶") {
		err = fmt.Errorf("the engine's sync undid the page's changes: %s", page("todos"))
	}
	r.step("在页上添加「买牛奶」并点完成：页面立即更新，引擎接下后同步也没有冲掉", err, milk)
	if err != nil {
		return
	}
	err = turn("in-4", "我有哪些待办？", "还没做完：")
	if err == nil && !view.has("还没做完：周五前交报销单") {
		err = fmt.Errorf("the model saw the wrong list: %s; todo_list gave %s", strings.Join(view.texts(), " | "), bot.lastTool)
	}
	r.step("再问待办：模型的 todo_list 只剩「周五前交报销单」（页上的完成已到引擎）", err, "")
	if err != nil {
		return
	}

	// 5. Feishu: bound by the code only, answers there.
	err = action("channels", "lark", map[string]any{"on": true, "mirror": true, "cli": cli})
	var code string
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(page("channels"), "已连接") }, 30*time.Second)
	}
	if err == nil {
		code = firstMatch(page("channels"), `绑定码 \d{6}`)
		if code == "" {
			err = fmt.Errorf("no pairing code: %s", page("channels"))
		}
		code = strings.TrimPrefix(code, "绑定码 ")
	}
	r.step("在「消息通道」页启用飞书：lark-cli 检查通过、事件连接就绪，页上显示 6 位绑定码", err, "绑定码 "+code)
	if err != nil {
		return
	}
	larkEvent := func(id, sender, text string) {
		b, _ := json.Marshal(map[string]any{"message_id": id, "chat_id": "oc_e2e", "chat_type": "p2p", "sender_id": sender, "sender_type": "user",
			"content": text, "create_time": fmt.Sprint(time.Now().UnixMilli())})
		f, _ := os.OpenFile(events, os.O_APPEND|os.O_WRONLY, 0o644)
		f.Write(append(b, '\n'))
		f.Close()
	}
	larkEvent("om_1", "ou_stranger", "你好")
	larkEvent("om_2", "ou_me", code)
	err = waitFor(func() bool { return strings.Contains(page("channels"), "已绑定") }, 20*time.Second)
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(larkSent(), "--message-id om_2") }, 20*time.Second)
	}
	if err == nil && (strings.Contains(larkSent(), "om_1") || bot.saw("ou_stranger")) {
		err = fmt.Errorf("a message from before binding was taken")
	}
	r.step("没发绑定码的消息被忽略；发了绑定码的人绑定成功，并在飞书里收到确认", err, "")
	if err != nil {
		return
	}
	larkEvent("om_3", "ou_me", "飞书里问个好")
	larkEvent("om_4", "ou_other", "我也想用")
	err = waitFor(func() bool {
		return strings.Contains(larkSent(), "--message-id om_3") && strings.Contains(larkSent(), "收到：飞书里问个好")
	}, 90*time.Second)
	if err == nil {
		time.Sleep(2 * time.Second)
		if strings.Contains(larkSent(), "om_4") || bot.saw("我也想用") {
			err = fmt.Errorf("a message from someone else was taken")
		}
	}
	r.step("绑定的人在飞书私聊：助手回到飞书原消息下；别人的私聊不进助手", err, "")
	if err != nil {
		return
	}

	// 6. A reminder card: snoozed, listed, cancelled; mirrored to Feishu.
	err = turn("in-5", "20 秒后提醒我喝水", "好的，到点提醒你喝水")
	var cardID string
	if err == nil {
		err = waitFor(func() bool { cardID = view.cardWith("⏰ 提醒", "喝水"); return cardID != "" }, 90*time.Second)
	}
	if err == nil && !strings.Contains(view.actions(cardID), "snooze:10m") {
		err = fmt.Errorf("card actions %s", view.actions(cardID))
	}
	if err == nil {
		err = waitFor(func() bool {
			return strings.Contains(larkSent(), "+messages-send") && strings.Contains(larkSent(), "喝水")
		}, 20*time.Second)
	}
	r.step("到点的提醒是带「完成 / 10 分钟后 / 1 小时后 / 明天早上」的卡片，同时发到飞书", err, cardID+"："+view.actions(cardID))
	if err != nil {
		return
	}
	err = a.Sidecars.UIAction(ctx, dailyPlugin, cardID, "snooze:10m", json.RawMessage(`{}`))
	if err == nil {
		err = waitFor(func() bool { return view.cardHas(cardID, "稍后提醒：") && view.actions(cardID) == "" }, 10*time.Second)
	}
	var unsnooze string
	if err == nil {
		err = waitFor(func() bool { unsnooze = firstMatch(page("reminders"), `unsnooze:[0-9a-f]+`); return unsnooze != "" }, 10*time.Second)
	}
	if err == nil {
		err = action("reminders", unsnooze, nil)
	}
	if err == nil {
		err = waitFor(func() bool { return !strings.Contains(page("reminders"), "unsnooze:") }, 10*time.Second)
	}
	r.step("点「10 分钟后」：卡片收起按钮写明稍后提醒的时间，「提醒与日程」页列出它，可以取消", err, unsnooze)
}

// pageButton returns the id of the button starting with prefix in the
// settings page section whose title has title.
func pageButton(raw, title, prefix string) string {
	var pg struct {
		Sections []struct {
			Title  string
			Fields []struct{ ID string }
		}
	}
	json.Unmarshal([]byte(raw), &pg)
	for _, sec := range pg.Sections {
		if strings.Contains(sec.Title, title) {
			for _, f := range sec.Fields {
				if strings.HasPrefix(f.ID, prefix) {
					return f.ID
				}
			}
		}
	}
	return ""
}

// cardWith returns the id of a card titled title whose body has sub.
func (v *inboxView) cardWith(title, sub string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, id := range v.order {
		if c, ok := v.items[id]["card"].(map[string]any); ok {
			if t, _ := c["title"].(string); t == title {
				if b, _ := c["body"].(string); strings.Contains(b, sub) {
					return id
				}
			}
		}
	}
	return ""
}

func (v *inboxView) cardHas(id, sub string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	c, _ := v.items[id]["card"].(map[string]any)
	b, _ := c["body"].(string)
	return strings.Contains(b, sub)
}

// actions lists a card's button ids.
func (v *inboxView) actions(id string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	c, _ := v.items[id]["card"].(map[string]any)
	acts, _ := c["actions"].([]any)
	var ids []string
	for _, x := range acts {
		if m, ok := x.(map[string]any); ok {
			s, _ := m["id"].(string)
			ids = append(ids, s)
		}
	}
	return strings.Join(ids, ",")
}

// moreModel scripts the daily router for TestDailyMore and keeps every
// request it got.
type moreModel struct {
	mu       sync.Mutex
	seen     []string
	lastTool string // the last tool result it read
}

func (m *moreModel) saw(sub string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.seen {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// sawLast reports whether the last request had sub.
func (m *moreModel) sawLast(sub string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen) > 0 && strings.Contains(m.seen[len(m.seen)-1], sub)
}

func (m *moreModel) respond(body []byte) (*fakemodel.ToolCall, string) {
	m.mu.Lock()
	m.seen = append(m.seen, string(body))
	m.mu.Unlock()
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "好"
	}
	var trigger, text, todos string
	calls, sinceTodos := 0, 0
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
						trigger, calls, todos = id, 0, ""
						text, _ = raw["text"].(string)
					}
				}
			case "tool_use":
				calls++
				sinceTodos++
			case "tool_result":
				if strings.Contains(string(b.Content), `todos`) {
					todos, sinceTodos = string(b.Content), 0
				}
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
	case strings.HasPrefix(text, "加个待办："):
		plan = []fakemodel.ToolCall{{Name: "todo_add", Input: map[string]any{"text": strings.TrimPrefix(text, "加个待办："), "trigger_id": trigger}}, reply("加好了。"), ignore}
	case strings.Contains(text, "哪些待办"):
		if todos == "" {
			if calls > 0 {
				return nil, "好"
			}
			return &fakemodel.ToolCall{Name: "todo_list", Input: map[string]any{}}, ""
		}
		var blocks []msgBlock
		json.Unmarshal([]byte(todos), &blocks)
		var res struct {
			Todos []struct{ Text, DoneAt string } `json:"todos"`
		}
		for _, b := range blocks {
			json.Unmarshal([]byte(b.Text), &res)
		}
		var open []string
		for _, t := range res.Todos {
			if t.DoneAt == "" {
				open = append(open, t.Text)
			}
		}
		m.mu.Lock()
		m.lastTool = todos
		m.mu.Unlock()
		plan = []fakemodel.ToolCall{reply("还没做完：" + strings.Join(open, "、")), ignore}
		calls = sinceTodos
	case strings.Contains(text, "提醒我喝水"):
		at := time.Now().UTC().Add(20 * time.Second).Format("2006-01-02T15:04:05+00:00")
		plan = []fakemodel.ToolCall{
			{Name: "schedule_create", Input: map[string]any{"at": at, "timezone": "UTC", "kind": "notify", "text": "喝水", "trigger_id": trigger}},
			reply("好的，到点提醒你喝水。"), ignore}
	default:
		plan = []fakemodel.ToolCall{reply("收到：" + text), ignore}
	}
	if calls < len(plan) {
		c := plan[calls]
		return &c, ""
	}
	return nil, "好"
}
