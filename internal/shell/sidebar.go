package shell

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// sidebar is the left column: the workspace switcher, a new task, the
// workspace's tasks by when they last moved, the plugins' conversation
// views, and the way to plugins and settings.
func (s *Shell) sidebar(c *ui.Context, pal *palette) {
	t := c.Theme()
	v := s.view(s.curWS)
	ui.Column(c).Padding(12, 10, 8).Gap(8).Children(func() {
		s.workspaceSwitcher(c, pal, v)
		if v != nil {
			b := ui.Row(c).Padding(8, 10).Gap(6).AlignItems(ui.Center).Justify(ui.Center).Radius(8).Cursor(ui.CursorPointer).Focusable().Label("新任务")
			bg := t.Accent
			if b.Hovered() {
				bg = t.AccentHover
			}
			b.Background(bg).Children(func() {
				ui.Icon(c, icPlus).Size(15, 15).TextColor(t.AccentText)
				ui.Text(c, "新任务").TextColor(t.AccentText).Bold()
			})
			if b.Clicked() {
				s.newTask(v.ws.ID)
			}
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(4, 10, 12).Gap(2).Children(func() {
			if v != nil {
				s.taskList(c, pal, v)
			}
			if nav := s.pluginNav(); len(nav) > 0 {
				sectionLabel(c, "助手")
				for _, n := range nav {
					if navRow(c, pal, n.page, icChat, n.title, s.page == n.page) {
						s.page = n.page
					}
				}
			}
		})
	})
	ui.Row(c).Padding(8, 10, 10).Gap(4).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		plugins := s.page == PagePlugins || strings.HasPrefix(s.page, pagePlugin)
		if footButton(c, pal, icPuzzle, "插件", plugins) {
			s.page = PagePlugins
		}
		if footButton(c, pal, icGear, "设置", s.page == PageSettings) {
			s.page = PageSettings
		}
	})
}

// workspaceSwitcher shows the current workspace; a click lists the others,
// adding one and removing this one.
func (s *Shell) workspaceSwitcher(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	name, sub := "还没有工作区", "点这里添加"
	if v != nil {
		name, sub = v.ws.Name, shortDir(v.ws.Dir)
	}
	anchor := ui.Row(c).Padding(8, 10).Gap(10).AlignItems(ui.Center).Radius(8).Cursor(ui.CursorPointer).Focusable().Label("切换工作区")
	if anchor.Hovered() || s.wsMenu {
		anchor.Background(pal.hover)
	}
	anchor.Children(func() {
		ui.Column(c).Size(30, 30).Radius(8).Background(t.Accent).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
			ui.Text(c, initial(name)).Bold().TextColor(t.AccentText)
		})
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, name).Bold().SingleLine().Grow(1).Shrink(1).MinWidth(0)
				if v != nil && v.ws.OwnerName() != "personal" {
					pill(c, "组织", pal.accentSoft, t.Accent)
				}
			})
			ui.Text(c, sub).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
		})
		ui.Icon(c, icChevron).Size(14, 14).TextColor(t.TextMuted)
	})
	if anchor.Clicked() {
		if v == nil {
			s.createWorkspace()
		} else {
			s.wsMenu = !s.wsMenu
		}
	}
	ui.Popover(c, anchor, &s.wsMenu, func() {
		ui.Column(c).Width(236).Padding(6).Gap(2).Children(func() {
			sectionLabel(c, "工作区")
			for _, w := range s.workspaces {
				label := w.Name
				if w.Missing() {
					label += "（目录不在）"
				}
				if navRow(c, pal, "ws:"+w.ID, icFolder, label, v != nil && w.ID == v.ws.ID) {
					s.wsMenu = false
					if vv := s.view(w.ID); vv != nil {
						s.page, s.curWS = pageWS+w.ID, w.ID
					}
				}
			}
			ui.Divider(c).Margin(4, 0)
			if navRow(c, pal, "add", icPlus, "添加工作区…", false) {
				s.wsMenu = false
				s.createWorkspace()
			}
			if v != nil {
				label := "移除「" + v.ws.Name + "」"
				if v.confirmRemove {
					label = "再点一次确认移除（文件不会删）"
				}
				r := ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(label)
				if r.Hovered() {
					r.Background(pal.hover)
				}
				r.Children(func() {
					ui.Icon(c, icX).Size(15, 15).TextColor(t.Danger)
					ui.Text(c, label).TextColor(t.Danger).SingleLine()
				})
				if r.Clicked() {
					if v.confirmRemove {
						v.confirmRemove, s.wsMenu = false, false
						s.removeWorkspace(v.ws.ID)
					} else {
						v.confirmRemove = true
					}
				}
			}
		})
	})
	if !s.wsMenu && v != nil {
		v.confirmRemove = false
	}
}

