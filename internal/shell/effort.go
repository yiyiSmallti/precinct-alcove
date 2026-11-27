package shell

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

// The run panel's thinking-effort control, in the look the user picks in
// 外观 (app.Appearance.EffortStyle): 星核, a slider whose knob is a swirl
// of particles; 流光, a slider whose track is a stream of them flowing
// into the knob; 火星, the efforts as labeled segments, embers rising
// from the chosen one. More thinking is more particles, moving faster;
// the service's default drifts, muted, and off all but stands still.
//
// The particles keep nothing between frames: each one's place comes from
// its index, hashed, and a clock that runs at the level's rate
// (effortClock). The control repaints, without building the view, while
// it shows, and holds still when the desktop asks for less motion.

// effortStyle is a look of the control.
type effortStyle struct{ id, name, about string }

// effortStyles are the looks to choose from, the default first.
var effortStyles = []effortStyle{
	{"", "星核", "旋钮是一团绕着核心转的粒子。"},
	{"stream", "流光", "轨道是一条流向旋钮的粒子流。"},
	{"embers", "火星", "档位写在按钮上，选中的往上冒火星。"},
}

// effortStyleOf is the look an appearance draws effort in: the default
// for one it does not know.
func effortStyleOf(ap app.Appearance) string {
	for _, st := range effortStyles {
		if st.id == ap.EffortStyle {
			return st.id
		}
	}
	return ""
}

// effortHeight is how tall a look's control is; 火星's leaves room above
// its segments for the embers.
func effortHeight(style string) float32 {
	switch style {
	case "stream":
		return 30
	case "embers":
		return 46
	}
	return 36
}

// What the particles do at each intensity: the service's default, off,
// then the least to the most thinking (effortLevels). Between two, they
// do what lies between.
var (
	effortMute = []float32{1, 0.55, 0, 0, 0} // how far toward the muted color

	orbCount  = []float32{9, 3, 10, 18, 30}
	orbSpin   = []float32{0.3, 0.05, 1, 2, 3.4} // radians a second
	orbRadius = []float32{7, 4.5, 8, 10.5, 12.5}
	orbCore   = []float32{0, 1.8, 2.4, 2.9, 3.4}
	orbGlow   = []float32{0, 0.06, 0.22, 0.38, 0.6}

	streamCount = []float32{22, 0, 26, 42, 68}   // over the whole track
	streamSpeed = []float32{15, 0, 48, 102, 186} // DIPs a second
	streamWave  = []float32{1.2, 0, 1.4, 2.2, 3.6}

	emberCount = []float32{4, 0, 8, 14, 24}
	emberRate  = []float32{0.22, 0, 0.5, 0.8, 1.25} // rises a second
	emberRise  = []float32{11, 0, 15, 19, 23}
	emberGlow  = []float32{0, 0.05, 0.16, 0.24, 0.34}
)

// effortLevels are the intensities of stops: 0 the service's default, 1
// off, and the efforts that think from 2, the least, to 4, the most, so
// that every model's top effort burns alike.
func effortLevels(stops []string) []float32 {
	lv := make([]float32, len(stops))
	var on []int
	for i, e := range stops {
		switch {
		case i == 0:
		case e == "off":
			lv[i] = 1
		default:
			on = append(on, i)
		}
	}
	for k, i := range on {
		lv[i] = 3
		if len(on) > 1 {
			lv[i] = 2 + 2*float32(k)/float32(len(on)-1)
		}
	}
	return lv
}

// levelAt is a table's value at a fractional index.
func levelAt(tab []float32, x float32) float32 {
	if len(tab) == 1 {
		return tab[0]
	}
	x = min(max(x, 0), float32(len(tab)-1))
	i := min(int(x), len(tab)-2)
	return tab[i] + (tab[i+1]-tab[i])*(x-float32(i))
}

