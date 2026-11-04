package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Plugin conversation views (inbox capability) are kept across restarts:
// the window hands each view's entries over as it changes, one file per
// plugin.

type inboxFiles struct {
	mu    sync.Mutex
	saved map[string]int // plugin → the newest version written
}

func (a *App) inboxPath(plugin string) string {
	name := strings.NewReplacer("/", "__", "\\", "__", ":", "_").Replace(plugin)
	return filepath.Join(a.Layout.PluginInbox(), name+".json")
}

// LoadInbox returns the saved conversation view of plugin, nil if none.
func (a *App) LoadInbox(plugin string) []byte {
	b, err := os.ReadFile(a.inboxPath(plugin))
	if err != nil {
		return nil
	}
	return b
}

// SaveInbox stores version seq of plugin's conversation view. Saves may
// run concurrently: one older than the last written is dropped.
func (a *App) SaveInbox(plugin string, seq int, data []byte) {
	f := &a.inbox
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saved == nil {
		f.saved = map[string]int{}
	}
	if seq <= f.saved[plugin] {
		return
	}
	path := a.inboxPath(plugin)
	os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	err := os.WriteFile(tmp, data, 0o600)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		a.pluginLog("host", "warn", "saving the conversation view of "+plugin+": "+err.Error())
		return
	}
	f.saved[plugin] = seq
}

// DropInbox removes plugin's saved conversation view (uninstall).
func (a *App) DropInbox(plugin string) {
	a.inbox.mu.Lock()
	defer a.inbox.mu.Unlock()
	os.Remove(a.inboxPath(plugin))
	delete(a.inbox.saved, plugin)
}
