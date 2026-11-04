package shell

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// workspaceView is the middle of the window: the task chosen in the
// sidebar, or the new-task page.
func (s *Shell) workspaceView(c *ui.Context, pal *palette, v *wsView) {
	tk := v.tasks[v.current]
	if tk == nil {
		v.current = ""
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).Basis(0).MinWidth(0).Children(func() {
			if tk == nil {
				s.newTaskView(c, pal, v)
			} else {
				s.taskView(c, pal, v, tk)
			}
		})
		switch {
		case v.browsing:
			s.browserPane(c, pal, v)
		case !s.panels.hidden:
			s.panel(c, pal, v, tk)
		}
	})
}

// panelToggle shows or hides the right panel, which the browser pane
// takes the place of while it shows.
func (s *Shell) panelToggle(c *ui.Context, pal *palette, v *wsView) {
	shown := !s.panels.hidden && !v.browsing
	label := "隐藏右栏"
	if !shown {
		label = "显示右栏"
	}
	if iconButton(c, pal, icPanel, label, shown).Clicked() {
		s.panels.hidden = shown
		v.browsing = false
	}
}

// topBar is a column's title bar: 52 DIPs, draggable where the window has
// no title bar of its own.
func topBar(c *ui.Context, pal *palette, fn func()) {
	r := ui.Row(c).Height(52).Shrink(0).Padding(0, 14, 0, 22).Gap(6).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline)
	if c.TitleBar().Height > 0 {
		r.DragWindow()
	}
	r.Children(fn)
}

// jsonString is one string field of a JSON object, or "".
func jsonString(raw, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

// newTaskView asks what to do: the question in the middle of the page, and
// the composer at its foot, as on a task's page, with the workspace it will
// run in.
func (s *Shell) newTaskView(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	as, qs := v.pending(nil)
	topBar(c, pal, func() {
		ui.Spacer(c)
		s.browserToggle(c, pal, v)
		s.panelToggle(c, pal, v)
	})
	// A scroll area does not center what it holds, so the column takes its
	// height from the last frame, drawing one more when that height changed
	// or is not known yet (0, on the page's first frame).
	sc := ui.Scroll(c).Grow(1)
	if h := sc.LayoutBounds().H; h == 0 || h != s.newTaskH {
		s.newTaskH = h
		c.Invalidate()
	}
	sc.Children(func() {
		ui.Column(c).MinHeight(s.newTaskH).Padding(32).AlignItems(ui.Center).Justify(ui.Center).Gap(10).Children(func() {
			ui.Text(c, "要做什么？").FontSize(26).Bold().LetterSpacing(-0.3)
			ui.Text(c, "描述一个任务，Alcove 会在这个文件夹里动手完成，越界的操作会先问你。").FontSize(14).TextColor(t.TextMuted).MaxWidth(560).TextAlign(ui.Center)
		})
	})
	ui.Column(c).Padding(0, 32, 16).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(720).MaxWidthPercent(100).Gap(10).Children(func() {
			if len(s.creds) == 0 {
				s.credentialHint(c, pal)
			}
			if v.err != "" {
				ui.Text(c, v.err).TextColor(t.Danger).Selectable()
			}
			for _, a := range as {
				s.approvalCard(c, pal, a)
			}
			for _, q := range qs {
				questionCard(c, pal, q)
			}
			s.composer(c, pal, v, nil, &v.draft, "例如：把 data/ 下的报表汇总成季度报告", 3, false, func(text string) {
				s.send(v, nil, text)
			}, nil)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(0, 4).Children(func() {
				ui.Icon(c, icFolder).Size(13, 13).TextColor(pal.subtle)
				name := v.ws.Name
				if v.ws.ID == s.scratchID {
					name = "默认文件夹"
				}
				ui.Text(c, name).FontSize(12).TextColor(t.TextMuted)
				ui.Text(c, shortDir(v.ws.Dir)).FontSize(12).TextColor(pal.subtle).SingleLine().MinWidth(0)
			})
		})
	})
}

