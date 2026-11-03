package shell

import (
	"encoding/json"
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
		s.newTaskView(c, pal, v)
		return
	}
	s.taskView(c, pal, v, tk)
}

// newTaskView asks what to do, with the workspace it will run in.
func (s *Shell) newTaskView(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	as, qs := v.pending(nil)
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(72, 32, 24).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(640).MaxWidthPercent(100).Gap(14).Children(func() {
				ui.Icon(c, icChat).Size(28, 28).TextColor(t.Accent)
				ui.Text(c, "要做什么？").FontSize(24).Bold()
				ui.Text(c, "描述一个任务，Alcove 会在「"+v.ws.Name+"」这个文件夹里动手完成。需要越界操作时会先问你。").TextColor(t.TextMuted)
				ui.Column(c).Height(4)
				s.composer(c, pal, &v.draft, "例如：把 data/ 下的报表汇总成季度报告", false, false, func(text string) {
					s.send(v, nil, text)
				}, nil)
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icFolder).Size(13, 13).TextColor(t.TextMuted)
					ui.Text(c, v.ws.Dir).FontSize(12).TextColor(t.TextMuted).SingleLine()
				})
				if v.err != "" {
					ui.Text(c, v.err).TextColor(t.Danger).Selectable()
				}
				for _, a := range as {
					s.approvalCard(c, pal, a)
				}
				for _, q := range qs {
					questionCard(c, pal, q)
				}
			})
		})
	})
}

// taskView is one task's conversation, with its header and what it waits
// on pinned above the input.
func (s *Shell) taskView(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	t := c.Theme()
	sess := tk.session
	running := sess.Running() || tk.starting
	ui.Row(c).Padding(12, 20).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, taskTitle(tk)).FontSize(15).Bold().SingleLine()
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, v.ws.Name).FontSize(12).TextColor(t.TextMuted)
				if tk.meta.Source != "" {
					ui.Text(c, "· 来自云端 bot（"+displayName(tk.meta.Source)+"）").FontSize(12).TextColor(t.TextMuted)
				}
			})
		})
		switch {
		case v.waiting(tk):
			pill(c, "等你确认", pal.warnSoft, t.Warning)
		case running:
			pill(c, "运行中", pal.accentSoft, t.Accent)
		case len(sess.Entries) > 0:
			pill(c, "已完成", pal.okSoft, t.Success)
		}
		more := ui.Button(c, "").Label("更多").Children(func() {
			ui.Icon(c, icMore).Size(16, 16)
		})
		more.PopoverMenu(func(m *ui.Menu) {
			if m.Item("从列表移除").Disabled(sess.Running()).Chosen() {
				s.forget(v, tk)
			}
		})
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(20, 28, 28).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(760).MaxWidthPercent(100).Gap(14).Children(func() {
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
						ui.Spinner(c).Size(14, 14)
						ui.Text(c, "正在启动运行时…").TextColor(t.TextMuted)
					})
				}
				if tk.err != "" {
					ui.Text(c, tk.err).TextColor(t.Danger).Selectable()
				}
			})
		})
	})
	as, qs := v.pending(tk)
	ui.Column(c).Padding(0, 28, 18).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(760).MaxWidthPercent(100).Gap(10).Children(func() {
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
			s.composer(c, pal, &tk.input, "继续补充要求…", tk.starting, running, func(text string) {
				s.send(v, tk, text)
			}, onStop)
		})
	})
}

// composer is the message box: a text area with the send (or stop) button
// in its corner.
func (s *Shell) composer(c *ui.Context, pal *palette, input *string, placeholder string, busy, running bool, send func(string), stop func()) {
	t := c.Theme()
	ui.Column(c).Padding(10, 12, 8).Gap(6).Radius(12).Border(1, t.Border).Background(t.Background).Shadow(0, 2, 10, 0, ui.RGBA(20, 22, 40, 0.06)).Children(func() {
		in := ui.TextArea(c, input).Lines(2, 10).Placeholder(placeholder).Background(ui.Transparent).Border(0, ui.Transparent).Padding(2, 4)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			hint := "Enter 发送，Shift+Enter 换行"
			if running {
				hint = "运行中也可以补充，会在当前步骤后交给它"
			}
			ui.Text(c, hint).FontSize(11.5).TextColor(t.TextMuted).Grow(1).SingleLine()
			if stop != nil {
				b := ui.Button(c, "停止").Label("停止")
				if b.Clicked() {
					stop()
				}
			}
			text := strings.TrimSpace(*input)
			b := ui.PrimaryButton(c, "发送").Disabled(text == "" || busy)
			if (b.Clicked() || in.Submitted()) && text != "" && !busy {
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
				ui.Text(c, e.Text).Selectable().Padding(9, 14).Radius(14).Background(pal.userMsg).MaxWidth(560)
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
			ui.Text(c, e.Text).Key(e.Key).FontSize(12).TextColor(t.TextMuted).Selectable().MaxLines(3).Padding(0, 0, 0, 10).BorderWidth(0, 0, 0, 2).BorderColor(t.Border)
		case events.Tool:
			s.toolStep(c, pal, tk, e)
		case events.Notice:
			ui.Row(c).Key(e.Key).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icAlert).Size(14, 14).TextColor(t.Warning)
				ui.Text(c, e.Text).TextColor(t.Warning).Selectable()
			})
		}
	}
}

func (s *Shell) toolStep(c *ui.Context, pal *palette, tk *taskView, e *events.Entry) {
	t := c.Theme()
	open := tk.open[e.Key]
	ui.Column(c).Key(e.Key).Gap(6).Children(func() {
		r := ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(5, 8).Radius(7).Cursor(ui.CursorPointer).Focusable().Label(e.ToolName)
		if r.Hovered() || open {
			r.Background(pal.hover)
		}
		r.Children(func() {
			switch {
			case !e.Done:
				ui.Spinner(c).Size(14, 14).TextColor(pal.running)
			case e.IsError:
				ui.Icon(c, icX).Size(14, 14).TextColor(t.Danger)
			default:
				ui.Icon(c, icCheck).Size(14, 14).TextColor(t.Success)
			}
			ui.Text(c, toolVerb(e.ToolName)).Bold().FontSize(13).Shrink(0)
			ui.Text(c, toolSummary(e.Args)).FontSize(12.5).TextColor(t.TextMuted).Font("monospace").SingleLine().Grow(1).MinWidth(0)
			ui.Icon(c, icChevron).Size(13, 13).TextColor(t.TextMuted)
		})
		if r.Clicked() {
			tk.open[e.Key] = !open
		}
		if open {
			ui.Column(c).Gap(6).Padding(10, 12).Radius(8).Background(pal.code).Border(1, t.Border).Margin(0, 0, 0, 22).Children(func() {
				if e.Args != "" {
					ui.Text(c, "参数").FontSize(11).Bold().TextColor(t.TextMuted)
					ui.Text(c, clip(prettyArgs(e.Args), 2000)).FontSize(12).Font("monospace").Selectable()
				}
				if e.Result != "" {
					color := t.Text
					if e.IsError {
						color = t.Danger
					}
					ui.Text(c, "输出").FontSize(11).Bold().TextColor(t.TextMuted)
					ui.Text(c, clip(e.Result, 4000)).FontSize(12).Font("monospace").TextColor(color).Selectable()
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
	}
	return name
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
