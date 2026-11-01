//go:build e2e

// End-to-end checks for the alcove-bridge bundle (SPEC 3.1): approval cards
// and question forms reach the Alcove answerer over the host-callback
// connection and their answers steer the real dsh runtime.
// Writes e2e/report/bridge.md.
package e2e

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// scriptedAnswerer answers approvals with a fixed outcome and questions with
// the last option of each question, recording what it was asked.
type scriptedAnswerer struct {
	mu        sync.Mutex
	outcome   string
	approvals []alrt.ApprovalRequest
	questions []alrt.QuestionRequest
}

func (a *scriptedAnswerer) Approve(ctx context.Context, runtimeID string, req alrt.ApprovalRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.approvals = append(a.approvals, req)
	return a.outcome, nil
}

func (a *scriptedAnswerer) AnswerQuestions(ctx context.Context, runtimeID string, req alrt.QuestionRequest) ([]alrt.Answer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.questions = append(a.questions, req)
	var out []alrt.Answer
	for _, q := range req.Questions {
		ans := alrt.Answer{ID: q.ID, Selected: []string{}}
		if n := len(q.Options); n > 0 {
			ans.Selected = []string{q.Options[n-1].Label}
		}
		out = append(out, ans)
	}
	return out, nil
}

func TestBridge(t *testing.T) {
	work := t.TempDir()
	cli := testCLI(t, work)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &report{t: t, title: "桥接 bundle e2e：审批卡与提问表单（SPEC 3.1）", file: "bridge.md"}
	ver, _ := cli.Version(ctx)
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	// Workspace profile: the shipped sdk template plus the bridge bundle.
	const profile = "ws-bridge-e2e"
	_, err := cli.Run(ctx, profile, "--from-default-profile", "sdk", "--dump-config")
	if err == nil {
		var tgz string
		if tgz, err = packDir(filepath.Join("..", "bridge"), work); err == nil {
			_, err = cli.PluginAdd(ctx, profile, tgz)
		}
	}
	if err == nil {
		_, err = cli.DumpConfig(ctx, profile)
	}
	r.step("工作区 profile = sdk 模板 + 桥接 bundle，--dump-config 通过", err, "")
	if err != nil {
		return
	}

	ans := &scriptedAnswerer{outcome: alrt.AllowedOnce}
	bridge := &alrt.Bridge{Answerer: ans, AuditLog: filepath.Join(work, "alcove", "logs", "decisions.jsonl")}
	endpoint := filepath.Join(work, "bridge.sock")
	if runtime.GOOS == "windows" {
		endpoint = fmt.Sprintf("alcove-bridge-e2e-%d", os.Getpid())
	}
	err = bridge.Start(ctx, endpoint)
	r.step("宿主监听回连端点", err, "")
	if err != nil {
		return
	}

	// Writes outside the workspace need escalation. The temp dir is writable
	// under workspace-write, so use a directory under the home directory.
	home, _ := os.UserHomeDir()
	outside, err := os.MkdirTemp(home, ".alcove-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)

	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	ws := filepath.Join(work, "ws")
	os.MkdirAll(ws, 0o755)
	p, err := alrt.Launch(ctx, cli, alrt.LaunchSpec{
		Profile: profile, Cwd: ws, Provider: "deepseek-official", Model: "deepseek-v4-flash",
		Env:    []string{"DEEPSEEK_API_KEY=sk-e2e", "DEEPSEEK_BASE_URL=" + model.URL},
		Bridge: bridge, IdleTimeout: -1,
	})
	r.step("带桥接拉起运行时", err, "")
	if err != nil {
		return
	}
	defer p.Stop(0)
	var events []alrt.Event
	var evMu sync.Mutex
	go func() {
		for e := range p.Events() {
			evMu.Lock()
			events = append(events, e)
			evMu.Unlock()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !bridge.Attached(p.ID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !bridge.Attached(p.ID) {
		r.step("bundle 用一次性令牌回连并握手", errors.New("not attached after 10 s; stderr: "+p.Stderr(5)), "")
		return
	}
	r.step("bundle 用一次性令牌回连并握手", nil, "")

	// dsh offers escalation only for the exact command the sandbox just
	// denied, so the script runs it plainly first, then asks to escalate.
	escalate := func(path string) []fakemodel.ToolCall {
		cmd := "touch " + path
		return []fakemodel.ToolCall{
			{Name: "bash", Input: map[string]any{"command": cmd, "description": "write outside the workspace"}},
			{Name: "bash", Input: map[string]any{
				"command": cmd, "description": "write outside the workspace",
				"sandbox_permissions": "danger-full-access", "justification": "e2e: needs a write outside the workspace",
			}},
		}
	}
	run := fmt.Sprint(time.Now().UnixNano())

	// 1. Allowed.
	allowed := filepath.Join(outside, "allowed.txt")
	model.SetScript(escalate(allowed))
	err = turn(ctx, p, "allow-"+run)
	if err == nil {
		_, err = os.Stat(allowed)
	}
	if err != nil {
		evMu.Lock()
		t.Logf("tool results: %s\nstderr: %s", toolResults(events), p.Stderr(40))
		evMu.Unlock()
	}
	r.step("审批卡选\"允许一次\"：提权命令执行", err, approvalDetail(ans))

	// 2. Rejected.
	ans.mu.Lock()
	ans.outcome = alrt.Rejected
	ans.mu.Unlock()
	rejected := filepath.Join(outside, "rejected.txt")
	model.SetScript(escalate(rejected))
	err = turn(ctx, p, "reject-"+run)
	if err == nil {
		if _, serr := os.Stat(rejected); serr == nil {
			err = errors.New("command ran although rejected")
		}
	}
	r.step("审批卡选\"拒绝\"：命令不执行", err, "")

	// 3. Question form.
	model.SetScript([]fakemodel.ToolCall{{Name: "ask_user_question", Input: map[string]any{
		"questions": []any{map[string]any{"id": "color", "question": "Which colour?", "options": []any{
			map[string]any{"label": "Red (Recommended)"}, map[string]any{"label": "Blue"},
		}}},
	}}})
	err = turn(ctx, p, "ask-"+run)
	if err == nil {
		err = waitFor(func() bool {
			evMu.Lock()
			defer evMu.Unlock()
			return toolResultContains(events, "Blue")
		}, 5*time.Second)
	}
	detail := ""
	ans.mu.Lock()
	if len(ans.questions) > 0 && len(ans.questions[0].Questions) > 0 {
		detail = "问题：" + ans.questions[0].Questions[0].Question
	}
	ans.mu.Unlock()
	r.step("提问表单：回答经工具结果回到模型", err, detail)

	// 4. Every decision is on record.
	data, err := os.ReadFile(bridge.AuditLog)
	if err == nil {
		lines := strings.Count(strings.TrimSpace(string(data)), "\n") + 1
		if !strings.Contains(string(data), `"outcome":"allowed-once"`) || !strings.Contains(string(data), `"outcome":"rejected"`) || !strings.Contains(string(data), `"kind":"questions"`) {
			err = fmt.Errorf("audit log incomplete: %s", data)
		} else {
			detail = fmt.Sprintf("%d 条记录", lines)
		}
	}
	r.step("每个审批与回答都有本地记录", err, detail)

	// 5. A forged or reused token is refused.
	err = helloWith(bridge, "not-a-token")
	r.step("错误令牌被拒（-32012）", err, "")
}

func turn(ctx context.Context, p *alrt.Process, session string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := p.PromptText(ctx, session, "do it"); err != nil {
		return err
	}
	return p.Wait(ctx, session)
}

func approvalDetail(a *scriptedAnswerer) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.approvals) == 0 {
		return "没有收到审批请求"
	}
	return "原因：" + a.approvals[0].DisplayReason.Text("zh")
}

func toolResultContains(events []alrt.Event, s string) bool {
	for _, e := range events {
		if strings.Contains(string(e.Params), `"tool/result"`) && strings.Contains(string(e.Params), s) {
			return true
		}
	}
	return false
}

func toolResults(events []alrt.Event) string {
	var out []string
	for _, e := range events {
		if strings.Contains(string(e.Params), `"tool/result"`) || strings.Contains(string(e.Params), `"approval/`) {
			out = append(out, string(e.Params))
		}
	}
	return strings.Join(out, "\n")
}

func waitFor(cond func() bool, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("condition not met in time")
}

// helloWith dials the bridge endpoint and expects an unauthorized answer.
func helloWith(b *alrt.Bridge, token string) error {
	network := "unix"
	if runtime.GOOS == "windows" {
		return nil // dialing a pipe needs winio; covered on Windows by the bundle path
	}
	c, err := net.DialTimeout(network, b.Endpoint(), 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	fmt.Fprintf(c, `{"jsonrpc":"2.0","id":1,"method":"host.hello","params":{"protocol":"alcove-bridge/1","token":%q}}`+"\n", token)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return err
	}
	var m struct {
		Error *struct{ Code int } `json:"error"`
	}
	json.Unmarshal(line, &m)
	if m.Error == nil || m.Error.Code != -32012 {
		return fmt.Errorf("unexpected answer %s", line)
	}
	return nil
}

// packDir tars dir as an npm package (entries under package/).
func packDir(dir, out string) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tw.WriteHeader(&tar.Header{Name: "package/" + filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(499162500, 0), Typeflag: tar.TypeReg})
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return "", err
	}
	tw.Close()
	gz.Close()
	path := filepath.Join(out, "alcove-bridge.tgz")
	return path, os.WriteFile(path, buf.Bytes(), 0o644)
}
