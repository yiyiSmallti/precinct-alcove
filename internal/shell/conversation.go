package shell

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// workspaceView is a workspace's page: its home or the task chosen in the
// sidebar, on the sheet, and beside it the browser on a sheet of its own,
// or Index's alcove.
func (s *Shell) workspaceView(c *ui.Context, pal *palette, v *wsView) {
	tk := v.tasks[v.current]
	if tk == nil {
		v.current = ""
	}
	// The home and a task's page lie on the same sheet, so that sending the
	// first message changes what is on it, not the sheet.
	s.onSheet(c, pal, func() {
		if tk == nil {
			s.homeView(c, pal, v)
		} else {
			s.taskView(c, pal, v, tk)
		}
	})
	if v.browsing {
		s.browserPane(c, pal, v)
		return
	}
	m := s.moodOf(v, tk)
	key := "home:" + v.ws.ID
	if tk != nil {
		key = tk.meta.ID
	}
	s.band(c, pal, m, key, moodWord(v, tk, m), nowLine(v, tk, m))
}

// topBar is a page's title bar on the sheet: 52 DIPs, draggable where the
// window has no title bar of its own.
func topBar(c *ui.Context, pal *palette, fn func()) {
	r := ui.Row(c).Height(52).Shrink(0).Padding(0, 12, 0, 20).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline)
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

