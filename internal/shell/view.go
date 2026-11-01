package shell

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// View builds the window.
func (s *Shell) View(c *ui.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := c.Theme()
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(220).PaddingY(12).Background(t.Surface).Shrink(0).Children(func() {
			s.sidebar(c)
		})
		ui.Column(c).Grow(1).Children(func() {
			if s.notice != "" {
				ui.Row(c).Padding(8, 16).Gap(8).Background(t.Surface).Children(func() {
					ui.Text(c, s.notice).TextColor(t.Danger).Grow(1)
					if ui.Button(c, "关闭").Clicked() {
						s.notice = ""
					}
				})
			}
			switch {
			case strings.HasPrefix(s.page, pageWS):
				if v := s.view(strings.TrimPrefix(s.page, pageWS)); v != nil {
					s.workspaceView(c, v)
					return
				}
				s.settingsView(c)
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

func (s *Shell) sidebar(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, "工作区").FontSize(12).Bold().TextColor(t.TextMuted).Padding(4, 16, 6)
	ui.Sidebar(c, &s.page, func() {
		for _, w := range s.workspaces {
			item := ui.SidebarItem(c, pageWS+w.ID, nil, w.Name)
			if w.Missing() {
				item.Children(func() { ui.Badge(c, "目录不在") })
			}
		}
		if nav := s.pluginNav(); len(nav) > 0 {
			ui.Text(c, "插件").FontSize(12).Bold().TextColor(t.TextMuted).Padding(10, 6, 6)
			for _, n := range nav {
				ui.SidebarItem(c, n.page, nil, n.title)
			}
		}
		ui.Divider(c).Margin(6, 12)
		ui.SidebarItem(c, PagePlugins, nil, "管理插件")
		ui.SidebarItem(c, PageSettings, nil, "设置")
	}).Grow(1).Label("页面")
	if ui.Button(c, "添加工作区…").Margin(8, 12).Clicked() {
		s.createWorkspace()
	}
}

func (s *Shell) workspaceView(c *ui.Context, v *wsView) {
	t := c.Theme()
	sess := v.session
	ui.Row(c).Padding(10, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		title := v.ws.Name
		if sess.Title != "" {
			title += " · " + sess.Title
		}
		ui.Text(c, title).Bold().Grow(1)
		ui.Text(c, v.ws.Dir).FontSize(12).TextColor(t.TextMuted)
		if ui.Button(c, "移除").Clicked() {
			s.removeWorkspace(v.ws.ID)
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(16, 20).Gap(12).Children(func() {
			if len(sess.Entries) == 0 {
				ui.Text(c, "在下面输入任务，Alcove 会在这个文件夹里执行。").TextColor(t.TextMuted)
			}
			for _, e := range sess.Entries {
				entry(c, e)
			}
			for _, a := range v.approvals {
				s.approvalCard(c, a)
			}
			for _, q := range v.questions {
				questionCard(c, q)
			}
			if v.err != "" {
				ui.Text(c, v.err).TextColor(t.Danger).Selectable()
			}
		})
	})
	busy := v.starting || sess.Running()
	ui.Row(c).Gap(8).Padding(12, 20).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		in := ui.TextArea(c, &v.input).Lines(1, 8).Placeholder("输入任务…").Grow(1)
		if busy {
			ui.Spinner(c)
		}
		text := strings.TrimSpace(v.input)
		if (ui.PrimaryButton(c, "发送").Disabled(text == "" || v.starting).Clicked() || in.Submitted()) && text != "" && !v.starting {
			v.input = ""
			s.send(v, text)
		}
	})
}

func entry(c *ui.Context, e *events.Entry) {
	t := c.Theme()
	switch e.Kind {
	case events.User:
		ui.Row(c).Key(e.Key).Children(func() {
			ui.Spacer(c)
			ui.Text(c, e.Text).Selectable().Padding(8, 12).Radius(12).Background(t.Surface).MaxWidth(560)
		})
	case events.Assistant:
		ui.Column(c).Key(e.Key).MaxWidth(760).Children(func() {
			if e.Streaming {
				markdown.Stream(c, e.Text, nil)
			} else {
				markdown.View(c, e.Text, nil)
			}
		})
	case events.Reasoning:
		ui.Text(c, e.Text).Key(e.Key).FontSize(12).TextColor(t.TextMuted).Selectable()
	case events.Tool:
		ui.Column(c).Key(e.Key).Gap(4).Padding(8, 12).Radius(8).Border(1, t.Border).Children(func() {
			state := "运行中"
			if e.Done {
				state = "完成"
				if e.IsError {
					state = "出错"
				}
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, e.ToolName).Bold()
				ui.Text(c, state).TextColor(t.TextMuted)
			})
			if e.Args != "" {
				ui.Text(c, clip(e.Args, 400)).FontSize(12).TextColor(t.TextMuted).Selectable()
			}
			if e.Result != "" {
				color := t.Text
				if e.IsError {
					color = t.Danger
				}
				ui.Text(c, clip(e.Result, 2000)).FontSize(12).TextColor(color).Selectable()
			}
		})
	case events.Notice:
		ui.Text(c, e.Text).Key(e.Key).TextColor(t.Warning)
	}
}

func (s *Shell) approvalCard(c *ui.Context, a *approval) {
	t := c.Theme()
	ui.Column(c).Gap(8).Padding(12).Radius(8).Border(1, t.Warning).Children(func() {
		ui.Text(c, "需要你的确认："+a.req.ToolName).Bold()
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

func questionCard(c *ui.Context, q *question) {
	t := c.Theme()
	ui.Column(c).Gap(10).Padding(12).Radius(8).Border(1, t.Accent).Children(func() {
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
