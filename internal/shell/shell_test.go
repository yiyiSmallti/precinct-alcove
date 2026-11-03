package shell

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	tt.Click("模型")
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
	s.page, s.curWS = pageWS+"w1", "w1"
	return v
}

func TestConversationFromRuntimeEvents(t *testing.T) {
	s, tt := newShell(t)
	v := openWorkspace(s, "rt1")
	// The recorded turn of the events package, under a task's session id:
	// the task shows up in the sidebar on its first event.
	const sid = "chat-0001"
	f, err := os.Open("../runtime/events/testdata/turn.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		method, params, _ := strings.Cut(sc.Text(), " ")
		params = strings.ReplaceAll(params, `"sessionId":"s1"`, `"sessionId":"`+sid+`"`)
		s.OnEvent("rt1", alrt.Event{Method: method, Params: json.RawMessage(params)})
	}
	tt.Frame()
	s.mu.Lock()
	tk := v.tasks[sid]
	s.mu.Unlock()
	if tk == nil {
		t.Fatalf("no task for the session; texts: %v", tt.Texts())
	}
	if !tt.HasText("do it") { // the title, from the first message
		t.Errorf("task not listed: %v", tt.Texts())
	}
	if err := tt.Click("do it"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"写入文件", "运行命令", "echo step 1", "done after 2 tool calls"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q; texts: %v", want, tt.Texts())
		}
	}
	s.mu.Lock()
	n := len(tk.session.Entries)
	s.mu.Unlock()
	if n != 4 {
		t.Errorf("%d entries", n)
	}
	// Events of another runtime do not reach this workspace, and sessions
	// Alcove did not start are not listed.
	s.OnEvent("rt2", alrt.Event{Method: "session.status", Params: json.RawMessage(`{"sessionId":"` + sid + `","status":"running"}`)})
	s.OnEvent("rt1", alrt.Event{Method: "session.status", Params: json.RawMessage(`{"sessionId":"sub-1","status":"running"}`)})
	s.mu.Lock()
	running, n := tk.session.Running(), len(v.tasks)
	s.mu.Unlock()
	if running || n != 1 {
		t.Errorf("running=%v tasks=%d", running, n)
	}
	// The index and the transcript survive a restart.
	until(t, tt, s, func() bool {
		l, _ := s.App.Tasks.List("w1")
		return len(l) == 1 && l[0].Title == "do it"
	})
	until(t, tt, s, func() bool {
		var snap events.Snapshot
		return s.App.Tasks.LoadTranscript("w1", sid, &snap) == nil && len(snap.Entries) == 4
	})
	// A new window (after a restart) shows the task with its transcript.
	s2 := New(s.App)
	s2.Load()
	s2.mu.Lock()
	s2.workspaces = append(s2.workspaces, workspace.Workspace{ID: "w1", Name: "项目 A", Dir: os.TempDir()})
	s2.index["w1"], _ = s.App.Tasks.List("w1")
	v2 := s2.view("w1")
	n2 := 0
	if tk2 := v2.tasks[sid]; tk2 != nil {
		n2 = len(tk2.session.Entries)
	}
	s2.mu.Unlock()
	if n2 != 4 {
		t.Errorf("after restart: %d entries", n2)
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
	s.send(v, nil, "hi")
	n, msg, notice := len(v.tasks), v.err, s.notice
	s.mu.Unlock()
	if n != 0 || !strings.Contains(msg, "path too long") || notice == "" {
		t.Errorf("tasks=%d err=%q notice=%q", n, msg, notice)
	}
}

func TestExportDiagnostics(t *testing.T) {
	s, tt := newShell(t)
	out := filepath.Join(t.TempDir(), "diag.zip")
	s.SaveFile = func(string) (string, error) { return out, nil }
	if _, err := s.App.Creds.Add("k", "deepseek-official", "", "sk-shell-diag-123456"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.page, s.prefs.tab = PageSettings, tabOther
	s.mu.Unlock()
	tt.Frame()
	if err := tt.Click("导出诊断包…"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { return strings.HasPrefix(s.diagStatus, "已导出") })
	b, err := os.ReadFile(out)
	if err != nil || len(b) == 0 || bytes.Contains(b, []byte("sk-shell-diag-123456")) {
		t.Errorf("bundle %d bytes, err %v", len(b), err)
	}
}