// effortClock is how far an effort control's particles have gone. It
// runs at the rate of the moment, so a rate that changes as the control
// glides to another level never makes them jump, as time × rate would.
type effortClock struct {
	at    time.Time
	phase float64
}

// tick runs the clock on to now at rate a second, a tenth of a second at
// most since the last (the control was hidden), and returns its phase.
func (k *effortClock) tick(now time.Time, rate float32) float64 {
	if !k.at.IsZero() {
		k.phase += min(max(now.Sub(k.at).Seconds(), 0), 0.1) * float64(rate)
	}
	k.at = now
	return k.phase
}

// effortSeconds is the time the particles wobble by: seconds within the
// hour, which float32 still keeps fine.
func effortSeconds(now time.Time) float32 {
	return float32(math.Mod(float64(now.UnixNano())/1e9, 3600))
}

// effortRate is how fast a look's clock runs at an intensity: the swirl's
// radians, the stream's DIPs (faster in the surge after a choice), or the
// embers' rises, a second.
func effortRate(style string, lv, surge float32) float32 {
	switch style {
	case "stream":
		return levelAt(streamSpeed, lv) * (1 + 2.6*surge)
	case "embers":
		return levelAt(emberRate, lv)
	}
	return levelAt(orbSpin, lv)
}

// effortTrailKey keys the springs of the swirl's tail.
type effortTrailKey int

// effortTrail are the springs the swirl's particles follow the knob on,
// in four groups, stiffest first, each critically damped: gliding, the
// slower groups lag behind into a tail.
var effortTrail = [4]ui.Animation{
	{Spring: ui.SpringOptions{Stiffness: 300, Damping: 34.6}},
	{Spring: ui.SpringOptions{Stiffness: 170, Damping: 26.1}},
	{Spring: ui.SpringOptions{Stiffness: 100, Damping: 20}},
	{Spring: ui.SpringOptions{Stiffness: 60, Damping: 15.5}},
}

// effortSlider is the run panel's effort control: a heading with the name
// of the chosen stop (火星 names them all on its segments) over the
// control, in the chosen look. Over a stop, the pointer marks it and the
// name says it, muted. A stop reached saves at once; the next send
// restarts an idle runtime with it.
func (s *Shell) effortSlider(c *ui.Context, pal *palette, stops []string, cur int) {
	t := c.Theme()
	n := len(stops)
	style := effortStyleOf(s.settings.Appearance)
	named := style != "embers"
	var name ui.Element
	ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
		ui.Text(c, "思考强度").FontSize(12.5).FontWeight(600).Grow(1)
		if named {
			// Filled in below, once the control tells what the pointer is
			// over in this frame.
			name = ui.Box(c).Shrink(0)
		}
	})
	sl := ui.SliderBase(c, &s.effortPos, 0, float64(n-1)).Step(1).Height(effortHeight(style)).
		Label("调整思考强度").Cursor(ui.CursorPointer)
	// Each stop owns a column, the stop in its middle: half a column of
	// padding at either end puts the slider's stops, and where it snaps
	// between them, on the columns. The width is the last frame's; the
	// first takes the run panel's inside.
	w := sl.LayoutBounds().W
	if w <= 0 {
		w = 282
	}
	colW := w / float32(n)
	sl.PaddingX(colW / 2)
	if sl.Changed() {
		if i := int(math.Round(s.effortPos)); i >= 0 && i < n && i != cur {
			s.setEffort(stops[i])
			cur = i
			sl.Jump("pop", 1)
		}
	}
	sl.Value(effortName(stops[cur]))
	// Past the slider's own change, the position follows the setting
	// (another model, or a save come back) unless a drag holds it.
	held := sl.Pressed()
	if !held {
		s.effortPos = float64(cur)
	}
	fx := newEffortFx(t, pal, stops, cur)
	fx.pos = sl.AnimateTo("pos", float32(s.effortPos), ui.Smooth)
	for i, a := range effortTrail {
		fx.trail[i] = sl.AnimateTo(effortTrailKey(i), float32(s.effortPos), a)
	}
	fx.pop = sl.AnimateTo("pop", 0, ui.Smooth)
	if px, _, over := sl.PointerPosition(); over && !held {
		fx.hover = min(max(int(px/colW), 0), n-1)
	}
	if named {
		// The stop dragged to, or, muted, the one under the pointer; the
		// default is no level, muted too.
		at, ink := min(max(int(math.Round(s.effortPos)), 0), n-1), t.Accent
		if fx.hover >= 0 && fx.hover != at {
			at, ink = fx.hover, t.TextMuted
		}
		if at == 0 {
			ink = t.TextMuted
		}
		name.Children(func() {
			ui.Text(c, effortName(stops[at])).FontSize(12.5).FontWeight(600).TextColor(ink).SingleLine()
		})
	}
	still := c.Preferences().ReduceMotion
	sl.Draw(func(p *ui.Painter, r ui.Rect) {
		f := fx
		f.t, f.phase = 2.4, s.effortClock.phase // a moment held, for less motion
		if !still {
			now := p.Now()
			f.t = effortSeconds(now)
			f.phase = s.effortClock.tick(now, effortRate(style, levelAt(f.levels, f.pos), f.pop))
			p.AnimationFrameRate(60)
		}
		paintEffort(style, p, r, &f)
	})
}