// taskView is one task's conversation under its title bar, following its
// end while it is there, with what it waits on and the composer pinned at
// its foot.
func (s *Shell) taskView(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	t := c.Theme()
	sess := tk.session
	running := sess.Running() || tk.starting
	as, qs := v.pending(tk)
	short := endedShort(sess)
	topBar(c, pal, func() {
		ui.Text(c, taskTitle(tk)).FontSize(14).Bold().SingleLine().Shrink(1).MinWidth(0)
		if tk.meta.Source != "" {
			tag(c, pal, s.sourceLabel(v.ws, tk.meta.Source))
		}
		switch {
		case len(as)+len(qs) > 0 || v.waiting(tk):
			statusPill(c, pal.waiting, "等你"+v.waitWord(tk), false)
		case tk.err != "":
			statusPill(c, t.Danger, "出错", false)
		case running:
			statusPill(c, pal.running, "运行中", true)
		case sess.AgentsRunning() > 0:
			statusPill(c, pal.running, "子代理运行中", true)
		case short != nil && short.IsError:
			statusPill(c, t.Danger, "出错", false)
		case short != nil:
			statusPill(c, pal.subtle, "已停止", false)
		case len(sess.Entries) > 0:
			statusPill(c, pal.done, "已完成", false)
		}
		ui.Spacer(c)
		s.filesChip(c, pal, v, tk)
		s.browserToggle(c, pal, v)
		if !v.browsing { // the browser stands where the alcove would
			s.bandToggle(c, pal)
		}
		more := iconButton(c, pal, icMore, "更多", false)
		more.PopoverMenuButton(func(m *ui.Menu) {
			if m.Item("从列表移除").Disabled(sess.Running()).Chosen() {
				s.forget(v, tk)
			}
		})
	})
	// At the end (and at first), the conversation stays at its end as it
	// grows; scrolled up, it stays put.
	if tk.scroll.Y >= tk.scroll.MaxY-1 {
		tk.scroll.Y = math.MaxFloat32
	}
	scrollFading(c, pal, &tk.scroll, func() {
		ui.Column(c).Padding(28, 32, 36).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(720).MaxWidthPercent(100).Gap(18).Children(func() {
				if len(sess.Entries) == 0 {
					msg := "在下面输入，Alcove 会在这个文件夹里执行。"
					if tk.legacy {
						msg = "这是更新前的对话，之前的记录还在，继续输入就会接着它往下做。"
					}
					ui.Text(c, msg).TextColor(t.TextMuted)
				}
				s.entries(c, pal, v, tk, as)
				if tk.starting && !sess.Running() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c).Size(13, 13).TextColor(pal.running)
						ui.Text(c, "正在启动…").FontSize(13).TextColor(t.TextMuted)
					})
				}
				// Only a turn that completed is summed up as done; one that
				// failed or was stopped ends on its notice.
				if !running && sess.AgentsRunning() == 0 && tk.err == "" && short == nil && len(as)+len(qs) == 0 {
					s.turnSummary(c, pal, v, tk)
				}
				if tk.err != "" {
					errorNote(c, tk.err)
				}
			})
		})
	})
	ui.Column(c).Padding(0, 32, 18).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(720).MaxWidthPercent(100).Gap(10).Children(func() {
			for _, a := range as {
				s.approvalCard(c, pal, v, a)
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

// filesChip is the title bar's count of the files the task changed; a
// click lists them, each opening on a click.
func (s *Shell) filesChip(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	paths, created := changedFiles(tk.session)
	if len(paths) == 0 {
		return
	}
	t := c.Theme()
	chip := ui.Row(c).Key("files").Height(28).Padding(0, 11, 0, 9).Gap(6).AlignItems(ui.Center).Radius(14).Border(1, pal.hairline).Shrink(0).Cursor(ui.CursorPointer).Focusable().Label("改动的文件").Tooltip("这个任务写入或修改的文件").Transition(fade)
	if chip.Hovered() || s.filesMenu {
		chip.Background(pal.hover)
	}
	chip.Children(func() {
		ui.Icon(c, icPencil).Size(13, 13).TextColor(t.TextMuted)
		ui.Text(c, fmt.Sprintf("%d 个文件", len(paths))).FontSize(12.5).FontFeatures("tnum").SingleLine()
	})
	if chip.Clicked() {
		s.filesMenu = !s.filesMenu
	}
	ui.PopoverBase(c, chip, &s.filesMenu, func(panel ui.Element) {
		panel.AttachTo(chip, ui.AnchorBottomRight, ui.AnchorTopRight).Margin(6, 0, 0, 0).Width(340).Padding(6).Radius(12).
			Background(t.Background).Border(1, t.Border).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.18))
		ui.Text(c, "这个任务改动的文件").FontSize(11.5).FontWeight(600).TextColor(pal.subtle).Padding(6, 8, 4)
		ui.Column(c).MaxHeight(320).Children(func() {
			ui.Scroll(c).Children(func() { s.fileRows(c, pal, v, paths, created) })
		})
		ui.Text(c, "命令行里改的文件不在这里统计，可以到工作区首页的「文件」里看。").FontSize(11).TextColor(pal.subtle).Padding(6, 8, 4)
	})
}

// fileRows lists changed files, new ones marked 新 and edited ones 改; a
// click opens one.
func (s *Shell) fileRows(c *ui.Context, pal *palette, v *wsView, paths []string, created map[string]bool) {
	t := c.Theme()
	for _, p := range paths {
		abs := absIn(v.ws.Dir, p)
		r := ui.Row(c).Key(p).Height(30).Gap(8).Padding(0, 8).AlignItems(ui.Center).Radius(7).Cursor(ui.CursorPointer).Focusable().Label(p).Tooltip("打开 " + abs).Transition(fade)
		hot := r.Hovered()
		if hot {
			r.Background(pal.hover)
		}
		r.Children(func() {
			mark, color := "改", t.Accent
			if created[p] {
				mark, color = "新", pal.done
			}
			ui.Text(c, mark).FontSize(10.5).Bold().TextColor(color).Background(color.Alpha(0.12)).Padding(1, 5).Radius(4).Shrink(0)
			ui.Text(c, relTo(v.ws.Dir, p)).FontSize(12.5).Font(pal.mono).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
			if hot {
				ui.Icon(c, icOpen).Size(13, 13).TextColor(t.TextMuted)
			}
		})
		if r.Clicked() {
			s.openPath(abs)
		}
	}
}

// turnSummary closes a finished turn that used tools: how many steps it
// took and the files it changed, each a click from opening.
func (s *Shell) turnSummary(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	es := tk.session.Entries
	start := 0
	for i := len(es) - 1; i >= 0; i-- {
		if es[i].Kind == events.User {
			start = i + 1
			break
		}
	}
	turn := es[start:]
	calls, failed := 0, 0
	for _, e := range turn {
		if e.Kind == events.Tool {
			calls++
			if e.IsError {
				failed++
			}
		}
	}
	if calls == 0 {
		return
	}
	t := c.Theme()
	paths, created := filesIn(turn)
	paperCard(c, pal).Key("summary").Children(func() {
		ui.Row(c).Padding(12, 14).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Size(28, 28).Radius(14).Background(pal.done.Alpha(0.13)).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
				ui.Icon(c, icCheck).Size(15, 15).TextColor(pal.done)
			})
			ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, "这一轮做完了").FontSize(13.5).FontWeight(600)
				meta := fmt.Sprintf("%d 步", calls)
				if failed > 0 {
					meta += fmt.Sprintf("，其中 %d 步出错", failed)
				}
				if len(paths) > 0 {
					meta += fmt.Sprintf("，改了 %d 个文件", len(paths))
				}
				ui.Text(c, meta).FontSize(12.5).TextColor(t.TextMuted)
			})
		})
		if len(paths) > 0 {
			ui.Column(c).Padding(0, 6, 6).BorderWidth(1, 0, 0, 0).BorderColor(pal.hairline).Children(func() {
				ui.Box(c).Height(4)
				s.fileRows(c, pal, v, paths, created)
			})
		}
	})
}

