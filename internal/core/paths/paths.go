// Package paths says where Alcove keeps its data on each platform (SPEC 1).
package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// DataDir is <数据目录>: %LOCALAPPDATA%\Alcove on Windows,
// ~/Library/Application Support/Alcove on macOS, and $XDG_DATA_HOME/alcove
// (default ~/.local/share/alcove) on Linux, which is a development target only.
// ALCOVE_DATA_DIR overrides it (tests, portable runs).
func DataDir() (string, error) {
	if d := os.Getenv("ALCOVE_DATA_DIR"); d != "" {
		return filepath.Abs(d)
	}
	return dataDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func dataDir(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	switch goos {
	case "windows":
		if d := getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Alcove"), nil
		}
		return "", errors.New("LOCALAPPDATA is not set")
	case "darwin":
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, "Library", "Application Support", "Alcove"), nil
	default:
		if d := getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, "alcove"), nil
		}
		h, err := home()
		if err != nil {
			return "", err
		}
		return filepath.Join(h, ".local", "share", "alcove"), nil
	}
}

// Layout names the files and folders under the data directory.
type Layout struct{ Root string }

func (l Layout) Identity() string   { return filepath.Join(l.Root, "identity.json") }
func (l Layout) Workspaces() string { return filepath.Join(l.Root, "workspaces.json") }
func (l Layout) Credentials() string {
	return filepath.Join(l.Root, "credentials.json")
}
func (l Layout) Settings() string { return filepath.Join(l.Root, "settings.json") }
func (l Layout) Tasks() string    { return filepath.Join(l.Root, "tasks.json") }
func (l Layout) Transcripts() string {
	return filepath.Join(l.Root, "tasks")
}
func (l Layout) Plugins() string { return filepath.Join(l.Root, "plugins") }

// PluginInbox holds each plugin's conversation view, one file per plugin.
func (l Layout) PluginInbox() string { return filepath.Join(l.Root, "plugins", "inbox") }

// TaskGrants records the tasks the user let plugins run in their own
// workspaces (task.start).
func (l Layout) TaskGrants() string { return filepath.Join(l.Root, "plugins", "task-grants.json") }

func (l Layout) Logs() string    { return filepath.Join(l.Root, "logs") }
func (l Layout) DSHHome() string { return filepath.Join(l.Root, "dsh") }

// Wallpaper is the user's own picture behind the window, a copy made when
// they pick it, with its thumbnail beside it (wallpaper-thumb.jpg).
func (l Layout) Wallpaper() string { return filepath.Join(l.Root, "wallpaper.jpg") }

// PersonalHome is the user's personal context: AGENTS.md (what the agent
// should know about them) and skills/, which only personal workspaces read.
func (l Layout) PersonalHome() string { return filepath.Join(l.Root, "personal") }
func (l Layout) PluginSettings() string {
	return filepath.Join(l.Root, "plugins", "settings")
}
