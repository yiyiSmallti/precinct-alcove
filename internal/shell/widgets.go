package shell

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// Small pieces shared by the window's panes.

// The window's frame: the gutters between the panes over the painting,
// the sidebar's and the alcove's widths, the radii of the panes and of the
// sheet, and how narrow the sheet may get before the alcove steps aside.
const (
	gutter    = 8
	sideW     = 256
	bandW     = 280
	paneR     = 18
	sheetR    = 16
	minSheetW = 620
)

// fade eases a control's colors between rest, hover and chosen.
var fade = ui.ElementTransition{Duration: 140 * time.Millisecond, Colors: true}

// glassPane fills a pane laid over the painting with Liquid Glass, tinted
// toward the tone's sidebar; without a painting the pane is clear, over
// the window's ground or its material.
func glassPane(pal *palette, e ui.Element) ui.Element {
	if pal.wall != nil {
		return e.Radius(paneR).Material(glass.Glass{Clarity: 0.35, Tint: pal.sidebar.Alpha(0.3)})
	}
	return e
}

// sheet lays a page on paper: opaque, rounded and lifted off the window by
// a soft shadow, so it reads as paper on a desk.
func sheet(pal *palette, e ui.Element) ui.Element {
	e.Background(pal.content).Radius(sheetR).Clip()
	if pal.wall != nil {
		return e.Shadow(0, 12, 36, -8, pal.shadow.Alpha(2)).Shadow(0, 1, 3, 0, pal.shadow)
	}
	return e.Border(1, pal.hairline).Shadow(0, 2, 10, 0, pal.shadow)
}

// listRow is a clickable row of a side list, highlighted when chosen or
// under the pointer; it reports a click.
func listRow(c *ui.Context, pal *palette, key, label string, sel bool, fn func()) bool {
	r := ui.Row(c).Key(key).Height(30).Padding(0, 8).Gap(8).AlignItems(ui.Center).Radius(8).Cursor(ui.CursorPointer).Focusable().Label(label).Transition(fade)
	switch {
	case sel:
		r.Background(pal.selected)
	case r.Hovered():
		r.Background(pal.hover)
	}
	r.Children(fn)
	return r.Clicked()
}

// navRow is a list row with an icon and a label.
func navRow(c *ui.Context, pal *palette, key string, ic *ui.SVG, label string, sel bool) bool {
	t := c.Theme()
	return listRow(c, pal, key, label, sel, func() {
		ui.Icon(c, ic).Size(15, 15).TextColor(t.TextMuted)
		ui.Text(c, label).FontSize(13).SingleLine().Grow(1).MinWidth(0)
	})
}

// countBadge is a count in a capsule of color; nothing at 0.
func countBadge(c *ui.Context, color ui.Color, n int) {
	if n <= 0 {
		return
	}
	text := fmt.Sprint(n)
	if n > 99 {
		text = "99+"
	}
	ui.Text(c, text).FontSize(10.5).Bold().FontFeatures("tnum").TextColor(onAccent(color)).Background(color).Padding(1, 6).Radius(8).Shrink(0)
}

// toggle is a switch as ui.Switch draws it, but with its knob in the color
// that reads on the accent: ui.Switch's knob is always white, lost on a
// white track (the ink accent in the dark).
func toggle(c *ui.Context, on *bool) ui.Element {
	t := c.Theme()
	sw := ui.SwitchBase(c, on).Size(t.Space(9), t.Space(5)).Radius(t.Space(2.5))
	var to float32
	if *on {
		to = 1
	}
	pos := sw.Animate("knob", to, 140*time.Millisecond)
	sw.Background(t.Border.Mix(t.Text, 0.15).Mix(t.Accent, pos))
	sw.Draw(func(p *ui.Painter, r ui.Rect) {
		in := t.Space(0.5)
		d := r.H - 2*in
		knob := ui.Rect{X: r.X + in + pos*(r.W-r.H), Y: r.Y + in, W: d, H: d}
		p.Shadow(knob, d/2, 0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.25))
		p.Fill(knob, ui.RGB(255, 255, 255).Mix(t.AccentText, pos), d/2)
	})
	return sw
}

// glow is the light an accent casts under what it fills. The ink casts
// none: white light under a white button in the dark reads as a smudge,
// so it lays a plain shadow instead.
func glow(c *ui.Context, pal *palette, a float32) ui.Color {
	if pal.inkAccent {
		return ui.RGBA(0, 0, 0, a*0.6)
	}
	return c.Theme().Accent.Alpha(a)
}

