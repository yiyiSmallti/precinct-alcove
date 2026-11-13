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
//
// Development builds (version ending in -dev) also read
// ALCOVE_DEV_TRUSTED_KEYS and ALCOVE_DEV_ALLOW_UNSIGNED (trust.go).
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
	// Tasks outside a project (最近会话) live in ~/Alcove, where Finder shows what its tasks made;
	// workspaces may not be inside the data directory.
	if home, err := os.UserHomeDir(); err == nil {
		cfg.ScratchDir = filepath.Join(home, "Alcove")
	}
	if cfg.TrustedKeys, cfg.AllowUnsigned, err = trust(Version, os.Getenv); err != nil {
		log.Fatal(err)
	}
	s := shell.New(nil)
	s.Version, s.Updater = Version, &update.MyGo{}
	cfg.Answerer, cfg.SidecarUI, cfg.Inbox, cfg.Browser = s, s, s.Inbox, s
	cfg.ConfirmInstall, cfg.PluginsChanged = s.ConfirmInstall, s.PluginsChanged
	cfg.ConfirmTask = s.ConfirmTask
	// The plugin market ships with Alcove (installer/stage-market.sh).
	cfg.Preinstall, _ = filepath.Glob(filepath.Join(rt, "plugins", "*.tgz"))
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
		opts := mygo.WindowOptions{Title: "Alcove", Width: 1280, Height: 800, MinWidth: 960, MinHeight: 600, Content: ui.View(s.View)}
		if runtime.GOOS == "darwin" {
			// The sidebar sits on the window's material, with the traffic
			// lights over it, as native Mac apps do.
			opts.TitleBarStyle = mygo.TitleBarHidden
			opts.Vibrancy = mygo.VibrancySidebar
		}
		win := mygo.NewWindow(opts)
		s.Post = win.Update
		// The browser's pages keep their sign-ins and site data apart from
		// the user's browsers and from Alcove's own.
		browsing := mygo.NewSession("browser")
		s.NewWebView = func() (*mygo.WebView, error) {
			return win.NewWebView(mygo.WebViewOptions{URL: "about:blank", BackgroundColor: "#ffffff",
				Page: mygo.PageOptions{Session: browsing, SwipeNavigation: true, PinchZoom: true}})
		}
		s.OpenPath = mygo.Shell.OpenPath
		s.Reveal = mygo.Shell.ShowItemInFolder
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
		// New conversation items: a notification (needs a built app) and
		// the unread total on the Dock icon. A click opens the conversation.
		if mygo.NotificationsSupported() {
			s.Notify = func(plugin, title, subtitle, body string) {
				n := mygo.NewNotification(mygo.NotificationOptions{Title: title, Subtitle: subtitle, Body: body, Group: plugin})
				n.OnClick(func() {
					bg.show()
					s.OpenInbox(plugin)
				})
				if err := n.Show(); err != nil && err != mygo.ErrNotificationsDenied {
					log.Printf("notification: %v", err)
				}
			}
		}
		s.SetBadge = mygo.App.Dock.SetBadge
		s.Active = mygo.App.IsActive
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
	// go run: resources/ as stage-runtime.sh left it, one directory per
	// platform.
	plat := filepath.Join(res, runtime.GOOS+"-"+runtime.GOARCH, "runtime")
	return plat, plat
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