// previewStops are DeepSeek's efforts, which the looks show themselves
// at in 外观.
var previewStops = []string{"", "off", "low", "high", "max"}

// effortStyleCard is a look of the effort control to choose, at work: its
// control at DeepSeek's 高, on the run panel's ground.
func effortStyleCard(c *ui.Context, pal *palette, st effortStyle, sel bool) bool {
	t := c.Theme()
	still := c.Preferences().ReduceMotion
	card := ui.Column(c).Key("effort-" + st.name).Width(218).Gap(6).Cursor(ui.CursorPointer).Focusable().Label(st.name)
	card.Children(func() {
		frame := ui.Column(c).Height(66).Padding(0, 12).Justify(ui.Center).Radius(8).BorderWidth(2).Background(t.Background)
		switch {
		case sel:
			frame.BorderColor(t.Accent)
		case card.Hovered():
			frame.BorderColor(pal.hairline.Alpha(3))
		default:
			frame.BorderColor(pal.hairline)
		}
		frame.Children(func() {
			ui.Box(c).Height(effortHeight(st.id)).Draw(func(p *ui.Painter, r ui.Rect) {
				fx := newEffortFx(t, pal, previewStops, 3)
				fx.t = 2.4
				if !still {
					fx.t = effortSeconds(p.Now())
					p.AnimationFrameRate(60)
				}
				fx.phase = float64(fx.t) * float64(effortRate(st.id, fx.levels[3], 0))
				paintEffort(st.id, p, r, &fx)
			})
		})
		ui.Column(c).Gap(2).Padding(0, 2).Children(func() {
			label := ui.Text(c, st.name).FontSize(12.5)
			if sel {
				label.FontWeight(600)
			} else {
				label.TextColor(t.TextMuted)
			}
			ui.Text(c, st.about).FontSize(11.5).TextColor(pal.subtle)
		})
	})
	return card.Clicked()
}

// effortFx is a frame of an effort control: where it stands and what it
// paints with.
type effortFx struct {
	stops  []string   // the service's default first
	levels []float32  // their intensities (effortLevels)
	cur    int        // the chosen stop
	hover  int        // the stop under the pointer, or -1
	pos    float32    // where the knob is, in stops, on its way to cur
	trail  [4]float32 // the knob again, on slower springs: the swirl's tail
	pop    float32    // 1 as a stop is chosen, falling to 0
	t      float32    // seconds, for wobbles (effortSeconds)
	phase  float64    // the particles' clock (effortClock)
	dark   bool

	accent, onAccent, text, subtle, ground, glow ui.Color
}

