package shell

import (
	"github.com/egoist/mygo/ui"
)

// Small pieces shared by the workbench's columns.

// sideGround paints a side column: the window's material where there is
// one (macOS), else the sidebar color.
func sideGround(c *ui.Context, pal *palette, col ui.Element) ui.Element {
	if c.Vibrancy() {
		return col.Background(ui.Transparent)
	}
	return col.Background(pal.sidebar)
}

// listRow is a clickable row of a side list, highlighted when chosen or
// under the pointer; it reports a click.
func listRow(c *ui.Context, pal *palette, key, label string, sel bool, fn func()) bool {
	r := ui.Row(c).Key(key).Height(30).Padding(0, 8).Gap(8).AlignItems(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(label)
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

// iconButton is a quiet square button showing an icon.
func iconButton(c *ui.Context, pal *palette, ic *ui.SVG, label string, on bool) ui.Element {
	t := c.Theme()
	b := ui.Row(c).Size(28, 28).AlignItems(ui.Center).Justify(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(label).Tooltip(label)
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
	b := ui.Row(c).Size(30, 30).Radius(15).AlignItems(ui.Center).Justify(ui.Center).Label(label).Tooltip(label)
	if !disabled {
		b.Cursor(ui.CursorPointer).Focusable()
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

// tag is a quiet word beside a name, such as 组织.
func tag(c *ui.Context, pal *palette, text string) {
	ui.Text(c, text).FontSize(10.5).FontWeight(500).TextColor(c.Theme().TextMuted).Background(pal.selected).Padding(1, 6).Radius(4).Shrink(0).SingleLine()
}
