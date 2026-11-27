package shell

import (
	_ "embed"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
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

// maxTreeTasks is how many tasks a workspace lists before 显示更多; tasks
// that run or wait on the user, and the one shown, are always listed.
const maxTreeTasks = 5

// sidebar is the left pane: the wordmark and a new task, what waits on
// you, the plugins' own entries (the market, their conversations) and the
// assistants (plugins with a picture and a conversation view), every
// workspace with its tasks as a tree, 最近会话 (the tasks outside a
// project), and the way to plugins and settings.
func (s *Shell) sidebar(c *ui.Context, pal *palette) {
	t := c.Theme()
	v := s.view(s.curWS)
	if bar := c.TitleBar(); bar.Height > gutter {
		// The window's buttons sit here.
		ui.Row(c).Height(bar.Height - gutter).Shrink(0).DragWindow()
	} else {
		ui.Box(c).Height(6)
	}
	ui.Column(c).Padding(0, 10, 4).Gap(1).Children(func() {
		ui.Row(c).Height(40).Padding(0, 6).Gap(10).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Image(c, logo).Size(24, 24).Shrink(0)
			ui.Text(c, "Alcove").Font(pal.display).FontSize(20).FontWeight(600).LetterSpacing(0.2)
		})
		ui.Box(c).Height(4)
		if v != nil && s.newTaskButton(c, pal) {
			s.newTask(v.ws.ID)
		}
		ui.Box(c).Height(8)
		n := s.waitingTotal()
		if listRow(c, pal, "waiting", "等你处理", s.page == PageWaiting, func() {
			ic := ui.Icon(c, icInbox).Size(15, 15).TextColor(t.TextMuted)
			if n > 0 {
				ic.TextColor(pal.waiting)
			}
			ui.Text(c, "等你处理").FontSize(13).Grow(1)
			countBadge(c, pal.waiting, n)
		}) {
			s.page = PageWaiting
		}
		for _, n := range s.pluginNav() {
			unread := 0
			if p, ok := strings.CutPrefix(n.page, pageInbox); ok {
				unread = s.unread(p)
			}
			if listRow(c, pal, n.page, n.title, s.page == n.page, func() {
				ui.Icon(c, n.icon).Size(15, 15).TextColor(t.TextMuted)
				ui.Text(c, n.title).FontSize(13).SingleLine().Grow(1).MinWidth(0)
				unreadBadge(c, pal, unread)
			}) {
				s.page = n.page
			}
		}
		if as := s.assistants(); len(as) > 0 {
			sectionLabel(c, "助手")
			for _, n := range as {
				// The assistant's own pages belong to it too.
				own := s.page == n.page || strings.HasPrefix(s.page, pageView+strings.TrimPrefix(n.page, pageInbox)+"#")
				if listRow(c, pal, n.page, n.title, own, func() {
					avatar(c, n.avatar, 20)
					ui.Text(c, n.title).FontSize(13).SingleLine().Grow(1).MinWidth(0)
					unreadBadge(c, pal, s.unread(strings.TrimPrefix(n.page, pageInbox)))
				}) {
					s.page = n.page
				}
			}
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(0, 10, 12).Gap(1).Children(func() {
			s.workspaceTree(c, pal)
			s.recentList(c, pal)
		})
	})
	ui.Column(c).Padding(6, 10, 10).Gap(1).BorderWidth(1, 0, 0, 0).BorderColor(pal.hairline).Children(func() {
		plugins := s.page == PagePlugins || strings.HasPrefix(s.page, pagePlugin)
		if navRow(c, pal, "plugins", icPuzzle, "插件", plugins) {
			s.page = PagePlugins
		}
		if navRow(c, pal, "settings", icGear, "设置", s.page == PageSettings) {
			s.page = PageSettings
		}
	})
}

// newTaskButton is the sidebar's 新任务: a raised card, in the workspace
// new tasks go to.
func (s *Shell) newTaskButton(c *ui.Context, pal *palette) bool {
	t := c.Theme()
	b := ui.Row(c).Key("new").Height(36).Padding(0, 10).Gap(9).AlignItems(ui.Center).Radius(10).Cursor(ui.CursorPointer).Focusable().Label("新任务").Transition(fade)
	b.Background(pal.content.Alpha(0.72)).Border(1, pal.hairline).Shadow(0, 1, 2, 0, pal.shadow)
	if b.Hovered() {
		b.Background(pal.content).Shadow(0, 4, 12, -2, pal.shadow.Alpha(1.6))
	}
	b.Children(func() {
		ui.Icon(c, icCompose).Size(15, 15).TextColor(t.Accent)
		ui.Text(c, "新任务").FontSize(13).FontWeight(600).Grow(1)
	})
	return b.Clicked()
}