// composer is the message box with the task's run controls (the model and
// its thinking effort, and how full the context is) in its foot, and a
// round send (and stop) button in its corner. tk is nil for a task not
// started yet.
func (s *Shell) composer(c *ui.Context, pal *palette, v *wsView, tk *taskView, input *string, placeholder string, lines int, busy bool, send func(string), stop func()) {
	messageBox(c, pal, input, placeholder, lines, busy, send, stop, func() { s.runControls(c, pal, v, tk) })
}

// messageBox is the rounded input with its send (and stop) button, shared
// by tasks and plugin conversations; controls, if any, lead its foot.
func messageBox(c *ui.Context, pal *palette, input *string, placeholder string, lines int, busy bool, send func(string), stop func(), controls func()) {
	t := c.Theme()
	box := ui.Column(c).Padding(12, 10, 10, 14).Gap(8).Radius(16).Background(pal.content).Transition(fade)
	// Typing lights the box in the accent.
	focused := box.FocusWithin()
	if focused {
		box.Border(1, t.Accent.Alpha(0.5)).Shadow(0, 10, 30, -8, glow(c, pal, 0.25))
	} else {
		box.Border(1, t.Border).Shadow(0, 8, 24, -10, pal.shadow.Alpha(2.2))
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
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if controls != nil {
				controls()
				ui.Spacer(c)
				if focused {
					ui.Text(c, "↵ 发送 · ⇧↵ 换行").FontSize(11).TextColor(pal.subtle).SingleLine().Shrink(1).MinWidth(0)
				}
			} else {
				ui.Text(c, "↵ 发送 · ⇧↵ 换行").FontSize(11).TextColor(pal.subtle).Grow(1).SingleLine()
			}
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

// entries draws the conversation: the user's messages on the right, the
// replies as text, and between them the work as a thread of steps, each a
// line opened on a click. Steps waiting on an approval (as) are marked.
func (s *Shell) entries(c *ui.Context, pal *palette, v *wsView, tk *taskView, as []*approval) {
	asked := map[*events.Entry]bool{}
	for _, a := range as {
		if e := tk.session.Call(a.req.CallID); e != nil {
			asked[e] = true
		}
	}
	es := tk.session.Entries
	for i := 0; i < len(es); {
		e := es[i]
		switch e.Kind {
		case events.User:
			ui.Row(c).Key(e.Key).Children(func() {
				ui.Spacer(c)
				ui.Text(c, e.Text).FontSize(14).LineHeight(1.5).Selectable().Padding(10, 15).Radius(18, 18, 6, 18).Background(pal.userMsg).MaxWidth(540)
			})
			i++
		case events.Assistant:
			ui.Column(c).Key(e.Key).Children(func() {
				if e.Streaming {
					markdown.Stream(c, e.Text, pal.md)
				} else {
					markdown.View(c, e.Text, pal.md)
				}
			})
			i++
		default:
			j := i
			for j < len(es) && es[j].Kind != events.User && es[j].Kind != events.Assistant {
				j++
			}
			steps := es[i:j]
			ui.Column(c).Key("trace:" + e.Key).Children(func() {
				for k, st := range steps {
					first, last := k == 0, k == len(steps)-1
					switch st.Kind {
					case events.Tool:
						s.toolStep(c, pal, v, tk, st, first, last, asked[st])
					case events.Reasoning:
						s.reasoningStep(c, pal, tk, st, first, last)
					case events.Compaction:
						s.compactionStep(c, pal, tk, st, first, last)
					default:
						noticeStep(c, pal, st, first, last)
					}
				}
			})
			i = j
		}
	}
}

// The thread of a task's work: each step's node is a circle of nodeD on a
// line through the nodes, its row stepH high.
const (
	nodeD = 22
	stepH = 34
)

// threadColor is the thread's line and the ring of a step at rest.
func threadColor(c *ui.Context, pal *palette) ui.Color {
	return pal.hairline.Mix(c.Theme().TextMuted, 0.3)
}

// step draws one step on the thread: its node, its line (what fills its
// row after the node), and what opens under it (body, if any), the thread
// running on past it to the next step. A step that opens (openable) shows
// its chevron; it reports a click on its row.
func step(c *ui.Context, pal *palette, key, label string, first, last, open, openable bool, node, line, body func()) bool {
	col := ui.Column(c).Key(key)
	thread := threadColor(c, pal)
	col.Draw(func(p *ui.Painter, r ui.Rect) {
		top, bottom := r.Y, r.Y+r.H
		if first {
			top = r.Y + stepH/2
		}
		if last {
			bottom = r.Y + stepH/2
		}
		if bottom > top {
			p.Fill(ui.Rect{X: r.X + nodeD/2 - 0.75, Y: top, W: 1.5, H: bottom - top}, thread, 0.75)
		}
	})
	clicked := false
	col.Children(func() {
		row := ui.Row(c).Height(stepH).Gap(8).AlignItems(ui.Center).Label(label)
		if openable {
			row.Cursor(ui.CursorPointer).Focusable()
		}
		hot := openable && row.Hovered()
		row.Children(func() {
			ui.Row(c).Size(nodeD, nodeD).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).Children(node)
			face := ui.Row(c).Grow(1).MinWidth(0).Height(stepH-4).Padding(0, 8).Gap(8).AlignItems(ui.Center).Radius(8).Transition(fade)
			if hot {
				face.Background(pal.hover)
			}
			face.Children(func() {
				line()
				if openable {
					arrow := ui.Icon(c, icChevron).Size(13, 13).TextColor(pal.subtle).Shrink(0)
					target := float32(-90)
					if open {
						target = 0
					}
					arrow.Rotate(arrow.AnimateTo("turn", target, ui.Snappy))
				}
			})
		})
		clicked = openable && row.Clicked()
		if body != nil {
			ui.Column(c).Padding(2, 0, 10, nodeD+10).Children(body)
		}
	})
	return clicked
}

// node is a step's circle: paper, so that the thread passes under it,
// ringed and tinted in a state's color, with its mark.
func node(c *ui.Context, pal *palette, ring, tint ui.Color, mark func()) {
	ui.Column(c).Size(nodeD, nodeD).Radius(nodeD/2).Background(tint.Over(pal.content)).Border(1, ring).AlignItems(ui.Center).Justify(ui.Center).Children(mark)
}

func (s *Shell) toolStep(c *ui.Context, pal *palette, v *wsView, tk *taskView, e *events.Entry, first, last, asked bool) {
	t := c.Theme()
	open := tk.open[e.Key]
	failed := e.IsError || e.AgentStatus == "error" || e.AgentStatus == "stopped"
	var body func()
	switch {
	case open:
		body = func() { s.stepDetail(c, pal, v, e) }
	case e.Busy():
		// While the call runs, what its command prints (or, while the model
		// still writes it, the long argument it is writing) scrolls under
		// its line.
		if live := liveText(e); live != "" {
			body = func() { liveBox(c, pal, live, e.Output != "" && toolVerb(e.ToolName) == "运行命令", true) }
		}
	}
	if step(c, pal, e.Key, e.ToolName, first, last, open, true, func() {
		switch {
		case asked:
			node(c, pal, pal.waiting, pal.waiting.Alpha(0.14), func() { ui.Icon(c, stepIcon(e.ToolName)).Size(12, 12).TextColor(pal.waiting) })
		case e.Busy():
			node(c, pal, pal.running.Alpha(0.5), pal.running.Alpha(0.08), func() { ui.Spinner(c).Size(11, 11).TextColor(pal.running) })
		case failed:
			node(c, pal, t.Danger.Alpha(0.5), t.Danger.Alpha(0.08), func() { ui.Icon(c, icX).Size(11, 11).TextColor(t.Danger) })
		default:
			node(c, pal, threadColor(c, pal), ui.Transparent, func() { ui.Icon(c, stepIcon(e.ToolName)).Size(12, 12).TextColor(t.TextMuted) })
		}
	}, func() {
		ui.Text(c, toolVerb(e.ToolName)).FontSize(13).FontWeight(550).Shrink(0)
		if e.Agent != "" {
			ui.Text(c, entrySummary(*e)).FontSize(12.5).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
			ui.Text(c, agentProgress(*e)).FontSize(12).TextColor(pal.subtle).SingleLine().Grow(1).MinWidth(0)
		} else {
			ui.Text(c, entrySummary(*e)).FontSize(12).TextColor(t.TextMuted).Font(pal.mono).SingleLine().Grow(1).MinWidth(0)
		}
		if asked {
			ui.Text(c, "等你确认").FontSize(11.5).FontWeight(600).TextColor(pal.waiting).Shrink(0)
		}
	}, body) {
		tk.open[e.Key] = !open
	}
}

// stepDetail is what a tool call did, opened under its line, shown as its
// kind reads best: a command and its output as in a terminal, an edit as
// the lines it took out and put in, a file written as its start, a
// subagent's task and answer, and anything else as its arguments and
// output.
func (s *Shell) stepDetail(c *ui.Context, pal *palette, v *wsView, e *events.Entry) {
	t := c.Theme()
	verb := toolVerb(e.ToolName)
	var args map[string]any
	complete := !e.Streaming && json.Unmarshal([]byte(e.Args), &args) == nil
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, _ := args[k].(string); v != "" {
				return v
			}
		}
		return ""
	}
	label := func(text string) { ui.Text(c, text).FontSize(11.5).FontWeight(600).TextColor(pal.subtle) }
	failure := func() {
		if e.IsError && e.Result != "" {
			ui.Text(c, clip(e.Result, 4000)).FontSize(12).Font(pal.mono).TextColor(t.Danger).Selectable()
		}
	}
	ui.Column(c).Gap(8).Children(func() {
		switch {
		case e.Agent != "":
			if p := str("prompt"); p != "" {
				label("交给子代理的任务")
				codeBox(c, pal, clip(p, 4000), false)
			}
			if e.AgentResult != "" {
				label("子代理的回答")
				ui.Column(c).Padding(10, 12).Radius(8).Background(pal.code).Border(1, pal.hairline).Children(func() {
					markdown.View(c, clip(e.AgentResult, 4000), pal.md)
				})
			}
		case verb == "运行命令" && complete && str("command", "cmd") != "":
			out := e.Result
			if out == "" {
				out = e.Output
			}
			terminal(c, pal, str("command", "cmd"), out, e.IsError)
		case verb == "修改文件" && complete:
			s.pathLine(c, pal, v, argPath(e.Args))
			switch edits, _ := args["edits"].([]any); {
			case str("old_string", "new_string") != "":
				diffView(c, pal, str("old_string"), str("new_string"))
			case len(edits) > 0:
				for i, ed := range edits {
					m, _ := ed.(map[string]any)
					old, _ := m["old_string"].(string)
					new, _ := m["new_string"].(string)
					ui.Column(c).Key(i).Children(func() { diffView(c, pal, old, new) })
				}
			case str("patch", "input", "diff") != "":
				patchView(c, pal, str("patch", "input", "diff"))
			default:
				codeBox(c, pal, clip(prettyArgs(e.Args), 2000), true)
			}
			failure()
		case verb == "读取文件" && complete:
			s.pathLine(c, pal, v, argPath(e.Args))
			if e.Result != "" {
				box := codeBox(c, pal, headLines(e.Result, 40), true)
				if e.IsError {
					box.TextColor(t.Danger)
				}
			}
		case verb == "写入文件" && complete:
			s.pathLine(c, pal, v, argPath(e.Args))
			codeBox(c, pal, headLines(str("content"), 40), true)
			failure()
		default:
			// An argument alone is on the step's line already.
			if e.Args != "" && (!complete || len(args) > 1) {
				label("参数")
				if e.Streaming {
					codeBox(c, pal, "…"+tailText(e.Args, 2000), true)
				} else {
					codeBox(c, pal, clip(prettyArgs(e.Args), 2000), true)
				}
			}
			if e.Result == "" && e.Output != "" {
				label("输出（运行中）")
				codeBox(c, pal, tailLines(e.Output, 40), true)
			}
			if e.Result != "" {
				label("输出")
				box := codeBox(c, pal, clip(e.Result, 4000), true)
				if e.IsError {
					box.TextColor(t.Danger)
				}
			}
		}
	})
}

