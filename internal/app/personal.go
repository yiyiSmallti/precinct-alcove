package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/personal"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// PersonalHomeEnv tells the dsh of a personal workspace where the personal
// context is; the bridge bundle points dsh's instructions and skills at it.
// Runtimes of org workspaces and of plugins never get it.
const PersonalHomeEnv = "ALCOVE_PERSONAL_HOME"

// History limits: a page carries at most this many conversations and about
// this much text, each message at most historyText runes, and a
// conversation at most historyConversation bytes of its latest messages.
const (
	historyLimit        = 20
	historyMaxLimit     = 50
	historyBytes        = 96 << 10
	historyConversation = 24 << 10
	historyText         = 4000
)

// personalService serves the personal_history and personal_context
// capabilities.
type personalService struct{ a *App }

func (p personalService) store() *personal.Store { return p.a.Personal }

func (p personalService) Context(context.Context) (sidecar.PersonalContext, error) {
	prof, err := p.store().Profile()
	if err != nil {
		return sidecar.PersonalContext{}, err
	}
	list, err := p.store().Skills()
	if err != nil {
		return sidecar.PersonalContext{}, err
	}
	out := sidecar.PersonalContext{Profile: prof, Skills: make([]sidecar.PersonalSkill, 0, len(list))}
	for _, s := range list {
		out.Skills = append(out.Skills, sidecar.PersonalSkill{Name: s.Name, Description: s.Description, Source: s.Source})
	}
	return out, nil
}

func (p personalService) Skill(_ context.Context, name string) (sidecar.PersonalSkill, error) {
	s, err := p.store().Skill(name)
	if errors.Is(err, os.ErrNotExist) {
		return sidecar.PersonalSkill{}, sidecar.InvalidPersonal{Err: fmt.Errorf("there is no personal skill %s", name)}
	}
	if err != nil {
		return sidecar.PersonalSkill{}, err
	}
	return sidecar.PersonalSkill{Name: s.Name, Description: s.Description, Body: s.Body, Source: s.Source}, nil
}

func (p personalService) SetProfile(_ context.Context, plugin, text string) error {
	if err := p.store().SetProfile(text); err != nil {
		return sidecar.InvalidPersonal{Err: err}
	}
	p.a.pluginLog(plugin, "info", fmt.Sprintf("set the personal profile (%d bytes)", len(text)))
	return nil
}

func (p personalService) PutSkill(_ context.Context, plugin string, s sidecar.PersonalSkill) error {
	if err := p.store().PutSkill(personal.Skill{Name: s.Name, Description: s.Description, Body: s.Body, Source: plugin}); err != nil {
		return sidecar.InvalidPersonal{Err: err}
	}
	p.a.pluginLog(plugin, "info", "wrote the personal skill "+s.Name)
	return nil
}

func (p personalService) RemoveSkill(_ context.Context, plugin, name string) error {
	if err := p.store().RemoveSkill(name); err != nil {
		return sidecar.InvalidPersonal{Err: err}
	}
	p.a.pluginLog(plugin, "info", "removed the personal skill "+name)
	return nil
}

// History pages through the tasks the user started in personal workspaces,
// by when they last moved, oldest first, as their views last saved them. A
// task with no saved view (never shown since it ran) is skipped.
func (p personalService) History(_ context.Context, plugin string, q sidecar.HistoryQuery) (sidecar.History, error) {
	var after time.Time
	if q.After != "" {
		t, err := time.Parse(time.RFC3339Nano, q.After)
		if err != nil {
			return sidecar.History{}, sidecar.InvalidPersonal{Err: fmt.Errorf("after is not a cursor this host gave: %v", err)}
		}
		after = t
	}
	limit := q.Limit
	if limit == 0 {
		limit = historyLimit
	}
	limit = min(limit, historyMaxLimit)
	all, err := p.a.Workspaces.All()
	if err != nil {
		return sidecar.History{}, err
	}
	type item struct {
		ws   workspace.Workspace
		task tasks.Task
	}
	var items []item
	for _, w := range all {
		if w.OwnerName() != workspace.Personal {
			continue
		}
		list, err := p.a.Tasks.List(w.ID)
		if err != nil {
			return sidecar.History{}, err
		}
		for _, t := range list {
			if t.Source == "" && t.UpdatedAt.After(after) {
				items = append(items, item{w, t})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].task.UpdatedAt.Before(items[j].task.UpdatedAt) })
	out := sidecar.History{Conversations: []sidecar.Conversation{}}
	size := 0
	for _, it := range items {
		if len(out.Conversations) == limit || (len(out.Conversations) > 0 && size >= historyBytes) {
			out.More = true
			break
		}
		cursor := it.task.UpdatedAt.UTC().Format(time.RFC3339Nano)
		out.Next = cursor
		var snap events.Snapshot
		if err := p.a.Tasks.LoadTranscript(it.ws.ID, it.task.ID, &snap); err != nil {
			continue
		}
		c := conversation(it.ws, it.task, snap)
		if len(c.Messages) == 0 {
			continue
		}
		for _, m := range c.Messages {
			size += len(m.Text)
		}
		out.Conversations = append(out.Conversations, c)
	}
	if n := len(out.Conversations); n > 0 {
		p.a.pluginLog(plugin, "info", fmt.Sprintf("read %d personal conversation(s)", n))
	}
	return out, nil
}

// conversation keeps the user's and the agent's text of a task view, the
// latest historyConversation bytes of it.
func conversation(w workspace.Workspace, t tasks.Task, snap events.Snapshot) sidecar.Conversation {
	var msgs []sidecar.HistoryMessage
	for _, e := range snap.Entries {
		if (e.Kind != events.User && e.Kind != events.Assistant) || e.Text == "" {
			continue
		}
		text := e.Text
		if utf8.RuneCountInString(text) > historyText {
			text = string([]rune(text)[:historyText]) + "…"
		}
		msgs = append(msgs, sidecar.HistoryMessage{Role: string(e.Kind), Text: text})
	}
	c := sidecar.Conversation{WorkspaceID: w.ID, Workspace: w.Name, TaskID: t.ID, Title: t.Title,
		UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	size, from := 0, len(msgs)
	for from > 0 && size+len(msgs[from-1].Text) <= historyConversation {
		from--
		size += len(msgs[from].Text)
	}
	if from == len(msgs) && from > 0 { // one message longer than the cap: keep it alone
		from--
	}
	c.Cut = from > 0
	c.Messages = append([]sidecar.HistoryMessage{}, msgs[from:]...)
	return c
}
