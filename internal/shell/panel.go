package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// The right panel: what the task shown has done (files it changed, its
// steps), the workspace's files, and the workspace itself with what its
// owner plugin offers for it (an organization workspace's sync and cloud
// runs).

const (
	tabProgress = iota
	tabFiles
	tabWorkspace
)

// panelState is the right panel's state.
type panelState struct {
	hidden bool
	tab    int
	dirs   map[string]*dirListing // by absolute path
	open   map[string]bool        // folders opened in the file tree
}

type dirListing struct {
	entries []os.DirEntry
	err     string
	loading bool
}

func (s *Shell) panel(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	col := ui.Column(c).Width(300).Shrink(0).BorderWidth(0, 0, 0, 1).BorderColor(pal.hairline)
	sideGround(c, pal, col).Children(func() {
		r := ui.Row(c).Height(52).Shrink(0).Padding(0, 12).Gap(2).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline)
		if c.TitleBar().Height > 0 {
			r.DragWindow()
		}
		r.Children(func() {
			for i, label := range []string{"进度", "文件", "工作区"} {
				if tabButton(c, pal, label, s.panels.tab == i) {
					s.panels.tab = i
				}
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(16, 16, 24).Gap(8).Children(func() {
				switch s.panels.tab {
				case tabFiles:
					s.filesTab(c, pal, v)
				case tabWorkspace:
					s.workspaceTab(c, pal, v)
				default:
					s.progressTab(c, pal, v, tk)
				}
			})
		})
	})
}

// tabButton is a pill tab, filled when chosen.
func tabButton(c *ui.Context, pal *palette, label string, sel bool) bool {
	t := c.Theme()
	r := ui.Row(c).Height(28).Padding(0, 12).AlignItems(ui.Center).Radius(14).Cursor(ui.CursorPointer).Focusable().Label(label).Transition(fade)
	switch {
	case sel:
		r.Background(pal.selected)
	case r.Hovered():
		r.Background(pal.hover)
	}
	r.Children(func() {
		txt := ui.Text(c, label).FontSize(13)
		if sel {
			txt.FontWeight(600)
		} else {
			txt.TextColor(t.TextMuted)
		}
	})
	return r.Clicked()
}

// statRow is one line of a panel's facts: a label and its value.
func statRow(c *ui.Context, pal *palette, label string, value func()) {
	ui.Row(c).Height(32).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
		ui.Text(c, label).FontSize(13).TextColor(c.Theme().TextMuted).Grow(1)
		value()
	})
}

// changedFiles lists the files a task's tool calls wrote or edited, in the
// order first touched.
func changedFiles(sess *events.Session) (paths []string, created map[string]bool) {
	created = map[string]bool{}
	seen := map[string]bool{}
	for _, e := range sess.Entries {
		if e.Kind != events.Tool || e.IsError {
			continue
		}
		verb := toolVerb(e.ToolName)
		if verb != "写入文件" && verb != "修改文件" {
			continue
		}
		p := argPath(e.Args)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
		if verb == "写入文件" {
			created[p] = true
		}
	}
	return paths, created
}

func argPath(args string) string {
	for _, k := range []string{"file_path", "path"} {
		if v := jsonString(args, k); v != "" {
			return v
		}
	}
	return ""
}

func (s *Shell) progressTab(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	t := c.Theme()
	if tk == nil {
		panelHint(c, "开始一个任务后，这里会显示它改了哪些文件、做了几步。")
		return
	}
	calls, failed, running := 0, 0, 0
	for _, e := range tk.session.Entries {
		if e.Kind == events.Tool {
			calls++
			switch {
			case !e.Done:
				running++
			case e.IsError:
				failed++
			}
		}
	}
	as, qs := v.pending(tk)
	panelLabel(c, "概况")
	ui.Column(c).Children(func() {
		statRow(c, pal, "状态", func() {
			switch {
			case len(as)+len(qs) > 0:
				status(c, pal.waiting, "等你确认")
			case tk.session.Running() || tk.starting:
				status(c, pal.running, "运行中")
			case len(tk.session.Entries) > 0:
				status(c, pal.done, "已完成")
			default:
				status(c, pal.subtle, "空闲")
			}
		})
		statRow(c, pal, "工具调用", func() {
			txt := fmt.Sprint(calls)
			if failed > 0 {
				txt += fmt.Sprintf("（%d 次出错）", failed)
			}
			ui.Text(c, txt).FontSize(13).FontFeatures("tnum")
		})
		statRow(c, pal, "最后活动", func() { ui.Text(c, when(tk.meta.UpdatedAt)).FontSize(13) })
	})
	_ = running
	_ = t
	paths, created := changedFiles(tk.session)
	ui.Box(c).Height(8)
	panelLabel(c, fmt.Sprintf("改动的文件 · %d", len(paths)))
	if len(paths) == 0 {
		panelHint(c, "还没有写入或修改文件。命令行里改的文件不在这里统计，可以到“文件”里看。")
		return
	}
	for _, p := range paths {
		mark, color := "改", pal.waiting
		if created[p] {
			mark, color = "新", pal.done
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(v.ws.Dir, p)
		}
		r := ui.Row(c).Key(p).Height(28).Gap(8).Padding(0, 8).Margin(0, -8).AlignItems(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(p).Tooltip("打开 " + abs)
		if r.Hovered() {
			r.Background(pal.hover)
		}
		r.Children(func() {
			ui.Text(c, mark).FontSize(10.5).Bold().TextColor(color).Shrink(0)
			ui.Text(c, relTo(v.ws.Dir, p)).FontSize(12.5).SingleLine().Grow(1).MinWidth(0)
		})
		if r.Clicked() {
			s.openPath(abs)
		}
	}
}

func relTo(dir, p string) string {
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return p
}

// filesTab is the workspace's folder as a tree, read as folders open.
func (s *Shell) filesTab(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, shortDir(v.ws.Dir)).FontSize(12).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
		if ui.Button(c, "刷新").Clicked() {
			s.panels.dirs = nil
		}
		if s.Reveal != nil && ui.Button(c, "在文件夹中显示").Clicked() {
			go s.Reveal(v.ws.Dir)
		}
	})
	s.dirRows(c, pal, v.ws.Dir, 0)
}