// unreadBadge is a conversation's unread count in the sidebar.
func unreadBadge(c *ui.Context, pal *palette, n int) { countBadge(c, pal.running, n) }

// iconButton is a quiet square button showing an icon.
func iconButton(c *ui.Context, pal *palette, ic *ui.SVG, label string, on bool) ui.Element {
	t := c.Theme()
	b := ui.Row(c).Size(30, 30).AlignItems(ui.Center).Justify(ui.Center).Radius(8).Cursor(ui.CursorPointer).Focusable().Label(label).Tooltip(label).Transition(fade)
	col := t.TextMuted
	switch {
	case on:
		b.Background(pal.selected)
		col = t.Text
	case b.Hovered():
		b.Background(pal.hover)
		col = t.Text
	}
	b.Children(func() { ui.Icon(c, ic).Size(16, 16).TextColor(col) })
	return b
}

// smallIconButton is a 22-DIP icon button beside a section's title.
func smallIconButton(c *ui.Context, pal *palette, ic *ui.SVG, label string) ui.Element {
	t := c.Theme()
	b := ui.Row(c).Size(22, 22).AlignItems(ui.Center).Justify(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(label).Tooltip(label).Transition(fade)
	col := t.TextMuted
	if b.Hovered() {
		b.Background(pal.hover)
		col = t.Text
	}
	b.Children(func() { ui.Icon(c, ic).Size(14, 14).TextColor(col) })
	return b
}

// roundButton is the composer's circular send or stop button.
func roundButton(c *ui.Context, pal *palette, ic *ui.SVG, label string, ink, disabled bool) bool {
	t := c.Theme()
	bg, fg := pal.hover, t.Text
	if ink {
		bg, fg = t.Accent, t.AccentText
		if disabled {
			bg, fg = pal.selected, pal.subtle
		}
	}
	b := ui.Row(c).Size(32, 32).Radius(16).AlignItems(ui.Center).Justify(ui.Center).Label(label).Tooltip(label).Transition(fade)
	if !disabled {
		b.Cursor(ui.CursorPointer).Focusable()
		if ink {
			b.Shadow(0, 4, 12, -2, glow(c, pal, 0.45))
		}
		if b.Hovered() {
			if ink {
				bg = t.AccentHover
			} else {
				bg = pal.selected
			}
		}
	}
	b.Background(bg).Children(func() { ui.Icon(c, ic).Size(15, 15).TextColor(fg) })
	return !disabled && b.Clicked()
}

// dot is a small round status mark.
func dot(c *ui.Context, color ui.Color) {
	ui.Box(c).Size(7, 7).Radius(3.5).Background(color).Shrink(0)
}

// status is a dot and a word: 运行中, 等你确认, 已完成.
func status(c *ui.Context, color ui.Color, text string) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
		dot(c, color)
		ui.Text(c, text).FontSize(12).TextColor(c.Theme().TextMuted)
	})
}

// statusPill is a state as a word on a capsule tinted in its color, with
// its dot, or a spinner while it runs.
func statusPill(c *ui.Context, color ui.Color, text string, spinning bool) {
	ink := color.Mix(c.Theme().Text, 0.25)
	ui.Row(c).Height(24).Padding(0, 10, 0, 8).Gap(6).AlignItems(ui.Center).Radius(12).Background(color.Alpha(0.12)).Shrink(0).Children(func() {
		if spinning {
			ui.Spinner(c).Size(11, 11).TextColor(color)
		} else {
			dot(c, color)
		}
		ui.Text(c, text).FontSize(12).FontWeight(500).TextColor(ink).SingleLine()
	})
}

// sectionLabel is the small title over a group of a side list.
func sectionLabel(c *ui.Context, title string) {
	ui.Text(c, title).FontSize(11.5).FontWeight(600).TextColor(c.Theme().TextMuted).Padding(14, 8, 4)
}

// sectionHead is a side list's group title with an action at its end; it
// reports a click on the action.
func sectionHead(c *ui.Context, pal *palette, title string, ic *ui.SVG, action string) bool {
	clicked := false
	ui.Row(c).Height(30).Padding(12, 4, 0, 8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(11.5).FontWeight(600).TextColor(c.Theme().TextMuted).Grow(1)
		if action != "" {
			clicked = smallIconButton(c, pal, ic, action).Clicked()
		}
	})
	return clicked
}