// workspaceTree lists the projects, each with its tasks under it while it
// is open: a project opens on its own while it is the one tasks go to, or
// while one of its tasks runs or waits on the user.
func (s *Shell) workspaceTree(c *ui.Context, pal *palette) {
	if sectionHead(c, pal, "工作区", icPlus, "添加工作区…") {
		s.createWorkspace()
	}
	ps := s.projects()
	if len(ps) == 0 {
		if navRow(c, pal, "add-ws", icFolder, "添加工作区…", false) {
			s.createWorkspace()
		}
		return
	}
	for _, w := range ps {
		if v := s.view(w.ID); v != nil {
			s.workspaceRow(c, pal, v)
		}
	}
}

// activity counts a workspace's tasks at work and those waiting on the
// user.
func activity(v *wsView) (running, waiting int) {
	for _, tk := range v.tasks {
		switch {
		case v.waiting(tk):
			waiting++
		case tk.session.Running() || tk.starting:
			running++
		}
	}
	return running, waiting
}

// treeOpen reports whether a workspace lists its tasks.
func (s *Shell) treeOpen(v *wsView) bool {
	if open, ok := s.wsOpen[v.ws.ID]; ok {
		return open
	}
	running, waiting := activity(v)
	return v.ws.ID == s.curProj || running+waiting > 0
}

// workspaceRow is one project and, open, its tasks. A click opens its home;
// on its home, a click folds it.
func (s *Shell) workspaceRow(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	open := s.treeOpen(v)
	home := s.page == pageWS+v.ws.ID && v.current == ""
	running, waiting := activity(v)
	if listRow(c, pal, "ws:"+v.ws.ID, v.ws.Name, home, func() {
		archTile(c, pal, initial(v.ws.Name), 20, pal.avatar)
		ui.Text(c, v.ws.Name).FontSize(13).FontWeight(550).SingleLine().Shrink(1).MinWidth(0)
		if v.ws.OwnerName() != workspace.Personal {
			tag(c, pal, "组织")
		}
		ui.Spacer(c)
		switch {
		case v.ws.Missing():
			ui.Icon(c, icAlert).Size(13, 13).TextColor(t.Danger)
		case waiting > 0:
			countBadge(c, pal.waiting, waiting)
		case running > 0 && !open:
			ui.Spinner(c).Size(11, 11).TextColor(pal.running)
		}
		arrow := ui.Icon(c, icChevron).Size(12, 12).TextColor(pal.subtle)
		target := float32(0)
		if !open {
			target = -90
		}
		arrow.Rotate(arrow.AnimateTo("turn", target, ui.Snappy))
	}) {
		if s.wsOpen == nil {
			s.wsOpen = map[string]bool{}
		}
		if home {
			s.wsOpen[v.ws.ID] = !open
		} else {
			s.wsOpen[v.ws.ID] = true
			s.newTask(v.ws.ID)
		}
	}
	if !open {
		return
	}
	ui.Column(c).Key("tasks:"+v.ws.ID).Gap(1).Padding(0, 0, 4).Children(func() {
		if v.ws.Missing() {
			ui.Text(c, "文件夹不在了："+shortDir(v.ws.Dir)).FontSize(12).TextColor(t.Danger).Padding(4, 8, 4, 38)
		}
		list := ordered(v)
		if len(list) == 0 {
			ui.Text(c, "还没有任务").FontSize(12).TextColor(pal.subtle).Padding(4, 8, 6, 38)
			return
		}
		shown, more := s.treeTasks(v, list)
		for _, tk := range shown {
			s.taskRow(c, pal, v, tk)
		}
		switch {
		case more > 0:
			if moreRow(c, pal, "more:"+v.ws.ID, fmt.Sprintf("显示更多（%d）", more)) {
				s.showAll(v.ws.ID, true)
			}
		case s.wsAll[v.ws.ID] && len(list) > maxTreeTasks:
			if moreRow(c, pal, "less:"+v.ws.ID, "收起") {
				s.showAll(v.ws.ID, false)
			}
		}
	})
}

func (s *Shell) showAll(id string, all bool) {
	if s.wsAll == nil {
		s.wsAll = map[string]bool{}
	}
	s.wsAll[id] = all
}

// treeTasks picks the tasks a workspace lists: those at work or waiting on
// the user first, then the most recent, up to maxTreeTasks unless all are
// asked for; the task shown is always among them. more is how many are
// left out.
func (s *Shell) treeTasks(v *wsView, list []*taskView) (shown []*taskView, more int) {
	var active, rest []*taskView
	for _, tk := range list {
		if tk.session.Running() || tk.starting || v.waiting(tk) {
			active = append(active, tk)
		} else {
			rest = append(rest, tk)
		}
	}
	shown = active
	for _, tk := range rest {
		if s.wsAll[v.ws.ID] || len(shown) < maxTreeTasks || tk.meta.ID == v.current && s.page == pageWS+v.ws.ID {
			shown = append(shown, tk)
		} else {
			more++
		}
	}
	return shown, more
}