// newEffortFx is a frame of a control over stops with cur chosen, at
// rest, in the theme's colors. The glow is the accent's own light or,
// for the ink on paper, the shadow it casts.
func newEffortFx(t *ui.Theme, pal *palette, stops []string, cur int) effortFx {
	fx := effortFx{stops: stops, levels: effortLevels(stops), cur: cur, hover: -1, pos: float32(cur), dark: t.Dark,
		accent: t.Accent, onAccent: t.AccentText, text: t.Text, subtle: pal.subtle, ground: t.Background, glow: t.Accent}
	for i := range fx.trail {
		fx.trail[i] = fx.pos
	}
	if pal.inkAccent && !t.Dark {
		fx.glow = ui.RGBA(0, 0, 0, 0.45)
	}
	return fx
}

// paintEffort paints a frame of a look's control in r.
func paintEffort(style string, p *ui.Painter, r ui.Rect, fx *effortFx) {
	switch style {
	case "stream":
		fx.paintStream(p, r)
	case "embers":
		fx.paintEmbers(p, r)
	default:
		fx.paintOrb(p, r)
	}
}

// stopX is where stop i lies across r, fractional as a gliding knob is:
// in the middle of its column.
func (fx *effortFx) stopX(r ui.Rect, i float32) float32 {
	return r.X + r.W/float32(len(fx.stops))*(i+0.5)
}

