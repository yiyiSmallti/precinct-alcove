package shell

import (
	"fmt"
	"strings"
	"time"

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

// stateColor is the dot beside stateText.
func stateColor(c *ui.Context, pal *palette, r host.Installed, st sidecar.Status, known bool) ui.Color {
	switch {
	case r.Incomplete:
		return c.Theme().Danger
	case r.Sidecar == "" || !known:
		return pal.subtle
	}
	switch st.State {
	case sidecar.Running:
		return pal.done
	case sidecar.Starting, sidecar.Backoff:
		return pal.waiting
	case sidecar.Crashed:
		return c.Theme().Danger
	}
	return pal.subtle
}

// pluginTile is the puzzle-piece mark beside a plugin's name.
func pluginTile(c *ui.Context, size float32) { iconTile(c, icPuzzle, size) }

// iconTile is an icon on a tinted square.
func iconTile(c *ui.Context, ic *ui.SVG, size float32) {
	t := c.Theme()
	ui.Column(c).Size(size, size).Shrink(0).Radius(size / 4).Background(t.Accent.Alpha(0.12)).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Icon(c, ic).Size(size/2, size/2).TextColor(t.Accent)
	})
}

// errorNote is an error in a tinted box, selectable for copying.
func errorNote(c *ui.Context, text string) {
	t := c.Theme()
	ui.Text(c, text).FontSize(12.5).TextColor(t.Danger).Selectable().Padding(10, 14).Radius(8).Background(t.Danger.Alpha(0.08))
}

// pluginsView is the plugin list with the install flow.
func (s *Shell) pluginsView(c *ui.Context, pal *palette) {
	topBar(c, pal, func() {
		ui.Text(c, "插件").FontSize(14).Bold().Grow(1)
		if s.planBusy {
			ui.Spinner(c)
		}
		if ui.Button(c, "从文件安装…").Disabled(s.planBusy).Clicked() && !s.planBusy {
			s.pickPlugin()
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(28, 40, 40).Gap(16).MaxWidth(720).Children(func() {
			if s.planErr != "" {
				errorNote(c, s.planErr)
			}
			if s.plan != nil {
				s.planCard(c, pal, s.plan)
			}
			if len(s.plugins) == 0 {
				emptyState(c, icPuzzle, c.Theme().Accent, "还没有安装插件", "插件包是 .tgz 文件，旁边带同名 .sig 签名文件。", nil)
				return
			}
			settingsGroup(c, pal, "已安装", func() {
				for _, r := range s.plugins {
					s.pluginRow(c, pal, r)
				}
			})
		})
	})
}

func (s *Shell) planCard(c *ui.Context, pal *palette, plan *host.Plan) {
	t := c.Theme()
	p := plan.Package
	verb, title := "安装", "安装 "+displayName(p.Name)+" "+p.Version+"？"
	if r := plan.Replaces; r != nil {
		verb, title = "更新", "更新 "+displayName(p.Name)+" "+r.Version+" → "+p.Version+"？"
	}
	ui.Column(c).Gap(14).Padding(16, 18).Radius(10).Border(1, t.Accent.Alpha(0.45)).Background(pal.content).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			pluginTile(c, 36)
			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Text(c, title).FontSize(14).FontWeight(600)
				signed, col := "签名有效", pal.done
				if plan.SignedBy < 0 {
					signed, col = "未签名（只在开发模式下允许）", pal.waiting
				}
				if s.planFrom != "" {
					signed = "由「" + displayName(s.planFrom) + "」发起 · " + signed
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					dot(c, col)
					ui.Text(c, p.Name+" · "+signed).FontSize(11.5).TextColor(t.TextMuted)
				})
			})
		})
		if p.Description != "" {
			ui.Text(c, p.Description).FontSize(13)
		}
		if p.Alcove != nil && len(p.Alcove.Capabilities) > 0 {
			ui.Column(c).Gap(7).Padding(12, 14).Radius(8).Background(pal.code).Children(func() {
				ui.Text(c, "它会").FontSize(11.5).FontWeight(500).TextColor(t.TextMuted)
				for _, cp := range p.Alcove.Capabilities {
					capLine(c, cp)
				}
				if len(plan.Profiles) > 0 {
					ui.Text(c, "装入 profile："+strings.Join(plan.Profiles, "、")).FontSize(11.5).TextColor(t.TextMuted).Padding(2, 0, 0)
				}
			})
		}
		for _, pr := range plan.Problems {
			errorNote(c, pr.Message)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Disabled(s.planBusy).Clicked() && !s.planBusy {
				s.cancelPlan()
			}
			if primaryButton(c, verb, plan.OK() && !s.planBusy) {
				s.installPlan()
			}
		})
	})
}

