// Package tasks keeps the list of tasks (conversations) of each workspace,
// so the sidebar can show them without starting a runtime. dsh keeps the
// conversations themselves; this is only the index: id, title, who started
// it and when it last moved.
package tasks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
)

// Task is one conversation of a workspace.
type Task struct {
	ID        string    `json:"id"` // the dsh session id
	Title     string    `json:"title"`
	Source    string    `json:"source,omitempty"` // "" for the user, else the plugin that started it
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type file struct {
	// Workspaces maps a workspace id to its tasks.
	Workspaces map[string][]Task `json:"workspaces"`
	// Migrated lists workspaces whose conversation from before tasks
	// existed ("main-<id>") was looked for.
	Migrated map[string]bool `json:"migrated,omitempty"`
}

// Store keeps tasks.json at Path, and each task's transcript (what the
// conversation view showed) under Dir/<workspace>/<task>.json.
type Store struct {
	Path string
	Dir  string
	mu   sync.Mutex
}

func (s *Store) transcript(ws, id string) string {
	return filepath.Join(s.Dir, safe(ws), safe(id)+".json")
}

// safe keeps ids to one path element.
func safe(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			return '_'
		}
		return r
	}, filepath.Base(id))
}

// SaveTranscript writes a task's transcript.
func (s *Store) SaveTranscript(ws, id string, v any) error {
	if s.Dir == "" {
		return nil
	}
	return fsutil.WriteJSON(s.transcript(ws, id), v, 0o600)
}

// LoadTranscript reads a task's transcript into v; a task without one
// leaves v alone and returns an error matching os.ErrNotExist.
func (s *Store) LoadTranscript(ws, id string, v any) error {
	if s.Dir == "" {
		return os.ErrNotExist
	}
	b, err := os.ReadFile(s.transcript(ws, id))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s *Store) load() (file, error) {
	var f file
	err := fsutil.ReadJSON(s.Path, &f)
	if f.Workspaces == nil {
		f.Workspaces = map[string][]Task{}
	}
	if f.Migrated == nil {
		f.Migrated = map[string]bool{}
	}
	return f, err
}

func (s *Store) edit(fn func(*file)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	fn(&f)
	return fsutil.WriteJSON(s.Path, f, 0o600)
}

// List returns a workspace's tasks, most recently updated first.
func (s *Store) List(ws string) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	out := append([]Task(nil), f.Workspaces[ws]...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, err
}

// Put adds t to workspace ws, or replaces the task with the same id.
func (s *Store) Put(ws string, t Task) error {
	return s.edit(func(f *file) {
		all := f.Workspaces[ws]
		for i := range all {
			if all[i].ID == t.ID {
				all[i] = t
				return
			}
		}
		f.Workspaces[ws] = append(all, t)
	})
}

// Rename sets a task's title; an unknown task is left alone.
func (s *Store) Rename(ws, id, title string) error {
	return s.edit(func(f *file) {
		for i, t := range f.Workspaces[ws] {
			if t.ID == id {
				f.Workspaces[ws][i].Title = title
			}
		}
	})
}

// Touch moves a task to the top of its workspace's list.
func (s *Store) Touch(ws, id string, at time.Time) error {
	return s.edit(func(f *file) {
		for i, t := range f.Workspaces[ws] {
			if t.ID == id {
				f.Workspaces[ws][i].UpdatedAt = at
			}
		}
	})
}

// Remove forgets one task. The conversation stays with dsh.
func (s *Store) Remove(ws, id string) error {
	return s.edit(func(f *file) {
		all := f.Workspaces[ws]
		for i := range all {
			if all[i].ID == id {
				f.Workspaces[ws] = append(all[:i], all[i+1:]...)
				break
			}
		}
		if s.Dir != "" {
			os.Remove(s.transcript(ws, id))
		}
	})
}

// RemoveWorkspace forgets all of a workspace's tasks.
func (s *Store) RemoveWorkspace(ws string) error {
	return s.edit(func(f *file) {
		delete(f.Workspaces, ws)
		delete(f.Migrated, ws)
		if s.Dir != "" {
			os.RemoveAll(filepath.Join(s.Dir, safe(ws)))
		}
	})
}

// Migrated reports whether the workspace's old conversation was looked for.
func (s *Store) Migrated(ws string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, _ := s.load()
	return f.Migrated[ws]
}

// Migrate records that the workspace's old conversation was looked for,
// adding it as a task when found is set.
func (s *Store) Migrate(ws string, found *Task) error {
	return s.edit(func(f *file) {
		if f.Migrated[ws] {
			return
		}
		f.Migrated[ws] = true
		if found != nil {
			f.Workspaces[ws] = append(f.Workspaces[ws], *found)
		}
	})
}
