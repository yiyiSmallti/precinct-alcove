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
	"sort"
	"sync"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
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
	// Lang picks displayReason texts ("zh", "en").
	Lang string

	mu         sync.Mutex
	page       string
	workspaces []workspace.Workspace
	views      map[string]*wsView // by workspace id
	byRuntime  map[string]string  // runtime id → workspace id
	creds      []credentials.Credential
	settings   app.Settings
	form       credForm
	model      string // the model field being edited
	notice     string // one line shown at the top, e.g. an error
	contribs   map[string]sidecar.Contribution
	inbox      map[string][]json.RawMessage
}

// wsView is one workspace's conversation.
type wsView struct {
	ws        workspace.Workspace
	session   *events.Session
	runtimeID string
	starting  bool
	err       string
	input     string
	approvals []*approval
	questions []*question
}

type approval struct {
	req    alrt.ApprovalRequest
	answer chan string
}

type question struct {
	req    alrt.QuestionRequest
	choice []int // chosen option per question
	custom []string
	answer chan []alrt.Answer
	cancel chan struct{}
}

type credForm struct {
	label, provider, baseURL, key string
	err                           string
}

// New returns a shell for a; call Load once a is started.
func New(a *app.App) *Shell {
	return &Shell{App: a, Lang: "zh", views: map[string]*wsView{}, byRuntime: map[string]string{},
		contribs: map[string]sidecar.Contribution{}, inbox: map[string][]json.RawMessage{}}
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

// Load reads workspaces, credentials and settings, and opens the first
// workspace (or the settings page when there is none, or no credential).
func (s *Shell) Load() {
	s.reload()
	s.post(func() {
		switch {
		case len(s.creds) == 0 || len(s.workspaces) == 0:
			s.page = PageSettings
		default:
			s.page = pageWS + s.workspaces[0].ID
		}
	})
}

func (s *Shell) reload() {
	wss, err1 := s.App.Workspaces.All()
	creds, err2 := s.App.Creds.List()
	st, err3 := s.App.Settings()
	s.post(func() {
		if s.model == s.settings.Model {
			s.model = st.Model // not being edited: follow the saved value
		}
		s.workspaces, s.creds, s.settings = wss, creds, st
		for _, w := range wss {
			if v := s.views[w.ID]; v != nil {
				v.ws = w
			}
		}
		if err := errors.Join(err1, err2, err3); err != nil {
			s.notice = err.Error()
		}
	})
}

// OnEvent is Runtimes.OnEvent: it folds the event into the workspace whose
// runtime sent it.
func (s *Shell) OnEvent(runtimeID string, e alrt.Event) {
	s.post(func() {
		if v := s.views[s.byRuntime[runtimeID]]; v != nil && v.session != nil {
			v.session.Apply(e.Method, e.Params)
		}
	})
}

// OnExit is Runtimes.OnExit: the workspace's next message starts a new
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
		if err != nil {
			v.err = "运行时退出：" + err.Error()
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

// sessionID is the one conversation each workspace keeps; dsh persists it,
// so it survives restarts.
func sessionID(w workspace.Workspace) string { return "main-" + w.ID }

func (s *Shell) view(id string) *wsView {
	v := s.views[id]
	if v == nil {
		for _, w := range s.workspaces {
			if w.ID == id {
				v = &wsView{ws: w, session: events.NewSession(sessionID(w))}
				s.views[id] = v
			}
		}
	}
	return v
}

// send starts the workspace's runtime if needed and queues the message.
func (s *Shell) send(v *wsView, text string) {
	v.starting, v.err = true, ""
	id := v.ws.ID
	sid := v.session.ID
	go func() {
		ctx := context.Background()
		p, err := s.App.OpenWorkspace(ctx, id)
		if err == nil {
			s.post(func() {
				v.runtimeID = p.ID
				s.byRuntime[p.ID] = id
			})
			// A restored session continues; a new one is created by prompt.
			p.Resume(ctx, sid)
			_, err = p.PromptText(ctx, sid, text)
		}
		s.post(func() {
			v.starting = false
			if err != nil {
				v.err = describe(err)
			}
		})
	}()
}

// Approve is the bridge's approval card: it waits for the user's choice on
// the workspace the runtime belongs to.
func (s *Shell) Approve(ctx context.Context, runtimeID string, req alrt.ApprovalRequest) (string, error) {
	a := &approval{req: req, answer: make(chan string, 1)}
	ok := false
	s.post(func() {
		if v := s.views[s.byRuntime[runtimeID]]; v != nil {
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
	q := &question{req: req, choice: make([]int, len(req.Questions)), custom: make([]string, len(req.Questions)),
		answer: make(chan []alrt.Answer, 1), cancel: make(chan struct{})}
	ok := false
	s.post(func() {
		if v := s.views[s.byRuntime[runtimeID]]; v != nil {
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

// Inbox keeps a plugin's conversation items; a repeated id replaces the
// earlier item.
func (s *Shell) Inbox(plugin string, item json.RawMessage) error {
	var head struct {
		ID string `json:"id"`
	}
	json.Unmarshal(item, &head)
	s.post(func() {
		items := s.inbox[plugin]
		for i, it := range items {
			var h struct {
				ID string `json:"id"`
			}
			json.Unmarshal(it, &h)
			if h.ID == head.ID {
				items[i] = item
				return
			}
		}
		s.inbox[plugin] = append(items, item)
	})
	return nil
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
			s.page = pageWS + w.ID
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
			s.page = PageSettings
			if len(s.workspaces) > 0 {
				s.page = pageWS + s.workspaces[0].ID
			}
		})
	}()
}

func (s *Shell) addCredential() {
	f := s.form
	go func() {
		c, err := s.App.Creds.Add(f.label, f.provider, f.baseURL, f.key)
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

// providerIDs lists the credential providers in a stable order.
func providerIDs() []string {
	var ids []string
	for id := range credentials.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
