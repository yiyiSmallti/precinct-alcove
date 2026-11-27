package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// A workspace's home is where a new task starts: the question, and the
// composer at the foot, where a task's composer is, so that sending the
// first message changes the page over it and not the composer. Under the
// question lies the workspace itself: its recent tasks, its files, and who
// runs it with what (an organization workspace's own pages from its owner
// plugin, such as sync and cloud runs).

// panelState is the state of the alcove and of the file tree.
type panelState struct {
	hidden bool                   // the alcove is hidden
	dirs   map[string]*dirListing // by absolute path
	open   map[string]bool        // folders opened in the file tree
}

type dirListing struct {
	entries []os.DirEntry
	err     string
	loading bool
}

// homeView is the workspace's home.
func (s *Shell) homeView(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	as, qs := v.pending(nil)
	name := v.ws.Name
	if v.ws.ID == s.scratchID {
		name = "默认文件夹"
	}
	topBar(c, pal, func() {
		if v.ws.ID == s.scratchID {
			ui.Icon(c, icHome).Size(16, 16).TextColor(t.TextMuted)
		} else {
			archTile(c, pal, initial(name), 22, pal.avatar)
		}
		ui.Text(c, name).FontSize(14).Bold().SingleLine().Shrink(0)
		if v.ws.OwnerName() != workspace.Personal {
			tag(c, pal, "组织")
		}
		ui.Text(c, shortDir(v.ws.Dir)).FontSize(12).TextColor(pal.subtle).SingleLine().Shrink(1).MinWidth(0)
		ui.Spacer(c)
		s.browserToggle(c, pal, v)
		if !v.browsing { // the browser stands where the alcove would
			s.bandToggle(c, pal)
		}
	})
	scrollFading(c, pal, nil, func() {
		ui.Column(c).Padding(44, 32, 28).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(720).MaxWidthPercent(100).Gap(24).Children(func() {
				ui.Column(c).Gap(10).AlignItems(ui.Center).Padding(8, 0, 4).Children(func() {
					display(c, pal, "要做什么？", 32)
					where := "在「" + name + "」里"
					ui.Text(c, "描述一个任务，Alcove 会"+where+"动手完成；越界的操作会先问你。").FontSize(14).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(520).LineHeight(1.5)
				})
				recent := ordered(v)
				ui.Row(c).Gap(14).Wrap().AlignItems(ui.Stretch).Children(func() {
					if len(recent) > 0 {
						s.recentCard(c, pal, v, recent)
					}
					s.filesCard(c, pal, v)
				})
				s.workspaceCard(c, pal, v)
			})
		})
	})
	ui.Column(c).Padding(0, 32, 18).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(720).MaxWidthPercent(100).Gap(10).Children(func() {
			if len(s.creds) == 0 && v.ws.OwnerName() == workspace.Personal {
				s.credentialHint(c, pal)
			}
			if v.err != "" {
				errorNote(c, v.err)
			}
			for _, a := range as {
				s.approvalCard(c, pal, v, a)
			}
			for _, q := range qs {
				questionCard(c, pal, q)
			}
			s.composer(c, pal, v, nil, &v.draft, "例如：把 data/ 下的报表汇总成季度报告", 3, false, func(text string) {
				s.send(v, nil, text)
			}, nil)
		})
	})
}

// credentialHint asks for a model credential before the first task.
func (s *Shell) credentialHint(c *ui.Context, pal *palette) {
	t := c.Theme()
	ui.Row(c).Gap(12).Padding(12, 14).Radius(12).AlignItems(ui.Center).Background(t.Accent.Alpha(0.07).Over(pal.content)).Border(1, t.Accent.Alpha(0.2)).Children(func() {
		ui.Icon(c, icAlert).Size(16, 16).TextColor(t.Accent)
		ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, "还没有模型凭据").FontSize(13).FontWeight(600)
			ui.Text(c, "添加一个模型 API key 后才能执行任务。").FontSize(12.5).TextColor(t.TextMuted)
		})
		if ui.PrimaryButton(c, "去添加").Clicked() {
			s.page, s.prefs.tab = PageSettings, tabModel
		}
	})
}

