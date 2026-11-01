//go:build e2e

// End-to-end checks for the dsh runtime lifecycle (SPEC 3.1; acceptance A1's
// runtime half) against the real dsh and a local fake model endpoint.
// Same environment as plugininstall_test.go. Writes e2e/report/runtime.md.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

func testCLI(t *testing.T, work string) *dsh.CLI {
	node, bin := os.Getenv("ALCOVE_E2E_NODE"), os.Getenv("ALCOVE_E2E_DSH")
	if node == "" || bin == "" {
		t.Skip("set ALCOVE_E2E_NODE and ALCOVE_E2E_DSH")
	}
	cli := &dsh.CLI{Node: node, Bin: bin, Home: filepath.Join(work, "dsh-home"), Timeout: 3 * time.Minute}
	if d := os.Getenv("ALCOVE_E2E_TOOLDIR"); d != "" {
		cli.ToolDirs = []string{d}
	}
	return cli
}

func TestRuntime(t *testing.T) {
	work := t.TempDir()
	cli := testCLI(t, work)
	ctx := context.Background()
	model, err := fakemodel.Start(3)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()

	r := &report{t: t, title: "dsh 运行时生命周期 e2e（SPEC 3.1）", file: "runtime.md"}
	ver, _ := cli.Version(ctx)
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	wsA, wsB := filepath.Join(work, "ws-a"), filepath.Join(work, "ws-b")
	os.MkdirAll(wsA, 0o755)
	os.MkdirAll(wsB, 0o755)
	spec := alrt.LaunchSpec{
		Profile: "sdk", Cwd: wsA, Provider: "deepseek-official", Model: "deepseek-v4-flash",
		// The key exists only in this process's environment.
		Env:         []string{"DEEPSEEK_API_KEY=sk-e2e-not-a-real-key", "DEEPSEEK_BASE_URL=" + model.URL},
		IdleTimeout: 3 * time.Second,
	}

	// 1. Launch and readiness.
	t0 := time.Now()
	p, err := alrt.Launch(ctx, cli, spec)
	r.step("拉起并在 initialize 应答后就绪", err, fmt.Sprintf("%d ms", time.Since(t0).Milliseconds()))
	if err != nil {
		return
	}
	var events []alrt.Event
	collected := make(chan struct{})
	go func() {
		for e := range p.Events() {
			events = append(events, e)
		}
		close(collected)
	}()

	// 2. A task with tool calls, then a second session in another workspace.
	run := fmt.Sprint(time.Now().UnixNano())
	sa, sb := "a-"+run, "b-"+run
	_, err = p.PromptText(ctx, sa, "do the task")
	if err == nil {
		err = p.Wait(ctx, sa)
	}
	if err == nil {
		_, err = os.Stat(filepath.Join(wsA, "hello.txt"))
	}
	r.step("会话 A 完成含工具调用的任务，文件写进工作区 A", err, "")
	if _, err = p.SetWorkingDirectory(ctx, sb, wsB); err == nil {
		if _, err = p.PromptText(ctx, sb, "do the task"); err == nil {
			err = p.Wait(ctx, sb)
		}
	}
	if err == nil {
		_, err = os.Stat(filepath.Join(wsB, "hello.txt"))
	}
	r.step("同一进程里会话 B 在工作区 B 完成任务", err, "")

	// 3. Idle exit: no events for IdleTimeout, the process stops by itself.
	select {
	case <-p.Exited():
		r.step("空闲超时后自动退出", nil, "空闲阈值 3 s（测试值）")
	case <-time.After(30 * time.Second):
		r.step("空闲超时后自动退出", errors.New("still running after 30 s"), "")
		p.Stop(0)
	}
	<-collected
	kinds := map[string]int{}
	for _, e := range events {
		var ev struct {
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		}
		json.Unmarshal(e.Params, &ev)
		kinds[e.Method+":"+ev.Event.Type]++
	}
	need := []string{"session.event:tool/call", "session.event:tool/result", "session.event:assistant/message", "session.event:turn/end"}
	var missing []string
	for _, k := range need {
		if kinds[k] == 0 {
			missing = append(missing, k)
		}
	}
	err = nil
	if len(missing) > 0 {
		err = fmt.Errorf("missing %v", missing)
	}
	r.step("收到工具调用、结果、助手消息与回合结束事件", err, fmt.Sprintf("%d 条通知", len(events)))

	// 4. The key never reaches disk.
	err = grepTree(cli.Home, "sk-e2e-not-a-real-key")
	r.step("API key 不出现在 dsh home 的任何文件里", err, "")

	// 5. Pinned process does not idle out; Stop shuts it down.
	p2, err := alrt.Launch(ctx, cli, spec)
	if err != nil {
		r.step("钉住：拉起", err, "")
		return
	}
	p2.Pin(true)
	select {
	case <-p2.Exited():
		r.step("钉住后不因空闲退出", errors.New("exited"), "")
	case <-time.After(6 * time.Second):
		r.step("钉住后不因空闲退出", nil, "等待 6 s")
	}
	t1 := time.Now()
	err = p2.Stop(0)
	r.step("手动停止（shutdown，5 s 后强杀）", err, fmt.Sprintf("%d ms", time.Since(t1).Milliseconds()))

	// 6. A profile that cannot boot fails readiness with the stderr tail.
	bad := spec
	bad.Profile = "desktop" // reserved by dsh; the CLI refuses to boot it
	bad.ReadyTimeout = 10 * time.Second
	_, err = alrt.Launch(ctx, cli, bad)
	switch {
	case err == nil:
		err = errors.New("launched a reserved profile")
	case !errors.Is(err, alrt.ErrNotReady):
		err = fmt.Errorf("not ErrNotReady: %w", err)
	default:
		r.step("无法启动时报未就绪并带 stderr 尾部", nil, firstLine(err.Error()))
		err = nil
	}
	if err != nil {
		r.step("无法启动时报未就绪并带 stderr 尾部", err, "")
	}

	// 7. Only the configured endpoint was contacted.
	var other []string
	for _, req := range model.Requests() {
		if !strings.HasSuffix(req, "/v1/messages") {
			other = append(other, req)
		}
	}
	var routeErr error
	if len(other) > 0 {
		routeErr = fmt.Errorf("unexpected model requests: %v", other)
	}
	r.step("模型请求只打到配置的端点", routeErr, fmt.Sprintf("%d 次 /v1/messages，其他路径：%v", len(model.Requests())-len(other), other))
}

func grepTree(root, needle string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Size() > 64<<20 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), needle) {
			return fmt.Errorf("found in %s", path)
		}
		return nil
	})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
