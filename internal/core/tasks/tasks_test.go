package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Path: filepath.Join(dir, "tasks.json"), Dir: filepath.Join(dir, "tasks")}
	if l, err := s.List("w1"); err != nil || len(l) != 0 {
		t.Fatalf("empty store: %v %v", l, err)
	}
	t0 := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	s.Put("w1", Task{ID: "a", Title: "旧", UpdatedAt: t0})
	s.Put("w1", Task{ID: "b", Title: "新", UpdatedAt: t0.Add(time.Hour)})
	s.Put("w2", Task{ID: "c", UpdatedAt: t0})
	s.Put("w1", Task{ID: "a", Title: "旧（改）", UpdatedAt: t0})
	l, _ := s.List("w1")
	if len(l) != 2 || l[0].ID != "b" || l[1].Title != "旧（改）" {
		t.Fatalf("list %+v", l)
	}
	if err := s.SaveTranscript("w1", "b", map[string]int{"seq": 3}); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := s.LoadTranscript("w1", "b", &got); err != nil || got["seq"] != 3 {
		t.Fatalf("transcript %v %v", got, err)
	}
	s.Rename("w1", "b", "新标题")
	s.Touch("w1", "a", t0.Add(2*time.Hour))
	if l, _ := s.List("w1"); l[0].ID != "a" || l[1].Title != "新标题" {
		t.Fatalf("after rename/touch %+v", l)
	}
	s.Remove("w1", "b")
	if err := s.LoadTranscript("w1", "b", &got); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("transcript kept: %v", err)
	}
	if l, _ := s.List("w1"); len(l) != 1 {
		t.Fatalf("after remove %+v", l)
	}
	if s.Migrated("w1") {
		t.Fatal("migrated before")
	}
	s.Migrate("w1", &Task{ID: "main-w1", UpdatedAt: t0.Add(-time.Hour)})
	s.Migrate("w1", &Task{ID: "main-w1"}) // once only
	if l, _ := s.List("w1"); len(l) != 2 || !s.Migrated("w1") {
		t.Fatalf("after migrate %+v", l)
	}
	s.RemoveWorkspace("w1")
	if l, _ := s.List("w1"); len(l) != 0 || s.Migrated("w1") {
		t.Fatalf("after workspace removal %+v", l)
	}
	if l, _ := s.List("w2"); len(l) != 1 {
		t.Fatal("other workspace touched")
	}
}

func TestSafeKeepsIDsApart(t *testing.T) {
	seen := map[string]string{}
	for _, id := range []string{"task-a:b", "task-a_b", "task-a/b", "task-a%3Ab", "..", ".", "task-" + strings.Repeat(":", 120), "task-" + strings.Repeat(".", 120)} {
		n := safe(id)
		if o, ok := seen[n]; ok {
			t.Fatalf("%q and %q both map to %q", o, id, n)
		}
		if strings.ContainsAny(n, `/\:.`) {
			t.Fatalf("%q maps to %q", id, n)
		}
		if len(n) > maxName+40 {
			t.Fatalf("%q is %d bytes", n, len(n))
		}
		seen[n] = id
	}
}