// moreRow is a quiet row that unfolds or folds a list.
func moreRow(c *ui.Context, pal *palette, key, label string) bool {
	r := ui.Row(c).Key(key).Height(26).Padding(0, 8, 0, 38).AlignItems(ui.Center).Radius(8).Cursor(ui.CursorPointer).Focusable().Label(label).Transition(fade)
	col := pal.subtle
	if r.Hovered() {
		r.Background(pal.hover)
		col = c.Theme().TextMuted
	}
	r.Children(func() { ui.Text(c, label).FontSize(12).TextColor(col) })
	return r.Clicked()
}

// recentList is 最近会话: the conversations outside a project, in the
// default folder.
func (s *Shell) recentList(c *ui.Context, pal *palette) {
	sv := s.view(s.scratchID)
	if sv == nil {
		return
	}
	recent := ordered(sv)
	ui.Box(c).Height(8)
	action := ""
	if len(recent) > 0 {
		action = "新会话"
	}
	if sectionHead(c, pal, "最近会话", icCompose, action) {
		s.newTask(s.scratchID)
	}
	// With a project chosen, 新任务 goes there: this is the way back to the
	// default folder before it has a conversation.
	if len(recent) == 0 {
		if navRow(c, pal, "new-scratch", icCompose, "新会话", s.page == pageWS+s.scratchID && sv.current == "") {
			s.newTask(s.scratchID)
		}
		return
	}
	shown, more := s.treeTasks(sv, recent)
	for _, tk := range shown {
		s.taskRow(c, pal, sv, tk)
	}
	switch {
	case more > 0:
		if moreRow(c, pal, "more:"+sv.ws.ID, fmt.Sprintf("显示更多（%d）", more)) {
			s.showAll(sv.ws.ID, true)
		}
	case s.wsAll[sv.ws.ID] && len(recent) > maxTreeTasks:
		if moreRow(c, pal, "less:"+sv.ws.ID, "收起") {
			s.showAll(sv.ws.ID, false)
		}
	}
}

// taskRow is one task in the tree: how it is going, its title, and when it
// last moved (or that it waits on the user).
func (s *Shell) taskRow(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	t := c.Theme()
	sel := s.page == pageWS+v.ws.ID && v.current == tk.meta.ID
	title := taskTitle(tk)
	waiting := v.waiting(tk)
	running := tk.session.Running() || tk.starting
	r := ui.Row(c).Key(tk.meta.ID).Height(30).Padding(0, 8, 0, 18).Gap(8).AlignItems(ui.Center).Radius(8).Cursor(ui.CursorPointer).Focusable().Label(title).Transition(fade)
	switch {
	case sel:
		r.Background(pal.selected)
	case r.Hovered():
		r.Background(pal.hover)
	}
	r.Children(func() {
		s.taskGlyph(c, pal, v, tk)
		txt := ui.Text(c, title).FontSize(13).SingleLine().Grow(1).Shrink(1).MinWidth(0)
		if !sel {
			txt.TextColor(t.Text.Alpha(0.86))
		}
		switch {
		case waiting:
			ui.Text(c, "待"+v.waitWord(tk)).FontSize(11).FontWeight(500).TextColor(pal.waiting).Shrink(0)
		case running:
		default:
			ui.Text(c, when(tk.meta.UpdatedAt)).FontSize(11).FontFeatures("tnum").TextColor(pal.subtle).Shrink(0)
		}
	})
	if r.Clicked() {
		s.openTask(v, tk.meta.ID)
	}
}

// taskGlyph is a task's state before its title: waiting on the user,
// running, failed, started by a plugin or the cloud bot, or at rest.
func (s *Shell) taskGlyph(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	short := endedShort(tk.session)
	ui.Row(c).Width(12).Justify(ui.Center).Shrink(0).Children(func() {
		switch {
		case v.waiting(tk):
			dot(c, pal.waiting)
		case tk.session.Running() || tk.starting:
			ui.Spinner(c).Size(11, 11).TextColor(pal.running)
		case tk.err != "" || short != nil && short.IsError:
			dot(c, c.Theme().Danger)
		case tk.meta.Source != "" && s.avatars[tk.meta.Source] != nil && tk.meta.Source != v.ws.OwnerName():
			avatar(c, s.avatars[tk.meta.Source], 14)
		case tk.meta.Source != "":
			ui.Icon(c, icCloud).Size(12, 12).TextColor(pal.subtle)
		default:
			ui.Box(c).Size(5, 5).Radius(2.5).Background(pal.subtle.Alpha(0.6))
		}
	})
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