// capLine is one thing a plugin may do, with a check mark.
func capLine(c *ui.Context, cp string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icCheck).Size(13, 13).TextColor(c.Theme().Accent)
		ui.Text(c, capName(cp)).FontSize(13)
	})
}

// pluginRow is one installed plugin in the list.
func (s *Shell) pluginRow(c *ui.Context, pal *palette, r host.Installed) {
	t := c.Theme()
	st, known := s.sidecars[r.Name]
	ui.Column(c).Key(r.Name).Padding(12, 16).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			pluginTile(c, 32)
			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, displayName(r.Name)).FontSize(13).FontWeight(500).SingleLine()
					tag(c, pal, r.Version)
				})
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					dot(c, stateColor(c, pal, r, st, known))
					ui.Text(c, stateText(r, st, known)+" · "+r.Name).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
				})
			})
			if s.busy[r.Name] {
				ui.Spinner(c)
			}
			if ui.Button(c, "打开").Clicked() {
				s.page = pagePlugin + r.Name
			}
			s.pluginActions(c, r, st, known)
		})
		if known && st.LastError != "" && st.State != sidecar.Running {
			ui.Text(c, st.LastError).FontSize(12).TextColor(t.Danger).Selectable().Padding(0, 0, 0, 44)
		}
	})
}

// pluginActions are the restart and uninstall buttons, the latter asking
// once more before it acts.
func (s *Shell) pluginActions(c *ui.Context, r host.Installed, st sidecar.Status, known bool) {
	busy := s.busy[r.Name]
	if r.Sidecar != "" && known && (st.State == sidecar.Crashed || st.State == sidecar.Stopped) {
		if ui.Button(c, "重新启动").Disabled(busy).Clicked() && !busy {
			s.restart(r.Name)
		}
	}
	if s.confirm == r.Name {
		if ui.Button(c, "不卸载").Clicked() {
			s.confirm = ""
		}
		if styledButton(c, "确认卸载", "danger").Clicked() {
			s.uninstall(r.Name)
		}
	} else if ui.Button(c, "卸载").Disabled(busy).Clicked() && !busy {
		s.confirm = r.Name
	}
}

// pluginView is one plugin's page: its state, what it is allowed to do,
// and its settings pages.
func (s *Shell) pluginView(c *ui.Context, pal *palette, name string) {
	r, ok := s.installed(name)
	if !ok {
		s.pluginsView(c, pal)
		return
	}
	t := c.Theme()
	st, known := s.sidecars[name]
	topBar(c, pal, func() {
		back := ui.Row(c).Cursor(ui.CursorPointer).Label("插件")
		back.Children(func() {
			txt := ui.Text(c, "插件").FontSize(14).TextColor(t.TextMuted)
			if back.Hovered() {
				txt.TextColor(t.Text)
			}
		})
		if back.Clicked() {
			s.page = PagePlugins
		}
		ui.Text(c, "/").FontSize(14).TextColor(pal.subtle)
		ui.Text(c, displayName(name)).FontSize(14).Bold().Grow(1).SingleLine()
		if s.busy[name] {
			ui.Spinner(c)
		}
		s.pluginActions(c, r, st, known)
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(28, 40, 40).Gap(16).MaxWidth(720).Children(func() {
			if s.plan != nil && s.planFrom != "" {
				s.planCard(c, pal, s.plan)
			}
			settingsGroup(c, pal, "概况", func() {
				ui.Row(c).Padding(14, 16).Gap(12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
					pluginTile(c, 40)
					ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							ui.Text(c, displayName(name)).FontSize(15).FontWeight(600).SingleLine()
							tag(c, pal, r.Version)
						})
						ui.Text(c, name).FontSize(11.5).TextColor(t.TextMuted).Selectable()
					})
				})
				prefRow(c, pal, "状态", "", func() {
					ui.Column(c).Gap(8).Grow(1).MinWidth(0).Padding(4, 0, 0).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							dot(c, stateColor(c, pal, r, st, known))
							ui.Text(c, stateText(r, st, known)).FontSize(13)
						})
						if known && st.LastError != "" && st.State != sidecar.Running {
							ui.Text(c, st.LastError).FontSize(12).TextColor(t.Danger).Selectable()
						}
						if known && st.Stderr != "" && st.State != sidecar.Running {
							ui.Text(c, st.Stderr).FontSize(11.5).Font(pal.mono).TextColor(t.TextMuted).Selectable().Padding(8, 10).Radius(6).Background(pal.code)
						}
					})
				})
				if len(r.Capabilities) > 0 {
					prefRow(c, pal, "权限", "安装时你确认过的能力。", func() {
						ui.Column(c).Gap(7).Padding(4, 0, 0).Children(func() {
							for _, cp := range r.Capabilities {
								capLine(c, cp)
							}
						})
					})
				}
			})
			for _, ct := range s.contributions(name, "settings_page") {
				s.settingsPageView(c, pal, name, ct, false, true)
			}
		})
	})
}