// paintOrb paints 星核: a thin track with a dot for each stop, lit up to
// the chosen one, and the knob, a core with particles circling it on a
// tilted ring, the far ones dim. Gliding, the particles follow in four
// groups, each on its own spring, and stretch into a tail; at the most
// thinking sparks fly off, and a ring of them spreads where a choice
// lands.
func (fx *effortFx) paintOrb(p *ui.Painter, r ui.Rect) {
	n := len(fx.stops)
	cy := r.Y + r.H/2
	x, x0, x1 := fx.stopX(r, fx.pos), fx.stopX(r, 0), fx.stopX(r, float32(n-1))
	lv := levelAt(fx.levels, fx.pos)

	p.Fill(ui.Rect{X: x0, Y: cy - 1, W: x1 - x0, H: 2}, fx.text.Alpha(0.1), 1)
	if x > x0+1 {
		p.FillGradient(ui.Rect{X: x0, Y: cy - 1, W: x - x0, H: 2}, ui.LinearGradient{Angle: 90, From: fx.accent.Alpha(0.12), To: fx.accent.Alpha(0.85)}, 1)
	}
	for i := range n {
		sx := fx.stopX(r, float32(i))
		switch {
		case i == 0:
			p.StrokeDashed(circle(sx, cy, 3.6), fx.subtle.Alpha(0.9), 3.6, 1.2)
		case i <= fx.cur:
			fillDot(p, sx, cy, 2.2, fx.accent.Alpha(0.75))
		default:
			fillDot(p, sx, cy, 2.2, fx.text.Alpha(0.22))
		}
	}
	if fx.hover >= 0 && fx.hover != fx.cur {
		p.Stroke(circle(fx.stopX(r, float32(fx.hover)), cy, 7), fx.accent.Alpha(0.4), 7, 1)
	}

	col := fx.accent.Mix(fx.subtle, levelAt(effortMute, lv))
	rad := levelAt(orbRadius, lv)
	glowAt(p, x, cy, 9+rad*1.3, fx.glow.Alpha(levelAt(orbGlow, lv)))
	type particle struct{ x, y, z, s float32 }
	var ps [32]particle
	count := min(int(levelAt(orbCount, lv)+0.5), len(ps))
	swirl := clamp01(lv)                   // the default's particles drift loose
	sinT, cosT := sinf(-0.38), cosf(-0.38) // the ring's tilt
	for i := range count {
		rr := rad * (0.5 + 0.5*hash01(i, 2)) * (1 + 0.1*sinf(fx.t*1.7+float32(i)))
		th := float32(math.Mod(float64(hash01(i, 1))*2*math.Pi+fx.phase*float64(0.7+0.6*hash01(i, 3)), 2*math.Pi))
		ex, ey := cosf(th)*rr, sinf(th)*rr*0.42
		dx := (hash01(i, 6)-0.5)*16 + sinf(fx.t*0.7+float32(i)*1.3)*2.5
		dy := (hash01(i, 7)-0.5)*12 + cosf(fx.t*0.6+float32(i)*2.1)*2
		z := lerp(0.5, sinf(th)*0.5+0.5, swirl)
		ps[i] = particle{
			x: fx.stopX(r, fx.trail[i%len(fx.trail)]) + lerp(dx, ex*cosT-ey*sinT, swirl),
			y: cy + lerp(dy, ex*sinT+ey*cosT, swirl),
			z: z,
			s: (0.7 + 0.9*hash01(i, 5)) * (0.75 + 0.5*z),
		}
	}
	// The far half, the core, the near half.
	for _, q := range ps[:count] {
		if q.z < 0.5 {
			fillDot(p, q.x, q.y, q.s, col.Alpha(0.25+0.6*q.z))
		}
	}
	if core := levelAt(orbCore, lv); core > 0.1 {
		fillDot(p, x, cy, core+1.6, fx.ground.Alpha(0.9))
		fillDot(p, x, cy, core, col)
	} else {
		p.StrokeDashed(circle(x, cy, 6), fx.subtle.Alpha(0.9), 6, 1.2)
	}
	for _, q := range ps[:count] {
		if q.z >= 0.5 {
			fillDot(p, q.x, q.y, q.s, col.Alpha(0.35+0.65*q.z))
		}
	}
	if spark := clamp01(lv - 3); spark > 0 {
		const period = 0.8
		for j := range 4 {
			tt := float64(fx.t) + float64(j)*period/4
			b := math.Floor(tt / period)
			ph := float32(tt/period - b)
			ang := hash01(int(b)*7+j, 9) * 2 * math.Pi
			dx, dy := cosf(ang), sinf(ang)*0.55
			for k, a := range [2]float32{1, 0.4} { // the spark and its tail
				d := 5 + ph*15 - 3*float32(k)
				fillDot(p, x+dx*d, cy+dy*d, (1.2*(1-ph)+0.3)*(1-0.3*float32(k)), fx.accent.Alpha(a*(1-ph)*0.9*spark))
			}
		}
	}
	if fx.pop > 0.01 {
		age, lx := 1-fx.pop, fx.stopX(r, float32(fx.cur))
		for j := range 12 {
			ang := float32(j)/12*2*math.Pi + hash01(j, 4)*0.4
			d := 4 + age*18
			fillDot(p, lx+cosf(ang)*d, cy+sinf(ang)*d*0.7, 1.3*(1-age)+0.3, col.Alpha((1-age)*0.8))
		}
	}
}

