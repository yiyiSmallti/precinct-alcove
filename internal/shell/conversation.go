package shell

import (
	"encoding/json"
	"fmt"
	"regexp"
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
		main := ui.Column(c).Grow(1).Basis(0).MinWidth(0)
		// The new-task page and a task's lie on the same glass, so that
		// sending the first message changes what is on it, not the glass.
		if tk == nil {
			main.Children(func() { s.newTaskView(c, pal, v) })
		} else {
			main.Children(func() { s.taskView(c, pal, v, tk) })
		}
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
	// Tinted over the content color, not see-through: it may sit on a
	// painting.
	ui.Row(c).Gap(12).Padding(12, 14).Radius(12).AlignItems(ui.Center).Background(t.Accent.Alpha(0.08).Over(pal.content)).Border(1, t.Accent.Alpha(0.2)).Children(func() {
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
			tag(c, pal, s.sourceLabel(v.ws, tk.meta.Source))
		}
		ui.Spacer(c)
		switch {
		case v.waiting(tk):
			status(c, pal.waiting, "等你确认")
		case tk.err != "":
			status(c, c.Theme().Danger, "出错")
		case running:
			status(c, pal.running, "运行中")
		case sess.AgentsRunning() > 0:
			status(c, pal.running, "子代理运行中")
		case len(sess.Entries) > 0:
			status(c, pal.done, "已完成")
		}
		ui.Box(c).Width(8)
		s.browserToggle(c, pal, v)
		s.panelToggle(c, pal, v)
		more := iconButton(c, pal, icMore, "更多", false)
		more.PopoverMenuButton(func(m *ui.Menu) {
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
	s.runBar(c, pal, v, tk)
	messageBox(c, pal, input, placeholder, lines, busy, send, stop)
}

// messageBox is the rounded input with its send (and stop) button, shared
// by tasks and plugin conversations.
func messageBox(c *ui.Context, pal *palette, input *string, placeholder string, lines int, busy bool, send func(string), stop func()) {
	t := c.Theme()
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
			s.reasoningStep(c, pal, tk, e)
		case events.Tool:
			s.toolStep(c, pal, tk, e)
		case events.Compaction:
			s.compactionStep(c, pal, tk, e)
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
				case e.Busy():
					ui.Spinner(c).Size(12, 12).TextColor(pal.running)
				case e.IsError || e.AgentStatus == "error" || e.AgentStatus == "stopped":
					ui.Icon(c, icX).Size(13, 13).TextColor(t.Danger)
				default:
					ui.Icon(c, icCheck).Size(13, 13).TextColor(pal.subtle)
				}
			})
			ui.Text(c, toolVerb(e.ToolName)).FontSize(13).FontWeight(500).Shrink(0)
			if e.Agent != "" {
				ui.Text(c, entrySummary(*e)).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
				ui.Text(c, agentProgress(*e)).FontSize(12).TextColor(pal.subtle).SingleLine().Grow(1).MinWidth(0)
			} else {
				ui.Text(c, entrySummary(*e)).FontSize(12).TextColor(t.TextMuted).Font(pal.mono).SingleLine().Grow(1).MinWidth(0)
			}
			arrow := ui.Icon(c, icChevron).Size(13, 13).TextColor(pal.subtle)
			if !open {
				arrow.Rotate(-90)
			}
		})
		if r.Clicked() {
			tk.open[e.Key] = !open
		}
		// While the call runs, what its command prints (or, while the model
		// still writes it, the long argument it is writing) scrolls under
		// its line.
		if !open && e.Busy() {
			if live := liveText(e); live != "" {
				liveBox(c, pal, live, true)
			}
		}
		if open {
			ui.Column(c).Gap(6).Padding(10, 12).Radius(8).Background(pal.code).Border(1, pal.hairline).Margin(0, 8, 6, 31).Children(func() {
				if e.Args != "" {
					ui.Text(c, "参数").FontSize(11).FontWeight(500).TextColor(pal.subtle)
					if e.Streaming {
						ui.Text(c, "…"+tailText(e.Args, 2000)).FontSize(12).Font(pal.mono).Selectable()
					} else {
						ui.Text(c, clip(prettyArgs(e.Args), 2000)).FontSize(12).Font(pal.mono).Selectable()
					}
				}
				switch {
				case e.Result == "" && e.Output != "":
					ui.Text(c, "输出（运行中）").FontSize(11).FontWeight(500).TextColor(pal.subtle)
					ui.Text(c, tailLines(e.Output, 40)).FontSize(12).Font(pal.mono).Selectable()
				}
				switch {
				case e.Agent != "":
					if e.AgentResult != "" {
						ui.Text(c, "子代理的回答").FontSize(11).FontWeight(500).TextColor(pal.subtle)
						ui.Text(c, clip(e.AgentResult, 4000)).FontSize(12).Selectable()
					}
				case e.Result != "":
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

// reasoningStep is the model's reasoning. While it streams, its latest lines
// follow the text under a "思考中" line; once done, its first lines show,
// and a long one opens in full from its "思考" line.
func (s *Shell) reasoningStep(c *ui.Context, pal *palette, tk *taskView, e *events.Entry) {
	t := c.Theme()
	quote := func(text string) ui.Element {
		return ui.Text(c, text).FontSize(12.5).TextColor(t.TextMuted).Selectable().Padding(0, 0, 0, 12).BorderWidth(0, 0, 0, 2).BorderColor(pal.hairline)
	}
	ui.Column(c).Key(e.Key).Gap(4).Children(func() {
		if e.Streaming {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(11, 11).TextColor(pal.subtle)
				ui.Text(c, "思考中").FontSize(12).TextColor(pal.subtle)
			})
			quote(tailLines(e.Text, 4))
			return
		}
		long := strings.Count(strings.TrimSpace(e.Text), "\n") >= 3 || len([]rune(e.Text)) > 240
		open := tk.open[e.Key]
		if long {
			r := ui.Row(c).Gap(4).AlignItems(ui.Center).Cursor(ui.CursorPointer).Focusable().Label("思考")
			r.Children(func() {
				ui.Text(c, "思考").FontSize(12).TextColor(pal.subtle)
				arrow := ui.Icon(c, icChevron).Size(12, 12).TextColor(pal.subtle)
				if !open {
					arrow.Rotate(-90)
				}
			})
			if r.Clicked() {
				tk.open[e.Key] = !open
			}
		}
		q := quote(e.Text)
		if !open {
			q.MaxLines(3)
		}
	})
}

// compactionStep is a compaction of the conversation's context, one line
// like a tool call's: the summary scrolls under it while it is written, and
// opens in full on a click.
func (s *Shell) compactionStep(c *ui.Context, pal *palette, tk *taskView, e *events.Entry) {
	t := c.Theme()
	open := tk.open[e.Key]
	ui.Column(c).Key(e.Key).Gap(4).Margin(-6, -8).Children(func() {
		r := ui.Row(c).Height(30).Gap(9).AlignItems(ui.Center).Padding(0, 8).Radius(6).Cursor(ui.CursorPointer).Focusable().Label("压缩上下文")
		if r.Hovered() {
			r.Background(pal.hover)
		}
		r.Children(func() {
			verb := "已压缩上下文"
			ui.Row(c).Width(14).Justify(ui.Center).Shrink(0).Children(func() {
				switch {
				case e.Streaming:
					verb = "正在压缩上下文"
					ui.Spinner(c).Size(12, 12).TextColor(pal.running)
				case e.IsError:
					verb = "压缩上下文失败"
					ui.Icon(c, icX).Size(13, 13).TextColor(t.Danger)
				default:
					ui.Icon(c, icCheck).Size(13, 13).TextColor(pal.subtle)
				}
			})
			ui.Text(c, verb).FontSize(13).FontWeight(500).Shrink(0)
			// While it is written, the summary scrolls under the line instead.
			line := oneLine(e.Text)
			if e.Streaming && !open {
				line = ""
			}
			ui.Text(c, line).FontSize(12).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
			arrow := ui.Icon(c, icChevron).Size(13, 13).TextColor(pal.subtle)
			if !open {
				arrow.Rotate(-90)
			}
		})
		if r.Clicked() {
			tk.open[e.Key] = !open
		}
		if !open && e.Streaming && e.Text != "" {
			liveBox(c, pal, tailLines(e.Text, 6), false)
		}
		if open && (e.Text != "" || e.Result != "") {
			ui.Column(c).Gap(6).Padding(10, 12).Radius(8).Background(pal.code).Border(1, pal.hairline).Margin(0, 8, 6, 31).Children(func() {
				if e.Text != "" {
					ui.Text(c, "摘要").FontSize(11).FontWeight(500).TextColor(pal.subtle)
					ui.Text(c, clip(e.Text, 8000)).FontSize(12.5).Selectable()
				}
				if e.Result != "" {
					ui.Text(c, e.Result).FontSize(12).TextColor(t.Danger).Selectable()
				}
			})
		}
	})
}

// liveBox shows text that is still growing, under a step's line.
func liveBox(c *ui.Context, pal *palette, text string, mono bool) {
	t := c.Theme()
	ui.Column(c).Padding(8, 10).Radius(8).Background(pal.code).Border(1, pal.hairline).Margin(0, 8, 6, 31).Children(func() {
		txt := ui.Text(c, text).FontSize(12).TextColor(t.TextMuted)
		if mono {
			txt.Font(pal.mono)
		}
	})
}

// liveText is what a running call shows under its line: the tail of what
// its command printed, or, while the model still writes the call, the tail
// of the long argument it is writing (a file's content, a patch); "" when
// there is neither.
func liveText(e *events.Entry) string {
	if e.Output != "" {
		return tailLines(e.Output, 8)
	}
	if !e.Streaming {
		return ""
	}
	long := ""
	for _, v := range partialStrings(e.Args) {
		if len(v) > len(long) {
			long = v
		}
	}
	if !strings.Contains(long, "\n") && len(long) < 160 {
		return "" // short ones are on the line already
	}
	return tailLines(long, 8)
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// tailLines is the last n lines of text as a terminal would leave them:
// colors dropped, and a line rewritten with carriage returns (a progress
// bar) shown as its last state.
func tailLines(text string, n int) string {
	text = ansi.ReplaceAllString(tailText(text, 8<<10), "")
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		if j := strings.LastIndexByte(strings.TrimRight(l, "\r"), '\r'); j >= 0 {
			lines[i] = l[j+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// tailText is the last max bytes of text, cut on a rune boundary.
func tailText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	i := len(text) - max
	for i < len(text) && text[i]&0xC0 == 0x80 {
		i++
	}
	return text[i:]
}

// partialStrings reads the string fields of a JSON object that may be cut
// off, as a call's arguments are while the model writes them: the last value
// may be unterminated. Other values are skipped.
func partialStrings(raw string) map[string]string {
	out := map[string]string{}
	i := strings.IndexByte(raw, '{')
	if i < 0 {
		return out
	}
	space := func(i int) int {
		for i < len(raw) && strings.IndexByte(" \t\r\n", raw[i]) >= 0 {
			i++
		}
		return i
	}
	for i++; ; i++ {
		i = space(i)
		if i >= len(raw) || raw[i] != '"' {
			return out
		}
		key, j, closed := jsonStringAt(raw, i)
		if !closed {
			return out
		}
		if i = space(j); i >= len(raw) || raw[i] != ':' {
			return out
		}
		if i = space(i + 1); i >= len(raw) {
			return out
		}
		if raw[i] == '"' {
			v, j, closed := jsonStringAt(raw, i)
			out[key] = v
			if !closed {
				return out
			}
			i = j
		} else {
			// A number, literal, array or object: up to the comma that ends it.
			depth, str := 0, false
			for ; i < len(raw); i++ {
				ch := raw[i]
				switch {
				case str && ch == '\\':
					i++
				case ch == '"':
					str = !str
				case str:
				case ch == '{' || ch == '[':
					depth++
				case ch == '}' || ch == ']':
					depth--
				}
				if !str && depth <= 0 && (ch == ',' || depth < 0) {
					break
				}
			}
		}
		if i = space(i); i >= len(raw) || raw[i] != ',' {
			return out
		}
	}
}

// jsonStringAt decodes the JSON string starting at raw[i] (a quote) and
// returns the offset after it; closed is false when raw ends inside it, and
// the string is then what it holds so far.
func jsonStringAt(raw string, i int) (string, int, bool) {
	for j := i + 1; j < len(raw); j++ {
		switch raw[j] {
		case '\\':
			j++
		case '"':
			var v string
			if json.Unmarshal([]byte(raw[i:j+1]), &v) != nil {
				v = raw[i+1 : j]
			}
			return v, j + 1, true
		}
	}
	body := raw[i:]
	// An escape cut short (\, \u00) cannot be decoded: leave it for the next
	// delta.
	if k := strings.LastIndexByte(body, '\\'); k > 0 && len(body)-k < 6 {
		body = body[:k]
	}
	var v string
	if json.Unmarshal([]byte(body+`"`), &v) != nil {
		v = body[1:]
	}
	return v, len(raw), false
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
	case "subagent":
		return "子代理"
	}
	return name
}

// agentProgress is what a subagent call's line says after its task: its
// steps so far and what it is doing, or how it ended.
func agentProgress(e events.Entry) string {
	steps := ""
	if e.AgentSteps > 0 {
		steps = fmt.Sprintf("%d 步", e.AgentSteps)
	}
	join := func(parts ...string) string {
		var out []string
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, " · ")
	}
	switch e.AgentStatus {
	case "running":
		doing := "开始"
		if e.AgentTool != "" {
			doing = strings.TrimSpace("正在" + toolVerb(e.AgentTool) + " " + toolSummary(e.AgentArgs))
		}
		return join(steps, doing)
	case "ok":
		return join(steps, "已完成")
	case "stopped":
		return join(steps, "已停止")
	}
	return join(steps, "出错")
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
	keys := []string{"command", "cmd", "file_path", "path", "pattern", "url", "query", "description"}
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) == nil {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return oneLine(v)
			}
		}
		return oneLine(args)
	}
	// Arguments the model is still writing.
	if p := partialStrings(args); len(p) > 0 {
		for _, k := range keys {
			if v := p[k]; strings.TrimSpace(v) != "" {
				return oneLine(v)
			}
		}
		return ""
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
