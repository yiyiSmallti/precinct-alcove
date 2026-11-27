package shell

import (
	"embed"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// Index stands in the alcove beside the page: an arch cut through the
// glass, through which the theme's painting shows sharp, and she stands in
// it dressed in the theme's outfit. She shows how the task is going: ready
// for one, at work, waiting on the user, done, or stuck; a placard over the
// arch names it and says what is going on, and she hops when it changes.
// She is a layer of her own over the painting, so she can change while the
// scene stays: a still picture of her for each mood.
type mood string

const (
	moodIdle    mood = "idle"
	moodWorking mood = "working"
	moodAsking  mood = "asking"
	moodDone    mood = "done"
	moodOops    mood = "oops"
)

// lines are what she says in each mood, in her voice (SOUL.md): short,
// a little dry, and no jab when something broke.
var lines = map[mood][]string{
	moodIdle:    {"说吧，今天拆什么", "又来活了？", "我在，开口就行"},
	moodWorking: {"在弄了，别催", "跑着呢，喝口水", "让我专心一下"},
	moodAsking:  {"这步得你拿主意", "等你一句话", "你定，我照做"},
	moodDone:    {"搞定，验收吧", "完事了，看看对不对", "好了，不用谢"},
	moodOops:    {"出错了，我看看哪儿不对", "卡住了，报错在左边", "没成，先看下原因"},
}

// moodNames are the placard's words for each mood.
var moodNames = map[mood]string{
	moodIdle: "待命", moodWorking: "工作中", moodAsking: "等你确认", moodDone: "完成了", moodOops: "出错了",
}

// moodWord is the placard's word for how a task is going: waiting on the
// user says for what, an approval or an answer.
func moodWord(v *wsView, tk *taskView, m mood) string {
	if m == moodAsking && tk != nil {
		return "等你" + v.waitWord(tk)
	}
	return moodNames[m]
}

// line picks one of a mood's lines for a task, the same every frame.
func line(m mood, key string) string {
	ls := lines[m]
	h := fnv.New32a()
	h.Write([]byte(key + string(m)))
	return ls[int(h.Sum32()%uint32(len(ls)))]
}

// moodOf is how a task is going, as its header says; no task is the
// workspace's home.
func (s *Shell) moodOf(v *wsView, tk *taskView) mood {
	if tk == nil {
		return moodIdle
	}
	as, qs := v.pending(tk)
	switch {
	case len(as)+len(qs) > 0:
		return moodAsking
	case tk.err != "":
		return moodOops
	case tk.session.Running() || tk.starting || tk.session.AgentsRunning() > 0:
		return moodWorking
	}
	// A turn that failed is stuck; one that was stopped is just waiting
	// for the next; only one that completed is done.
	if e := endedShort(tk.session); e != nil {
		if e.IsError {
			return moodOops
		}
		return moodIdle
	}
	if len(tk.session.Entries) > 0 {
		return moodDone
	}
	return moodIdle
}

// moodColor is the placard's dot for a mood.
func moodColor(c *ui.Context, pal *palette, m mood) ui.Color {
	switch m {
	case moodWorking:
		return pal.running
	case moodAsking:
		return pal.waiting
	case moodDone:
		return pal.done
	case moodOops:
		return c.Theme().Danger
	}
	return pal.subtle
}

// nowLine says what a task is about, under the placard's word.
func nowLine(v *wsView, tk *taskView, m mood) string {
	if tk == nil {
		return "在「" + v.ws.Name + "」里，说一件要做的事"
	}
	switch m {
	case moodAsking:
		as, _ := v.pending(tk)
		if len(as) > 0 {
			return toolVerb(as[0].req.ToolName) + "要你点头才能继续"
		}
		return "有个问题要你回答"
	case moodWorking:
		for i := len(tk.session.Entries) - 1; i >= 0; i-- {
			e := tk.session.Entries[i]
			switch {
			case e.Busy() && e.Agent != "":
				return "子代理在" + strings.TrimPrefix(agentProgress(*e), "正在")
			case e.Busy():
				return strings.TrimSpace("正在" + toolVerb(e.ToolName) + " " + clip(entrySummary(*e), 60))
			case e.Kind == events.Reasoning && e.Streaming:
				return "正在想下一步"
			case e.Kind == events.Assistant && e.Streaming:
				return "正在写回复"
			}
		}
		if tk.starting {
			return "正在启动"
		}
		return "在处理你的要求"
	case moodDone:
		calls := 0
		for _, e := range tk.session.Entries {
			if e.Kind == events.Tool {
				calls++
			}
		}
		paths, _ := changedFiles(tk.session)
		if calls == 0 {
			return "回答完了"
		}
		if len(paths) == 0 {
			return fmt.Sprintf("做了 %d 步", calls)
		}
		return fmt.Sprintf("做了 %d 步，改了 %d 个文件", calls, len(paths))
	case moodOops:
		why := tk.err
		if e := endedShort(tk.session); why == "" && e != nil {
			head, more := noticeParts(e.Text)
			why = more
			if why == "" {
				why = head
			}
		}
		first, _, _ := strings.Cut(strings.TrimSpace(why), "\n")
		return clip(first, 80)
	case moodIdle:
		if e := endedShort(tk.session); e != nil {
			head, _ := noticeParts(e.Text)
			return head
		}
	}
	return ""
}

// moodTrack notices Index's mood changing on one page, so that she hops
// once; going to another page only changes her line.
type moodTrack struct {
	last mood
	key  string
	run  uint64
}

func (t *moodTrack) see(m mood, key string) uint64 {
	if key == t.key && m != t.last && t.last != "" {
		t.run++
	}
	t.last, t.key = m, key
	return t.run
}

// hop is her little jump when her mood changes.
var hop = []ui.Keyframe{
	{At: 0, Value: 0},
	{At: 150 * time.Millisecond, Value: -14, Ease: ui.EaseOut},
	{At: 330 * time.Millisecond, Value: 0, Ease: ui.EaseInOut},
	{At: 420 * time.Millisecond, Value: -3, Ease: ui.EaseOut},
	{At: 500 * time.Millisecond, Value: 0, Ease: ui.EaseInOut},
}

// Her pictures, index/<outfit>-<mood>/1x.webp and 2x.webp: resized from
// her full picture to exactly the pixels she fills (picW by picH DIPs) on
// a plain display and a Retina one, so that each is drawn a pixel for a
// pixel. Drawn at another size, a picture blurs.
//
//go:embed index
var indexFS embed.FS

// picW by picH DIPs is her picture; the alcove's arch is as wide.
const picW, picH = 256, 384

// A still is her picture in an outfit and a mood, at each density.
type still struct{ x1, x2 *ui.Bitmap }

var stills sync.Map // "outfit-mood" → *still, or nil when missing

// stillOf is Index in an outfit and a mood, or nil when that outfit has
// no picture for it.
func stillOf(outfit string, m mood) *still {
	key := outfit + "-" + string(m)
	if v, ok := stills.Load(key); ok {
		st, _ := v.(*still)
		return st
	}
	var st *still
	x1, err1 := decodeIndex("index/" + key + "/1x.webp")
	x2, err2 := decodeIndex("index/" + key + "/2x.webp")
	if err1 == nil && err2 == nil {
		st = &still{x1, x2}
	}
	stills.Store(key, st)
	return st
}

func decodeIndex(name string) (*ui.Bitmap, error) {
	data, err := indexFS.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return ui.DecodeBitmap(data)
}

// paint draws her standing at the foot of r, in the middle, on whole
// device pixels, from the picture made for the display's density.
func (st *still) paint(p *ui.Painter, r ui.Rect) {
	d := p.Scale()
	bmp := st.x1
	if d > 1.25 {
		bmp = st.x2
	}
	snap := func(v float32) float32 { return float32(math.Round(float64(v*d))) / d }
	dst := ui.Rect{X: snap(r.X + (r.W-picW)/2), Y: snap(r.Y + r.H - picH), W: picW, H: picH}
	p.Image(bmp, dst, ui.FillBox)
}

// bandFits reports whether the alcove shows beside the sheet: over a
// painting whose outfit has pictures of her, unless hidden, and while the
// sheet keeps room enough beside it.
func (s *Shell) bandFits(pal *palette) bool {
	if pal.wall == nil || s.panels.hidden {
		return false
	}
	sc, _ := sceneFor(s.settings.Appearance)
	if stillOf(sc.outfit, moodIdle) == nil {
		return false
	}
	return pal.win.W-(sideW+bandW+4*gutter) >= minSheetW
}

// bandToggle shows or hides the alcove, where it has room to show.
func (s *Shell) bandToggle(c *ui.Context, pal *palette) {
	shown := s.bandFits(pal)
	if !shown && !s.panels.hidden {
		return // no painting, or no room: nothing to toggle
	}
	label := "隐藏 Index"
	if !shown {
		label = "显示 Index"
	}
	if iconButton(c, pal, icArch, label, shown).Clicked() {
		s.panels.hidden = shown
	}
}

// band is the alcove beside the sheet: the placard (her mood's word, and
// now, what is going on) over the arch, through which the painting shows
// sharp, where she stands with her line. key is the page or task it is
// about, word the placard's word for m; now "" says nothing under it.
func (s *Shell) band(c *ui.Context, pal *palette, m mood, key, word, now string) {
	if !s.bandFits(pal) {
		return
	}
	t := c.Theme()
	sc, _ := sceneFor(s.settings.Appearance)
	pet := stillOf(sc.outfit, m)
	if pet == nil {
		pet = stillOf(sc.outfit, moodIdle)
	}
	run := s.moods.see(m, key)
	col := ui.Column(c).Width(bandW).Shrink(0)
	glassPane(pal, col).Children(func() {
		if bar := c.TitleBar(); bar.Height > gutter {
			ui.Row(c).Height(bar.Height - gutter).Shrink(0).DragWindow()
		} else {
			ui.Box(c).Height(10)
		}
		ui.Column(c).Padding(2, 20, 14).Gap(5).Children(func() {
			ui.Text(c, "INDEX").FontSize(10).FontWeight(650).LetterSpacing(2.4).TextColor(t.TextMuted)
			ui.Row(c).Gap(9).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(8, 8).Radius(4).Background(moodColor(c, pal, m)).Shrink(0).Transition(fade)
				display(c, pal, word, 21).Key("mood-" + word).
					Transition(ui.ElementTransition{Duration: 220 * time.Millisecond, Enter: &ui.Motion{Y: 4}})
			})
			if now != "" {
				ui.Text(c, now).FontSize(12).TextColor(t.TextMuted).MaxLines(2).Ellipsis("…").LineHeight(1.45)
			}
		})
		r := float32(picW / 2)
		arch := ui.Box(c).Grow(1).Margin(0, (bandW-picW)/2, (bandW-picW)/2).Radius(r, r, 14, 14).Clip()
		b, w := arch.LayoutBounds(), pal.win
		if b.W == 0 || w.W == 0 {
			c.Invalidate()
		}
		arch.Children(func() {
			// The painting where it lies in the window, so the arch is an
			// opening onto the scene behind the glass.
			ui.Image(c, pal.wall.sharp).Fit(ui.Cover).Absolute().Left(w.X-b.X).Top(w.Y-b.Y).Size(w.W, w.H).IgnoreInput(true)
			stand := ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(picH).IgnoreInput(true)
			lift := stand.Keyframes("hop", hop, ui.KeyframeOptions{Run: run})
			stand.Draw(func(p *ui.Painter, r ui.Rect) {
				pet.paint(p, ui.Rect{X: r.X, Y: r.Y + lift, W: r.W, H: r.H})
			})
			// Her line, by her head; over it when the arch is short.
			said := ui.Column(c).Absolute().Left(12).AlignItems(ui.Start).IgnoreInput(true).Key("said-" + key + string(m)).
				Transition(ui.ElementTransition{Duration: 260 * time.Millisecond, Enter: &ui.Motion{Y: 8, Opacity: 0, Scale: 0.94}})
			if b.H > picH+64 {
				said.Bottom(picH - 18)
			} else {
				said.Top(16)
			}
			said.Children(func() {
				ui.Text(c, line(m, key)).FontSize(13).FontWeight(500).Padding(8, 12).Radius(14).MaxWidth(170).
					Background(pal.content.Alpha(0.95)).Shadow(0, 4, 14, -2, ui.RGBA(0, 0, 0, 0.18))
				ui.Box(c).Size(9, 9).Rotate(45).Margin(-5, 0, 0, 22).Background(pal.content.Alpha(0.95))
			})
			// The arch's rim: a shade inside its edge, and a lit hairline.
			ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).IgnoreInput(true).Radius(r, r, 14, 14).
				Border(1, pal.rim).InsetShadow(0, 4, 16, 0, ui.RGBA(0, 0, 0, 0.2))
		})
	})
}