// paintStream paints 流光: particles flowing along the track into the
// knob, more, faster and wavier the more the model thinks, and surging
// after a choice; past the knob, a quiet dotted rail. The default
// shimmers faintly along all of it; off is still.
func (fx *effortFx) paintStream(p *ui.Painter, r ui.Rect) {
	n := len(fx.stops)
	cy := r.Y + r.H/2
	kx, x0, x1 := fx.stopX(r, fx.pos), fx.stopX(r, 0), fx.stopX(r, float32(n-1))
	lv := levelAt(fx.levels, fx.pos)

	for x := x0; x <= x1+0.5; x += 6 {
		if x > kx+6 || fx.pos < 0.5 {
			fillDot(p, x, cy, 0.9, fx.text.Alpha(0.16))
		}
	}
	for i := 1; i < n; i++ {
		if sx := fx.stopX(r, float32(i)); sx > kx+6 {
			fillDot(p, sx, cy, 2, fx.text.Alpha(0.24))
		}
	}
	p.StrokeDashed(circle(x0, cy, 3.6), fx.subtle.Alpha(0.9), 3.6, 1.2)
	if fx.hover >= 0 && fx.hover != fx.cur {
		p.Stroke(circle(fx.stopX(r, float32(fx.hover)), cy, 6.5), fx.accent.Alpha(0.4), 6.5, 1)
	}

	col := fx.accent.Mix(fx.subtle, levelAt(effortMute, lv))
	count := int(levelAt(streamCount, lv) + 0.5)
	speed := levelAt(streamSpeed, lv) * (1 + 2.6*fx.pop)
	wave := levelAt(streamWave, lv) * (1 + 0.6*fx.pop)
	span, length := max(kx-x0, 1), max(x1-x0, 1)
	loose := clamp01(1 - lv) // the default's shimmer
	for i := range count {
		v := 0.7 + 0.6*hash01(i, 2)
		// Each particle laps the whole track; the clock is DIPs flowed.
		u := float32(math.Mod(float64(hash01(i, 1))+fx.phase*float64(v/length), 1))
		x := x0 + u*length
		y := cy + sinf(u*23+fx.t*2.2*hash01(i, 3)+float32(i))*wave*(0.25+0.75*hash01(i, 4))
		x = lerp(x, x0+hash01(i, 8)*length+sinf(fx.t*0.5+float32(i))*3, loose)
		y = lerp(y, cy+(hash01(i, 9)-0.5)*7, loose)
		// Fading in at the start and out into the knob: nothing flows
		// past it.
		edge := min((x-x0)/12+0.15, 1) * lerp(clamp01((kx-6-x)/10), 1, loose)
		if edge <= 0 {
			continue
		}
		rel := clamp01((x - x0) / span)
		a := lerp((0.2+0.8*rel)*edge, 0.35*(0.55+0.45*sinf(fx.t*2+float32(i)*1.7)), loose)
		s := (0.5 + 0.8*hash01(i, 5)) * (0.8 + 0.4*rel)
		// A streak behind, longer the faster it goes.
		if l := min(speed*v*14/300, 6) * (1 - loose); l > 2 {
			p.Fill(ui.Rect{X: x - l, Y: y - s/2, W: l, H: s}, col.Alpha(a*0.3), s/2)
		}
		fillDot(p, x, y, s, col.Alpha(a))
	}
	// The knob: a bead lit by the stream; at the default an empty ring.
	if fx.pos > 0.5 {
		glowAt(p, kx, cy, 14, fx.glow.Alpha((0.2+0.25*clamp01((lv-1)/3))*(1+fx.pop)))
		fillDot(p, kx, cy, 5.2, fx.ground)
		fillDot(p, kx, cy, 4, col)
	} else {
		fillDot(p, kx, cy, 5.5, fx.ground)
		p.StrokeDashed(circle(kx, cy, 5), fx.accent.Alpha(0.8), 5, 1.2)
	}
}