// recentCard lists the workspace's latest tasks, each opening on a click.
func (s *Shell) recentCard(c *ui.Context, pal *palette, v *wsView, recent []*taskView) {
	paperCard(c, pal).Key("recent").Grow(1).Basis(300).MinWidth(0).Children(func() {
		cardHead(c, pal, icChat, "最近任务", func() {
			ui.Text(c, fmt.Sprint(len(recent))).FontSize(12).FontFeatures("tnum").TextColor(pal.subtle)
		})
		ui.Column(c).Padding(6).Gap(1).Children(func() {
			for i, tk := range recent {
				if i == 6 {
					break
				}
				title := taskTitle(tk)
				r := ui.Row(c).Key(tk.meta.ID).Height(34).Padding(0, 8).Gap(10).AlignItems(ui.Center).Radius(8).Cursor(ui.CursorPointer).Focusable().Label(title).Transition(fade)
				if r.Hovered() {
					r.Background(pal.hover)
				}
				r.Children(func() {
					s.taskGlyph(c, pal, v, tk)
					ui.Text(c, title).FontSize(13).SingleLine().Grow(1).MinWidth(0)
					if v.waiting(tk) {
						ui.Text(c, "待"+v.waitWord(tk)).FontSize(11.5).FontWeight(500).TextColor(pal.waiting).Shrink(0)
					} else {
						ui.Text(c, when(tk.meta.UpdatedAt)).FontSize(11.5).FontFeatures("tnum").TextColor(pal.subtle).Shrink(0)
					}
				})
				if r.Clicked() {
					s.openTask(v, tk.meta.ID)
				}
			}
		})
	})
}

// filesCard is the workspace's folder as a tree, read as folders open.
func (s *Shell) filesCard(c *ui.Context, pal *palette, v *wsView) {
	paperCard(c, pal).Key("files").Grow(1).Basis(300).MinWidth(0).Children(func() {
		cardHead(c, pal, icFolder, "文件", func() {
			if iconButton(c, pal, icReload, "刷新文件", false).Size(26, 26).Clicked() {
				s.panels.dirs = nil
			}
			if s.Reveal != nil && iconButton(c, pal, icOpen, "在文件夹中显示", false).Size(26, 26).Clicked() {
				go s.Reveal(v.ws.Dir)
			}
		})
		ui.Column(c).MaxHeight(300).Children(func() {
			ui.Scroll(c).Children(func() {
				ui.Column(c).Padding(6).Children(func() { s.dirRows(c, pal, v.ws.Dir, 0) })
			})
		})
	})
}

