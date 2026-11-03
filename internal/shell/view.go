package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"

	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// View builds the window: the sidebar of the workspace's tasks, and the
// page it chose (a task, the new-task page, plugins or settings).
func (s *Shell) View(c *ui.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pal := useTheme(c)
	t := c.Theme()
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(256).Background(pal.sidebar).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Shrink(0).Children(func() {
			s.sidebar(c, pal)
		})
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(func() {
			if s.notice != "" {
				ui.Row(c).Padding(8, 16).Gap(8).AlignItems(ui.Center).Background(pal.warnSoft).Children(func() {
					ui.Icon(c, icAlert).TextColor(t.Danger)
					ui.Text(c, s.notice).TextColor(t.Danger).Grow(1).Selectable()
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
			case s.page == PagePlugins:
				s.pluginsView(c)
			case strings.HasPrefix(s.page, pagePlugin):
				s.pluginView(c, strings.TrimPrefix(s.page, pagePlugin))
			case strings.HasPrefix(s.page, pageInbox):
				s.inboxView(c, strings.TrimPrefix(s.page, pageInbox))
			default:
				s.settingsView(c)
			}
		})
	})
}

// welcomeView is the first page when there is no workspace yet.
func (s *Shell) welcomeView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Padding(32).Gap(12).Children(func() {
		ui.Icon(c, icFolder).Size(36, 36).TextColor(t.Accent)
		ui.Text(c, "先添加一个工作区").FontSize(20).Bold()
		ui.Text(c, "工作区是你电脑上的一个文件夹，Alcove 的任务都在里面执行。").TextColor(t.TextMuted)
		if ui.PrimaryButton(c, "添加工作区…").Clicked() {
			s.createWorkspace()
		}
	})
}

func (s *Shell) approvalCard(c *ui.Context, pal *palette, a *approval) {
	t := c.Theme()
	ui.Column(c).Gap(8).Padding(12, 14).Radius(10).Border(1, t.Warning).Background(pal.warnSoft).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icAlert).Size(15, 15).TextColor(t.Warning)
			ui.Text(c, "需要你确认："+toolVerb(a.req.ToolName)).Bold()
		})
		reason := a.req.DisplayReason.Text(s.Lang)
		if reason == "" {
			reason = a.req.Reason
		}
		ui.Text(c, reason).Selectable()
		ui.Row(c).Gap(8).Children(func() {
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
	ui.Column(c).Gap(10).Padding(12, 14).Radius(10).Border(1, t.Accent).Background(pal.accentSoft).Children(func() {
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

func (s *Shell) settingsView(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(24).Gap(16).MaxWidth(640).Children(func() {
			ui.Text(c, "模型凭据").FontSize(18).Bold()
			if len(s.creds) == 0 {
				ui.Text(c, "还没有凭据。添加一个模型 API key 后才能执行任务；key 只存在系统凭据库里。").TextColor(t.TextMuted)
			}
			for _, cr := range s.creds {
				ui.Row(c).Key(cr.ID).Gap(8).AlignItems(ui.Center).Children(func() {
					label := cr.Label + "（" + cr.Provider + "）"
					if cr.ID == s.settings.DefaultCredential {
						label += " · 默认"
					}
					ui.Text(c, label).Grow(1)
					if cr.ID != s.settings.DefaultCredential && ui.Button(c, "设为默认").Clicked() {
						st := s.settings
						st.DefaultCredential = cr.ID
						s.saveSettings(st)
					}
					if ui.Button(c, "删除").Clicked() {
						s.removeCredential(cr.ID)
					}
				})
			}
			f := &s.form
			if f.provider == "" {
				f.provider = providerIDs()[0]
			}
			ui.Column(c).Gap(8).Padding(12).Radius(8).Border(1, t.Border).Children(func() {
				ui.Text(c, "添加凭据").Bold()
				ui.Field(c, "名称", func() { ui.TextInput(c, &f.label).Placeholder("例如：DeepSeek 个人") })
				ui.Field(c, "服务", func() { ui.Select(c, &f.provider, providerIDs()) })
				ui.Field(c, "接口地址（可选）", func() { ui.TextInput(c, &f.baseURL).Placeholder("https://…") })
				ui.Field(c, "API key", func() { ui.TextInput(c, &f.key).Password() })
				if f.err != "" {
					ui.Text(c, f.err).TextColor(t.Danger)
				}
				ok := strings.TrimSpace(f.label) != "" && strings.TrimSpace(f.key) != ""
				if ui.PrimaryButton(c, "添加").Disabled(!ok).Clicked() && ok {
					s.addCredential()
				}
			})
			ui.Text(c, "模型").FontSize(18).Bold()
			ui.Field(c, "模型名（留空用默认）", func() {
				ui.Row(c).Gap(8).Children(func() {
					in := ui.TextInput(c, &s.model).Placeholder("deepseek-v4-flash").Grow(1)
					changed := strings.TrimSpace(s.model) != s.settings.Model
					if (ui.Button(c, "保存").Disabled(!changed).Clicked() || in.Submitted()) && changed {
						st := s.settings
						st.Model = strings.TrimSpace(s.model)
						s.saveSettings(st)
					}
				})
			})
			s.updatesView(c)
			ui.Text(c, "诊断").FontSize(18).Bold()
			ui.Text(c, "把日志和设置打包成 zip，发给支持人员。key、token 和对话内容都不会包含在内。").TextColor(t.TextMuted)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.Button(c, "导出诊断包…").Disabled(s.diagBusy).Clicked() && !s.diagBusy {
					s.exportDiagnostics()
				}
				if s.diagBusy {
					ui.Spinner(c)
				}
			})
			if s.diagStatus != "" {
				ui.Text(c, s.diagStatus).TextColor(t.TextMuted).Selectable()
			}
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
