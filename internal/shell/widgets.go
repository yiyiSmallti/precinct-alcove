package shell

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// Small pieces shared by the workbench's columns.

// fade eases a control's colors between rest, hover and chosen.
var fade = ui.ElementTransition{Duration: 140 * time.Millisecond, Colors: true}

// sideGround paints a side column: the window's material where there is
// one (macOS), else the sidebar color.
func sideGround(c *ui.Context, pal *palette, col ui.Element) ui.Element {
	if pal.vibrant {
		// The material, tinted toward the tone's sidebar.
		return col.Background(pal.sidebar.Alpha(0.55))
	}
	return col.Background(pal.sidebar)
}

// listRow is a clickable row of a side list, highlighted when chosen or
// under the pointer; it reports a click.
func listRow(c *ui.Context, pal *palette, key, label string, sel bool, fn func()) bool {
	r := ui.Row(c).Key(key).Height(30).Padding(0, 8).Gap(8).AlignItems(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(label).Transition(fade)
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
// unreadBadge is a conversation's unread count in the sidebar; nothing at 0.
func unreadBadge(c *ui.Context, pal *palette, n int) {
	if n <= 0 {
		return
	}
	text := fmt.Sprint(n)
	if n > 99 {
		text = "99+"
	}
	ui.Text(c, text).FontSize(11).Bold().TextColor(c.Theme().AccentText).Background(pal.running).Padding(0, 6).Radius(8).Shrink(0)
}

func navRow(c *ui.Context, pal *palette, key string, ic *ui.SVG, label string, sel bool) bool {
	t := c.Theme()
	return listRow(c, pal, key, label, sel, func() {
		ui.Icon(c, ic).Size(15, 15).TextColor(t.TextMuted)
		ui.Text(c, label).FontSize(13).SingleLine().Grow(1).MinWidth(0)
	})
}

// iconButton is a quiet square button showing an icon.
func iconButton(c *ui.Context, pal *palette, ic *ui.SVG, label string, on bool) ui.Element {
	t := c.Theme()
	b := ui.Row(c).Size(28, 28).AlignItems(ui.Center).Justify(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(label).Tooltip(label).Transition(fade)
	switch {
	case on:
		b.Background(pal.selected)
	case b.Hovered():
		b.Background(pal.hover)
	}
	b.Children(func() { ui.Icon(c, ic).Size(16, 16).TextColor(t.TextMuted) })
	return b
}

// roundButton is the composer's circular send or stop button.
func roundButton(c *ui.Context, pal *palette, ic *ui.SVG, label string, ink, disabled bool) bool {
	t := c.Theme()
	bg, fg := t.Surface, t.Text
	if ink {
		bg, fg = t.Accent, t.AccentText
		if disabled {
			bg, fg = t.Surface, pal.subtle
		}
	}
	b := ui.Row(c).Size(30, 30).Radius(15).AlignItems(ui.Center).Justify(ui.Center).Label(label).Tooltip(label).Transition(fade)
	if !disabled {
		b.Cursor(ui.CursorPointer).Focusable()
		if ink {
			b.Shadow(0, 3, 10, 0, t.Accent.Alpha(0.3))
		}
		if b.Hovered() {
			if ink {
				bg = t.AccentHover
			} else {
				bg = t.SurfaceHover
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

func sectionLabel(c *ui.Context, title string) {
	ui.Text(c, title).FontSize(11).FontWeight(500).TextColor(c.Theme().TextMuted).Padding(14, 8, 4)
}

// emptyState stands in for a page or list with nothing to show: an icon on
// a soft tile of its color, a title, a line on what will appear, and an
// optional action.
func emptyState(c *ui.Context, ic *ui.SVG, color ui.Color, title, body string, action func()) {
	t := c.Theme()
	ui.Column(c).Padding(56, 24).AlignItems(ui.Center).Gap(10).Children(func() {
		ui.Column(c).Size(52, 52).Radius(14).Background(color.Alpha(0.12)).AlignItems(ui.Center).Justify(ui.Center).Margin(0, 0, 6).Children(func() {
			ui.Icon(c, ic).Size(24, 24).TextColor(color)
		})
		ui.Text(c, title).FontSize(15).FontWeight(600)
		ui.Text(c, body).FontSize(13).TextColor(t.TextMuted).MaxWidth(380).TextAlign(ui.Center)
		if action != nil {
			ui.Row(c).Padding(8, 0, 0).Children(action)
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
	ui.Text(c, text).FontSize(10.5).FontWeight(500).TextColor(c.Theme().TextMuted).Background(pal.selected).Padding(1, 6).Radius(4).Shrink(0).SingleLine()
}
