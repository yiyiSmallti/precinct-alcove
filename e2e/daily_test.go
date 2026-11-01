//go:build e2e

// D2 (Dovetail PRD): the @precinct/daily plugin in a real Alcove core with
// real dsh. Installs the signed package (DOVETAIL_DAILY_TGZ, built by
// precinct-dovetail's plugins/alcove/daily/build.sh and `dovetail pack`),
// then: three conversation turns → a reminder two minutes out → idle exit of
// the daily turn process → the reminder shown by the sidecar alone → one
// more turn that still sees what was remembered. A scripted model stands in
// for the LLM and drives the engine's tools. Writes e2e/report/daily.md.
// DAILY_E2E_MODEL_LOG=<file> appends each model request, one per line.
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

const dailyPlugin = "@precinct/daily"

// inboxView records what the sidecar posts to the conversation view.
type inboxView struct {
	mu    sync.Mutex
	order []string
	items map[string]map[string]any
}

func (v *inboxView) post(plugin string, raw json.RawMessage) error {
	var it map[string]any
	if err := json.Unmarshal(raw, &it); err != nil {
		return err
	}
	id, _ := it["id"].(string)
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.items[id]; !ok {
		v.order = append(v.order, id)
	}
	v.items[id] = it
	return nil
}

func (v *inboxView) texts() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, id := range v.order {
		it := v.items[id]
		if t, _ := it["text"].(string); t != "" {
			out = append(out, t)
		} else if c, ok := it["card"].(map[string]any); ok {
			b, _ := c["body"].(string)
			out = append(out, b)
		}
	}
	return out
}

func (v *inboxView) has(sub string) bool {
	for _, t := range v.texts() {
		if strings.Contains(t, sub) {
			return true
		}
	}
	return false
}

func TestDaily(t *testing.T) {
	src := os.Getenv("DOVETAIL_DAILY_TGZ")
	if src == "" {
		t.Skip("set DOVETAIL_DAILY_TGZ to a packed @precinct/daily")
	}
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "D2 日常 bot e2e：daily 插件在真 Alcove 核心与 dsh 里跑通（Dovetail PRD D2）", file: "daily.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	bot := &dailyModel{t: t}
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
	r.step("启动 Alcove 核心", err, "")
	if err != nil {
		return
	}
	defer a.Close()
	a.Sidecars.Services.Log = rec.log
	a.Sidecars.Backoff = 100 * time.Millisecond
	a.Sidecars.Env = []string{"ALCOVE_DATA_DIR=" + dataDir, "PRECINCT_DAILY_IDLE_AFTER=10s"}
	_, err = a.Creds.Add("假模型", "deepseek-official", model.URL, "sk-e2e-daily")
	r.step("录入凭据（假模型）", err, "")

	tgz := filepath.Join(work, filepath.Base(src))
	if err == nil {
		err = copyFile(src, tgz)
	}
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
		err = profileHas(a, "daily", "@deepseek-ai/dsh-sdk-app", dailyPlugin)
	}
	if err == nil {
		err = waitFor(func() bool { st, ok := a.Sidecars.Status(dailyPlugin); return ok && st.State == sidecar.Running }, 30*time.Second)
	}
	st, _ := a.Sidecars.Status(dailyPlugin)
	r.step("安装 daily：建 daily profile、装包、边车握手", err, fmt.Sprintf("%+v", st))
	if err != nil {
		rec.mu.Lock()
		t.Logf("sidecar log:\n%s", strings.Join(rec.logs, "\n"))
		rec.mu.Unlock()
		return
	}

	sidecarLog := func() string {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return strings.Join(rec.logs, "\n")
	}
	defer func() {
		if t.Failed() {
			t.Logf("sidecar log:\n%s\ninbox: %s", sidecarLog(), strings.Join(view.texts(), " | "))
		}
	}()
	turn := func(n int, id, text, want string) bool {
		err := a.Sidecars.InboxInput(ctx, dailyPlugin, id, text, time.Now())
		if err == nil {
			err = waitFor(func() bool { return view.has(want) }, 90*time.Second)
		}
		r.step(fmt.Sprintf("第 %d 轮：%s", n, text), err, "回复含「"+want+"」")
		return err == nil
	}
	if !turn(1, "in-1", "你好", "你好，我在") ||
		!turn(2, "in-2", "记住我喜欢喝美式", "记住了") ||
		!turn(3, "in-3", "两分钟后提醒我喝水", "两分钟后提醒你") {
		return
	}
	asked := time.Now()

	err = waitFor(func() bool { return strings.Contains(sidecarLog(), "exited after idle") }, 60*time.Second)
	r.step("回合进程空闲退出（PRECINCT_DAILY_IDLE_AFTER=10s）", err, "")
	if err != nil {
		return
	}
	// The reminder must arrive after the turn process is gone and near its
	// deadline (two minutes after it was asked for), not straight away.
	if view.has("该喝水了") {
		r.step("提醒按时出现，由边车自己显示，不拉起回合进程", fmt.Errorf("the reminder arrived %s after it was asked for, before the turn process went idle", time.Since(asked).Round(time.Second)), "")
		return
	}
	launches := strings.Count(sidecarLog(), "turn process attached")
	err = waitFor(func() bool { return view.has("该喝水了") }, 3*time.Minute)
	took := time.Since(asked)
	if err == nil && (took < 110*time.Second || took > 150*time.Second) {
		err = fmt.Errorf("the reminder arrived %s after it was asked for, want about 2m", took.Round(time.Second))
	}
	if err == nil && strings.Count(sidecarLog(), "turn process attached") != launches {
		err = fmt.Errorf("the turn process was started again for the reminder")
	}
	r.step("提醒按时出现，由边车自己显示，不拉起回合进程", err, fmt.Sprintf("创建后 %s 出现", took.Round(time.Second)))
	if err != nil {
		return
	}
	if turn(4, "in-4", "我喜欢喝什么？", "来自记忆") {
		r.step("记忆跨进程保留", nil, "回合进程重新启动后 memory_read 读到「美式」")
	}
}

