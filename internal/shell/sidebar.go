package shell

import (
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

//go:embed logo.png
var logoPNG []byte

// logo is the app's mark, the icon's tile without its padding.
var logo = func() *ui.Bitmap {
	b, err := ui.DecodeBitmap(logoPNG)
	if err != nil {
		panic(err)
	}
	return b
}()

// sidebar is the left column: the app's name, a new task, what waits on
// you, the plugins' own entries (the market, their conversations) and the
// assistants (plugins with a picture and a conversation view), the
// project switcher over the project's tasks by when they last moved, 最近会话
// (the tasks outside a project), and the way to plugins and settings.
func (s *Shell) sidebar(c *ui.Context, pal *palette) {
	t := c.Theme()
	v := s.view(s.curWS)
	top := float32(10)
	if bar := c.TitleBar(); bar.Height > 0 {
		ui.Row(c).Height(bar.Height).Shrink(0).DragWindow()
		top = 2
	}
	ui.Column(c).Padding(top, 10, 4).Gap(1).Children(func() {
		ui.Row(c).Height(36).Padding(0, 6).Gap(9).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Image(c, logo).Size(22, 22).Shrink(0)
			ui.Text(c, "Alcove").FontSize(15).FontWeight(650).LetterSpacing(-0.2)
		})
		ui.Box(c).Height(8)
		if v != nil && navRow(c, pal, "new", icPlus, "新任务", s.page == pageWS+v.ws.ID && v.current == "") {
			s.newTask(v.ws.ID)
		}
		n := s.waitingTotal()
		if listRow(c, pal, "waiting", "等你处理", s.page == PageWaiting, func() {
			ui.Icon(c, icInbox).Size(15, 15).TextColor(t.TextMuted)
			ui.Text(c, "等你处理").FontSize(13).Grow(1)
			if n > 0 {
				ui.Text(c, fmt.Sprint(n)).FontSize(11).Bold().TextColor(t.AccentText).Background(pal.waiting).Padding(0, 6).Radius(8)
			}
		}) {
			s.page = PageWaiting
		}
		for _, n := range s.pluginNav() {
			if navRow(c, pal, n.page, n.icon, n.title, s.page == n.page) {
				s.page = n.page
			}
		}
		if as := s.assistants(); len(as) > 0 {
			sectionLabel(c, "助手")
			for _, n := range as {
				if listRow(c, pal, n.page, n.title, s.page == n.page, func() {
					avatar(c, n.avatar, 20)
					ui.Text(c, n.title).FontSize(13).SingleLine().Grow(1).MinWidth(0)
				}) {
					s.page = n.page
				}
			}
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(0, 10, 12).Gap(1).Children(func() {
			pv := s.view(s.curProj)
			s.workspaceSwitcher(c, pal, pv)
			if pv != nil {
				s.taskList(c, pal, pv)
			}
			if sv := s.view(s.scratchID); sv != nil {
				ui.Box(c).Height(14)
				sectionLabel(c, "最近会话")
				recent := ordered(sv)
				if len(recent) == 0 {
					ui.Text(c, "还没有会话").FontSize(12.5).TextColor(pal.subtle).Padding(4, 8)
				}
				for _, tk := range recent {
					s.taskRow(c, pal, sv, tk)
				}
			}
		})
	})
	ui.Column(c).Padding(6, 10, 10).Gap(1).Children(func() {
		plugins := s.page == PagePlugins || strings.HasPrefix(s.page, pagePlugin)
		if navRow(c, pal, "plugins", icPuzzle, "插件", plugins) {
			s.page = PagePlugins
		}
		if navRow(c, pal, "settings", icGear, "设置", s.page == PageSettings) {
			s.page = PageSettings
		}
	})
}