// credentialHint asks for a model credential before the first task.
func (s *Shell) credentialHint(c *ui.Context, pal *palette) {
	t := c.Theme()
	ui.Row(c).Gap(12).Padding(12, 14).Radius(12).AlignItems(ui.Center).Background(t.Accent.Alpha(0.08)).Border(1, t.Accent.Alpha(0.2)).Children(func() {
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

// taskView is one task's conversation, with its title bar and what it
// waits on pinned above the input.
func (s *Shell) taskView(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	t := c.Theme()
	sess := tk.session
	running := sess.Running() || tk.starting
	topBar(c, pal, func() {
		ui.Text(c, taskTitle(tk)).FontSize(14).Bold().SingleLine().Shrink(1).MinWidth(0)
		if tk.meta.Source != "" {
			tag(c, pal, "云端 bot")
		}
		ui.Spacer(c)
		switch {
		case v.waiting(tk):
			status(c, pal.waiting, "等你确认")
		case running:
			status(c, pal.running, "运行中")
		case len(sess.Entries) > 0:
			status(c, pal.done, "已完成")
		}
		ui.Box(c).Width(8)
		s.browserToggle(c, pal, v)
		s.panelToggle(c, pal, v)
		more := iconButton(c, pal, icMore, "更多", false)
		more.PopoverMenu(func(m *ui.Menu) {
			if m.Item("从列表移除").Disabled(sess.Running()).Chosen() {
				s.forget(v, tk)
			}
		})
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(28, 32, 32).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(720).MaxWidthPercent(100).Gap(16).Children(func() {
				if len(sess.Entries) == 0 {
					msg := "在下面输入，Alcove 会在这个文件夹里执行。"
					if tk.legacy {
						msg = "这是更新前的对话，之前的记录还在，继续输入就会接着它往下做。"
					}
					ui.Text(c, msg).TextColor(t.TextMuted)
				}
				s.entries(c, pal, tk)
				if tk.starting && !sess.Running() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c).Size(13, 13).TextColor(pal.running)
						ui.Text(c, "正在启动…").FontSize(13).TextColor(t.TextMuted)
					})
				}
				if tk.err != "" {
					ui.Text(c, tk.err).TextColor(t.Danger).Selectable()
				}
			})
		})
	})
	as, qs := v.pending(tk)
	ui.Column(c).Padding(0, 32, 20).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(720).MaxWidthPercent(100).Gap(10).Children(func() {
			for _, a := range as {
				s.approvalCard(c, pal, a)
			}
			for _, q := range qs {
				questionCard(c, pal, q)
			}
			var onStop func()
			if sess.Running() && !tk.stopping {
				onStop = func() { s.stop(v, tk) }
			}
			s.composer(c, pal, v, tk, &tk.input, "补充要求或继续追问…", 1, tk.starting, func(text string) {
				s.send(v, tk, text)
			}, onStop)
		})
	})
}

// composer is the message box under the run bar (the task's context, the
// model and its thinking effort), with a round send (and stop) button in
// its corner. tk is nil for a task not started yet.
func (s *Shell) composer(c *ui.Context, pal *palette, v *wsView, tk *taskView, input *string, placeholder string, lines int, busy bool, send func(string), stop func()) {
	t := c.Theme()
	s.runBar(c, pal, v, tk)
	box := ui.Column(c).Padding(12, 12, 10, 14).Gap(4).Radius(14).Background(pal.content).Transition(fade)
	// Typing lights the box in the accent.
	if box.FocusWithin() {
		box.Border(1, t.Accent.Alpha(0.55)).Shadow(0, 6, 24, 0, t.Accent.Alpha(0.16))
	} else {
		box.Border(1, t.Border).Shadow(0, 4, 18, 0, pal.shadow)
	}
	box.Children(func() {
		// A text area reports no Submitted: Enter sends through OnTextKey,
		// Shift+Enter (any modifier) breaks the line, and an input method's
		// own Enter never reaches it.
		ui.TextArea(c, input).Placeholder(placeholder).Label("消息").Background(ui.Transparent).Border(0, ui.Transparent).Padding(0).FontSize(14).Lines(lines, 10).MinHeight(0).
			OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter || k.Mods != 0 {
					return false
				}
				if text := strings.TrimSpace(k.Text()); text != "" && !busy {
					k.SetText("")
					send(text)
				}
				return true
			})
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "↵ 发送 · ⇧↵ 换行").FontSize(11).TextColor(pal.subtle).Grow(1).SingleLine()
			if stop != nil && roundButton(c, pal, icStop, "停止", false, false) {
				stop()
			}
			text := strings.TrimSpace(*input)
			off := text == "" || busy
			if roundButton(c, pal, icSend, "发送", true, off) && !off {
				*input = ""
				send(text)
			}
		})
	})
}

// entries draws the conversation. Tool calls are one line each, opened on
// a click.
func (s *Shell) entries(c *ui.Context, pal *palette, tk *taskView) {
	t := c.Theme()
	for _, e := range tk.session.Entries {
		switch e.Kind {
		case events.User:
			ui.Row(c).Key(e.Key).Children(func() {
				ui.Spacer(c)
				ui.Text(c, e.Text).FontSize(14).Selectable().Padding(10, 15).Radius(18).Background(pal.userMsg).MaxWidth(540)
			})
		case events.Assistant:
			ui.Column(c).Key(e.Key).Children(func() {
				if e.Streaming {
					markdown.Stream(c, e.Text, nil)
				} else {
					markdown.View(c, e.Text, nil)
				}
			})
		case events.Reasoning:
			ui.Text(c, e.Text).Key(e.Key).FontSize(12.5).TextColor(t.TextMuted).Selectable().MaxLines(3).Padding(0, 0, 0, 12).BorderWidth(0, 0, 0, 2).BorderColor(pal.hairline)
		case events.Tool:
			s.toolStep(c, pal, tk, e)
		case events.Notice:
			ui.Row(c).Key(e.Key).Gap(6).AlignItems(ui.Center).Children(func() {
				dot(c, pal.waiting)
				ui.Text(c, e.Text).FontSize(13).TextColor(t.TextMuted).Selectable()
			})
		}
	}
}

