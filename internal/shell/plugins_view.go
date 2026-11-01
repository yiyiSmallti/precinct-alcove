package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

func stateText(r host.Installed, st sidecar.Status, known bool) string {
	if r.Incomplete {
		return "安装未完成，请卸载后重装"
	}
	if r.Sidecar == "" {
		return "无后台进程"
	}
	if !known {
		return "未启动"
	}
	switch st.State {
	case sidecar.Starting:
		return "启动中"
	case sidecar.Running:
		return "运行中"
	case sidecar.Backoff:
		return fmt.Sprintf("崩溃后等待重启（第 %d 次）", st.Crashes)
	case sidecar.Crashed:
		return "崩溃次数过多，已停止"
	case sidecar.Stopped:
		return "已停止"
	}
	return string(st.State)
}

// pluginsView is the plugin list with the install flow.
func (s *Shell) pluginsView(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(24).Gap(16).MaxWidth(720).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "插件").FontSize(18).Bold().Grow(1)
				if s.planBusy {
					ui.Spinner(c)
				}
				if ui.Button(c, "从文件安装…").Disabled(s.planBusy).Clicked() && !s.planBusy {
					s.pickPlugin()
				}
			})
			if s.planErr != "" {
				ui.Text(c, s.planErr).TextColor(t.Danger).Selectable()
			}
			if s.plan != nil {
				s.planCard(c, s.plan)
			}
			if len(s.plugins) == 0 {
				ui.Text(c, "还没有安装插件。插件包是 .tgz 文件，旁边带同名 .sig 签名文件。").TextColor(t.TextMuted)
			}
			for _, r := range s.plugins {
				s.pluginRow(c, r)
			}
		})
	})
}

func (s *Shell) planCard(c *ui.Context, plan *host.Plan) {
	t := c.Theme()
	p := plan.Package
	ui.Column(c).Gap(8).Padding(12).Radius(8).Border(1, t.Accent).Children(func() {
		ui.Text(c, "安装 "+p.Name+" "+p.Version+"？").Bold()
		if p.Description != "" {
			ui.Text(c, p.Description)
		}
		signed := "未签名（只在开发模式下允许）"
		if plan.SignedBy >= 0 {
			signed = "签名有效"
		}
		ui.Text(c, signed).FontSize(12).TextColor(t.TextMuted)
		if p.Alcove != nil && len(p.Alcove.Capabilities) > 0 {
			ui.Text(c, "它会：").FontSize(12).TextColor(t.TextMuted)
			for _, cp := range p.Alcove.Capabilities {
				ui.Text(c, "· "+capName(cp))
			}
		}
		if len(plan.Profiles) > 0 {
			ui.Text(c, "装入："+strings.Join(plan.Profiles, "、")).FontSize(12).TextColor(t.TextMuted)
		}
		for _, pr := range plan.Problems {
			ui.Text(c, pr.Message).TextColor(t.Danger).Selectable()
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "安装").Disabled(!plan.OK() || s.planBusy).Clicked() && plan.OK() && !s.planBusy {
				s.installPlan()
			}
			if ui.Button(c, "取消").Disabled(s.planBusy).Clicked() && !s.planBusy {
				s.cancelPlan()
			}
		})
	})
}

func (s *Shell) pluginRow(c *ui.Context, r host.Installed) {
	t := c.Theme()
	st, known := s.sidecars[r.Name]
	ui.Column(c).Key(r.Name).Gap(6).Padding(12).Radius(8).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, r.Name+" "+r.Version).Bold().Grow(1)
			if s.busy[r.Name] {
				ui.Spinner(c)
			}
			if s.page != pagePlugin+r.Name && ui.Button(c, "打开").Clicked() {
				s.page = pagePlugin + r.Name
			}
			if r.Sidecar != "" && known && (st.State == sidecar.Crashed || st.State == sidecar.Stopped) {
				if ui.Button(c, "重新启动").Disabled(s.busy[r.Name]).Clicked() && !s.busy[r.Name] {
					s.restart(r.Name)
				}
			}
			if s.confirm == r.Name {
				if styledButton(c, "确认卸载", "danger").Clicked() {
					s.uninstall(r.Name)
				}
				if ui.Button(c, "不卸载").Clicked() {
					s.confirm = ""
				}
			} else if ui.Button(c, "卸载").Disabled(s.busy[r.Name]).Clicked() && !s.busy[r.Name] {
				s.confirm = r.Name
			}
		})
		ui.Text(c, stateText(r, st, known)).FontSize(12).TextColor(t.TextMuted)
		if known && st.LastError != "" && st.State != sidecar.Running {
			ui.Text(c, st.LastError).FontSize(12).TextColor(t.Danger).Selectable()
		}
		if known && st.Stderr != "" && st.State != sidecar.Running {
			ui.Text(c, st.Stderr).FontSize(12).TextColor(t.TextMuted).Selectable()
		}
	})
}