// workspaceSwitcher shows the project the sidebar lists the tasks of; a
// click lists the others, adding one and removing this one. 最近会话's
// workspace is not among them.
func (s *Shell) workspaceSwitcher(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	name := "添加工作区"
	if v != nil {
		name = v.ws.Name
	}
	anchor := ui.Row(c).Height(36).Padding(0, 6).Gap(9).AlignItems(ui.Center).Radius(7).Cursor(ui.CursorPointer).Focusable().Label("切换工作区")
	if anchor.Hovered() || s.wsMenu {
		anchor.Background(pal.hover)
	}
	anchor.Children(func() {
		ui.Column(c).Size(22, 22).Radius(6).Background(pal.avatar).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
			if v == nil {
				ui.Icon(c, icPlus).Size(13, 13).TextColor(t.TextMuted)
			} else {
				ui.Text(c, initial(name)).FontSize(11.5).Bold()
			}
		})
		ui.Text(c, name).FontSize(13.5).Bold().SingleLine().Shrink(1).MinWidth(0)
		if v != nil && v.ws.OwnerName() != "personal" {
			tag(c, pal, "组织")
		}
		ui.Spacer(c)
		ui.Icon(c, icUpDown).Size(14, 14).TextColor(t.TextMuted)
	})
	if anchor.Clicked() {
		if v == nil {
			s.createWorkspace()
		} else {
			s.wsMenu = !s.wsMenu
		}
	}
	ui.Popover(c, anchor, &s.wsMenu, func() {
		ui.Column(c).Width(240).Padding(4).Gap(1).Children(func() {
			for _, w := range s.projects() {
				w := w
				sel := v != nil && w.ID == v.ws.ID
				if listRow(c, pal, "ws:"+w.ID, w.Name, sel, func() {
					ui.Column(c).Size(20, 20).Radius(5).Background(pal.avatar).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
						ui.Text(c, initial(w.Name)).FontSize(10.5).Bold()
					})
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, w.Name).FontSize(13).SingleLine()
						sub := shortDir(w.Dir)
						if w.Missing() {
							sub = "文件夹不在了"
						}
						ui.Text(c, sub).FontSize(11).TextColor(pal.subtle).SingleLine()
					})
					if sel {
						ui.Icon(c, icCheck).Size(14, 14).TextColor(t.TextMuted)
					}
				}) {
					s.wsMenu = false
					if vv := s.view(w.ID); vv != nil {
						s.focus(w.ID)
					}
				}
			}
			ui.Box(c).Height(1).Background(pal.hairline).Margin(4, 6)
			if navRow(c, pal, "add", icPlus, "添加工作区…", false) {
				s.wsMenu = false
				s.createWorkspace()
			}
			if v != nil {
				label := "移除这个工作区"
				if v.confirmRemove {
					label = "再点一次确认移除（文件保留）"
				}
				if listRow(c, pal, "remove", label, false, func() {
					ui.Icon(c, icX).Size(15, 15).TextColor(t.Danger)
					ui.Text(c, label).FontSize(13).TextColor(t.Danger).SingleLine()
				}) {
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
		sectionLabel(c, "任务")
		ui.Text(c, "还没有任务").FontSize(12.5).TextColor(pal.subtle).Padding(4, 8)
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
	sel := s.page == pageWS+v.ws.ID && v.current == tk.meta.ID
	title := taskTitle(tk)
	waiting := v.waiting(tk)
	running := tk.session.Running() || tk.starting
	if listRow(c, pal, tk.meta.ID, title, sel, func() {
		ui.Row(c).Width(14).Justify(ui.Center).Shrink(0).Children(func() {
			switch {
			case waiting:
				dot(c, pal.waiting)
			case running:
				ui.Spinner(c).Size(12, 12).TextColor(pal.running)
			case tk.meta.Source != "":
				ui.Icon(c, icCloud).Size(13, 13).TextColor(pal.subtle)
			default:
				ui.Icon(c, icChat).Size(13, 13).TextColor(pal.subtle)
			}
		})
		ui.Text(c, title).FontSize(13).SingleLine().Grow(1).Shrink(1).MinWidth(0)
		switch {
		case waiting:
			ui.Text(c, "待确认").FontSize(11).TextColor(pal.waiting).Shrink(0)
		default:
			ui.Text(c, when(tk.meta.UpdatedAt)).FontSize(11).TextColor(pal.subtle).Shrink(0)
		}
	}) {
		s.openTask(v, tk.meta.ID)
	}
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
