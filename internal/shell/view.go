package shell

import (
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"

	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// View builds the window: the sidebar of the workspace's tasks, and the
// page it chose (a task, the new-task page, plugins or settings).
func (s *Shell) View(c *ui.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pal := useTheme(c, s.settings.Appearance, s.customWallpaper())
	t := c.Theme()
	window := ui.Row(c).Fill().AlignItems(ui.Stretch)
	if pal.wall != nil {
		// Absolute children paint after those in flow, in order: the
		// frosted painting first, then the window over it.
		ground := ui.Box(c).Fill()
		pal.win = ground.LayoutBounds()
		ground.Children(func() {
			paint(c, pal.wall.frost)
			s.window(c, pal, t, window.Absolute().Top(0).Left(0).Right(0).Bottom(0))
		})
		return
	}
	s.window(c, pal, t, window)
}

// window is the sidebar and the chosen page side by side.
func (s *Shell) window(c *ui.Context, pal *palette, t *ui.Theme, row ui.Element) {
	row.Children(func() {
		side := ui.Column(c).Width(248).BorderWidth(0, 1, 0, 0).BorderColor(pal.hairline).Shrink(0)
		sideGround(c, pal, side).Children(func() {
			s.sidebar(c, pal)
		})
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Background(pal.ground()).Children(func() {
			if s.notice != "" {
				ui.Row(c).Padding(8, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
					ui.Icon(c, icAlert).Size(15, 15).TextColor(t.Danger)
					ui.Text(c, s.notice).FontSize(13).TextColor(t.Danger).Grow(1).Selectable()
					if ui.Button(c, "关闭").Clicked() {
						s.notice = ""
					}
				})
			}
			switch {
			case strings.HasPrefix(s.page, pageWS):
				if v := s.view(strings.TrimPrefix(s.page, pageWS)); v != nil {
					s.workspaceView(c, pal, v)
					return
				}
				s.welcomeView(c)
			case s.page == PageWaiting:
				s.waitingView(c, pal)
			case s.page == PagePlugins:
				s.pluginsView(c, pal)
			case strings.HasPrefix(s.page, pagePlugin):
				s.pluginView(c, pal, strings.TrimPrefix(s.page, pagePlugin))
			case strings.HasPrefix(s.page, pageView):
				plugin, id, _ := strings.Cut(strings.TrimPrefix(s.page, pageView), "#")
				s.pluginPageView(c, pal, plugin, id)
			case strings.HasPrefix(s.page, pageInbox):
				s.inboxView(c, pal, strings.TrimPrefix(s.page, pageInbox))
			default:
				s.settingsView(c, pal)
			}
		})
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

func (s *Shell) approvalCard(c *ui.Context, pal *palette, a *approval) {
	t := c.Theme()
	ui.Column(c).Gap(8).Padding(14, 16).Radius(12).Border(1, t.Border).Background(pal.content).Shadow(0, 4, 18, 0, pal.shadow).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			dot(c, pal.waiting)
			ui.Text(c, "需要你确认").FontSize(13).Bold()
			ui.Text(c, toolVerb(a.req.ToolName)).FontSize(13).TextColor(t.TextMuted)
		})
		reason := a.req.DisplayReason.Text(s.Lang)
		if reason == "" {
			reason = a.req.Reason
		}
		ui.Text(c, reason).FontSize(13.5).Selectable()
		ui.Row(c).Gap(8).Padding(4, 0, 0).Children(func() {
			if ui.PrimaryButton(c, "允许这一次").Clicked() {
				answer(a, alrt.AllowedOnce)
			}
			if ui.Button(c, "拒绝").Clicked() {
				answer(a, alrt.Rejected)
			}
		})
	})
}

func answer(a *approval, outcome string) {
	select {
	case a.answer <- outcome:
	default: // already answered
	}
}

func questionCard(c *ui.Context, pal *palette, q *question) {
	t := c.Theme()
	ui.Column(c).Gap(10).Padding(14, 16).Radius(12).Border(1, t.Border).Background(pal.content).Shadow(0, 4, 18, 0, pal.shadow).Children(func() {
		for i, qq := range q.req.Questions {
			if qq.Header != "" {
				ui.Text(c, qq.Header).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Text(c, qq.Question).Bold()
			if qq.Detail != "" {
				ui.Text(c, qq.Detail)
			}
			if qq.MultiSelect {
				for j, o := range qq.Options {
					ui.Checkbox(c, &q.multi[i][j], o.Label)
				}
			} else if len(qq.Options) > 0 {
				labels := make([]string, len(qq.Options))
				for j, o := range qq.Options {
					labels[j] = o.Label
				}
				ui.Segmented(c, &q.choice[i], labels...)
			}
			ui.TextInput(c, &q.custom[i]).Placeholder("其他（可选）")
		}
		if ui.PrimaryButton(c, "提交").Clicked() {
			var out []alrt.Answer
			for i, qq := range q.req.Questions {
				a := alrt.Answer{ID: qq.ID, Custom: strings.TrimSpace(q.custom[i])}
				switch {
				case qq.MultiSelect:
					for j, on := range q.multi[i] {
						if on {
							a.Selected = append(a.Selected, qq.Options[j].Label)
						}
					}
				case len(qq.Options) > 0 && a.Custom == "":
					a.Selected = []string{qq.Options[q.choice[i]].Label}
				}
				out = append(out, a)
			}
			select {
			case q.answer <- out:
			default:
			}
		}
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