// paintEmbers paints 火星: the stops as labeled segments on a track, the
// chosen one a block of the accent that glides over, stretching as it
// goes, with embers rising from it, more and higher the more the model
// thinks, and a spray of them where a choice lands. The default's block
// is a dashed outline with a few grey motes drifting up.
func (fx *effortFx) paintEmbers(p *ui.Painter, r ui.Rect) {
	n := len(fx.stops)
	const segH = 28
	top, segW := r.Y+r.H-segH, r.W/float32(n)
	lv := levelAt(fx.levels, fx.pos)
	track := float32(0.05)
	if fx.dark {
		track = 0.07
	}
	p.Fill(ui.Rect{X: r.X, Y: top, W: r.W, H: segH}, fx.text.Alpha(track), 9)

	stretch := min(absf(fx.pos-fx.trail[3])*segW*0.5, 18)
	block := ui.Rect{X: r.X + fx.pos*segW + 2 - stretch/2, Y: top + 2, W: segW - 4 + stretch, H: segH - 4}
	mid := block.X + block.W/2
	col := fx.accent.Mix(fx.subtle, levelAt(effortMute, lv))
	glowAt(p, mid, top+segH/2, segW*0.75, fx.glow.Alpha(levelAt(emberGlow, lv)))
	if fx.pos < 0.5 {
		p.Fill(block, fx.ground, 7)
		p.StrokeDashed(block, fx.subtle, 7, 1.2)
	} else {
		p.Fill(block, col, 7)
	}

	count, rise := int(levelAt(emberCount, lv)+0.5), levelAt(emberRise, lv)
	dim := float32(1)
	if fx.pos < 0.5 {
		dim = 0.5
	}
	for i := range count {
		ph := float32(math.Mod(float64(hash01(i, 1))+fx.phase*float64(0.6+0.8*hash01(i, 2)), 1))
		x := mid + (hash01(i, 3)-0.5)*(segW-14) + sinf(ph*7+float32(i))*2.4
		y := top + 1 - ph*rise*(0.6+0.4*hash01(i, 4))
		a := sinf(math.Pi*min(ph*1.15, 1)) * (0.6 + 0.4*sinf(fx.t*9+float32(i)*2.3)) * dim
		fillDot(p, x, y, (2-ph)*(0.6+0.5*hash01(i, 5)), col.Alpha(a))
	}
	if fx.pop > 0.01 && fx.cur > 0 {
		age, bx := 1-fx.pop, r.X+(float32(fx.cur)+0.5)*segW
		for j := range 14 {
			ang := -math.Pi * (0.08 + 0.84*hash01(j, 11))
			d := age * (12 + 12*hash01(j, 12))
			fillDot(p, bx+cosf(ang)*d*1.4, top+3+sinf(ang)*d, 1.4*(1-age)+0.3, fx.accent.Alpha((1-age)*0.9))
		}
	}
	// The names: the chosen one and the one under the pointer in full
	// ink, and on the block in the accent's text color.
	for i, e := range fx.stops {
		c := fx.text.Mix(fx.subtle, 0.35)
		if i == fx.cur || i == fx.hover {
			c = fx.text
		}
		if fx.pos >= 0.5 {
			c = c.Mix(fx.onAccent, clamp01(1-absf(fx.pos-float32(i))))
		}
		sp := ui.Span{Text: effortName(e), Size: 12, Weight: 600, Color: c}
		w, h := p.MeasureText(0, sp)
		p.RichText(r.X+(float32(i)+0.5)*segW-w/2, top+(segH-h)/2, 0, sp)
	}
}

// circle is the box of a circle.
func circle(x, y, r float32) ui.Rect { return ui.Rect{X: x - r, Y: y - r, W: 2 * r, H: 2 * r} }

// fillDot paints a filled circle.
func fillDot(p *ui.Painter, x, y, r float32, c ui.Color) { p.Fill(circle(x, y, r), c, r) }

// glowAt paints light fading out from a point to radius r, c at its
// heart.
func glowAt(p *ui.Painter, x, y, r float32, c ui.Color) {
	if c.A == 0 {
		return
	}
	p.FillRadialGradient(circle(x, y, r), ui.RadialGradient{Stops: []ui.GradientStop{{Color: c}, {Color: c.Alpha(0), At: 1}}, Radius: r}, r)
}

// hash01 is a fixed pseudo-random number in [0, 1) for particle i, one
// for each salt.
func hash01(i, salt int) float32 {
	x := math.Sin(float64(i)*127.1+float64(salt)*311.7) * 43758.5453
	return float32(x - math.Floor(x))
}

func lerp(a, b, f float32) float32 { return a + (b-a)*f }
func clamp01(x float32) float32    { return min(max(x, 0), 1) }
func absf(x float32) float32       { return float32(math.Abs(float64(x))) }
func sinf(x float32) float32       { return float32(math.Sin(float64(x))) }
func cosf(x float32) float32       { return float32(math.Cos(float64(x))) }