// dailyModel scripts the router: for the newest candidate card it makes the
// calls of that message's plan one by one, then ends the turn with text.
type dailyModel struct {
	t  *testing.T
	mu sync.Mutex
}

type msgBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

func (m *dailyModel) respond(body []byte) (*fakemodel.ToolCall, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f := os.Getenv("DAILY_E2E_MODEL_LOG"); f != "" {
		if w, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			w.Write(append(body, '\n'))
			w.Close()
		}
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		m.t.Logf("model: bad request: %v", err)
		return nil, "好"
	}
	var trigger, text, lastResult string
	calls := 0
	for _, msg := range req.Messages {
		var blocks []msgBlock
		if json.Unmarshal(msg.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				var card struct{ Kind, TriggerID, Text string }
				var raw map[string]any
				if strings.Contains(b.Text, `"kind":"candidate"`) && json.Unmarshal([]byte(b.Text), &raw) == nil {
					card.TriggerID, _ = raw["trigger_id"].(string)
					card.Text, _ = raw["text"].(string)
					if card.TriggerID != "" && card.TriggerID != trigger {
						trigger, text, calls = card.TriggerID, card.Text, 0
					}
				}
			case "tool_use":
				if trigger != "" {
					calls++
				}
			case "tool_result":
				lastResult = string(b.Content)
				if b.IsError {
					m.t.Logf("model: tool error: %s", lastResult)
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
	route := fakemodel.ToolCall{Name: "orchestrator_route", Input: map[string]any{"decisions": []any{map[string]any{"kind": "ignore", "trigger_id": trigger, "reason": "已直接回答，不需要建工单"}}}}
	var plan []fakemodel.ToolCall
	switch {
	case strings.Contains(text, "你好"):
		plan = []fakemodel.ToolCall{reply("你好，我在。"), route}
	case strings.HasPrefix(text, "记住"):
		plan = []fakemodel.ToolCall{
			{Name: "memory_write", Input: map[string]any{"label": "饮品偏好", "text": "喜欢喝美式", "trigger_id": trigger}},
			reply("记住了：你喜欢喝美式。"), route}
	case strings.Contains(text, "提醒"):
		at := time.Now().UTC().Add(2 * time.Minute).Format("2006-01-02T15:04:05+00:00")
		plan = []fakemodel.ToolCall{
			{Name: "schedule_create", Input: map[string]any{"at": at, "timezone": "UTC", "kind": "notify", "text": "该喝水了", "trigger_id": trigger}},
			reply("好的，两分钟后提醒你喝水。"), route}
	case strings.Contains(text, "喜欢喝什么"):
		plan = []fakemodel.ToolCall{{Name: "memory_read", Input: map[string]any{"query": "喜欢喝", "limit": 5}}}
		if calls >= 1 {
			answer := "我不记得了。"
			if strings.Contains(lastResult, "美式") {
				answer = "你喜欢喝美式（来自记忆）。"
			}
			plan = append(plan, reply(answer), route)
		}
	}
	if calls < len(plan) {
		c := plan[calls]
		return &c, ""
	}
	return nil, "好"
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
