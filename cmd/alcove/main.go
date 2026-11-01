// Command alcove is the desktop app: it loads Alcove's state, starts the
// bridge and the plugins' sidecars, and opens the window.
//
// The bundled runtime is found next to the executable (runtime/ beside it,
// or Contents/Resources/runtime in a macOS .app). For development these
// variables point at a runtime elsewhere:
//
//	ALCOVE_NODE        node executable
//	ALCOVE_DSH         dsh entry script (node_modules/@deepseek-ai/dsh/lib/bin.js)
//	ALCOVE_TOOLDIR     directory with pnpm, put first on PATH for dsh
//	ALCOVE_BRIDGE_TGZ  the alcove-bridge package
//	ALCOVE_DATA_DIR    data directory (default: the platform's)
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/paths"
	"github.com/yiyiSmallti/precinct-alcove/internal/shell"
)

// Version is set at build time (-ldflags "-X main.Version=...").
var Version = "0.1.0-dev"

func main() {
	data, err := paths.DataDir()
	if err != nil {
		log.Fatal(err)
	}
	rt := bundledRuntime()
	node := "node"
	if runtime.GOOS == "windows" {
		node = "node.exe"
	}
	cfg := app.Config{
		DataDir:       data,
		AlcoveVersion: Version,
		Node:          env("ALCOVE_NODE", filepath.Join(rt, node)),
		DSHBin:        env("ALCOVE_DSH", filepath.Join(rt, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")),
		ToolDirs:      []string{env("ALCOVE_TOOLDIR", filepath.Join(rt, "node_modules", ".bin"))},
		BridgeTgz:     env("ALCOVE_BRIDGE_TGZ", filepath.Join(rt, "alcove-bridge.tgz")),
	}
	s := shell.New(nil)
	cfg.Answerer, cfg.SidecarUI, cfg.Inbox = s, s, s.Inbox
	a, err := app.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	s.App = a
	a.Runtimes.OnEvent, a.Runtimes.OnExit = s.OnEvent, s.OnExit

	mygo.App.SetName("Alcove")
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { a.Close() })
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{Title: "Alcove", Width: 1100, Height: 760, Content: ui.View(s.View)})
		s.Post = win.Update
		s.PickFolder = func() (string, error) {
			dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "选择工作区文件夹", Directory: true, CreateDirectories: true})
			if err != nil || len(dirs) == 0 {
				return "", err
			}
			return dirs[0], nil
		}
		go func() {
			if err := a.Start(context.Background()); err != nil {
				log.Printf("start: %v", err)
				s.StartFailed(err)
			}
			s.Load()
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// bundledRuntime is the runtime directory shipped with the app.
func bundledRuntime() string {
	exe, err := os.Executable()
	if err != nil {
		return "runtime"
	}
	dir := filepath.Dir(exe)
	if runtime.GOOS == "darwin" {
		// Alcove.app/Contents/MacOS/alcove → Contents/Resources/runtime
		if res := filepath.Join(dir, "..", "Resources", "runtime"); exists(res) {
			return res
		}
	}
	return filepath.Join(dir, "runtime")
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