// pluginPageView is one settings page of a plugin as a page of its own,
// opened from the plugin's sidebar item: the page's title on top, then the
// page, without the plugin's overview.
func (s *Shell) pluginPageView(c *ui.Context, pal *palette, name, id string) {
	r, ok := s.installed(name)
	ct := s.settingsPage(name, id)
	if !ok || ct == nil {
		s.pluginView(c, pal, name)
		return
	}
	f := s.pluginForm(name, *ct)
	title := f.page.Title
	if title == "" {
		title = displayName(r.Name)
	}
	topBar(c, pal, func() {
		ui.Text(c, title).FontSize(14).Bold().Grow(1).SingleLine()
		if f.busy || s.busy[name] {
			ui.Spinner(c)
		}
		if ui.Button(c, "插件详情").Clicked() {
			s.page = pagePlugin + name
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(28, 40, 40).Gap(16).MaxWidth(760).Children(func() {
			if s.plan != nil && s.planFrom == name {
				s.planCard(c, pal, s.plan)
			}
			s.settingsPageView(c, pal, name, *ct, false, false)
		})
	})
}

// settingsPage is the plugin's settings_page contribution id, nil if it has
// none by that id.
func (s *Shell) settingsPage(plugin, id string) *sidecar.Contribution {
	for _, ct := range s.contributions(plugin, "settings_page") {
		if ct.ID == id {
			return &ct
		}
	}
	return nil
}

// settingsPageView draws a plugin's declarative settings page as the
// settings page draws its own: a box, titled when titled, one block per
// section, fields as label-and-control rows, and the page's 保存 at its
// foot. Card sections follow the box as a grid of small cards. Narrow (the
// side panel) puts labels above their controls, buttons under the text and
// cards one under another.
func (s *Shell) settingsPageView(c *ui.Context, pal *palette, plugin string, ct sidecar.Contribution, narrow, titled bool) {
	t := c.Theme()
	f := s.pluginForm(plugin, ct)
	title := ""
	if titled {
		title = f.page.Title
		if title == "" {
			title = "设置"
		}
	}
	editable := false
	var rows, cards []int
	for i, sec := range f.page.Sections {
		if sec.Display == "card" {
			cards = append(cards, i)
		} else {
			rows = append(rows, i)
		}
		for _, fd := range sec.Fields {
			if fd.Type != "button" {
				editable = true
			}
		}
	}
	if f.page.Submit != nil && !*f.page.Submit {
		editable = false
	}
	footer := editable || f.err != ""
	ui.Column(c).Key(ct.ID).Gap(16).Children(func() {
		if len(rows) > 0 || footer {
			settingsGroup(c, pal, title, func() {
				for _, i := range rows {
					ui.Column(c).Key(i).Children(func() {
						s.sectionView(c, pal, plugin, ct.ID, f, f.page.Sections[i], narrow)
					})
				}
				if !footer {
					return
				}
				ui.Row(c).Padding(12, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
					switch {
					case f.err != "":
						ui.Text(c, f.err).FontSize(12.5).TextColor(t.Danger).Selectable().Grow(1).MinWidth(0)
					case f.status != "" && !f.dirty:
						ui.Text(c, f.status).FontSize(12.5).TextColor(t.TextMuted).Grow(1).MinWidth(0)
					default:
						ui.Spacer(c)
					}
					if f.busy {
						ui.Spinner(c)
					}
					if editable && primaryButton(c, "保存", !f.busy) {
						s.submit(plugin, ct.ID, f, "submit")
					}
				})
			})
		} else if title != "" {
			ui.Text(c, title).FontSize(13).Bold().Padding(0, 2)
		}
		if len(cards) == 0 {
			return
		}
		grid := ui.Row(c).Gap(12).Wrap().AlignItems(ui.Stretch)
		if narrow {
			grid = ui.Column(c).Gap(10)
		}
		grid.Children(func() {
			for _, i := range cards {
				s.sectionCard(c, pal, plugin, ct.ID, f, f.page.Sections[i], i, narrow)
			}
		})
	})
}

