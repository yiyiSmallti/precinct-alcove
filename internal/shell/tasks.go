package shell

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// Tasks (the sidebar's list): each is a dsh session of the workspace. The
// user's start as "chat-<id>"; a plugin's (the cloud bot's local tasks)
// are "task-<id>"; "main-<workspace>" is the one conversation a workspace
// had before tasks.

// taskSession reports whether a session id is one the sidebar lists.
func taskSession(id string) bool {
	return strings.HasPrefix(id, "chat-") || strings.HasPrefix(id, "task-") || strings.HasPrefix(id, "main-")
}

// sourceOf is who started a session: "" for the user, else the plugin that
// owns the workspace.
func sourceOf(w workspace.Workspace, id string) string {
	if strings.HasPrefix(id, "task-") {
		return w.OwnerName()
	}
	return ""
}

// addIndexed adds the tasks read from the index that v does not show yet.
func (s *Shell) addIndexed(v *wsView) {
	for _, t := range s.index[v.ws.ID] {
		if v.tasks[t.ID] == nil {
			s.addTask(v, t)
		}
	}
}

// addTask adds a task view, with its transcript when one was saved.
func (s *Shell) addTask(v *wsView, meta tasks.Task) *taskView {
	now := time.Now().UTC()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = now
	}
	t := &taskView{meta: meta, session: events.NewSession(meta.ID), open: map[string]bool{}}
	var snap events.Snapshot
	if err := s.App.Tasks.LoadTranscript(v.ws.ID, meta.ID, &snap); err == nil {
		t.session.Restore(snap)
	} else {
		t.legacy = meta.ID == app.LegacySessionID(v.ws.ID)
	}
	v.tasks[meta.ID] = t
	if !s.indexed(v.ws.ID, meta.ID) {
		s.index[v.ws.ID] = append(s.index[v.ws.ID], meta)
		ws := v.ws.ID
		go s.App.Tasks.Put(ws, meta)
	}
	return t
}

func (s *Shell) indexed(ws, id string) bool {
	for _, t := range s.index[ws] {
		if t.ID == id {
			return true
		}
	}
	return false
}

// apply folds a runtime event into a task, keeping the index's title and
// order and the transcript in step.
func (s *Shell) apply(v *wsView, t *taskView, e alrt.Event) {
	wasRunning := t.session.Running()
	if !t.session.Apply(e.Method, e.Params) {
		return
	}
	title := t.session.Title
	if title == "" && t.meta.Title == "" {
		for _, en := range t.session.Entries {
			if en.Kind == events.User {
				title = titleFrom(en.Text)
				break
			}
		}
	}
	if title != "" && title != t.meta.Title {
		t.meta.Title = title
		ws, id := v.ws.ID, t.meta.ID
		go s.App.Tasks.Rename(ws, id, title)
	}
	if running := t.session.Running(); running != wasRunning {
		t.meta.UpdatedAt = time.Now().UTC()
		ws, id, at := v.ws.ID, t.meta.ID, t.meta.UpdatedAt
		go s.App.Tasks.Touch(ws, id, at)
		if !running {
			t.stopping = false
			s.saveTranscript(v, t)
		}
	}
}

// saveTranscript writes what the task shows, for after a restart.
func (s *Shell) saveTranscript(v *wsView, t *taskView) {
	snap := t.session.Snapshot()
	ws, id := v.ws.ID, t.meta.ID
	go s.App.Tasks.SaveTranscript(ws, id, snap)
}

// titleFrom makes a task title from what the user typed: its first line,
// cut to about 40 characters.
func titleFrom(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > 40 {
		r := []rune(line)
		line = string(r[:40]) + "…"
	}
	return line
}

// taskTitle is what the list and header call a task.
func taskTitle(t *taskView) string {
	switch {
	case t.meta.Title != "":
		return t.meta.Title
	case t.legacy:
		return "之前的对话"
	case t.meta.Source != "":
		return "云端任务"
	}
	return "新任务"
}

