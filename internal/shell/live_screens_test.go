package shell

import (
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// TestLiveScreens draws a task while it is still being written (reasoning
// streaming, a call whose arguments the model is writing, a command's live
// output, a compaction's summary as it is written) and once all of it is
// done, into $ALCOVE_SCREENS, for reviewing the look.
func TestLiveScreens(t *testing.T) {
	dir := os.Getenv("ALCOVE_SCREENS")
	if dir == "" {
		t.Skip("set ALCOVE_SCREENS to a folder")
	}
	os.MkdirAll(dir, 0o755)
	for _, dark := range []bool{false, true} {
		s, tt := newShell(t)
		tt.SetDark(dark)
		tt.SetScale(2)
		tt.SetSize(1280, 900)
		tt.SetTitleBar(ui.TitleBar{Height: 38, Left: 78})
		v := demo(t, s)
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		shot := func(name string) {
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
		live := func(data map[string]any) {
			data["sessionId"] = "chat-live"
			b, _ := json.Marshal(data)
			s.OnEvent("rt1", alrt.Event{Method: "session.live", Params: b})
		}
		chunk := func(step, index int, c map[string]any) {
			c["index"] = index
			live(map[string]any{"kind": "chunk", "turn": 1, "step": step, "attempt": "a1", "chunk": c})
		}

		r := &ev{sid: "chat-live"}
		r.status(s, "running")
		r.user(s, "把 logs/ 里这周的报错按模块汇总，写成 report/errors.md")
		r.emit(s, "assistant/message", map[string]any{"turn": 1, "step": 1, "message": map[string]any{"role": "assistant", "content": []map[string]string{
			{"type": "reasoning", "text": "用户要按模块汇总这周的报错。\n先列出 logs/ 下的文件，看日志格式。\n日志大概是 JSON 行，每行有 module 和 level 字段。\n按 module 分组，统计 error 的条数和最常见的消息。\n最后写成 Markdown 表格。"},
			{"type": "text", "text": "先跑一遍统计脚本，看看各模块的报错数量。"},
		}}})
		r.emit(s, "tool/call", map[string]string{"callId": "c1", "name": "bash", "arguments": `{"command":"python3 scripts/count_errors.py logs/","description":"统计报错"}`})
		live(map[string]any{"kind": "tool-output", "callId": "c1", "text": "\x1b[32mscanning\x1b[0m logs/2026-11-17.jsonl\nscanning logs/2026-11-18.jsonl\nscanning logs/2026-11-19.jsonl\nprogress  10%\rprogress  40%\rprogress  70%"})
		r.emit(s, "compaction/start", map[string]any{"compactionId": "k1", "turn": 1})
		live(map[string]any{"kind": "compaction", "text": "## 目标\n- 按模块汇总本周 logs/ 的报错，写入 report/errors.md\n## 进展\n- 已确认日志为 JSON 行，含 module、level"})
		chunk(2, 0, map[string]any{"type": "reasoning-delta", "text": "统计脚本还在跑。\n趁这时候先把报告的骨架写出来，"})
		chunk(2, 0, map[string]any{"type": "reasoning-delta", "text": "表头是模块、报错数、最常见的消息。"})
		chunk(2, 1, map[string]any{"type": "tool-call-delta", "id": "c2", "name": "write", "argumentsDelta": `{"file_path":"report/errors.md","content":"# 本周报错汇总\n\n| 模块 | 报错数 | 最常见的消息 |\n| --- | --- | --- |\n| gateway | `})
		s.mu.Lock()
		s.page, v.current = pageWS+v.ws.ID, "chat-live"
		s.mu.Unlock()
		shot("live")

		// Everything settles.
		r.emit(s, "tool/result", map[string]any{"message": map[string]any{"toolCallId": "c1", "content": []map[string]string{{"type": "text", "text": "gateway 41\nscheduler 12\nauth 3"}}}})
		r.emit(s, "compaction/summary", map[string]any{"compactionId": "k1", "summary": []map[string]string{{"type": "text", "text": "## 目标\n- 按模块汇总本周 logs/ 的报错，写入 report/errors.md\n## 进展\n- 已确认日志为 JSON 行，含 module、level\n- 统计：gateway 41、scheduler 12、auth 3\n## 下一步\n- 写报告"}}})
		r.emit(s, "compaction/end", map[string]any{"compactionId": "k1", "turn": 1})
		content := "# 本周报错汇总\n\n| 模块 | 报错数 | 最常见的消息 |\n| --- | --- | --- |\n| gateway | 41 | upstream timeout |\n| scheduler | 12 | lock held |\n| auth | 3 | token expired |\n"
		args, _ := json.Marshal(map[string]string{"file_path": "report/errors.md", "content": content})
		r.emit(s, "assistant/message", map[string]any{"turn": 1, "step": 2, "message": map[string]any{"role": "assistant", "content": []map[string]string{
			{"type": "reasoning", "text": "统计脚本还在跑。\n趁这时候先把报告的骨架写出来，表头是模块、报错数、最常见的消息。"},
			{"type": "tool-call", "id": "c2", "name": "write", "arguments": string(args)},
		}}})
		r.emit(s, "tool/result", map[string]any{"message": map[string]any{"toolCallId": "c2", "content": []map[string]string{{"type": "text", "text": "Created file"}}}})
		r.say(s, 1, 3, "写好了：`report/errors.md`，gateway 的报错最多（41 条，多为上游超时）。")
		r.emit(s, "turn/end", map[string]any{"turn": 1, "reason": map[string]string{"kind": "completed"}})
		r.status(s, "idle")
		shot("live-done")
		// The reasoning and the compaction opened.
		s.mu.Lock()
		tk := v.tasks["chat-live"]
		for _, e := range tk.session.Entries {
			if e.Kind == "reasoning" || e.Kind == "compaction" {
				tk.open[e.Key] = true
			}
		}
		n := len(tk.session.Entries)
		s.mu.Unlock()
		shot("live-open")
		if n != 8 {
			t.Errorf("%d entries: %s", n, fmt.Sprint(kindsOf(tk)))
		}
	}
}

func kindsOf(tk *taskView) []string {
	var out []string
	for _, e := range tk.session.Entries {
		out = append(out, string(e.Kind))
	}
	return out
}