func (s *Shell) dirRows(c *ui.Context, pal *palette, dir string, depth int) {
	t := c.Theme()
	l := s.listing(dir)
	indent := float32(8 + depth*16)
	switch {
	case l.loading:
		ui.Row(c).Padding(6, indent).Children(func() { ui.Spinner(c).Size(12, 12) })
		return
	case l.err != "":
		ui.Text(c, l.err).FontSize(12).TextColor(t.Danger).Padding(6, indent)
		return
	case len(l.entries) == 0 && depth == 0:
		ui.Text(c, "文件夹是空的。").FontSize(12.5).TextColor(t.TextMuted).Padding(8, 8)
		return
	}
	for _, e := range l.entries {
		path := filepath.Join(dir, e.Name())
		isDir := e.IsDir()
		open := s.panels.open[path]
		r := ui.Row(c).Key(path).Height(28).Gap(7).Padding(0, 8, 0, indent).AlignItems(ui.Center).Radius(7).Cursor(ui.CursorPointer).Focusable().Label(e.Name()).Transition(fade)
		if r.Hovered() {
			r.Background(pal.hover)
		}
		r.Children(func() {
			ui.Row(c).Width(10).Shrink(0).Justify(ui.Center).Children(func() {
				if isDir {
					arrow := ui.Icon(c, icChevron).Size(10, 10).TextColor(pal.subtle)
					target := float32(-90)
					if open {
						target = 0
					}
					arrow.Rotate(arrow.AnimateTo("turn", target, ui.Snappy))
				}
			})
			ic, col := icFile, t.TextMuted
			if isDir {
				ic, col = icFolder, t.Accent.Alpha(0.8)
			}
			ui.Icon(c, ic).Size(14, 14).TextColor(col)
			ui.Text(c, e.Name()).FontSize(12.5).SingleLine().Grow(1).MinWidth(0)
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

// workspaceCard is the workspace itself: where it is, who runs it and with
// which model, and the pages its owner plugin adds (an organization
// workspace's sync and cloud runs); and the way to remove it, asking once
// more before it acts.
func (s *Shell) workspaceCard(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	owner := v.ws.OwnerName()
	paperCard(c, pal).Key("workspace").Children(func() {
		cardHead(c, pal, icHome, "工作区", nil)
		fact := func(label string, value func()) {
			ui.Row(c).Padding(10, 14).Gap(16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
				ui.Text(c, label).FontSize(12.5).TextColor(t.TextMuted).Width(64).Shrink(0)
				ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(value)
			})
		}
		fact("位置", func() {
			ui.Text(c, v.ws.Dir).FontSize(12.5).Font(pal.mono).SingleLine().Ellipsis("…").Grow(1).MinWidth(0).Selectable()
			if s.Reveal != nil && ui.Button(c, "在文件夹中显示").Clicked() {
				go s.Reveal(v.ws.Dir)
			}
		})
		fact("归属", func() {
			if owner == workspace.Personal {
				tag(c, pal, "个人")
				ui.Text(c, "用你自己的模型凭据").FontSize(12.5).TextColor(t.TextMuted)
			} else {
				tag(c, pal, "组织")
				ui.Text(c, "由 "+displayName(owner)+" 管理，模型走组织网关").FontSize(12.5).TextColor(t.TextMuted).SingleLine().MinWidth(0)
			}
		})
		if owner == workspace.Personal {
			fact("模型", func() {
				if cr := s.defaultCred(); cr != nil {
					ui.Text(c, providerName(cr.Provider)+" · "+credentials.ModelOf(*cr)).FontSize(12.5).SingleLine().Grow(1).MinWidth(0)
				} else {
					ui.Text(c, "还没有模型凭据").FontSize(12.5).TextColor(t.TextMuted).Grow(1)
				}
				if ui.Button(c, "模型设置").Clicked() {
					s.page, s.prefs.tab = PageSettings, tabModel
				}
			})
		}
		if v.ws.ID == s.scratchID {
			return // the default folder is moved in settings, not removed
		}
		ui.Row(c).Padding(10, 14).Gap(8).AlignItems(ui.Center).Margin(0, 0, -1, 0).Children(func() {
			if v.confirmRemove {
				ui.Text(c, "从 Alcove 移除这个工作区？文件夹里的文件会保留。").FontSize(12.5).TextColor(t.Danger).Grow(1).MinWidth(0)
				if ui.Button(c, "取消").Clicked() {
					v.confirmRemove = false
				}
				if styledButton(c, "确认移除", "danger").Clicked() {
					v.confirmRemove = false
					s.removeWorkspace(v.ws.ID)
				}
				return
			}
			ui.Text(c, "任务记录跟着工作区；移除后文件夹本身不受影响。").FontSize(12).TextColor(pal.subtle).Grow(1).MinWidth(0)
			if ui.Button(c, "移除工作区…").TextColor(t.Danger).Clicked() {
				v.confirmRemove = true
			}
		})
	})
	if owner == workspace.Personal {
		return
	}
	pages := s.contributions(owner, "settings_page")
	if len(pages) == 0 {
		ui.Text(c, displayName(owner)+" 没有为这个工作区提供设置；它没在运行时这里是空的。").FontSize(12.5).TextColor(t.TextMuted)
		return
	}
	for _, ct := range pages {
		s.settingsPageView(c, pal, owner, ct, false, true)
	}
}

// changedFiles lists the files a task's tool calls wrote or edited, in the
// order first touched.
func changedFiles(sess *events.Session) (paths []string, created map[string]bool) {
	return filesIn(sess.Entries)
}

// filesIn lists the files the tool calls among entries wrote or edited.
func filesIn(entries []*events.Entry) (paths []string, created map[string]bool) {
	created = map[string]bool{}
	seen := map[string]bool{}
	for _, e := range entries {
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

func relTo(dir, p string) string {
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
	}
	return p
}

// absIn is a path a tool call names, made absolute in the workspace.
func absIn(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return filepath.Join(dir, p)
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