// archTile is a workspace's initial in an arch, the alcove's own shape.
func archTile(c *ui.Context, pal *palette, text string, size float32, bg ui.Color) {
	r := size / 2
	ui.Column(c).Size(size, size).Radius(r, r, size*0.2, size*0.2).Background(bg).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
		ui.Text(c, text).FontSize(size*0.52).FontWeight(600).Font(pal.display).Padding(size*0.1, 0, 0)
	})
}

// display is text in the serif of titles.
func display(c *ui.Context, pal *palette, text string, size float32) ui.Element {
	return ui.Text(c, text).Font(pal.display).FontSize(size).FontWeight(600).LetterSpacing(0.3)
}

// emptyState stands in for a page or list with nothing to show: an icon in
// an arch of its color, a title in the serif, a line on what will appear,
// and an optional action.
func emptyState(c *ui.Context, ic *ui.SVG, color ui.Color, title, body string, action func()) {
	t := c.Theme()
	ui.Column(c).Padding(56, 24).AlignItems(ui.Center).Gap(10).Children(func() {
		ui.Column(c).Size(56, 64).Radius(28, 28, 12, 12).Background(color.Alpha(0.1)).Border(1, color.Alpha(0.18)).AlignItems(ui.Center).Justify(ui.Center).Margin(0, 0, 8).Children(func() {
			ui.Icon(c, ic).Size(24, 24).TextColor(color).Margin(6, 0, 0)
		})
		ui.Text(c, title).Font(displayFont).FontSize(19).FontWeight(600).LetterSpacing(0.3)
		ui.Text(c, body).FontSize(13).TextColor(t.TextMuted).MaxWidth(400).TextAlign(ui.Center).LineHeight(1.5)
		if action != nil {
			ui.Row(c).Padding(10, 0, 0).Children(action)
		}
	})
}

// scrollFading is a page's scrolling body, fading out at its foot into
// what is pinned under it (the slips, the composer); track, if any, keeps
// where it is scrolled.
func scrollFading(c *ui.Context, pal *palette, track *ui.ScrollState, body func()) {
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		sc := ui.Scroll(c).Grow(1)
		if track != nil {
			sc.TrackScroll(track)
		}
		sc.Children(body)
		ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(24).IgnoreInput(true).Draw(func(p *ui.Painter, r ui.Rect) {
			p.FillGradient(r, ui.LinearGradient{Angle: 180, From: pal.content.Alpha(0), To: pal.content}, 0)
		})
	})
}

// pageColumn is a page's scrolling body on the sheet: a column of reading
// width in the middle, under the page's title in the serif and a line on
// what the page is for.
func pageColumn(c *ui.Context, pal *palette, title, about string, body func()) {
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(30, 40, 48).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(720).MaxWidthPercent(100).Gap(16).Children(func() {
				ui.Column(c).Gap(6).Padding(0, 2, 6).Children(func() {
					display(c, pal, title, 28)
					if about != "" {
						ui.Text(c, about).FontSize(13).LineHeight(1.5).TextColor(c.Theme().TextMuted)
					}
				})
				body()
			})
		})
	})
}

// paperCard is a box of the sheet: paper on paper, edged by a hairline.
func paperCard(c *ui.Context, pal *palette) ui.Element {
	return ui.Column(c).Radius(12).Border(1, pal.hairline).Background(pal.content)
}

// cardHead is a card's title row: an icon, the title and what follows it.
func cardHead(c *ui.Context, pal *palette, ic *ui.SVG, title string, trailing func()) {
	t := c.Theme()
	ui.Row(c).Height(44).Padding(0, 10, 0, 14).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
		ui.Icon(c, ic).Size(14, 14).TextColor(t.TextMuted)
		ui.Text(c, title).FontSize(13).FontWeight(600).Grow(1).SingleLine()
		if trailing != nil {
			trailing()
		}
	})
}

// primaryButton is the theme's primary button while it can be used, and a
// plain one while it cannot: a dimmed ink face reads as a muddy gray.
func primaryButton(c *ui.Context, label string, enabled bool) bool {
	if !enabled {
		ui.Button(c, label).Disabled(true)
		return false
	}
	return ui.PrimaryButton(c, label).Clicked()
}

// tag is a quiet word beside a name, such as 组织.
func tag(c *ui.Context, pal *palette, text string) {
	ui.Text(c, text).FontSize(10.5).FontWeight(500).TextColor(c.Theme().TextMuted).Background(pal.selected).Padding(1, 6).Radius(5).Shrink(0).SingleLine()
}
