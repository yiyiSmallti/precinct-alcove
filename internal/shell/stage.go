package shell

import (
	"embed"
	"hash/fnv"
	"math"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Index stands at the foot of the right panel, inside the theme's painting,
// dressed in the theme's outfit, and shows how the task is going: ready
// for one, at work, waiting on the user, done, or stuck. She is a layer of
// her own over the painting, so she can change while the scene stays: a
// still picture of her for each mood.
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
	moodAsking:  {"这步得你点头", "等你一句话", "看一眼再放行"},
	moodDone:    {"搞定，验收吧", "完事了，看看对不对", "好了，不用谢"},
	moodOops:    {"出错了，我看看哪儿不对", "卡住了，报错在左边", "没成，先看下原因"},
}

// line picks one of a mood's lines for a task, the same every frame.
func line(m mood, key string) string {
	ls := lines[m]
	h := fnv.New32a()
	h.Write([]byte(key + string(m)))
	return ls[int(h.Sum32()%uint32(len(ls)))]
}

// moodOf is how a task is going, as the panel's status says; no task is
// the new-task page.
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
	case len(tk.session.Entries) > 0:
		return moodDone
	}
	return moodIdle
}

// Her pictures, index/<outfit>-<mood>/1x.webp and 2x.webp: resized from
// her full picture to exactly the pixels she fills (picW by picH DIPs) on
// a plain display and a Retina one, so that each is drawn a pixel for a
// pixel. Drawn at another size, a picture blurs.
//
//go:embed index
var indexFS embed.FS

// stageSize is the box she stands in, at the panel's foot, in DIPs; her
// picture stands at its foot, in the middle.
const (
	stageW, stageH = 300, 384
	picW, picH     = 256, 384
)

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

// stagePanel fills the right panel's column with fn over the painting, seen
// through tinted glass that clears toward the foot, where Index stands
// with a line for the task. Without a painting, or a picture of her in
// its outfit, the panel is as before.
func (s *Shell) stagePanel(c *ui.Context, pal *palette, col ui.Element, v *wsView, tk *taskView, fn func()) {
	sc, _ := sceneFor(s.settings.Appearance)
	m := s.moodOf(v, tk)
	pet := stillOf(sc.outfit, m)
	if pal.wall == nil || pet == nil {
		sideGround(c, pal, col).Children(fn)
		return
	}
	b, w := col.LayoutBounds(), pal.win
	if b.W == 0 || w.W == 0 {
		c.Invalidate()
	}
	key := "new"
	if tk != nil {
		key = tk.meta.ID
	}
	said := line(m, key)
	col.Clip().Children(func() {
		ui.Image(c, pal.wall.sharp).Fit(ui.Cover).Absolute().Left(w.X-b.X).Top(w.Y-b.Y).Size(w.W, w.H).IgnoreInput(true)
		ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).IgnoreInput(true).
			LinearGradient(ui.LinearGradient{From: pal.sidebar.Alpha(0.86), To: pal.sidebar.Alpha(0.08), Angle: 180, Start: 0.42, End: 0.95, Oklab: true})
		ui.Box(c).Absolute().Right(0).Bottom(0).Size(stageW, stageH).IgnoreInput(true).
			Draw(func(p *ui.Painter, r ui.Rect) { pet.paint(p, r) })
		ui.Row(c).Absolute().Left(16).Right(132).Bottom(stageH - 36).Justify(ui.Start).IgnoreInput(true).Children(func() {
			ui.Text(c, said).FontSize(12.5).Padding(7, 11).Radius(12).Background(pal.content.Alpha(0.94)).Shadow(0, 3, 12, 0, pal.shadow).
				Key("line-" + string(m))
		})
		ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Children(fn)
	})
}