// pluginView is one plugin's page: what it is allowed to do, its sidecar,
// and its settings pages.
func (s *Shell) pluginView(c *ui.Context, name string) {
	r, ok := s.installed(name)
	if !ok {
		s.pluginsView(c)
		return
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(24).Gap(16).MaxWidth(720).Children(func() {
			s.pluginRow(c, r)
			if len(r.Capabilities) > 0 {
				ui.Column(c).Gap(4).Children(func() {
					ui.Text(c, "权限").Bold()
					for _, cp := range r.Capabilities {
						ui.Text(c, "· "+capName(cp))
					}
				})
			}
			for _, ct := range s.contributions(name, "settings_page") {
				s.settingsPageView(c, name, ct)
			}
		})
	})
}

func (s *Shell) settingsPageView(c *ui.Context, plugin string, ct sidecar.Contribution) {
	t := c.Theme()
	f := s.pluginForm(plugin, ct)
	ui.Column(c).Key(ct.ID).Gap(12).Padding(12).Radius(8).Border(1, t.Border).Children(func() {
		if f.page.Title != "" {
			ui.Text(c, f.page.Title).FontSize(16).Bold()
		}
		editable := false
		for i, sec := range f.page.Sections {
			ui.Column(c).Key(i).Gap(8).Children(func() {
				if sec.Title != "" {
					ui.Text(c, sec.Title).Bold()
				}
				if sec.Description != "" {
					ui.Text(c, sec.Description).TextColor(t.TextMuted)
				}
				for _, fd := range sec.Fields {
					if fd.Type != "button" {
						editable = true
					}
					s.fieldView(c, plugin, ct.ID, f, fd)
				}
			})
		}
		if f.err != "" {
			ui.Text(c, f.err).TextColor(t.Danger).Selectable()
		}
		if editable {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.PrimaryButton(c, "保存").Disabled(f.busy).Clicked() && !f.busy {
					s.submit(plugin, ct.ID, f, "submit")
				}
				if f.busy {
					ui.Spinner(c)
				}
				if f.status != "" && !f.dirty {
					ui.Text(c, f.status).TextColor(t.TextMuted)
				}
			})
		}
	})
}

func (s *Shell) fieldView(c *ui.Context, plugin, id string, f *settingsForm, fd settingsField) {
	t := c.Theme()
	v := f.values[fd.ID]
	if v == nil {
		return
	}
	label := fd.Label
	if label == "" {
		label = fd.ID
	}
	changed := false
	switch fd.Type {
	case "text":
		ui.Field(c, label, func() {
			changed = ui.TextInput(c, &v.text).Placeholder(fd.Placeholder).Disabled(f.busy).Changed()
		})
	case "secretRef":
		ph := "输入后保存，只存在系统凭据库里"
		if v.hasRef {
			ph = "已保存；输入新值可替换"
		}
		ui.Field(c, label, func() {
			changed = ui.TextInput(c, &v.text).Password().Placeholder(ph).Disabled(f.busy).Changed()
		})
	case "toggle":
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).Grow(1)
			changed = ui.Switch(c, &v.on).Label(label).Disabled(f.busy).Changed()
		})
	case "select":
		labels := optionLabels(fd.Options)
		ui.Field(c, label, func() {
			changed = ui.Select(c, &v.text, labels).Disabled(f.busy).Changed()
		})
	case "button":
		if styledButton(c, label, fd.Style).Disabled(f.busy).Clicked() && !f.busy {
			s.submit(plugin, id, f, fd.ID)
		}
	default:
		return // unknown field types are skipped (schema v1 only grows)
	}
	if changed {
		f.dirty, f.status = true, ""
	}
	if fd.Help != "" {
		ui.Text(c, fd.Help).FontSize(12).TextColor(t.TextMuted)
	}
}