// pathLine names the file a call touched, with a way to open it.
func (s *Shell) pathLine(c *ui.Context, pal *palette, v *wsView, p string) {
	if p == "" {
		return
	}
	t := c.Theme()
	abs := absIn(v.ws.Dir, p)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icFile).Size(13, 13).TextColor(t.TextMuted)
		ui.Text(c, relTo(v.ws.Dir, p)).FontSize(12.5).Font(pal.mono).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0).Selectable()
		if s.OpenPath != nil && quietButton(c, pal, "打开").Height(24).Clicked() {
			s.openPath(abs)
		}
	})
}

// codeBox is text in a quiet box, in the code font when mono; it returns
// the text, to color.
func codeBox(c *ui.Context, pal *palette, text string, mono bool) ui.Element {
	var txt ui.Element
	ui.Column(c).Padding(10, 12).Radius(8).Background(pal.code).Border(1, pal.hairline).Children(func() {
		txt = ui.Text(c, text).FontSize(12).LineHeight(1.5).Selectable()
		if mono {
			txt.Font(pal.mono)
		}
	})
	return txt
}

// terminal shows a command and what it printed, as a terminal would.
func terminal(c *ui.Context, pal *palette, cmd, out string, failed bool) {
	t := c.Theme()
	ui.Column(c).Padding(10, 14, 12).Gap(6).Radius(10).Background(pal.term).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Text(c, "$").FontSize(12).Font(pal.mono).TextColor(pal.done.Mix(pal.termText, 0.3))
			ui.Text(c, cmd).FontSize(12).Font(pal.mono).TextColor(pal.termText).Selectable().Grow(1).MinWidth(0)
		})
		if out = tailLines(out, 80); strings.TrimSpace(out) != "" {
			col := pal.termText.Alpha(0.72)
			if failed {
				col = t.Danger.Mix(pal.termText, 0.35)
			}
			ui.Text(c, out).FontSize(12).Font(pal.mono).LineHeight(1.45).TextColor(col).Selectable()
		}
	})
}

