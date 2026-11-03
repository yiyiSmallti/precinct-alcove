package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
)

// LegacySessionID is the one conversation each workspace had before tasks:
// it is listed as a task when dsh has it.
func LegacySessionID(ws string) string { return "main-" + ws }

// MigrateTasks adds the old conversation of each workspace that was not
// looked at yet, when dsh persisted one. dsh keeps a session in
// <dsh home>/sessions/<project>/<session id>/, so one walk finds them all.
func (a *App) MigrateTasks(wss []workspace.Workspace) error {
	var todo []workspace.Workspace
	for _, w := range wss {
		if !a.Tasks.Migrated(w.ID) {
			todo = append(todo, w)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	found := map[string]time.Time{}
	root := filepath.Join(a.Layout.DSHHome(), "sessions")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), "main-") {
			if info, err := d.Info(); err == nil {
				found[d.Name()] = info.ModTime()
			}
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, w := range todo {
		id := LegacySessionID(w.ID)
		var t *tasks.Task
		if at, ok := found[id]; ok {
			t = &tasks.Task{ID: id, Title: "之前的对话", CreatedAt: at, UpdatedAt: at}
		}
		if err := a.Tasks.Migrate(w.ID, t); err != nil {
			return err
		}
	}
	return nil
}

// CancelSession stops the running turn of a session in runtime runtimeID.
func (a *App) CancelSession(ctx context.Context, runtimeID, sessionID string) error {
	if runtimeID == "" || !a.Bridge.Attached(runtimeID) {
		return errors.New("这个任务没有在运行")
	}
	return a.Bridge.Call(ctx, runtimeID, "session.cancel", map[string]any{"sessionId": sessionID}, nil)
}
