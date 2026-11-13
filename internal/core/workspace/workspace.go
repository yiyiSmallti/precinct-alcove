// Package workspace keeps the list of workspaces (PRD 3.3): each is a
// directory the user chose, with its own dsh profile ws-<id>. Removing a
// workspace forgets it; its files stay.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// Workspace is one workspace record.
type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Dir       string    `json:"dir"`
	Profile   string    `json:"profile"`
	Owner     string    `json:"owner"` // "personal", or the plugin that attached it
	CreatedAt time.Time `json:"createdAt"`
}

// Store keeps workspaces.json at Path. DataDir is Alcove's data directory,
// which may not be used as (or contain) a workspace.
type Store struct {
	Path    string
	DataDir string
	mu      sync.Mutex
}

// Errors the UI shows as is.
var (
	ErrNotDir   = errors.New("workspace folder is not a directory")
	ErrTaken    = errors.New("folder is already a workspace")
	ErrInData   = errors.New("folder is inside Alcove's data directory")
	ErrNotFound = errors.New("no such workspace")
)

// ProfileName is the dsh profile of workspace id.
func ProfileName(id string) string { return "ws-" + id }

// Create adds a workspace for dir.
func (s *Store) Create(name, dir string) (*Workspace, error) {
	dir, err := s.checkDir(dir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(dir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	for _, w := range all {
		if samePath(w.Dir, dir) {
			return nil, fmt.Errorf("%w: %s", ErrTaken, w.Name)
		}
	}
	raw := make([]byte, 6)
	rand.Read(raw)
	id := hex.EncodeToString(raw)
	w := Workspace{ID: id, Name: name, Dir: dir, Profile: ProfileName(id), Owner: "personal", CreatedAt: time.Now().UTC()}
	if err := fsutil.WriteJSON(s.Path, append(all, w), 0o600); err != nil {
		return nil, err
	}
	return &w, nil
}

// Move points workspace id at another folder, checked as Create checks it.
func (s *Store) Move(id, dir string) error {
	dir, err := s.checkDir(dir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	at := -1
	for i, w := range all {
		switch {
		case w.ID == id:
			at = i
		case samePath(w.Dir, dir):
			return fmt.Errorf("%w: %s", ErrTaken, w.Name)
		}
	}
	if at < 0 {
		return ErrNotFound
	}
	all[at].Dir = dir
	return fsutil.WriteJSON(s.Path, all, 0o600)
}

// ByDir returns the workspace of folder dir, or nil.
func (s *Store) ByDir(dir string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	for _, w := range all {
		if samePath(w.Dir, abs) {
			return &w, nil
		}
	}
	return nil, nil
}

// Rename changes a workspace's display name.
func (s *Store) Rename(id, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is empty")
	}
	return s.edit(id, func(all []Workspace, i int) []Workspace { all[i].Name = name; return all })
}

// SetOwner records who owns a workspace: "personal", or the plugin that
// attached it (workspace.attach).
func (s *Store) SetOwner(id, owner string) error {
	return s.edit(id, func(all []Workspace, i int) []Workspace { all[i].Owner = owner; return all })
}

// Remove forgets a workspace. Its folder and files are left alone; its dsh
// profile is removed by the caller after the runtime has stopped.
func (s *Store) Remove(id string) error {
	return s.edit(id, func(all []Workspace, i int) []Workspace { return append(all[:i], all[i+1:]...) })
}

func (s *Store) edit(id string, f func([]Workspace, int) []Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	for i := range all {
		if all[i].ID == id {
			return fsutil.WriteJSON(s.Path, f(all, i), 0o600)
		}
	}
	return ErrNotFound
}

// All returns workspaces sorted by name.
func (s *Store) All() ([]Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, err
}

// Find returns one workspace.
func (s *Store) Find(id string) (*Workspace, error) {
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	for _, w := range all {
		if w.ID == id {
			return &w, nil
		}
	}
	return nil, ErrNotFound
}

// OwnerName is Owner, with records from before owners existed read as
// "personal".
func (w Workspace) OwnerName() string {
	if w.Owner == "" {
		return Personal
	}
	return w.Owner
}

// Personal is the owner of workspaces no plugin attached.
const Personal = "personal"

// Missing reports whether the workspace folder has gone (moved or deleted
// outside Alcove); the UI offers to relocate or remove it.
func (w Workspace) Missing() bool {
	fi, err := os.Stat(w.Dir)
	return err != nil || !fi.IsDir()
}

func (s *Store) checkDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotDir, err)
	}
	if !fi.IsDir() {
		return "", ErrNotDir
	}
	if s.DataDir != "" {
		data, _ := filepath.Abs(s.DataDir)
		if real, err := filepath.EvalSymlinks(data); err == nil {
			data = real
		}
		if within(abs, data) || within(data, abs) {
			return "", ErrInData
		}
	}
	return abs, nil
}

func (s *Store) load() ([]Workspace, error) {
	var all []Workspace
	if err := fsutil.ReadJSON(s.Path, &all); err != nil {
		return nil, fmt.Errorf("workspaces file %s: %w", s.Path, err)
	}
	return all, nil
}

// within reports whether p is base or below it.
func within(p, base string) bool {
	if caseInsensitiveFS {
		p, base = strings.ToLower(p), strings.ToLower(base)
	}
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// samePath compares paths, case-insensitively on Windows and macOS, whose
// default file systems ignore case.
func samePath(a, b string) bool {
	return a == b || (caseInsensitiveFS && strings.EqualFold(a, b))
}

// Source adapts the store to the sidecar API (workspace.list/get).
type Source struct{ Store *Store }

func toSidecar(w Workspace) sidecar.Workspace {
	return sidecar.Workspace{ID: w.ID, Name: w.Name, Dir: w.Dir, Profile: w.Profile, Owner: w.OwnerName()}
}

func (s Source) List(ctx context.Context) ([]sidecar.Workspace, error) {
	all, err := s.Store.All()
	out := make([]sidecar.Workspace, 0, len(all))
	for _, w := range all {
		out = append(out, toSidecar(w))
	}
	return out, err
}

func (s Source) Get(ctx context.Context, id string) (*sidecar.Workspace, error) {
	w, err := s.Store.Find(id)
	if err != nil {
		return nil, err
	}
	sw := toSidecar(*w)
	return &sw, nil
}