// diffView shows an edit as the lines it took out and those it put in.
func diffView(c *ui.Context, pal *palette, old, new string) {
	t := c.Theme()
	ui.Column(c).Radius(8).Clip().Border(1, pal.hairline).Background(pal.code).Children(func() {
		for i, l := range limitLines(old, 30) {
			diffLine(c, pal, fmt.Sprint("-", i), "−", l, t.Danger)
		}
		for i, l := range limitLines(new, 30) {
			diffLine(c, pal, fmt.Sprint("+", i), "+", l, pal.done)
		}
	})
}

// patchView shows a patch, its added and removed lines in their colors.
func patchView(c *ui.Context, pal *palette, patch string) {
	t := c.Theme()
	ui.Column(c).Radius(8).Clip().Border(1, pal.hairline).Background(pal.code).Children(func() {
		for i, l := range limitLines(patch, 60) {
			key := fmt.Sprint(i)
			switch {
			case strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++"):
				diffLine(c, pal, key, "+", l[1:], pal.done)
			case strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---"):
				diffLine(c, pal, key, "−", l[1:], t.Danger)
			default:
				diffLine(c, pal, key, "", l, ui.Transparent)
			}
		}
	})
}

// diffLine is one line of a diff: its sign in the margin, tinted in its
// color.
func diffLine(c *ui.Context, pal *palette, key, sign, text string, color ui.Color) {
	t := c.Theme()
	r := ui.Row(c).Key(key).Padding(1, 10, 1, 0).Gap(0)
	if color != ui.Transparent {
		r.Background(color.Alpha(0.1))
	}
	r.Children(func() {
		ui.Text(c, sign).FontSize(12).Font(pal.mono).TextColor(color).Width(22).TextAlign(ui.Center).Shrink(0)
		txt := ui.Text(c, text).FontSize(12).Font(pal.mono).LineHeight(1.45).Selectable().Grow(1).MinWidth(0)
		if color == ui.Transparent {
			txt.TextColor(t.TextMuted)
		}
	})
}

