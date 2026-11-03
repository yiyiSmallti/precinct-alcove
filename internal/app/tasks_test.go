package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
)

func TestMigrateTasks(t *testing.T) {
	a, err := New(Config{DataDir: t.TempDir(), Secrets: &memSecrets{m: map[string][]byte{}}})
	if err != nil {
		t.Fatal(err)
	}
	root := a.Layout.DSHHome()
	os.MkdirAll(filepath.Join(root, "sessions", "--Users-me-proj--", "main-w1"), 0o700)
	os.MkdirAll(filepath.Join(root, "profiles", "ws-w2", "main-w2"), 0o700) // not a session
	wss := []workspace.Workspace{{ID: "w1"}, {ID: "w2"}}
	if err := a.MigrateTasks(wss); err != nil {
		t.Fatal(err)
	}
	l1, _ := a.Tasks.List("w1")
	l2, _ := a.Tasks.List("w2")
	if len(l1) != 1 || l1[0].ID != "main-w1" || len(l2) != 0 {
		t.Fatalf("w1 %+v, w2 %+v", l1, l2)
	}
	// Once only: a forgotten task does not come back.
	a.Tasks.Remove("w1", "main-w1")
	a.MigrateTasks(wss)
	if l1, _ := a.Tasks.List("w1"); len(l1) != 0 {
		t.Fatalf("migrated twice: %+v", l1)
	}
}
