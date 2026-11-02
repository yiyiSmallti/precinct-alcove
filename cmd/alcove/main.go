// Command alcove is the desktop app: it loads Alcove's state, starts the
// bridge and the plugins' sidecars, and opens the window.
//
// The bundled runtime is runtime/ in the app's resources (next to the
// executable on Windows, Contents/Resources in the macOS .app), staged by
// installer/stage-runtime.sh. For development these variables point at a
// runtime elsewhere:
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
	"github.com/yiyiSmallti/precinct-alcove/internal/update"
)

// Version is the version of a build without mygo.json's (go run).
var Version = "0.1.0-dev"

func main() {
	data, err := paths.DataDir()
	if err != nil {
		log.Fatal(err)
	}
	mygo.App.SetName("Alcove")
	if v := mygo.App.Version(); v != "" {
		Version = v
	}
	// A second start hands its arguments (e.g. an alcove:// link) to the
	// running one, whose OnOpenURL gets it, and exits.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	setupLogs(paths.Layout{Root: data}.Logs())
	log.Printf("alcove %s starting", Version)
	nodeDir, rt := bundledRuntime()
	node := "node"
	if runtime.GOOS == "windows" {
		node = "node.exe"
	}
	cfg := app.Config{
		DataDir:       data,
		AlcoveVersion: Version,
		Node:          env("ALCOVE_NODE", filepath.Join(nodeDir, node)),
		DSHBin:        env("ALCOVE_DSH", filepath.Join(rt, "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")),
		ToolDirs:      []string{env("ALCOVE_TOOLDIR", filepath.Join(rt, "bin")), rt},
		BridgeTgz:     env("ALCOVE_BRIDGE_TGZ", filepath.Join(rt, "alcove-bridge.tgz")),
	}
	s := shell.New(nil)
	s.Version, s.Updater = Version, &update.MyGo{}
	cfg.Answerer, cfg.SidecarUI, cfg.Inbox = s, s, s.Inbox
	a, err := app.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	s.App = a
	a.OnEvent, a.OnRuntimeExit = s.OnEvent, s.OnExit
	a.Sidecars.OnState = s.OnSidecarState

	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { a.Close() })
	// alcove:// links: the one Alcove was started with, and those of later
	// starts. They go to the plugins once their sidecars are up.
	ready := make(chan struct{})
	mygo.App.OnOpenURL(func(u string) {
		go func() {
			<-ready
			if err := a.Sidecars.DeepLink(context.Background(), u); err != nil {
				log.Printf("deep link %s: %v", u, err)
			}
		}()
	})
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
		bg := newBackground(win)
		s.OnBackground = bg.set
		mygo.App.OnSecondInstance(func([]string, string) { bg.show() })
		s.SaveFile = func(name string) (string, error) {
			return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, Title: "导出诊断包", DefaultPath: name,
				Filters: []mygo.FileFilter{{Name: "Zip", Extensions: []string{"zip"}}}})
		}
		s.PickFile = func() (string, error) {
			files, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "选择插件包",
				Filters: []mygo.FileFilter{{Name: "Alcove 插件包", Extensions: []string{"tgz"}}}})
			if err != nil || len(files) == 0 {
				return "", err
			}
			return files[0], nil
		}
		go func() {
			if err := a.Start(context.Background()); err != nil {
				log.Printf("start: %v", err)
				s.StartFailed(err)
			}
			s.Load()
			close(ready)
			s.StartUpdates(context.Background())
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// bundledRuntime returns where the shipped node is and the runtime
// directory (dsh, pnpm shim, bridge); in a built app both are runtime/ in
// its resources.
func bundledRuntime() (nodeDir, rt string) {
	res, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		return "runtime", "runtime"
	}
	if rt := filepath.Join(res, "runtime"); exists(rt) {
		return rt, rt
	}
	// go run: resources/ as stage-runtime.sh left it, platform directories
	// unmerged; for macOS node is per CPU and the rest in darwin/.
	plat := filepath.Join(res, runtime.GOOS+"-"+runtime.GOARCH, "runtime")
	if runtime.GOOS == "darwin" {
		return plat, filepath.Join(res, "darwin", "runtime")
	}
	return plat, plat
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