// limitLines splits text into lines, the last saying how many more there
// are past n.
func limitLines(text string, n int) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		more := len(lines) - n
		lines = append(lines[:n], fmt.Sprintf("…（还有 %d 行）", more))
	}
	return lines
}

// headLines is the first n lines of text, saying how many more there are.
func headLines(text string, n int) string {
	return strings.Join(limitLines(text, n), "\n")
}

// reasoningStep is the model's reasoning: while it streams, its latest
// lines follow under a "思考中" line; once done, its first line is on its
// line, and the rest opens on a click.
func (s *Shell) reasoningStep(c *ui.Context, pal *palette, tk *taskView, e *events.Entry, first, last bool) {
	t := c.Theme()
	open := tk.open[e.Key]
	text := strings.TrimSpace(e.Text)
	head, rest, _ := strings.Cut(text, "\n")
	openable := !e.Streaming && (strings.TrimSpace(rest) != "" || len([]rune(head)) > 60)
	thought := func(text string) {
		ui.Text(c, text).FontSize(12.5).LineHeight(1.55).TextColor(t.TextMuted).Selectable()
	}
	var body func()
	switch {
	case e.Streaming && text != "":
		body = func() { thought(tailLines(e.Text, 4)) }
	case open:
		body = func() { thought(text) }
	}
	if step(c, pal, e.Key, "思考", first, last, open, openable, func() {
		if e.Streaming {
			node(c, pal, threadColor(c, pal), ui.Transparent, func() { ui.Spinner(c).Size(11, 11).TextColor(pal.subtle) })
			return
		}
		node(c, pal, threadColor(c, pal), ui.Transparent, func() { ui.Icon(c, icBulb).Size(12, 12).TextColor(t.TextMuted) })
	}, func() {
		if e.Streaming {
			ui.Text(c, "思考中").FontSize(13).FontWeight(550).TextColor(t.TextMuted).Grow(1)
			return
		}
		ui.Text(c, "思考").FontSize(13).FontWeight(550).Shrink(0)
		summary := oneLine(head)
		if open {
			summary = ""
		}
		ui.Text(c, summary).FontSize(12.5).TextColor(pal.subtle).SingleLine().Grow(1).MinWidth(0)
	}, body) {
		tk.open[e.Key] = !open
	}
}