func (s *Shell) toolStep(c *ui.Context, pal *palette, tk *taskView, e *events.Entry) {
	t := c.Theme()
	open := tk.open[e.Key]
	ui.Column(c).Key(e.Key).Gap(4).Margin(-6, -8).Children(func() {
		r := ui.Row(c).Height(30).Gap(9).AlignItems(ui.Center).Padding(0, 8).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(e.ToolName)
		if r.Hovered() {
			r.Background(pal.hover)
		}
		r.Children(func() {
			ui.Row(c).Width(14).Justify(ui.Center).Shrink(0).Children(func() {
				switch {
				case !e.Done:
					ui.Spinner(c).Size(12, 12).TextColor(pal.running)
				case e.IsError:
					ui.Icon(c, icX).Size(13, 13).TextColor(t.Danger)
				default:
					ui.Icon(c, icCheck).Size(13, 13).TextColor(pal.subtle)
				}
			})
			ui.Text(c, toolVerb(e.ToolName)).FontSize(13).FontWeight(500).Shrink(0)
			ui.Text(c, entrySummary(*e)).FontSize(12).TextColor(t.TextMuted).Font(pal.mono).SingleLine().Grow(1).MinWidth(0)
			arrow := ui.Icon(c, icChevron).Size(13, 13).TextColor(pal.subtle)
			if !open {
				arrow.Rotate(-90)
			}
		})
		if r.Clicked() {
			tk.open[e.Key] = !open
		}
		if open {
			ui.Column(c).Gap(6).Padding(10, 12).Radius(8).Background(pal.code).Border(1, pal.hairline).Margin(0, 8, 6, 31).Children(func() {
				if e.Args != "" {
					ui.Text(c, "参数").FontSize(11).FontWeight(500).TextColor(pal.subtle)
					ui.Text(c, clip(prettyArgs(e.Args), 2000)).FontSize(12).Font(pal.mono).Selectable()
				}
				if e.Result != "" {
					color := t.Text
					if e.IsError {
						color = t.Danger
					}
					ui.Text(c, "输出").FontSize(11).FontWeight(500).TextColor(pal.subtle)
					ui.Text(c, clip(e.Result, 4000)).FontSize(12).Font(pal.mono).TextColor(color).Selectable()
				}
			})
		}
	})
}

// toolVerb names a tool call the way a person would.
func toolVerb(name string) string {
	switch strings.ToLower(name) {
	case "bash", "shell", "exec":
		return "运行命令"
	case "read", "read_file", "view":
		return "读取文件"
	case "write", "write_file", "create":
		return "写入文件"
	case "edit", "edit_file", "str_replace", "multiedit", "apply_patch":
		return "修改文件"
	case "ls", "list", "glob":
		return "查看目录"
	case "grep", "search":
		return "搜索"
	case "fetch", "webfetch", "web_fetch":
		return "访问网页"
	case "browser_open":
		return "打开网页"
	case "browser_snapshot":
		return "阅读页面"
	case "browser_click":
		return "点击"
	case "browser_type":
		return "输入"
	case "browser_press":
		return "按键"
	case "browser_scroll":
		return "滚动页面"
	case "browser_back":
		return "后退"
	case "browser_screenshot":
		return "截图"
	}
	return name
}

// entrySummary is what a tool entry's line shows after its verb. A click
// or an entry into a page names the element it reached, once the call is
// done and its result says so ({"result":{"clicked":"[e3] link \"关于\""}}).
func entrySummary(e events.Entry) string {
	switch strings.ToLower(e.ToolName) {
	case "browser_click", "browser_type":
		var r struct {
			Result struct{ Clicked, Typed string } `json:"result"`
		}
		var a struct{ Ref, Text string }
		json.Unmarshal([]byte(e.Args), &a)
		if json.Unmarshal([]byte(e.Result), &r) == nil {
			if el := r.Result.Clicked + r.Result.Typed; el != "" {
				// "[e3] link "关于我们" → /about": the element without its ref.
				if _, rest, ok := strings.Cut(el, "] "); ok {
					el = rest
				}
				if a.Text != "" {
					return oneLine(fmt.Sprintf("“%s” → %s", a.Text, el))
				}
				return oneLine(el)
			}
		}
		if a.Text != "" {
			return oneLine(fmt.Sprintf("“%s” → %s", a.Text, a.Ref))
		}
		return a.Ref
	case "browser_snapshot", "browser_back", "browser_screenshot":
		return ""
	}
	return toolSummary(e.Args)
}

// toolSummary is the one argument that says what a call does: the command,
// the path, the pattern; else the arguments squeezed onto one line.
func toolSummary(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		for _, k := range []string{"command", "cmd", "file_path", "path", "pattern", "url", "query", "description"} {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return oneLine(v)
			}
		}
	}
	return oneLine(args)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return clip(s, 200)
}

// prettyArgs indents JSON arguments; anything else is shown as it came.
func prettyArgs(args string) string {
	var v any
	if json.Unmarshal([]byte(args), &v) != nil {
		return args
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return args
	}
	return string(b)
}