// ordered lists a workspace's tasks, most recent first.
func ordered(v *wsView) []*taskView {
	out := make([]*taskView, 0, len(v.tasks))
	for _, t := range v.tasks {
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].meta, out[j].meta
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
	return out
}

// pending lists what a task waits on the user for. Requests whose session
// the window does not know go with the task shown (or the new-task page).
func (v *wsView) pending(t *taskView) (as []*approval, qs []*question) {
	mine := func(sid string) bool {
		if t != nil && sid == t.meta.ID {
			return true
		}
		return v.tasks[sid] == nil && (t == nil && v.current == "" || t != nil && v.current == t.meta.ID)
	}
	for _, a := range v.approvals {
		if mine(a.req.SessionID) {
			as = append(as, a)
		}
	}
	for _, q := range v.questions {
		if mine(q.req.SessionID) {
			qs = append(qs, q)
		}
	}
	return as, qs
}

// waiting reports whether a task has an approval or question open.
func (v *wsView) waiting(t *taskView) bool {
	for _, a := range v.approvals {
		if a.req.SessionID == t.meta.ID {
			return true
		}
	}
	for _, q := range v.questions {
		if q.req.SessionID == t.meta.ID {
			return true
		}
	}
	return false
}

// waitingCount is how many tasks of the workspace wait on the user.
func (v *wsView) waitingCount() int {
	n := 0
	for _, t := range v.tasks {
		if v.waiting(t) {
			n++
		}
	}
	return n
}

// newTask shows the new-task page of workspace id.
func (s *Shell) newTask(id string) {
	if v := s.view(id); v != nil {
		v.current = ""
		s.page, s.curWS = pageWS+id, id
	}
}

// openTask shows one task.
func (s *Shell) openTask(v *wsView, id string) {
	v.current = id
	s.page, s.curWS = pageWS+v.ws.ID, v.ws.ID
}

// send starts the workspace's runtime if needed and queues the message on
// task t; a nil t starts a new task.
func (s *Shell) send(v *wsView, t *taskView, text string) {
	if s.startErr != "" {
		msg := "Alcove 没有正常启动，暂时不能执行任务：" + s.startErr
		if t == nil {
			v.err = msg
		} else {
			t.err = msg
		}
		return
	}
	if t == nil {
		v.err = ""
		t = s.addTask(v, tasks.Task{ID: "chat-" + newID()[:16], Title: titleFrom(text)})
		v.current = t.meta.ID
	}
	t.starting, t.err = true, ""
	t.meta.UpdatedAt = time.Now().UTC()
	id, sid, at := v.ws.ID, t.meta.ID, t.meta.UpdatedAt
	go s.App.Tasks.Touch(id, sid, at)
	go func() {
		ctx := context.Background()
		s.App.RefreshBridge(ctx, id)
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
			t.starting = false
			if err != nil {
				t.err = describe(err)
			}
		})
	}()
}

// stop cancels a task's running turn.
func (s *Shell) stop(v *wsView, t *taskView) {
	t.stopping, t.err = true, ""
	rt, sid := v.runtimeID, t.meta.ID
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		err := s.App.CancelSession(ctx, rt, sid)
		cancel()
		s.post(func() {
			if err != nil {
				t.stopping, t.err = false, "停不下来："+describe(err)
			}
		})
	}()
}

// forget removes a task from the list; dsh keeps the conversation.
func (s *Shell) forget(v *wsView, t *taskView) {
	if t.session.Running() {
		return
	}
	delete(v.tasks, t.meta.ID)
	idx := s.index[v.ws.ID]
	for i := range idx {
		if idx[i].ID == t.meta.ID {
			s.index[v.ws.ID] = append(idx[:i], idx[i+1:]...)
			break
		}
	}
	if v.current == t.meta.ID {
		v.current = ""
	}
	ws, id := v.ws.ID, t.meta.ID
	go s.App.Tasks.Remove(ws, id)
}