// compactionStep is a compaction of the conversation's context, a step
// like a tool call's: the summary scrolls under it while it is written,
// and opens in full on a click.
func (s *Shell) compactionStep(c *ui.Context, pal *palette, tk *taskView, e *events.Entry, first, last bool) {
	t := c.Theme()
	open := tk.open[e.Key]
	var body func()
	switch {
	case open && (e.Text != "" || e.Result != ""):
		body = func() {
			ui.Column(c).Gap(6).Children(func() {
				if e.Text != "" {
					codeBox(c, pal, clip(e.Text, 8000), false)
				}
				if e.Result != "" {
					ui.Text(c, e.Result).FontSize(12).TextColor(t.Danger).Selectable()
				}
			})
		}
	case e.Streaming && e.Text != "":
		body = func() { liveBox(c, pal, tailLines(e.Text, 6), false, false) }
	}
	verb := "已压缩上下文"
	switch {
	case e.Streaming:
		verb = "正在压缩上下文"
	case e.IsError:
		verb = "压缩上下文失败"
	}
	if step(c, pal, e.Key, "压缩上下文", first, last, open, true, func() {
		switch {
		case e.Streaming:
			node(c, pal, pal.running.Alpha(0.5), pal.running.Alpha(0.08), func() { ui.Spinner(c).Size(11, 11).TextColor(pal.running) })
		case e.IsError:
			node(c, pal, t.Danger.Alpha(0.5), t.Danger.Alpha(0.08), func() { ui.Icon(c, icX).Size(11, 11).TextColor(t.Danger) })
		default:
			node(c, pal, threadColor(c, pal), ui.Transparent, func() { ui.Icon(c, icFold).Size(12, 12).TextColor(t.TextMuted) })
		}
	}, func() {
		ui.Text(c, verb).FontSize(13).FontWeight(550).Shrink(0)
		// While it is written, the summary scrolls under the line instead.
		line := oneLine(e.Text)
		if e.Streaming && !open || open {
			line = ""
		}
		ui.Text(c, line).FontSize(12.5).TextColor(pal.subtle).SingleLine().Grow(1).MinWidth(0)
	}, body) {
		tk.open[e.Key] = !open
	}
}

