// Package shell is Alcove's window (PRD 3.1–3.5, SPEC 2 internal/ui): the
// sidebar of workspaces, each workspace's conversation with its runtime,
// approval cards and question forms, and the settings page for model
// credentials. It drives Alcove only through app.App.
//
// State lives in Shell and changes on the UI thread: the view builds from it
// under mu, and everything that happens elsewhere (runtime events, approval
// requests from the bridge, results of slow calls) comes in through post.
package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
	"github.com/yiyiSmallti/precinct-alcove/internal/update"
)

// Page ids in the sidebar. Workspaces use "ws:<id>".
const (
	PageSettings = "settings"
	pageWS       = "ws:"
)

// Shell is the window's state and Alcove's UI services (approval answerer,
// sidecar UI sink, inbox).
type Shell struct {
	App *app.App
	// Post runs fn on the UI thread and redraws; nil runs fn under mu (tests).
	Post func(fn func())
	// PickFolder asks the user for a directory; "" means cancelled.
	PickFolder func() (string, error)
	// PickFile asks the user for a plugin package (.tgz); "" means cancelled.
	PickFile func() (string, error)
	// SaveFile asks where to save a file named name; "" means cancelled.
	SaveFile func(name string) (string, error)
	// OpenPath opens a file with its default application; Reveal shows it
	// in the file manager. Nil hides what needs them.
	OpenPath func(path string) error
	Reveal   func(path string)
	// OnBackground is called (on the UI thread) when Alcove starts or stops
	// having a plugin that runs in the background: the window then hides to
	// the tray / menu bar on close instead of quitting (SPEC 3.2 常驻).
	OnBackground func(on bool)
	// Lang picks displayReason texts ("zh", "en").
	Lang string
	// Version is Alcove's version, shown in settings.
	Version string
	// Updater checks for and installs new versions; nil hides updates.
	Updater update.Updater

	mu          sync.Mutex
	page        string
	curWS       string                  // the workspace a new task goes to
	curProj     string                  // the project the sidebar's switcher shows
	scratchID   string                  // the workspace of tasks outside a project (最近会话)
	wsMenu      bool                    // the workspace switcher is open
	index       map[string][]tasks.Task // tasks by workspace, as last read
	workspaces  []workspace.Workspace
	views       map[string]*wsView // by workspace id
	byRuntime   map[string]string  // runtime id → workspace id
	creds       []credentials.Credential
	settings    app.Settings
	form        credForm
	model       string  // the default credential's model field being edited
	modelFor    string  // the credential model was filled for
	savedModel  string  // its saved model, as last read
	notice      string  // one line shown at the top, e.g. an error
	startErr    string  // why Alcove could not start; tasks are refused
	preparing   bool    // 最近会话's workspace is being added on first start
	newTaskH    float32 // the new task page's height in the last frame
	scratchBusy bool    // 最近会话 is moving to another folder
	diagBusy    bool
	diagStatus  string
	contribs    map[string]sidecar.Contribution
	updates     updatesState

	pluginsState
	panels       panelState
	saves        transcriptSaves
	prefs        prefsState
	sidecarCalls sidecarCalls // nil: App.Sidecars
}

// wsView is one workspace: its tasks, the runtime they run in, and the
// approvals and questions that runtime is waiting on.
type wsView struct {
	ws            workspace.Workspace
	runtimeID     string
	tasks         map[string]*taskView // by session id
	current       string               // the task shown; "" is a new task
	draft         string               // what is typed for a new task
	err           string               // why a new task did not start
	approvals     []*approval
	questions     []*question
	confirmRemove bool
}

// taskView is one task: a dsh session of the workspace.
type taskView struct {
	meta     tasks.Task
	session  *events.Session
	input    string
	starting bool
	stopping bool
	err      string
	open     map[string]bool // tool entries shown in full, by entry key
	legacy   bool            // the conversation from before tasks; no transcript
}

type approval struct {
	req    alrt.ApprovalRequest
	answer chan string
}

type question struct {
	req    alrt.QuestionRequest
	choice []int    // chosen option per single-select question
	multi  [][]bool // ticked options per multi-select question
	custom []string
	answer chan []alrt.Answer
	cancel chan struct{}
}