// sectionCard is a card section: a small card with its icon, title,
// caption and a few lines of description, its buttons at the foot.
func (s *Shell) sectionCard(c *ui.Context, pal *palette, plugin, id string, f *settingsForm, sec settingsSection, key int, narrow bool) {
	t := c.Theme()
	card := ui.Column(c).Key(key).Gap(10).Padding(14).Radius(10).Border(1, pal.hairline).Background(pal.content)
	if !narrow {
		card.Width(216).MinHeight(150)
	}
	card.Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if ic := navIcons[sec.Icon]; ic != nil {
				iconTile(c, ic, 32)
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, sec.Title).FontSize(13.5).FontWeight(600).SingleLine().Ellipsis("…")
				if sec.Caption != "" {
					ui.Text(c, sec.Caption).FontSize(11).TextColor(pal.subtle)
				}
			})
		})
		if sec.Description != "" {
			ui.Text(c, sec.Description).FontSize(12).TextColor(t.TextMuted).MaxLines(3).Ellipsis("…")
		}
		ui.Spacer(c)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			for _, fd := range sec.Fields {
				if fd.Type != "button" || f.values[fd.ID] == nil {
					continue
				}
				label := fd.Label
				if label == "" {
					label = fd.ID
				}
				if styledButton(c, label, fd.Style).Key(fd.ID).Disabled(f.busy).Clicked() && !f.busy {
					s.submit(plugin, id, f, fd.ID)
				}
			}
		})
	})
}

// sectionView is one section of a settings page. A section of only
// buttons is a single row, its text on the left and its buttons on the
// right, so lists of items (a market's plugins) read as a list.
func (s *Shell) sectionView(c *ui.Context, pal *palette, plugin, id string, f *settingsForm, sec settingsSection, narrow bool) {
	t := c.Theme()
	var inputs, buttons []settingsField
	for _, fd := range sec.Fields {
		if f.values[fd.ID] == nil {
			continue
		}
		switch fd.Type {
		case "button":
			buttons = append(buttons, fd)
		case "text", "secretRef", "toggle", "select":
			inputs = append(inputs, fd)
		}
	}
	header := func() {
		if sec.Title != "" {
			ui.Text(c, sec.Title).FontSize(13).FontWeight(500)
		}
		if sec.Description != "" {
			ui.Text(c, sec.Description).FontSize(12).TextColor(t.TextMuted).Selectable()
		}
	}
	buttonRow := func() {
		for _, fd := range buttons {
			label := fd.Label
			if label == "" {
				label = fd.ID
			}
			if styledButton(c, label, fd.Style).Key(fd.ID).Disabled(f.busy).Clicked() && !f.busy {
				s.submit(plugin, id, f, fd.ID)
			}
		}
	}
	box := ui.Column(c).Padding(14, 16).Gap(12).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline)
	if narrow {
		box.Padding(12, 12).Gap(10)
	}
	if len(inputs) == 0 && !narrow {
		box.Children(func() {
			ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(header)
				ui.Row(c).Gap(8).Shrink(0).Children(buttonRow)
			})
		})
		return
	}
	box.Children(func() {
		if sec.Title != "" || sec.Description != "" {
			ui.Column(c).Gap(4).Children(header)
		}
		// One text input and its buttons share a line: an address and 刷新.
		inline := !narrow && len(inputs) == 1 && inputs[0].Type != "toggle" && len(buttons) > 0 && len(buttons) <= 2
		for _, fd := range inputs {
			var trailing func()
			if inline {
				trailing = buttonRow
			}
			s.fieldView(c, pal, f, fd, narrow, trailing)
		}
		if len(buttons) > 0 && !inline {
			r := ui.Row(c).Gap(8).Wrap()
			if !narrow {
				r.Justify(ui.End)
			}
			r.Children(buttonRow)
		}
	})
}

