package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecrets) Set(k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}

func (s *memSecrets) Get(k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[k]; ok {
		return v, nil
	}
	return nil, credentials.ErrNotFound
}

func (s *memSecrets) Delete(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}

func newShell(t *testing.T) (*Shell, *ui.Tester) {
	t.Helper()
	a, err := app.New(app.Config{DataDir: t.TempDir(), AlcoveVersion: "0.1.0", Secrets: &memSecrets{m: map[string][]byte{}}})
	if err != nil {
		t.Fatal(err)
	}
	s := New(a)
	s.Load()
	return s, ui.NewTester(s.View, 1100, 760)
}

// until waits for cond, checked under the shell's lock, redrawing as it goes.
func until(t *testing.T, tt *ui.Tester, s *Shell, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); ; {
		s.mu.Lock()
		ok := cond()
		s.mu.Unlock()
		tt.Frame()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSettingsAddCredential(t *testing.T) {
	s, tt := newShell(t)
	tt.Frame()
	if !tt.HasText("还没有凭据。添加一个模型 API key 后才能执行任务；key 只存在系统凭据库里。") {
		t.Fatalf("no empty state; texts: %v", tt.Texts())
	}
	s.mu.Lock()
	s.form.label, s.form.key = "DeepSeek 个人", "sk-test"
	s.mu.Unlock()
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { return len(s.creds) == 1 && s.settings.DefaultCredential != "" })
	if !tt.HasText("DeepSeek 个人（deepseek-official） · 默认") {
		t.Errorf("credential not listed as default: %v", tt.Texts())
	}
	s.mu.Lock()
	cleared := s.form.key == ""
	s.mu.Unlock()
	if !cleared {
		t.Error("key left in the form")
	}
}

// openWorkspace makes a workspace page bound to runtime rt without starting
// dsh.
func openWorkspace(s *Shell, rt string) *wsView {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := workspace.Workspace{ID: "w1", Name: "项目 A", Dir: os.TempDir(), Profile: "ws-w1"}
	s.workspaces = append(s.workspaces, w)
	v := s.view("w1")
	v.runtimeID = rt
	s.byRuntime[rt] = "w1"
	s.page = pageWS + "w1"
	return v
}

func TestConversationFromRuntimeEvents(t *testing.T) {
	s, tt := newShell(t)
	v := openWorkspace(s, "rt1")
	// The recorded turn of the events package, under this workspace's
	// session id.
	f, err := os.Open("../runtime/events/testdata/turn.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		method, params, _ := strings.Cut(sc.Text(), " ")
		params = strings.ReplaceAll(params, `"sessionId":"s1"`, `"sessionId":"`+v.session.ID+`"`)
		s.OnEvent("rt1", alrt.Event{Method: method, Params: json.RawMessage(params)})
	}
	tt.Frame()
	for _, want := range []string{"do it", "write", "bash", "done after 2 tool calls"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q; texts: %v", want, tt.Texts())
		}
	}
	s.mu.Lock()
	n := len(v.session.Entries)
	s.mu.Unlock()
	if n != 4 {
		t.Errorf("%d entries", n)
	}
	// Events of another runtime do not reach this workspace.
	s.OnEvent("rt2", alrt.Event{Method: "session.status", Params: json.RawMessage(`{"sessionId":"` + v.session.ID + `","status":"running"}`)})
	if v.session.Running() {
		t.Error("another runtime's event applied")
	}
}

func TestApprovalCard(t *testing.T) {
	s, tt := newShell(t)
	openWorkspace(s, "rt1")
	got := make(chan string, 1)
	go func() {
		out, _ := s.Approve(context.Background(), "rt1", alrt.ApprovalRequest{RequestID: "r1", ToolName: "bash",
			Reason: "write outside", DisplayReason: alrt.LocalText{"zh": "要在工作区外写文件", "en": "Write outside"}})
		got <- out
	}()
	until(t, tt, s, func() bool { return len(s.views["w1"].approvals) == 1 })
	if !tt.HasText("要在工作区外写文件") {
		t.Fatalf("card missing: %v", tt.Texts())
	}
	if err := tt.Click("允许这一次"); err != nil {
		t.Fatal(err)
	}
	if out := <-got; out != alrt.AllowedOnce {
		t.Errorf("outcome %q", out)
	}
	until(t, tt, s, func() bool { return len(s.views["w1"].approvals) == 0 })

	// A runtime with no window is denied, and an exiting runtime cancels
	// what it asked.
	if out, _ := s.Approve(context.Background(), "nobody", alrt.ApprovalRequest{}); out != alrt.Rejected {
		t.Errorf("unknown runtime: %q", out)
	}
	go func() {
		out, _ := s.Approve(context.Background(), "rt1", alrt.ApprovalRequest{ToolName: "bash", Reason: "x"})
		got <- out
	}()
	until(t, tt, s, func() bool { return len(s.views["w1"].approvals) == 1 })
	s.OnExit(alrt.Key{}, "rt1", nil)
	if out := <-got; out != alrt.Cancelled {
		t.Errorf("after exit: %q", out)
	}
}

func TestQuestionForm(t *testing.T) {
	s, tt := newShell(t)
	openWorkspace(s, "rt1")
	var q alrt.Question
	json.Unmarshal([]byte(`{"id":"q1","question":"用哪个数据库？","options":[{"label":"Postgres"},{"label":"SQLite"}]}`), &q)
	got := make(chan []alrt.Answer, 1)
	go func() {
		ans, _ := s.AnswerQuestions(context.Background(), "rt1", alrt.QuestionRequest{RequestID: "r2", Questions: []alrt.Question{q}})
		got <- ans
	}()
	until(t, tt, s, func() bool { return len(s.views["w1"].questions) == 1 })
	if err := tt.Click("SQLite"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	ans := <-got
	if len(ans) != 1 || ans[0].ID != "q1" || len(ans[0].Selected) != 1 || ans[0].Selected[0] != "SQLite" {
		t.Errorf("answers %+v", ans)
	}
}

var _ = events.User

func TestMultiSelectQuestion(t *testing.T) {
	s, tt := newShell(t)
	openWorkspace(s, "rt1")
	var q alrt.Question
	json.Unmarshal([]byte(`{"id":"q1","question":"要哪些功能？","multiSelect":true,"options":[{"label":"登录"},{"label":"搜索"},{"label":"导出"}]}`), &q)
	got := make(chan []alrt.Answer, 1)
	go func() {
		ans, _ := s.AnswerQuestions(context.Background(), "rt1", alrt.QuestionRequest{RequestID: "r3", Questions: []alrt.Question{q}})
		got <- ans
	}()
	until(t, tt, s, func() bool { return len(s.views["w1"].questions) == 1 })
	for _, l := range []string{"登录", "导出"} {
		if err := tt.Click(l); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if err := tt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	ans := <-got
	if len(ans) != 1 || strings.Join(ans[0].Selected, ",") != "登录,导出" {
		t.Errorf("answers %+v", ans)
	}
}

func TestStartFailedRefusesTasks(t *testing.T) {
	s, _ := newShell(t)
	openWorkspace(s, "rt1")
	s.StartFailed(errors.New("listen: path too long"))
	s.mu.Lock()
	v := s.views["w1"]
	s.send(v, "hi")
	starting, msg, notice := v.starting, v.err, s.notice
	s.mu.Unlock()
	if starting || !strings.Contains(msg, "path too long") || notice == "" {
		t.Errorf("starting=%v err=%q notice=%q", starting, msg, notice)
	}
}
