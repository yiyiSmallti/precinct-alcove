package sidecar

import (
	"context"
	"encoding/json"
	"errors"
)

// Personal is the user's personal context (what agents in their personal
// workspaces know about them, and their skills there) and the
// conversations of their personal workspaces, for a plugin that learns from
// them (personal_history) and writes what it learned (personal_context).
// Org workspaces, and tasks plugins ran, are never part of either.
type Personal interface {
	History(ctx context.Context, plugin string, q HistoryQuery) (History, error)
	Context(ctx context.Context) (PersonalContext, error)
	Skill(ctx context.Context, name string) (PersonalSkill, error)
	SetProfile(ctx context.Context, plugin, text string) error
	PutSkill(ctx context.Context, plugin string, s PersonalSkill) error
	RemoveSkill(ctx context.Context, plugin, name string) error
}

// HistoryQuery asks for conversations that moved after After (a cursor a
// previous History returned; empty for all), oldest first, at most Limit.
type HistoryQuery struct {
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// History is a page of conversations. Next is the cursor to ask from for
// the following page; More says whether there is one now.
type History struct {
	Conversations []Conversation `json:"conversations"`
	Next          string         `json:"next,omitempty"`
	More          bool           `json:"more"`
}

// Conversation is one task of a personal workspace the user started, as its
// view showed it: the user's messages and the agent's answers, without tool
// calls or reasoning. Long texts are cut, and a long conversation keeps its
// latest messages.
type Conversation struct {
	WorkspaceID string           `json:"workspaceId"`
	Workspace   string           `json:"workspace"`
	TaskID      string           `json:"taskId"`
	Title       string           `json:"title,omitempty"`
	UpdatedAt   string           `json:"updatedAt"`
	Messages    []HistoryMessage `json:"messages"`
	Cut         bool             `json:"cut,omitempty"`
}

// HistoryMessage is one message; Role is user or assistant.
type HistoryMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// PersonalContext is the profile (AGENTS.md) and the skills, without their
// bodies.
type PersonalContext struct {
	Profile string          `json:"profile"`
	Skills  []PersonalSkill `json:"skills"`
}

// PersonalSkill is one skill of the personal context. Source is the plugin
// that wrote it, empty for the user.
type PersonalSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	Source      string `json:"source,omitempty"`
}

func personalRequest(ctx context.Context, svc Personal, plugin, method string, raw json.RawMessage) (any, error) {
	var q struct {
		HistoryQuery
		Text  *string        `json:"text"`
		Name  string         `json:"name"`
		Skill *PersonalSkill `json:"skill"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, invalid(err)
		}
	}
	switch method {
	case "personal.history":
		if q.Limit < 0 {
			return nil, invalid(errors.New("personal.history limit must not be negative"))
		}
		return svc.History(ctx, plugin, q.HistoryQuery)
	case "personal.get":
		return svc.Context(ctx)
	case "personal.getSkill":
		if q.Name == "" {
			return nil, invalid(errors.New("personal.getSkill needs name"))
		}
		return svc.Skill(ctx, q.Name)
	case "personal.setProfile":
		if q.Text == nil {
			return nil, invalid(errors.New("personal.setProfile needs text (empty clears the profile)"))
		}
		return map[string]bool{"ok": true}, wrapInvalid(svc.SetProfile(ctx, plugin, *q.Text))
	case "personal.putSkill":
		if q.Skill == nil {
			return nil, invalid(errors.New("personal.putSkill needs skill {name, description, body}"))
		}
		q.Skill.Source = plugin
		return map[string]bool{"ok": true}, wrapInvalid(svc.PutSkill(ctx, plugin, *q.Skill))
	default: // personal.removeSkill
		if q.Name == "" {
			return nil, invalid(errors.New("personal.removeSkill needs name"))
		}
		return map[string]bool{"ok": true}, wrapInvalid(svc.RemoveSkill(ctx, plugin, q.Name))
	}
}

// InvalidPersonal marks an error of the request's own values (a bad skill
// name, a profile too long), which the sidecar gets as invalid params.
type InvalidPersonal struct{ Err error }

func (e InvalidPersonal) Error() string { return e.Err.Error() }
func (e InvalidPersonal) Unwrap() error { return e.Err }

func wrapInvalid(err error) error {
	var bad InvalidPersonal
	if errors.As(err, &bad) {
		return invalid(bad.Err)
	}
	return err
}
