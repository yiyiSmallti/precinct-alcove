package shell

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// The runtime panel (settings, 运行时): the running dsh runtimes, one per
// open workspace plus those plugins started, each to pin or stop.

// runtimesState is the runtime panel's own state, by runtime key.
type runtimesState struct {
	confirm  string          // the busy runtime whose stop waits for a second click
	stopping map[string]bool // stops under way
	errs     map[string]string
	ticking  bool // a look again is scheduled
}

func (s *Shell) runtimeSettings(c *ui.Context, pal *palette) {
	t := c.Theme()
	list := s.App.RunningRuntimes()
	settingsGroup(c, pal, fmt.Sprintf("运行中 · %d", len(list)), func() {
		if len(list) == 0 {
			ui.Text(c, "没有运行中的运行时。在工作区里发消息时按需启动，空闲 10 分钟后自动退出。").FontSize(12.5).TextColor(t.TextMuted).Padding(14, 16)
			return
		}
		for _, info := range list {
			s.runtimeRow(c, pal, info)
		}
	})
	ui.Text(c, "钉住的运行时空闲时也不退出，重新启动后仍然钉住，直到取消或退出 Alcove。停止会中断正在进行的任务，下次发消息时接着原来的会话。").FontSize(11.5).TextColor(t.TextMuted).Padding(0, 2)
	// Runtimes come and go on their own (idle exits, plugins): look again
	// in a while.
	if !s.runtimes.ticking {
		s.runtimes.ticking = true
		time.AfterFunc(5*time.Second, func() { s.post(func() { s.runtimes.ticking = false }) })
	}
}

func (s *Shell) runtimeRow(c *ui.Context, pal *palette, info alrt.Info) {
	t := c.Theme()
	key := info.Key.String()
	name, owned := s.runtimeName(info.Key)
	st := &s.runtimes
	stopping := st.stopping[key]
	ui.Row(c).Key(key).Padding(14, 16).Gap(16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, name).FontSize(13).SingleLine().Shrink(1).MinWidth(0)
				if !owned {
					tag(c, pal, "插件")
				}
			})
			ui.Text(c, shortDir(info.Cwd)).FontSize(11.5).TextColor(t.TextMuted).SingleLine().Selectable()
			if e := st.errs[key]; e != "" {
				ui.Text(c, e).FontSize(11.5).TextColor(t.Danger).Selectable()
			}
		})
		ui.Column(c).Width(150).Shrink(0).Gap(2).Children(func() {
			switch {
			case stopping:
				status(c, pal.subtle, "正在停止")
			case info.Busy:
				status(c, pal.running, "运行中")
			case info.Pinned:
				status(c, pal.done, "空闲 · 已钉住")
			default:
				status(c, pal.done, "空闲")
			}
			sub := "启动于 " + when(info.Started)
			if !info.IdleExit.IsZero() && !stopping {
				sub = info.IdleExit.Local().Format("15:04") + " 空闲退出"
			}
			ui.Text(c, sub).FontSize(11).TextColor(pal.subtle).SingleLine()
		})
		pinned := info.Pinned
		if ui.Switch(c, &pinned).Label("钉住").Disabled(stopping).Changed() && !stopping {
			s.App.PinRuntime(info.Key, pinned)
		}
		var b ui.Element
		if st.confirm == key {
			b = styledButton(c, "确认停止", "danger")
		} else {
			b = ui.Button(c, "停止")
		}
		if b.Disabled(stopping).Clicked() && !stopping {
			if info.Busy && st.confirm != key {
				st.confirm = key
				return
			}
			s.stopRuntime(info.Key)
		}
	})
}

// runtimeName is what a runtime is called: its workspace's name, or for a
// plugin's own profile the profile's.
func (s *Shell) runtimeName(k alrt.Key) (string, bool) {
	for _, w := range s.workspaces {
		if w.Profile == k.Profile && w.Dir == k.Cwd {
			return w.Name, true
		}
	}
	return k.Profile, false
}

func (s *Shell) stopRuntime(k alrt.Key) {
	st := &s.runtimes
	key := k.String()
	if st.stopping == nil {
		st.stopping, st.errs = map[string]bool{}, map[string]string{}
	}
	st.confirm, st.stopping[key] = "", true
	delete(st.errs, key)
	go func() {
		err := s.App.StopRuntime(k)
		s.post(func() {
			delete(st.stopping, key)
			if err != nil {
				st.errs[key] = "停止时出错：" + err.Error()
			}
		})
	}()
}