// inboxView is a plugin's conversation view.
func (s *Shell) inboxView(c *ui.Context, name string) {
	t := c.Theme()
	r, ok := s.installed(name)
	if !ok {
		s.pluginsView(c)
		return
	}
	ch := s.chat(name)
	st, known := s.sidecars[name]
	ui.Row(c).Padding(10, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, displayName(name)).Bold().Grow(1)
		ui.Text(c, stateText(r, st, known)).FontSize(12).TextColor(t.TextMuted)
		if ui.Button(c, "插件设置").Clicked() {
			s.page = pagePlugin + name
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(16, 20).Gap(12).Children(func() {
			for _, ct := range s.contributions(name, "card") {
				s.cardView(c, name, s.contribCard(name, ct), "c:"+ct.ID)
			}
			if len(ch.entries) == 0 {
				ui.Text(c, "在下面输入，消息会交给 "+displayName(name)+"。").TextColor(t.TextMuted)
			}
			for i, e := range ch.entries {
				switch {
				case e.out != nil:
					s.outgoingView(c, name, e.out, i)
				case e.item.Kind == "card":
					s.cardView(c, name, e.item, i)
				default:
					ui.Column(c).Key(i).MaxWidth(760).Children(func() {
						markdown.View(c, e.item.Text, nil)
					})
				}
			}
		})
	})
	ui.Row(c).Gap(8).Padding(12, 20).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		in := ui.TextArea(c, &ch.input).Lines(1, 8).Placeholder("输入消息…").Grow(1)
		text := strings.TrimSpace(ch.input)
		if (ui.PrimaryButton(c, "发送").Disabled(text == "").Clicked() || in.Submitted()) && text != "" {
			ch.input = ""
			s.sendInbox(name, ch, text)
		}
	})
}

func (s *Shell) outgoingView(c *ui.Context, plugin string, o *outgoing, key int) {
	t := c.Theme()
	ui.Column(c).Key(key).Gap(4).AlignItems(ui.End).Children(func() {
		ui.Text(c, o.text).Selectable().Padding(8, 12).Radius(12).Background(t.Surface).MaxWidth(560)
		switch o.state {
		case outSending:
			ui.Text(c, "发送中…").FontSize(12).TextColor(t.TextMuted)
		case outFailed:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "未送达").FontSize(12).TextColor(t.Danger)
				if ui.Button(c, "重发").Clicked() {
					s.deliver(plugin, o)
				}
			})
		}
	})
}

func (s *Shell) cardView(c *ui.Context, plugin string, it *inboxItem, key any) {
	t := c.Theme()
	ui.Column(c).Key(key).Gap(8).Padding(12).Radius(8).Border(1, t.Border).MaxWidth(760).Children(func() {
		if it.Card.Title != "" {
			ui.Text(c, it.Card.Title).Bold()
		}
		if it.Card.Body != "" {
			markdown.View(c, it.Card.Body, nil)
		}
		if len(it.Card.Actions) > 0 {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				for _, a := range it.Card.Actions {
					if styledButton(c, a.Label, a.Style).Key(a.ID).Disabled(it.busy != "").Clicked() && it.busy == "" {
						s.cardAction(plugin, it, a.ID)
					}
				}
				if it.busy != "" {
					ui.Spinner(c)
				}
			})
		}
		if it.err != "" {
			ui.Text(c, it.err).TextColor(t.Danger)
		}
	})
}

// styledButton is a button in one of the schema's styles.
func styledButton(c *ui.Context, label, style string) ui.Element {
	switch style {
	case "primary":
		return ui.PrimaryButton(c, label)
	case "danger":
		return ui.Button(c, label).TextColor(c.Theme().Danger)
	}
	return ui.Button(c, label)
}