func (s *Shell) dirRows(c *ui.Context, pal *palette, dir string, depth int) {
	t := c.Theme()
	l := s.listing(dir)
	switch {
	case l.loading:
		ui.Row(c).Padding(4, float32(8+depth*14)).Children(func() { ui.Spinner(c).Size(12, 12) })
		return
	case l.err != "":
		ui.Text(c, l.err).FontSize(12).TextColor(t.Danger).Padding(4, float32(8+depth*14))
		return
	case len(l.entries) == 0 && depth == 0:
		panelHint(c, "文件夹是空的。")
		return
	}
	for _, e := range l.entries {
		path := filepath.Join(dir, e.Name())
		isDir := e.IsDir()
		open := s.panels.open[path]
		r := ui.Row(c).Key(path).Gap(6).Padding(4, 8, 4, float32(8+depth*14)).AlignItems(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(e.Name())
		if r.Hovered() {
			r.Background(pal.hover)
		}
		r.Children(func() {
			ic := icFile
			if isDir {
				ic = icFolder
			}
			ui.Icon(c, ic).Size(13, 13).TextColor(t.TextMuted)
			ui.Text(c, e.Name()).FontSize(12.5).SingleLine().Grow(1).MinWidth(0)
			if isDir {
				arrow := ui.Icon(c, icChevron).Size(12, 12).TextColor(t.TextMuted)
				if !open {
					arrow.Rotate(-90)
				}
			}
		})
		if r.Clicked() {
			if isDir {
				if s.panels.open == nil {
					s.panels.open = map[string]bool{}
				}
				s.panels.open[path] = !open
			} else {
				s.openPath(path)
			}
		}
		if isDir && open && depth < 8 {
			s.dirRows(c, pal, path, depth+1)
		}
	}
}

// listing returns a folder's entries, reading them in the background the
// first time: folders first, hidden files and .git left out.
func (s *Shell) listing(dir string) *dirListing {
	if s.panels.dirs == nil {
		s.panels.dirs = map[string]*dirListing{}
	}
	l := s.panels.dirs[dir]
	if l != nil {
		return l
	}
	l = &dirListing{loading: true}
	s.panels.dirs[dir] = l
	go func() {
		all, err := os.ReadDir(dir)
		var keep []os.DirEntry
		for _, e := range all {
			if !strings.HasPrefix(e.Name(), ".") {
				keep = append(keep, e)
			}
		}
		sort.SliceStable(keep, func(i, j int) bool {
			if keep[i].IsDir() != keep[j].IsDir() {
				return keep[i].IsDir()
			}
			return strings.ToLower(keep[i].Name()) < strings.ToLower(keep[j].Name())
		})
		if len(keep) > 500 {
			keep = keep[:500]
		}
		s.post(func() {
			l.loading, l.entries = false, keep
			if err != nil {
				l.err = "读不了这个文件夹：" + describe(err)
			}
		})
	}()
	return l
}

// workspaceTab is the workspace: where it is, who runs it, and the pages
// its owner plugin adds (an organization workspace's sync and cloud runs).
func (s *Shell) workspaceTab(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	owner := v.ws.OwnerName()
	panelLabel(c, "工作区")
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, v.ws.Name).FontSize(14).Bold()
		ui.Text(c, v.ws.Dir).FontSize(12).TextColor(t.TextMuted).Selectable()
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if owner == "personal" {
				tag(c, pal, "个人")
				ui.Text(c, "用你自己的模型凭据").FontSize(12).TextColor(t.TextMuted)
			} else {
				tag(c, pal, "组织")
				ui.Text(c, "由 "+displayName(owner)+" 管理，模型走组织网关").FontSize(12).TextColor(t.TextMuted)
			}
		})
		if s.Reveal != nil && ui.Button(c, "在文件夹中显示").Clicked() {
			go s.Reveal(v.ws.Dir)
		}
	})
	if owner == "personal" {
		panelLabel(c, "模型")
		if cr := s.defaultCred(); cr != nil {
			panelHint(c, providerName(cr.Provider)+" · "+credentials.ModelOf(*cr)+"。在“设置”里换模型或凭据。")
		} else {
			panelHint(c, "还没有模型凭据，在“设置”里添加。")
		}
		return
	}
	pages := s.contributions(owner, "settings_page")
	if len(pages) == 0 {
		panelHint(c, displayName(owner)+" 没有为这个工作区提供设置；它没在运行时这里是空的。")
		return
	}
	for _, ct := range pages {
		s.settingsPageView(c, owner, ct)
	}
}

func panelLabel(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(11).FontWeight(500).TextColor(c.Theme().TextMuted).Padding(2, 0, 4)
}

func panelHint(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(12.5).TextColor(c.Theme().TextMuted)
}

// openPath opens a file with its default application.
func (s *Shell) openPath(path string) {
	if s.OpenPath == nil {
		return
	}
	go func() {
		if err := s.OpenPath(path); err != nil {
			s.post(func() { s.notice = "打不开 " + path + "：" + describe(err) })
		}
	}()
}
