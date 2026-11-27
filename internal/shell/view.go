package shell

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
)

// View builds the window: the theme's painting filling it, the sidebar on
// glass over the painting, the page chosen there laid on a sheet of paper,
// and beside it the alcove where Index stands (a task, a workspace's home,
// what waits on the user, a conversation). Without a painting the window's
// ground (or its material) takes its place.
func (s *Shell) View(c *ui.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pal := useTheme(c, s.settings.Appearance, s.customWallpaper())
	root := ui.Box(c).Fill()
	pal.win = root.LayoutBounds()
	if pal.win.W == 0 {
		// The panes size themselves from the window: draw again once known.
		c.Invalidate()
	}
	if pal.wall == nil && !pal.vibrant {
		root.Background(pal.canvas)
	}
	// Absolute children paint after those in flow, in order: the painting
	// first, then the window over it.
	root.Children(func() {
		if pal.wall != nil {
			paint(c, pal.wall.sharp)
		}
		s.window(c, pal, ui.Row(c).Absolute().Top(0).Left(0).Right(0).Bottom(0))
	})
}

// window is the sidebar and the chosen page side by side, apart by the
// gutters that show the painting.
func (s *Shell) window(c *ui.Context, pal *palette, row ui.Element) {
	row.Padding(gutter).Gap(gutter).AlignItems(ui.Stretch).Children(func() {
		side := ui.Column(c).Width(sideW).Shrink(0)
		glassPane(pal, side).Children(func() { s.sidebar(c, pal) })
		switch {
		case strings.HasPrefix(s.page, pageWS):
			if v := s.view(strings.TrimPrefix(s.page, pageWS)); v != nil {
				s.workspaceView(c, pal, v)
				return
			}
			s.onSheet(c, pal, func() { s.welcomeView(c) })
		case s.page == PageWaiting:
			s.onSheet(c, pal, func() { s.waitingView(c, pal) })
			m, now := moodIdle, "没有要你处理的事"
			if n := s.waitingTotal(); n > 0 {
				m, now = moodAsking, fmt.Sprintf("有 %d 件事等你处理", n)
			}
			s.band(c, pal, m, "waiting", moodNames[m], now)
		case s.page == PagePlugins:
			s.onSheet(c, pal, func() { s.pluginsView(c, pal) })
		case strings.HasPrefix(s.page, pagePlugin):
			s.onSheet(c, pal, func() { s.pluginView(c, pal, strings.TrimPrefix(s.page, pagePlugin)) })
		case strings.HasPrefix(s.page, pageView):
			plugin, id, _ := strings.Cut(strings.TrimPrefix(s.page, pageView), "#")
			s.onSheet(c, pal, func() { s.pluginPageView(c, pal, plugin, id) })
		case strings.HasPrefix(s.page, pageInbox):
			name := strings.TrimPrefix(s.page, pageInbox)
			s.onSheet(c, pal, func() { s.inboxView(c, pal, name) })
			s.band(c, pal, moodIdle, "inbox:"+name, moodNames[moodIdle], "陪你和「"+s.pluginTitle(name)+"」聊天")
		default:
			s.onSheet(c, pal, func() { s.settingsView(c, pal) })
		}
	})
}

// onSheet lays a page on the sheet, under the window's notice when there is
// one.
func (s *Shell) onSheet(c *ui.Context, pal *palette, page func()) {
	col := ui.Column(c).Grow(1).Basis(0).MinWidth(0)
	sheet(pal, col).Children(func() {
		s.noticeBar(c, pal)
		page()
	})
}

// noticeBar is the window's one-line notice, an error most often, over the
// page until closed.
func (s *Shell) noticeBar(c *ui.Context, pal *palette) {
	if s.notice == "" {
		return
	}
	t := c.Theme()
	ui.Row(c).Padding(8, 12, 8, 16).Gap(8).AlignItems(ui.Center).Background(t.Danger.Alpha(0.08)).BorderWidth(0, 0, 1, 0).BorderColor(t.Danger.Alpha(0.16)).Children(func() {
		ui.Icon(c, icAlert).Size(15, 15).TextColor(t.Danger)
		ui.Text(c, s.notice).FontSize(13).TextColor(t.Danger).Grow(1).Selectable()
		if ui.Button(c, "关闭").Clicked() {
			s.notice = ""
		}
	})
}

// welcomeView is the first page when there is no workspace yet, or while
// the folder of tasks outside a project is being prepared.
func (s *Shell) welcomeView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		if s.preparing {
			emptyState(c, icFolder, t.Accent, "正在准备", "第一次启动要在 ~/Alcove 准备放会话的文件夹，需要一两分钟。", nil)
			return
		}
		emptyState(c, icFolder, t.Accent, "先添加一个工作区", "工作区是你电脑上的一个文件夹，Alcove 的任务都在里面执行。", func() {
			if ui.PrimaryButton(c, "添加工作区…").Clicked() {
				s.createWorkspace()
			}
		})
	})
}

// hostOf is the host of an address, or the address when it has none.
func hostOf(addr string) string {
	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		return u.Host
	}
	return addr
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// otherSettings is the settings tab of updates and diagnostics.
func (s *Shell) otherSettings(c *ui.Context, pal *palette) {
	t := c.Theme()
	if def := s.App.DefaultScratchDir(); def != "" {
		settingsGroup(c, pal, "会话", func() {
			prefRow(c, pal, "默认文件夹", "不属于任何工作区的会话在这里进行，侧栏的「最近会话」列出它们。换文件夹时会话记录跟着走，正在运行的会停下。", func() {
				dir := s.settings.ScratchDir
				if dir == "" {
					dir = def
				}
				ui.Column(c).Gap(8).Grow(1).MinWidth(0).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icFolder).Size(14, 14).TextColor(t.TextMuted)
						ui.Text(c, shortDir(dir)).FontSize(13).SingleLine().MinWidth(0).Selectable()
					})
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						if ui.Button(c, "更改…").Disabled(s.scratchBusy).Clicked() && !s.scratchBusy {
							s.pickScratchDir()
						}
						if s.settings.ScratchDir != "" && ui.Button(c, "恢复默认").Disabled(s.scratchBusy).Clicked() && !s.scratchBusy {
							s.setScratchDir("")
						}
						if s.scratchBusy {
							ui.Spinner(c)
						}
					})
				})
			})
		})
	}
	s.updatesView(c, pal)
	settingsGroup(c, pal, "诊断", func() {
		prefRow(c, pal, "诊断包", "把日志和设置打包成 zip，发给支持人员。key、token 和对话内容都不会包含在内。", func() {
			ui.Column(c).Gap(8).Grow(1).MinWidth(0).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if ui.Button(c, "导出诊断包…").Disabled(s.diagBusy).Clicked() && !s.diagBusy {
						s.exportDiagnostics()
					}
					if s.diagBusy {
						ui.Spinner(c)
					}
				})
				if s.diagStatus != "" {
					ui.Text(c, s.diagStatus).FontSize(12).TextColor(t.TextMuted).Selectable()
				}
			})
		})
	})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