// fieldView is one input field: its label and help on the left, the
// control on the right.
// trailing, if any, draws after the control on its line.
func (s *Shell) fieldView(c *ui.Context, pal *palette, f *settingsForm, fd settingsField, narrow bool, trailing func()) {
	t := c.Theme()
	v := f.values[fd.ID]
	label := fd.Label
	if label == "" {
		label = fd.ID
	}
	changed := false
	var row ui.Element
	if narrow {
		row = ui.Column(c).Key(fd.ID).Gap(6)
	} else {
		row = ui.Row(c).Key(fd.ID).Gap(24).AlignItems(ui.Start)
	}
	row.Children(func() {
		lc := ui.Column(c).Gap(3)
		if !narrow {
			lc.Width(160).Shrink(0).Padding(7, 0, 0)
		}
		lc.Children(func() {
			ui.Text(c, label).FontSize(13)
			if fd.Help != "" {
				ui.Text(c, fd.Help).FontSize(11.5).TextColor(t.TextMuted)
			}
		})
		ui.Row(c).Grow(1).MinWidth(0).Gap(8).Children(func() {
			defer func() {
				if trailing != nil {
					trailing()
				}
			}()
			switch fd.Type {
			case "text":
				changed = ui.TextInput(c, &v.text).Placeholder(fd.Placeholder).Disabled(f.busy).Grow(1).Changed()
			case "secretRef":
				ph := "输入后保存，只存在系统凭据库里"
				if v.hasRef {
					ph = "已保存；输入新值可替换"
				}
				changed = ui.TextInput(c, &v.text).Password().Placeholder(ph).Disabled(f.busy).Grow(1).Changed()
			case "toggle":
				ui.Row(c).Grow(1).Justify(ui.End).Padding(4, 0, 0).Children(func() {
					changed = ui.Switch(c, &v.on).Label(label).Disabled(f.busy).Changed()
				})
			case "select":
				sel := ui.Select(c, &v.text, optionLabels(fd.Options)).Disabled(f.busy)
				if narrow {
					sel.Grow(1)
				} else {
					sel.Width(260)
				}
				changed = sel.Changed()
			}
		})
	})
	if changed {
		f.dirty, f.status = true, ""
	}
}

// inboxView is a plugin's conversation view.
func (s *Shell) inboxView(c *ui.Context, pal *palette, name string) {
	t := c.Theme()
	r, ok := s.installed(name)
	if !ok {
		s.pluginsView(c, pal)
		return
	}
	ch := s.chat(name)
	st, known := s.sidecars[name]
	ui.Row(c).Padding(10, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		if b := s.avatars[name]; b != nil {
			avatar(c, b, 24)
		}
		ui.Text(c, s.pluginTitle(name)).Bold().Grow(1)
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
				ui.Text(c, "在下面输入，消息会交给 "+s.pluginTitle(name)+"。").TextColor(t.TextMuted)
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
			if !ch.waiting.IsZero() && time.Since(ch.waiting) < replyWait {
				ui.Row(c).Key("waiting").Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Size(13, 13).TextColor(pal.running)
					ui.Text(c, s.pluginTitle(name)+" 正在处理…").FontSize(13).TextColor(t.TextMuted)
				})
			}
		})
	})
	ui.Row(c).Gap(8).Padding(12, 20).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		// A text area reports no Submitted: Enter sends, Shift+Enter (any
		// modifier) breaks the line, as in the task composer.
		ui.TextArea(c, &ch.input).Lines(1, 8).Placeholder("输入消息…  ↵ 发送 · ⇧↵ 换行").Grow(1).
			OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter || k.Mods != 0 {
					return false
				}
				if text := strings.TrimSpace(k.Text()); text != "" {
					k.SetText("")
					s.sendInbox(name, ch, text)
				}
				return true
			})
		text := strings.TrimSpace(ch.input)
		if primaryButton(c, "发送", text != "") && text != "" {
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
