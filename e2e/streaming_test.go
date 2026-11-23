//go:build e2e

// End-to-end checks for what the conversation shows while it is still being
// written, against a real dsh runtime with the bridge bundle and a fake model
// that streams in paced pieces: the model's reasoning, a tool call's
// arguments, what the call's command prints while it runs, and the summary a
// context compaction is writing.
//
// Ways this can fail, each checked below:
//   - reasoning deltas are folded but nothing is seen before the final
//     message (no live entry, or one that only appears whole);
//   - tool-call-delta chunks are dropped, so a call shows only once the
//     model has finished writing it; or a streamed call is left behind as a
//     second entry beside the one the final message makes;
//   - the bundle does not tie the command's job to its call (no
//     job/registered event, or a call id that differs from the message's),
//     so no output reaches the call before its result;
//   - output arrives only at the end (the job ring is read once), or keeps
//     arriving after the result and overwrites it;
//   - the compaction's summary call is not seen by the bundle's llm/stream
//     listener, so the summary appears only whole with compaction/summary;
//     or the compaction never closes (stays "正在压缩").
//
// Same environment as plugininstall_test.go. Writes e2e/report/streaming.md
// and the raw notifications to e2e/report/streaming-events.txt.
package e2e

import (
	"context"
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
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

func TestStreaming(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe command is a bash loop")
	}
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "流式输出 e2e：思考、工具参数、命令输出、压缩摘要", file: "streaming.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{
		DataDir: filepath.Join(work, "data"), AlcoveVersion: "0.1.0",
		Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
		Secrets: &memSecrets{m: map[string][]byte{}}, Answerer: &scriptedAnswerer{outcome: "allowed-once"},
	})
	if err == nil {
		err = a.Start(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	const (
		command = "for i in 1 2 3 4; do echo tick-$i; sleep 1; done"
		summary = "## 目标\n- 运行 tick 命令并汇报\n## 进展\n- 命令已运行，打印了 tick-1 到 tick-4\n## 下一步\n- 汇报结果\n"
	)
	// A long request, then long reasoning, under a small context (below):
	// the step after the reasoning is past the compaction threshold, the
	// reasoning is the tail a compaction keeps, and the request before it
	// is what gets summarized. (A long command output would not do: dsh
	// prunes tool results first.)
	prompt := "请运行 tick 命令。\n" + strings.Repeat("背景资料：这一段只是为了让对话足够长。\n", 1500)
	thinking := "先看看要做什么。\n要跑一个会分几次打印的命令。\n" + strings.Repeat("补充思考：确认命令和输出的格式。\n", 300) + "最后汇报结果。\n"
	compactionReq := func(body []byte) bool { return strings.Contains(string(body), "acting as a compaction engine") }
	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	model.Pace = 150 * time.Millisecond
	model.Respond = func(body []byte) (*fakemodel.ToolCall, string) {
		switch {
		case compactionReq(body):
			return nil, summary
		case strings.Count(string(body), `"tool_result"`) == 0:
			return &fakemodel.ToolCall{Name: "bash", Input: map[string]any{"description": "打印 tick", "command": command}}, ""
		}
		return nil, "命令跑完了：tick-1 到 tick-4。"
	}
	model.Think = func(body []byte) string {
		if compactionReq(body) || strings.Contains(string(body), `"tool_result"`) {
			return ""
		}
		return thinking
	}
	cred, err := a.Creds.Add("假 DeepSeek", "deepseek-official", model.URL, "sk-e2e-streaming", "deepseek-v4-pro")
	if err == nil {
		// A small context, so that the step after the command's output
		// compacts what came before it.
		err = a.Creds.SetContext(cred.ID, "deepseek-v4-pro", 12000)
	}
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, "proj")
	os.MkdirAll(dir, 0o755)
	w, err := a.CreateWorkspace(ctx, "流式", dir)
	if err != nil {
		t.Fatal(err)
	}

	// The view folds every notification as the shell does; after each one
	// the test notes what the conversation showed at that moment.
	sid := "task-streaming-1"
	view := events.NewSession(sid)
	var (
		mu  sync.Mutex
		raw strings.Builder
		// What was seen mid-stream.
		thinkLive, argsLive, outLive, compactLive string
		liveNotes                                 int
		outAfterDone                              bool
	)
	a.OnEvent = func(_ string, e alrt.Event) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&raw, "%s %s\n", e.Method, e.Params)
		if e.Method == "session.live" {
			liveNotes++
		}
		// Finished calls as they were, to catch a live note changing one.
		before := map[*events.Entry]string{}
		if e.Method == "session.live" {
			for _, en := range view.Entries {
				if en.Kind == events.Tool && en.Done {
					before[en] = en.Result + "\x00" + en.Output
				}
			}
		}
		if !view.Apply(e.Method, e.Params) {
			return
		}
		for _, en := range view.Entries {
			switch {
			case en.Kind == events.Reasoning && en.Streaming && en.Text != "" && len(en.Text) < len(thinking) && thinkLive == "":
				thinkLive = en.Text
			case en.Kind == events.Tool && en.Streaming && en.ToolName == "bash" && en.Args != "" && !json.Valid([]byte(en.Args)) && argsLive == "":
				argsLive = en.Args
			case en.Kind == events.Tool && !en.Done && strings.Contains(en.Output, "tick-1") && !strings.Contains(en.Output, "tick-4") && outLive == "":
				outLive = en.Output
			case en.Kind == events.Compaction && en.Streaming && en.Text != "" && len(en.Text) < len(summary) && compactLive == "":
				compactLive = en.Text
			}
			if was, ok := before[en]; ok && was != en.Result+"\x00"+en.Output {
				outAfterDone = true
			}
		}
	}
	defer func() {
		mu.Lock()
		os.MkdirAll("report", 0o755)
		os.WriteFile(filepath.Join("report", "streaming-events.txt"), []byte(raw.String()), 0o644)
		mu.Unlock()
	}()

	p, err := a.OpenWorkspace(ctx, w.ID)
	if err == nil {
		_, err = p.PromptText(ctx, sid, prompt)
	}
	if err == nil {
		wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err = p.Wait(wctx, sid)
		cancel()
	}
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // live notes on the other connection may trail

	mu.Lock()
	defer mu.Unlock()
	var think, call, comp []*events.Entry
	for _, en := range view.Entries {
		switch en.Kind {
		case events.Reasoning:
			think = append(think, en)
		case events.Tool:
			call = append(call, en)
		case events.Compaction:
			comp = append(comp, en)
		}
	}

	// 1. Reasoning.
	err = nil
	switch {
	case thinkLive == "":
		err = fmt.Errorf("no partial reasoning seen before it ended")
	case len(think) != 1:
		err = fmt.Errorf("%d reasoning entries", len(think))
	case think[0].Streaming || think[0].Text != thinking:
		err = fmt.Errorf("final reasoning streaming=%v text=%q", think[0].Streaming, think[0].Text)
	}
	r.step("思考边生成边显示，结束后是完整内容", err, fmt.Sprintf("中途看到 %d 字：%q", len([]rune(thinkLive)), tailOf(thinkLive, 30)))

	// 2. The tool call's arguments.
	err = nil
	switch {
	case argsLive == "":
		err = fmt.Errorf("no partial arguments seen")
	case len(call) != 1:
		err = fmt.Errorf("%d tool entries (a streamed call left beside the final one?)", len(call))
	case call[0].Streaming || jsonString(call[0].Args, "command") != command:
		err = fmt.Errorf("final call streaming=%v args=%q", call[0].Streaming, call[0].Args)
	}
	r.step("工具调用在模型写参数时就出现并逐步补全，最后只留一条", err, fmt.Sprintf("中途参数 %q", argsLive))

	// 3. What the command prints, before its result.
	err = nil
	switch {
	case liveNotes == 0:
		err = fmt.Errorf("the bridge sent no session.live notification")
	case outLive == "":
		err = fmt.Errorf("no output seen on the call before tick-4")
	case len(call) != 1 || !call[0].Done || !strings.Contains(call[0].Result, "tick-4"):
		err = fmt.Errorf("final result %q", call[0].Result)
	case outAfterDone:
		err = fmt.Errorf("a live note changed the call after its result")
	}
	r.step("命令运行中输出实时出现在调用下，结果到达后不再被覆盖", err, fmt.Sprintf("中途输出末尾 %q，session.live %d 条", tailOf(outLive, 40), liveNotes))

	// 4. The compaction's summary. dsh may compact more than once to get
	// under the threshold; each must close with the whole summary.
	err = nil
	switch {
	case len(comp) == 0:
		err = fmt.Errorf("no compaction")
	case compactLive == "":
		err = fmt.Errorf("no partial summary seen before compaction/summary")
	}
	for _, c := range comp {
		switch {
		case err != nil:
		case c.Streaming || c.IsError:
			err = fmt.Errorf("compaction streaming=%v error=%v %q", c.Streaming, c.IsError, c.Result)
		case strings.TrimSpace(c.Text) != strings.TrimSpace(summary):
			err = fmt.Errorf("final summary %q", c.Text)
		}
	}
	detail := fmt.Sprintf("压缩 %d 次，中途摘要 %q", len(comp), compactLive)
	r.step("压缩上下文时摘要边写边显示，结束后是完整摘要", err, detail)

	// 5. What is saved is the finished conversation.
	snap := view.Snapshot()
	back := events.NewSession(sid)
	back.Restore(snap)
	kinds := []string{}
	for _, en := range back.Entries {
		kinds = append(kinds, string(en.Kind))
	}
	err = nil
	if got := strings.Join(kinds, ","); !strings.Contains(got, "reasoning") || !strings.Contains(got, "tool") || !strings.Contains(got, "compaction") {
		err = fmt.Errorf("restored kinds %s", got)
	}
	r.step("保存的记录含思考、工具调用和压缩摘要，恢复后一致", err, strings.Join(kinds, ","))
}

func tailOf(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

// jsonString is one string field of a JSON object, or "".
func jsonString(raw, key string) string {
	var m map[string]any
	json.Unmarshal([]byte(raw), &m)
	v, _ := m[key].(string)
	return v
}
