package shell

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/update"
)

type fakeUpdater struct {
	mu         sync.Mutex
	source     string
	release    *update.Release
	installed  bool
	relaunched bool
}

func (f *fakeUpdater) Enabled() bool { return true }

func (f *fakeUpdater) SetSource(base string) error {
	if base != "" {
		if _, err := update.NewMirror(base); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.source = base
	return nil
}

func (f *fakeUpdater) Check(context.Context) (*update.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.release, nil
}

func (f *fakeUpdater) Install(_ context.Context, progress func(done, total int64)) error {
	progress(50, 100)
	progress(100, 100)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.release == nil {
		return errors.New("nothing to install")
	}
	f.installed = true
	return nil
}

func (f *fakeUpdater) Relaunch() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relaunched = true
}

func TestUpdates(t *testing.T) {
	s, _ := newShell(t)
	tt := ui.NewTester(s.View, 1100, 2400)
	f := &fakeUpdater{}
	s.Version, s.Updater = "0.1.0", f
	s.mu.Lock()
	s.page, s.prefs.tab = PageSettings, tabOther
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartUpdates(ctx)
	until(t, tt, s, func() bool { return !s.updates.checked.IsZero() })
	if !tt.HasText("已是最新") {
		t.Fatalf("texts %q", tt.Texts())
	}

	f.mu.Lock()
	f.release = &update.Release{Version: "0.2.0", Notes: "- 修了托盘图标"}
	f.mu.Unlock()
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { return s.updates.release != nil })
	if !tt.HasText("修了托盘图标") {
		t.Fatalf("notes not shown: %q", tt.Texts())
	}
	if err := tt.Click("下载并安装"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { return s.updates.installed == "0.2.0" })
	if err := tt.Click("立即重启"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	ok := f.installed && f.relaunched
	f.mu.Unlock()
	if !ok {
		t.Fatal("not installed and relaunched")
	}
	// A newer release found while the restart is pending is offered for
	// download, not shown as installed.
	f.mu.Lock()
	f.release = &update.Release{Version: "0.3.0"}
	f.mu.Unlock()
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, s, func() bool { return s.updates.release != nil && s.updates.release.Version == "0.3.0" })
	if !tt.HasText("下载并安装") || tt.HasText("立即重启") {
		t.Fatalf("0.3.0 shown as installed: %q", tt.Texts())
	}

	// 更新源: a bad URL is refused and not saved; a mirror is saved and used.
	s.mu.Lock()
	s.updates.source = "http://example.com/rel"
	s.saveUpdateSource()
	s.mu.Unlock()
	until(t, tt, s, func() bool { return s.updates.sourceErr != "" })
	if st, _ := s.App.Settings(); st.UpdateSource != "" {
		t.Fatalf("bad source saved: %q", st.UpdateSource)
	}
	s.mu.Lock()
	s.updates.source = "https://mirror.example.com/alcove/"
	s.saveUpdateSource()
	s.mu.Unlock()
	until(t, tt, s, func() bool { return s.settings.UpdateSource != "" && s.updates.sourceErr == "" })
	f.mu.Lock()
	src := f.source
	f.mu.Unlock()
	if st, _ := s.App.Settings(); st.UpdateSource != "https://mirror.example.com/alcove/" || src != st.UpdateSource {
		t.Fatalf("saved %q, updater uses %q", st.UpdateSource, src)
	}
}