// taskList lists the workspace's tasks: running or waiting first, then
// today's, then older ones.
func (s *Shell) taskList(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	var active, today, older []*taskView
	start := startOfDay(time.Now())
	for _, tk := range ordered(v) {
		switch {
		case tk.session.Running() || tk.starting || v.waiting(tk):
			active = append(active, tk)
		case tk.meta.UpdatedAt.After(start):
			today = append(today, tk)
		default:
			older = append(older, tk)
		}
	}
	if len(active)+len(today)+len(older) == 0 {
		ui.Text(c, "还没有任务。点上面的“新任务”开始。").FontSize(12.5).TextColor(t.TextMuted).Padding(10, 8)
		return
	}
	group := func(title string, list []*taskView) {
		if len(list) == 0 {
			return
		}
		sectionLabel(c, title)
		for _, tk := range list {
			s.taskRow(c, pal, v, tk)
		}
	}
	group("进行中", active)
	group("今天", today)
	group("更早", older)
}

func (s *Shell) taskRow(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	t := c.Theme()
	sel := s.page == pageWS+v.ws.ID && v.current == tk.meta.ID
	title := taskTitle(tk)
	r := ui.Row(c).Key(tk.meta.ID).Padding(7, 8).Gap(8).AlignItems(ui.Center).Radius(7).Cursor(ui.CursorPointer).Focusable().Label(title)
	switch {
	case sel:
		r.Background(pal.selected)
	case r.Hovered():
		r.Background(pal.hover)
	}
	r.Children(func() {
		switch {
		case v.waiting(tk):
			ui.Icon(c, icAlert).Size(14, 14).TextColor(t.Warning)
		case tk.session.Running() || tk.starting:
			ui.Spinner(c).Size(14, 14).TextColor(pal.running)
		case tk.meta.Source != "":
			ui.Icon(c, icCloud).Size(14, 14).TextColor(t.TextMuted)
		default:
			ui.Icon(c, icChat).Size(14, 14).TextColor(t.TextMuted)
		}
		txt := ui.Text(c, title).SingleLine().Grow(1).Shrink(1).MinWidth(0)
		if sel {
			txt.Bold()
		}
		switch {
		case v.waiting(tk):
			pill(c, "待确认", pal.warnSoft, t.Warning)
		default:
			ui.Text(c, when(tk.meta.UpdatedAt)).FontSize(11).TextColor(t.TextMuted).Shrink(0)
		}
	})
	if r.Clicked() {
		s.openTask(v, tk.meta.ID)
	}
}

// navRow is one clickable row with an icon; it reports a click.
func navRow(c *ui.Context, pal *palette, key string, ic *ui.SVG, label string, sel bool) bool {
	t := c.Theme()
	r := ui.Row(c).Key(key).Padding(7, 8).Gap(8).AlignItems(ui.Center).Radius(7).Cursor(ui.CursorPointer).Focusable().Label(label)
	switch {
	case sel:
		r.Background(pal.selected)
	case r.Hovered():
		r.Background(pal.hover)
	}
	r.Children(func() {
		ui.Icon(c, ic).Size(14, 14).TextColor(t.TextMuted)
		txt := ui.Text(c, label).SingleLine().Grow(1).MinWidth(0)
		if sel {
			txt.Bold()
		}
	})
	return r.Clicked()
}

func footButton(c *ui.Context, pal *palette, ic *ui.SVG, label string, sel bool) bool {
	t := c.Theme()
	r := ui.Row(c).Grow(1).Basis(0).Padding(7, 8).Gap(6).AlignItems(ui.Center).Justify(ui.Center).Radius(7).Cursor(ui.CursorPointer).Focusable().Label(label)
	switch {
	case sel:
		r.Background(pal.selected)
	case r.Hovered():
		r.Background(pal.hover)
	}
	r.Children(func() {
		ui.Icon(c, ic).Size(15, 15).TextColor(t.TextMuted)
		ui.Text(c, label)
	})
	return r.Clicked()
}

func sectionLabel(c *ui.Context, title string) {
	ui.Text(c, title).FontSize(11.5).Bold().TextColor(c.Theme().TextMuted).Padding(12, 8, 4)
}

func pill(c *ui.Context, text string, bg, fg ui.Color) {
	ui.Text(c, text).FontSize(11).TextColor(fg).Background(bg).Padding(1, 7).Radius(9).Shrink(0).SingleLine()
}

func initial(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return strings.ToUpper(string(r))
	}
	return "A"
}

// shortDir shows a folder with the home directory as ~.
func shortDir(dir string) string {
	for _, home := range []string{"/Users/", "/home/"} {
		if strings.HasPrefix(dir, home) {
			if i := strings.IndexByte(dir[len(home):], '/'); i >= 0 {
				return "~" + dir[len(home)+i:]
			}
			return "~"
		}
	}
	return dir
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// when is a short time for the task list: the time today, else the date.
func when(t time.Time) string {
	t = t.Local()
	now := time.Now()
	switch {
	case t.After(startOfDay(now)):
		return t.Format("15:04")
	case t.Year() == now.Year():
		return fmt.Sprintf("%d月%d日", t.Month(), t.Day())
	}
	return t.Format("2006-01-02")
}