type credForm struct {
	label, baseURL, key, model string
	provider                   string // the provider's Name, as the select shows it
	protocol                   string // a custom endpoint's protocol, by Name
	shown                      string // the provider the fields were last cleared for
	err                        string
}

// New returns a shell for a; call Load once a is started.
func New(a *app.App) *Shell {
	return &Shell{App: a, Lang: "zh", views: map[string]*wsView{}, byRuntime: map[string]string{}, index: map[string][]tasks.Task{},
		contribs: map[string]sidecar.Contribution{},
		pluginsState: pluginsState{sidecars: map[string]sidecar.Status{}, busy: map[string]bool{},
			forms: map[string]*settingsForm{}, chats: map[string]*chat{}, cards: map[string]*contribCard{}}}
}

func (s *Shell) post(fn func()) {
	if s.Post != nil {
		s.Post(func() { s.mu.Lock(); defer s.mu.Unlock(); fn() })
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

// Load reads workspaces, credentials and settings and opens 最近会话, the
// workspace of tasks outside a project. Without it, it adds it (slow: its
// profile installs the bridge plugin), showing that it is being prepared
// meanwhile, and falls back to the first workspace or the settings page if
// that fails. Without a credential the new task page says where to add one.
func (s *Shell) Load() {
	s.reload()
	s.post(func() {
		s.preparing = s.scratchID == ""
		s.openFirst()
	})
	w, err := s.App.EnsureWorkspace(context.Background())
	if w != nil {
		s.reload()
	}
	s.post(func() {
		s.preparing = false
		if err != nil {
			s.notice = "无法准备最近会话的文件夹：" + describe(err)
		}
		s.openFirst()
	})
}

// openFirst shows 最近会话 (else the first workspace), or the model
// settings when there is none and none is being prepared. It keeps a page
// the user has gone to.
func (s *Shell) openFirst() {
	if s.page != "" && s.page != pageWS {
		return
	}
	switch {
	case s.scratchID != "":
		s.page, s.curWS = pageWS+s.scratchID, s.scratchID
	case len(s.workspaces) > 0:
		s.page, s.curWS = pageWS+s.workspaces[0].ID, s.workspaces[0].ID
	case s.preparing:
		s.page = pageWS
	default:
		s.page, s.prefs.tab = PageSettings, tabModel
	}
}

// projects are the workspaces but 最近会话's.
func (s *Shell) projects() []workspace.Workspace {
	var ps []workspace.Workspace
	for _, w := range s.workspaces {
		if w.ID != s.scratchID {
			ps = append(ps, w)
		}
	}
	return ps
}

// StartFailed records that the app failed to start: the window still opens
// (settings stay usable) but tasks are refused and the reason is shown.
func (s *Shell) StartFailed(err error) {
	s.post(func() {
		s.startErr = describe(err)
		s.notice = "Alcove 没有正常启动：" + s.startErr
	})
}

func (s *Shell) reload() {
	s.reloadPlugins()
	wss, err1 := s.App.Workspaces.All()
	creds, err2 := s.App.Creds.List()
	st, err3 := s.App.Settings()
	scratch, err4 := s.App.ScratchWorkspace()
	idx := map[string][]tasks.Task{}
	if err1 == nil {
		s.App.MigrateTasks(wss)
		for _, w := range wss {
			idx[w.ID], _ = s.App.Tasks.List(w.ID)
		}
	}
	s.post(func() {
		s.workspaces, s.creds, s.settings = wss, creds, st
		// The model field follows the saved value unless it is being edited
		// for the credential still the default.
		if c := s.defaultCred(); c == nil {
			s.model, s.modelFor = "", ""
		} else if c.ID != s.modelFor || s.model == s.savedModel {
			s.model, s.modelFor = c.Model, c.ID
		}
		if c := s.defaultCred(); c != nil {
			s.savedModel = c.Model
		}
		if err1 == nil {
			s.index = idx
		}
		for _, w := range wss {
			if v := s.views[w.ID]; v != nil {
				v.ws = w
				s.addIndexed(v)
			}
		}
		s.scratchID = ""
		if scratch != nil {
			s.scratchID = scratch.ID
		}
		if s.find(s.curWS) == nil {
			s.curWS = s.scratchID
			if s.curWS == "" && len(wss) > 0 {
				s.curWS = wss[0].ID
			}
		}
		if s.find(s.curProj) == nil || s.curProj == s.scratchID {
			s.curProj = ""
			if ps := s.projects(); len(ps) > 0 {
				s.curProj = ps[0].ID
			}
		}
		if err := errors.Join(err1, err2, err3, err4); err != nil {
			s.notice = err.Error()
		}
	})
}

// OnEvent is App.OnEvent: it folds the event into the workspace whose
// runtime sent it.
func (s *Shell) OnEvent(runtimeID string, e alrt.Event) {
	// A runtime a plugin started (the cloud bot's local tasks) is not yet
	// the window's: adopt it by its workspace.
	ws := s.App.RuntimeWorkspace(runtimeID)
	s.post(func() {
		v := s.runtimeView(runtimeID, ws)
		if v == nil {
			return
		}
		sid := events.SessionOf(e.Params)
		t := v.tasks[sid]
		if t == nil {
			if !taskSession(sid) {
				return // subagents and sessions Alcove did not start
			}
			t = s.addTask(v, tasks.Task{ID: sid, Source: sourceOf(v.ws, sid)})
		}
		s.apply(v, t, e)
	})
}

// OnExit is App.OnRuntimeExit: the workspace's next message starts a new
// runtime; a crash is shown.
func (s *Shell) OnExit(k alrt.Key, runtimeID string, err error) {
	s.post(func() {
		id := s.byRuntime[runtimeID]
		delete(s.byRuntime, runtimeID)
		v := s.views[id]
		if v == nil || v.runtimeID != runtimeID {
			return
		}
		v.runtimeID = ""
		for _, t := range v.tasks {
			if t.session.Running() {
				t.session.Status = "idle"
				if err != nil {
					t.err = "运行时退出：" + err.Error()
				}
				s.saveTranscript(v, t)
			}
			t.stopping = false
		}
		// Requests of the gone runtime can no longer be answered.
		for _, a := range v.approvals {
			close(a.answer)
		}
		for _, q := range v.questions {
			close(q.cancel)
		}
		v.approvals, v.questions = nil, nil
	})
}

func (s *Shell) runtimeWorkspace(runtimeID string) string {
	if s.App == nil {
		return ""
	}
	return s.App.RuntimeWorkspace(runtimeID)
}

// runtimeView is the view of the workspace runtimeID belongs to. A runtime
// a plugin started (session.prompt) is adopted, so its approvals and
// questions reach the user on that workspace.
func (s *Shell) runtimeView(runtimeID, ws string) *wsView {
	if id := s.byRuntime[runtimeID]; id != "" {
		return s.views[id]
	}
	if ws == "" {
		return nil
	}
	v := s.view(ws)
	if v != nil {
		s.byRuntime[runtimeID] = ws
		if v.runtimeID == "" {
			v.runtimeID = runtimeID
		}
	}
	return v
}

func (s *Shell) view(id string) *wsView {
	v := s.views[id]
	if v == nil {
		w := s.find(id)
		if w == nil {
			return nil
		}
		v = &wsView{ws: *w, tasks: map[string]*taskView{}}
		s.views[id] = v
		s.addIndexed(v)
	}
	return v
}

func (s *Shell) find(id string) *workspace.Workspace {
	for i := range s.workspaces {
		if s.workspaces[i].ID == id {
			return &s.workspaces[i]
		}
	}
	return nil
}

// Approve is the bridge's approval card: it waits for the user's choice on
// the workspace the runtime belongs to.
func (s *Shell) Approve(ctx context.Context, runtimeID string, req alrt.ApprovalRequest) (string, error) {
	a := &approval{req: req, answer: make(chan string, 1)}
	ok := false
	ws := s.runtimeWorkspace(runtimeID)
	s.post(func() {
		if v := s.runtimeView(runtimeID, ws); v != nil {
			v.approvals = append(v.approvals, a)
			ok = true
		}
	})
	if !ok {
		return alrt.Rejected, nil // nobody to ask: deny
	}
	defer s.post(func() { s.dropApproval(runtimeID, a) })
	select {
	case out, open := <-a.answer:
		if !open {
			return alrt.Cancelled, nil
		}
		return out, nil
	case <-ctx.Done():
		return alrt.Cancelled, nil
	}
}

func (s *Shell) dropApproval(runtimeID string, a *approval) {
	v := s.views[s.byRuntime[runtimeID]]
	if v == nil {
		return
	}
	for i, x := range v.approvals {
		if x == a {
			v.approvals = append(v.approvals[:i], v.approvals[i+1:]...)
			return
		}
	}
}

// AnswerQuestions is the bridge's question form.
func (s *Shell) AnswerQuestions(ctx context.Context, runtimeID string, req alrt.QuestionRequest) ([]alrt.Answer, error) {
	q := &question{req: req, choice: make([]int, len(req.Questions)), multi: make([][]bool, len(req.Questions)),
		custom: make([]string, len(req.Questions)), answer: make(chan []alrt.Answer, 1), cancel: make(chan struct{})}
	for i, qq := range req.Questions {
		q.multi[i] = make([]bool, len(qq.Options))
	}
	ok := false
	ws := s.runtimeWorkspace(runtimeID)
	s.post(func() {
		if v := s.runtimeView(runtimeID, ws); v != nil {
			v.questions = append(v.questions, q)
			ok = true
		}
	})
	if !ok {
		return nil, errors.New("no window to ask in")
	}
	defer s.post(func() {
		if v := s.views[s.byRuntime[runtimeID]]; v != nil {
			for i, x := range v.questions {
				if x == q {
					v.questions = append(v.questions[:i], v.questions[i+1:]...)
					break
				}
			}
		}
	})
	select {
	case ans := <-q.answer:
		return ans, nil
	case <-q.cancel:
		return nil, errors.New("cancelled")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Register, Update and Unregister are the sidecars' UI sink. Contributions
// are kept for the plugin views.
func (s *Shell) Register(plugin string, c sidecar.Contribution) error {
	s.post(func() { s.contribs[plugin+"#"+c.ID] = c })
	return nil
}

func (s *Shell) Update(plugin, id string, data json.RawMessage) error {
	s.post(func() {
		if c, ok := s.contribs[plugin+"#"+id]; ok {
			c.Raw = data
			s.contribs[plugin+"#"+id] = c
		}
	})
	return nil
}

func (s *Shell) Unregister(plugin, id string) {
	s.post(func() { delete(s.contribs, plugin+"#"+id) })
}

// createWorkspace asks for a folder and adds it.
func (s *Shell) createWorkspace() {
	if s.PickFolder == nil {
		return
	}
	go func() {
		dir, err := s.PickFolder()
		if err != nil || dir == "" {
			return
		}
		w, err := s.App.CreateWorkspace(context.Background(), "", dir)
		s.reload()
		s.post(func() {
			if err != nil {
				s.notice = "无法添加工作区：" + describe(err)
				return
			}
			s.notice = ""
			s.page, s.curWS, s.curProj = pageWS+w.ID, w.ID, w.ID
		})
	}()
}

// pickScratchDir asks for 最近会话's folder and moves it there.
func (s *Shell) pickScratchDir() {
	if s.PickFolder == nil {
		return
	}
	go func() {
		dir, err := s.PickFolder()
		if err != nil || dir == "" {
			return
		}
		s.post(func() { s.setScratchDir(dir) })
	}()
}

// setScratchDir moves 最近会话 to dir ("" for the default folder).
func (s *Shell) setScratchDir(dir string) {
	s.scratchBusy = true
	go func() {
		err := s.App.SetScratchDir(context.Background(), dir)
		s.reload()
		s.post(func() {
			s.scratchBusy = false
			if err != nil {
				s.notice = "无法更换默认文件夹：" + describe(err)
			}
		})
	}()
}

func (s *Shell) removeWorkspace(id string) {
	go func() {
		err := s.App.RemoveWorkspace(context.Background(), id)
		s.reload()
		s.post(func() {
			if err != nil {
				s.notice = "无法移除工作区：" + describe(err)
				return
			}
			delete(s.views, id)
			if s.page == pageWS+id {
				s.page = ""
				s.openFirst()
			}
		})
	}()
}

func (s *Shell) addCredential() {
	f := s.form
	go func() {
		var c *credentials.Credential
		var err error
		if p, _ := providerNamed(f.provider); p.ID == credentials.Custom {
			c, err = s.App.Creds.AddCustom(f.label, strings.TrimSpace(f.baseURL), protocolNamed(f.protocol), f.key, f.model)
		} else {
			c, err = s.App.Creds.Add(f.label, p.ID, f.baseURL, f.key, f.model)
		}
		if err == nil {
			st, _ := s.App.Settings()
			if st.DefaultCredential == "" {
				st.DefaultCredential = c.ID
				err = s.App.SaveSettings(st)
			}
		}
		s.reload()
		s.post(func() {
			if err != nil {
				s.form.err = describe(err)
				return
			}
			s.form = credForm{}
		})
	}()
}

func (s *Shell) removeCredential(id string) {
	go func() {
		err := s.App.Creds.Remove(id)
		st, _ := s.App.Settings()
		if err == nil && st.DefaultCredential == id {
			st.DefaultCredential = ""
			err = s.App.SaveSettings(st)
		}
		s.reload()
		if err != nil {
			s.post(func() { s.notice = "无法删除凭据：" + describe(err) })
		}
	}()
}

func (s *Shell) saveSettings(st app.Settings) {
	go func() {
		err := s.App.SaveSettings(st)
		s.reload()
		if err != nil {
			s.post(func() { s.notice = "无法保存设置：" + describe(err) })
		}
	}()
}

// setModel changes the model of credential id.
func (s *Shell) setModel(id, model string) {
	go func() {
		err := s.App.Creds.SetModel(id, model)
		s.reload()
		if err != nil {
			s.post(func() { s.notice = "无法保存模型：" + describe(err) })
		}
	}()
}

// defaultCred is the credential tasks run with: the default one, else the
// first; nil when there is none.
func (s *Shell) defaultCred() *credentials.Credential {
	for i := range s.creds {
		if s.creds[i].ID == s.settings.DefaultCredential {
			return &s.creds[i]
		}
	}
	if len(s.creds) > 0 {
		return &s.creds[0]
	}
	return nil
}

// providerNames lists the credential providers as the settings page names
// them, in credentials.Providers' order.
func providerNames() []string {
	names := make([]string, len(credentials.Providers))
	for i, p := range credentials.Providers {
		names[i] = p.Name
	}
	return names
}

func providerNamed(name string) (credentials.Provider, bool) {
	for _, p := range credentials.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return credentials.Provider{}, false
}

// protocolNames lists the protocols a custom endpoint may speak as the
// settings page names them.
func protocolNames() []string {
	names := make([]string, len(credentials.Protocols))
	for i, p := range credentials.Protocols {
		names[i] = p.Name
	}
	return names
}

// protocolNamed is the id of the protocol the settings page calls name.
func protocolNamed(name string) string {
	for _, p := range credentials.Protocols {
		if p.Name == name {
			return p.ID
		}
	}
	return ""
}

// providerName is how the settings page names route id.
func providerName(id string) string {
	if p, ok := credentials.ProviderOf(id); ok {
		return p.Name
	}
	return id
}

// describe turns an error into the line the user sees.
func describe(err error) string {
	switch {
	case errors.Is(err, workspace.ErrTaken):
		return "这个文件夹已经是工作区了"
	case errors.Is(err, workspace.ErrInData):
		return "不能用 Alcove 数据目录里的文件夹"
	case errors.Is(err, workspace.ErrNotDir):
		return "请选择一个文件夹"
	case errors.Is(err, alrt.ErrLocalDisabled):
		return "本地执行已被策略关闭"
	}
	return fmt.Sprint(err)
}

// exportDiagnostics asks where to save the diagnostics bundle and writes it.
func (s *Shell) exportDiagnostics() {
	if s.SaveFile == nil {
		return
	}
	name := "alcove-diagnostics-" + time.Now().Format("20060102-150405") + ".zip"
	go func() {
		path, err := s.SaveFile(name)
		if err != nil || path == "" {
			return
		}
		s.post(func() { s.diagBusy, s.diagStatus = true, "" })
		err = writeDiagnostics(s.App, path)
		s.post(func() {
			s.diagBusy = false
			if err != nil {
				s.diagStatus = "导出失败：" + describe(err)
				return
			}
			s.diagStatus = "已导出到 " + path
		})
	}()
}

func writeDiagnostics(a *app.App, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = a.ExportDiagnostics(context.Background(), f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}