// noticeStep is how a turn that did not complete ended, among its steps:
// what happened on its line (failed, in red; stopped, in gray), and why
// in full under it, to read and copy.
func noticeStep(c *ui.Context, pal *palette, e *events.Entry, first, last bool) {
	t := c.Theme()
	head, why := noticeParts(e.Text)
	ink := pal.subtle
	if e.IsError {
		ink = t.Danger
	}
	var body func()
	if why != "" {
		body = func() {
			ui.Text(c, why).FontSize(12.5).LineHeight(1.5).TextColor(t.TextMuted).Selectable()
		}
	}
	step(c, pal, e.Key, e.Text, first, last, false, false, func() {
		node(c, pal, ink.Alpha(0.45), ink.Alpha(0.1), func() { dot(c, ink) })
	}, func() {
		ui.Text(c, head).FontSize(13).FontWeight(550).SingleLine().Ellipsis("…").Grow(1).MinWidth(0).Tooltip(e.Text).Selectable()
	}, body)
}

// endedShort is the notice the task's latest turn ended with, when it
// ended other than completed (it failed, or was stopped), or nil.
func endedShort(sess *events.Session) *events.Entry {
	for i := len(sess.Entries) - 1; i >= 0; i-- {
		switch e := sess.Entries[i]; e.Kind {
		case events.Notice:
			return e
		case events.User:
			return nil
		}
	}
	return nil
}

// noticeParts splits a notice into what happened and why, the "（…）" the
// runtime events put after it ("" without one).
func noticeParts(text string) (head, why string) {
	head, why, _ = strings.Cut(text, "（")
	return head, strings.TrimSpace(strings.TrimSuffix(why, "）"))
}

// liveBox shows text that is still growing under a step's line: a
// command's output as in a terminal (term), else in a quiet box, in the
// code font when mono.
func liveBox(c *ui.Context, pal *palette, text string, term, mono bool) {
	t := c.Theme()
	box := ui.Column(c).Padding(8, 12).Radius(8)
	col := t.TextMuted
	if term {
		box.Background(pal.term)
		col = pal.termText.Alpha(0.75)
	} else {
		box.Background(pal.code).Border(1, pal.hairline)
	}
	box.Children(func() {
		txt := ui.Text(c, text).FontSize(12).LineHeight(1.45).TextColor(col)
		if mono || term {
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
