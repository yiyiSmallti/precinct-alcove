package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Plugin conversation views (inbox capability) are kept across restarts:
// the window hands each view's entries over as it changes, one file per
// plugin.

type inboxFiles struct {
	mu      sync.Mutex
	saved   map[string][2]int // plugin → the newest {gen, version} written
	dropped map[string]int    // plugin → the newest gen uninstalled
}

// inboxPath is plugin's file: its name made safe to read, then a digest of
// the whole name, so names that read alike never share a file.
func (a *App) inboxPath(plugin string) string {
	sum := sha256.Sum256([]byte(plugin))
	return filepath.Join(a.Layout.PluginInbox(), legacyInboxName(plugin)+"-"+hex.EncodeToString(sum[:6])+".json")
}

// legacyInboxName is the file name before the digest, still read once.
func legacyInboxName(plugin string) string {
	return strings.NewReplacer("/", "__", "\\", "__", ":", "_").Replace(plugin)
}

func (a *App) legacyInboxPath(plugin string) string {
	return filepath.Join(a.Layout.PluginInbox(), legacyInboxName(plugin)+".json")
}

// LoadInbox returns the saved conversation view of plugin, nil if none.
func (a *App) LoadInbox(plugin string) []byte {
	b, err := os.ReadFile(a.inboxPath(plugin))
	if errors.Is(err, fs.ErrNotExist) {
		b, err = os.ReadFile(a.legacyInboxPath(plugin))
	}
	if err != nil {
		return nil
	}
	return b
}

// SaveInbox stores version seq of plugin's conversation view gen (one
// per view the window made). Saves may run concurrently: one older than
// the last written is dropped, and so is one of a view uninstalled since.
func (a *App) SaveInbox(plugin string, gen, seq int, data []byte) {
	f := &a.inbox
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saved == nil {
		f.saved = map[string][2]int{}
	}
	if last := f.saved[plugin]; gen <= f.dropped[plugin] || gen < last[0] || (gen == last[0] && seq <= last[1]) {
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
	f.saved[plugin] = [2]int{gen, seq}
	os.Remove(a.legacyInboxPath(plugin))
}

// DropInbox removes plugin's saved conversation view (uninstall); saves
// of views up to gen that are still on their way are dropped too.
func (a *App) DropInbox(plugin string, gen int) {
	f := &a.inbox
	f.mu.Lock()
	defer f.mu.Unlock()
	os.Remove(a.inboxPath(plugin))
	os.Remove(a.legacyInboxPath(plugin))
	delete(f.saved, plugin)
	if f.dropped == nil {
		f.dropped = map[string]int{}
	}
	f.dropped[plugin] = max(f.dropped[plugin], gen)
}
