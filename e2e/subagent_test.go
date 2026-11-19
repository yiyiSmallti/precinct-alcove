//go:build e2e

// End-to-end checks for subagent progress and the runtime panel (PRD 3.4
// 子代理进度, 3.5 运行中列表、手动停止、钉住) against a real dsh runtime.
//
// Ways this can fail, each checked below:
//   - the subagent tool is not offered in a workspace profile, so nothing
//     ever delegates;
//   - the call's result is not recognized as a started subagent (dsh
//     changed its wording), and the call shows done while the child runs;
//   - the child's tool calls are not tied to the call (subagent.started is
//     missing, or child events arrive under an id the parent never named);
//   - subagent.finished's answer has a shape the view does not read, so
//     the answer is lost;
//   - a pinned runtime still exits when idle, an unpinned one does not, or
//     a pin is forgotten when the runtime is started again;
//   - stopping from the panel leaves the runtime listed.
//
// Same environment as plugininstall_test.go. Writes e2e/report/subagent.md
// and, for inspection, the raw notifications to e2e/report/subagent-events.txt.
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

func TestSubagentAndRuntimePanel(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "子代理进度与运行时面板 e2e", file: "subagent.md"}
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

	// The parent delegates; the subagent (its first message is the
	// delegated prompt) runs one command and answers.
	const childPrompt = "CHILD-TASK: run the probe"
	var offered []string
	var mu sync.Mutex
	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	model.Respond = func(body []byte) (*fakemodel.ToolCall, string) {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		json.Unmarshal(body, &req)
		shell := ""
		mu.Lock()
		offered = offered[:0]
		for _, tl := range req.Tools {
			offered = append(offered, tl.Name)
			if shell == "" && (strings.Contains(tl.Name, "bash") || tl.Name == "pwsh") { // pwsh on Windows
				shell = tl.Name
			}
		}
		mu.Unlock()
		results := strings.Count(string(body), `"tool_result"`)
		child := len(req.Messages) > 0 && strings.Contains(string(req.Messages[0].Content), childPrompt)
		switch {
		case child && results == 0:
			return &fakemodel.ToolCall{Name: shell, Input: map[string]any{"command": "echo probe-ok", "description": "probe"}}, ""
		case child:
			return nil, "child answer 42"
		case results == 0:
			return &fakemodel.ToolCall{Name: "subagent", Input: map[string]any{"description": "run the probe", "prompt": childPrompt}}, ""
		}
		return nil, "parent done"
	}

	if _, err := a.Creds.Add("假 DeepSeek", "deepseek-official", model.URL, "sk-e2e-subagent", "deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, "proj")
	os.MkdirAll(dir, 0o755)
	w, err := a.CreateWorkspace(ctx, "子代理", dir)
	if err != nil {
		t.Fatal(err)
	}
	k := alrt.Key{Profile: w.Profile, Cwd: w.Dir}

	// The view folds every notification the way the shell does; the raw
	// stream is kept for inspection.
	sid := "task-subagent-1"
	view := events.NewSession(sid)
	var raw strings.Builder
	a.OnEvent = func(_ string, e alrt.Event) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&raw, "%s %s\n", e.Method, e.Params)
		view.Apply(e.Method, e.Params)
	}
	defer func() {
		mu.Lock()
		os.MkdirAll("report", 0o755)
		os.WriteFile(filepath.Join("report", "subagent-events.txt"), []byte(raw.String()), 0o644)
		mu.Unlock()
	}()

	// 1. A delegation shows the subagent's progress on the call.
	p, err := a.OpenWorkspace(ctx, w.ID)
	if err == nil {
		_, err = p.PromptText(ctx, sid, "delegate it")
	}
	if err == nil {
		wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = p.Wait(wctx, sid)
		cancel()
	}
	// session/wait returns once descendants settle; the finished
	// notification may trail it.
	var call *events.Entry
	for end := time.Now().Add(10 * time.Second); err == nil && time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		mu.Lock()
		call = nil
		for _, e := range view.Entries {
			if e.Kind == events.Tool && e.ToolName == "subagent" {
				c := *e
				call = &c
			}
		}
		mu.Unlock()
		if call != nil && call.AgentStatus != "running" && call.AgentStatus != "" {
			break
		}
	}
	mu.Lock()
	tools := strings.Join(offered, ",")
	mu.Unlock()
	switch {
	case err != nil:
	case call == nil:
		err = fmt.Errorf("no subagent call (tools offered: %s)", tools)
	case call.Agent == "":
		err = fmt.Errorf("call result %q not linked to a subagent", call.Result)
	case call.AgentStatus != "ok":
		err = fmt.Errorf("subagent status %q", call.AgentStatus)
	case call.AgentSteps < 1:
		err = fmt.Errorf("subagent steps %d: its tool calls were not counted", call.AgentSteps)
	case !strings.Contains(call.AgentResult, "child answer 42"):
		err = fmt.Errorf("subagent answer %q", call.AgentResult)
	}
	detail := ""
	if call != nil {
		detail = fmt.Sprintf("agent=%s status=%s steps=%d answer=%q", call.Agent, call.AgentStatus, call.AgentSteps, call.AgentResult)
	}
	r.step("父会话委派子代理：调用挂上子代理 id，计入子代理的工具调用，结束后带回回答", err, detail)

	// 2. A snapshot taken mid-run comes back stopped, not running.
	snap := events.Snapshot{Entries: []events.Entry{{Kind: events.Tool, ToolName: "subagent", Done: true, Agent: "x", AgentStatus: "running"}}}
	back := events.NewSession("s")
	back.Restore(snap)
	err = nil
	if got := back.Entries[0].AgentStatus; got != "stopped" || back.AgentsRunning() != 0 {
		err = fmt.Errorf("restored status %q", got)
	}
	r.step("运行中保存的记录恢复后显示为已停止", err, "")

	// 3. The panel lists the workspace runtime with its idle exit; a pin
	// holds it and survives a stop and start.
	find := func() *alrt.Info {
		for _, info := range a.RunningRuntimes() {
			if info.Key == k {
				return &info
			}
		}
		return nil
	}
	err = nil
	info := find()
	switch {
	case info == nil:
		err = fmt.Errorf("runtime not listed")
	case info.Pinned || info.Busy:
		err = fmt.Errorf("pinned=%v busy=%v", info.Pinned, info.Busy)
	case time.Until(info.IdleExit) < 9*time.Minute:
		err = fmt.Errorf("idle exit %s", info.IdleExit)
	}
	r.step("运行时面板列出工作区运行时：空闲，约 10 分钟后退出", err, "")

	a.PinRuntime(k, true)
	err = nil
	if info = find(); info == nil || !info.Pinned || !info.IdleExit.IsZero() {
		err = fmt.Errorf("after pin: %+v", info)
	}
	if err == nil {
		err = a.StopRuntime(k)
	}
	if err == nil && find() != nil {
		err = fmt.Errorf("still listed after stop")
	}
	if err == nil {
		_, err = a.OpenWorkspace(ctx, w.ID)
	}
	if err == nil {
		if info = find(); info == nil || !info.Pinned {
			err = fmt.Errorf("restarted runtime not pinned: %+v", info)
		}
	}
	a.PinRuntime(k, false)
	r.step("钉住后不再计空闲；停止后从列表消失；再次启动仍是钉住的", err, "")

	// 4. Idle exit with and without a pin, on a short timeout.
	m := &alrt.Manager{CLI: a.CLI}
	spec := alrt.LaunchSpec{Profile: w.Profile, Cwd: dir, Provider: "deepseek-official", Model: "deepseek-v4-pro",
		Env: []string{"DEEPSEEK_API_KEY=sk-e2e"}, IdleTimeout: 2 * time.Second}
	sk := alrt.Key{Profile: spec.Profile, Cwd: spec.Cwd}
	a.StopRuntime(k) // the same profile and folder
	m.Pin(sk, true)
	_, err = m.Ensure(ctx, spec)
	if err == nil {
		time.Sleep(4 * time.Second)
		if len(m.List()) != 1 {
			err = fmt.Errorf("pinned runtime exited when idle")
		}
	}
	if err == nil {
		m.Pin(sk, false)
		time.Sleep(4 * time.Second)
		if n := len(m.List()); n != 0 {
			err = fmt.Errorf("unpinned runtime still running")
		}
	}
	m.StopAll()
	r.step("空闲超时 2 秒：钉住时 4 秒后仍在；取消钉住后 4 秒内退出", err, "")
}
