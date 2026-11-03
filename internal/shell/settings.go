package shell

import (
	"sort"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

// The settings page: 外观 (the window's look), 模型 (credentials and the
// model) and 通用 (最近会话's folder, updates and diagnostics).

const (
	sysFont  = "系统默认"
	sysMono  = "默认等宽字体"
	tabLook  = 0
	tabModel = 1
	tabOther = 2
)

// prefsState is the settings page's own state.
type prefsState struct {
	tab              int
	fonts, monos     []string // font families, once listed
	listing          bool
	fontPick         string // the font fields' text
	codePick         string
	fontSet, codeSet string // what the fields were last filled from
	custom           ui.Color
	picking          bool // the custom accent's picker is open
	picked           bool // and a color was picked, not saved yet
}

var zooms = []struct {
	label string
	z     float32
}{{"小", 0.9}, {"标准", 1}, {"大", 1.1}, {"特大", 1.25}}

func (s *Shell) settingsView(c *ui.Context, pal *palette) {
	topBar(c, pal, func() {
		ui.Text(c, "设置").FontSize(14).Bold().Shrink(0)
		ui.Row(c).Gap(2).AlignItems(ui.Center).Padding(0, 0, 0, 16).Children(func() {
			for i, label := range []string{"外观", "模型", "通用"} {
				if tabButton(c, pal, label, s.prefs.tab == i) {
					s.prefs.tab = i
				}
			}
		})
		ui.Spacer(c)
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(28, 40, 40).Gap(16).MaxWidth(720).Children(func() {
			switch s.prefs.tab {
			case tabLook:
				s.appearanceSettings(c, pal)
			case tabModel:
				s.modelSettings(c, pal)
			default:
				s.otherSettings(c, pal)
			}
		})
	})
}

// setAppearance shows a new look at once and saves it.
func (s *Shell) setAppearance(ap app.Appearance) {
	s.settings.Appearance = ap
	s.saveSettings(s.settings)
}

// listFonts asks the system for its font families, once, off the UI
// thread (it can take a while with many fonts).
func (s *Shell) listFonts() {
	if s.prefs.listing || s.prefs.fonts != nil {
		return
	}
	s.prefs.listing = true
	go func() {
		all := ui.FontFamilies()
		fonts, monos := []string{sysFont}, []string{sysMono}
		for _, f := range all {
			if f.Name == "" || f.Name[0] == '.' {
				continue
			}
			fonts = append(fonts, f.Name)
			if f.Monospaced {
				monos = append(monos, f.Name)
			}
		}
		sort.Strings(fonts[1:])
		sort.Strings(monos[1:])
		s.post(func() { s.prefs.fonts, s.prefs.monos, s.prefs.listing = fonts, monos, false })
	}()
}

func (s *Shell) appearanceSettings(c *ui.Context, pal *palette) {
	t := c.Theme()
	ap := s.settings.Appearance
	s.listFonts()

	settingsGroup(c, pal, "主题", func() {
		prefRow(c, pal, "模式", "跟随系统时，Mac 侧栏使用系统半透明材质。", func() {
			ui.Row(c).Gap(12).Children(func() {
				for _, m := range []struct{ id, label string }{{"", "跟随系统"}, {"light", "浅色"}, {"dark", "深色"}} {
					if modeCard(c, pal, ap, m.id, m.label, ap.Mode == m.id) {
						ap.Mode = m.id
						s.setAppearance(ap)
					}
				}
			})
		})
		prefRow(c, pal, "色调", "背景和文字的底色；每种色调带一个默认主题色。", func() {
			ui.Row(c).Gap(8).Wrap().Grow(1).MinWidth(0).Children(func() {
				for _, tn := range tones {
					sel := toneOf(ap.Tone).id == tn.id
					if toneChip(c, pal, tn, t.Dark, sel) {
						ap.Tone = tn.id
						if tn.id == tones[0].id {
							ap.Tone = ""
						}
						s.setAppearance(ap)
					}
				}
			})
		})
		prefRow(c, pal, "主题色", "用在主按钮、选中的任务和运行中的状态上。", func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Grow(1).MinWidth(0).Children(func() {
				for _, a := range accents {
					sel := accentID(ap) == a.id
					col, _ := accentColor(app.Appearance{Tone: ap.Tone, Accent: a.id}, t.Dark)
					if swatch(c, pal, "accent-"+a.id, a.name, col, sel) {
						// The tone's own accent is stored as "", so it
						// follows a later change of tone.
						ap.Accent = a.id
						if a.id == toneOf(ap.Tone).accent {
							ap.Accent = ""
						}
						s.setAppearance(ap)
					}
				}
				ui.Box(c).Size(1, 18).Background(pal.hairline).Margin(0, 2)
				s.customAccent(c, pal, ap)
			})
		})
	})

	settingsGroup(c, pal, "文字", func() {
		prefRow(c, pal, "界面字体", "", func() {
			if ap.Font != s.prefs.fontSet || s.prefs.fontPick == "" {
				s.prefs.fontSet, s.prefs.fontPick = ap.Font, orDefault(ap.Font, sysFont)
			}
			if fontField(c, &s.prefs.fontPick, s.prefs.fonts, "界面字体") {
				ap.Font = fromDefault(s.prefs.fontPick, sysFont)
				s.prefs.fontSet = ap.Font
				s.setAppearance(ap)
			}
		})
		prefRow(c, pal, "代码字体", "工具调用的命令、参数和输出。", func() {
			if ap.CodeFont != s.prefs.codeSet || s.prefs.codePick == "" {
				s.prefs.codeSet, s.prefs.codePick = ap.CodeFont, orDefault(ap.CodeFont, sysMono)
			}
			if fontField(c, &s.prefs.codePick, s.prefs.monos, "代码字体") {
				ap.CodeFont = fromDefault(s.prefs.codePick, sysMono)
				s.prefs.codeSet = ap.CodeFont
				s.setAppearance(ap)
			}
		})
		prefRow(c, pal, "界面大小", "整个窗口的文字和间距一起缩放。", func() {
			cur := 1
			for i, z := range zooms {
				if ap.Zoom == z.z || ap.Zoom == 0 && z.z == 1 {
					cur = i
				}
			}
			labels := make([]string, len(zooms))
			for i, z := range zooms {
				labels[i] = z.label
			}
			if ui.Segmented(c, &cur, labels...).Label("界面大小").Changed() {
				ap.Zoom = zooms[cur].z
				if ap.Zoom == 1 {
					ap.Zoom = 0
				}
				s.setAppearance(ap)
			}
		})
		prefRow(c, pal, "预览", "", func() {
			ui.Column(c).Gap(6).Padding(12, 14).Radius(8).Background(pal.code).Grow(1).Children(func() {
				ui.Text(c, "汇总好了：第三季度收入 41.2 万，比上季度多 8%。")
				ui.Text(c, "pandoc report/Q3.md -o ~/Desktop/Q3.docx").FontSize(12).Font(pal.mono).TextColor(t.TextMuted)
			})
		})
	})

	if ap != (app.Appearance{}) {
		ui.Row(c).Children(func() {
			if ui.Button(c, "恢复默认外观").Clicked() {
				s.prefs.custom = ui.Color{}
				s.setAppearance(app.Appearance{})
			}
		})
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func fromDefault(v, def string) string {
	if v == def {
		return ""
	}
	return v
}

// fontField is a drop-down of font families; it reports a choice.
func fontField(c *ui.Context, pick *string, options []string, label string) bool {
	if options == nil {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "正在读取系统字体…").FontSize(12.5).TextColor(c.Theme().TextMuted)
		})
		return false
	}
	return ui.Select(c, pick, options).Label(label).Width(260).Changed()
}

// settingsGroup is a titled box of setting rows, divided by hairlines.
func settingsGroup(c *ui.Context, pal *palette, title string, rows func()) {
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, title).FontSize(13).Bold().Padding(0, 2)
		// The rows' bottom hairlines would double the box's own at the
		// end: the last one is pushed under the clipped edge.
		ui.Column(c).Radius(10).Border(1, pal.hairline).Background(pal.content).Clip().Children(func() {
			ui.Column(c).Margin(0, 0, -1, 0).Children(rows)
		})
	})
}

// prefRow is one setting: a label and a hint on the left, the control on
// the right.
func prefRow(c *ui.Context, pal *palette, label, hint string, control func()) {
	t := c.Theme()
	ui.Row(c).Padding(14, 16).Gap(24).AlignItems(ui.Start).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
		ui.Column(c).Width(180).Shrink(0).Gap(3).Padding(4, 0, 0).Children(func() {
			ui.Text(c, label).FontSize(13)
			if hint != "" {
				ui.Text(c, hint).FontSize(11.5).TextColor(t.TextMuted)
			}
		})
		ui.Row(c).Grow(1).MinWidth(0).Children(control)
	})
}

// modeCard is a small picture of the window in a mode, to choose it.
func modeCard(c *ui.Context, pal *palette, ap app.Appearance, mode, label string, sel bool) bool {
	t := c.Theme()
	col := ui.Column(c).Key("mode-" + mode).Gap(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Focusable().Label(label)
	col.Children(func() {
		frame := ui.Row(c).Size(112, 70).Radius(8).Clip().BorderWidth(2)
		if sel {
			frame.BorderColor(t.Accent)
		} else if col.Hovered() {
			frame.BorderColor(pal.hairline.Alpha(3))
		} else {
			frame.BorderColor(pal.hairline)
		}
		frame.Children(func() {
			switch mode {
			case "light":
				miniWindow(c, lookFor(ap, false), 108)
			case "dark":
				miniWindow(c, lookFor(ap, true), 108)
			default:
				ui.Row(c).Width(54).Clip().Children(func() { miniWindow(c, lookFor(ap, false), 108) })
				ui.Row(c).Width(54).Clip().Justify(ui.End).Children(func() { miniWindow(c, lookFor(ap, true), 108) })
			}
		})
		txt := ui.Text(c, label).FontSize(12)
		if !sel {
			txt.TextColor(t.TextMuted)
		}
	})
	return col.Clicked()
}

// miniWindow draws a window of the look in w×66: a sidebar with rows, a
// message, and the composer.
func miniWindow(c *ui.Context, l *look, w float32) {
	p, th := &l.pal, l.theme
	ui.Row(c).Size(w, 66).Shrink(0).Background(p.content).Children(func() {
		ui.Column(c).Width(30).Padding(8, 5).Gap(4).Background(p.sidebar).Children(func() {
			ui.Box(c).Height(4).Radius(2).Background(th.TextMuted.Alpha(0.5))
			ui.Box(c).Height(6).Radius(2).Background(p.selected)
			ui.Box(c).Height(4).Radius(2).Background(th.TextMuted.Alpha(0.35))
			ui.Box(c).Height(4).Radius(2).Background(th.TextMuted.Alpha(0.35))
		})
		ui.Column(c).Grow(1).Padding(8, 7).Gap(4).Children(func() {
			ui.Row(c).Justify(ui.End).Children(func() {
				ui.Box(c).Size(36, 7).Radius(3.5).Background(p.userMsg)
			})
			ui.Box(c).Size(52, 4).Radius(2).Background(th.Text.Alpha(0.55))
			ui.Box(c).Size(40, 4).Radius(2).Background(th.Text.Alpha(0.35))
			ui.Spacer(c)
			ui.Row(c).Height(12).Radius(4).Border(1, th.Border).Background(th.Background).Justify(ui.End).AlignItems(ui.Center).Padding(0, 2).Children(func() {
				ui.Box(c).Size(7, 7).Radius(3.5).Background(th.Accent)
			})
		})
	})
}

// toneChip is a choice of neutrals, shown by its colors.
func toneChip(c *ui.Context, pal *palette, tn tone, dark bool, sel bool) bool {
	t := c.Theme()
	n := tn.light
	if dark {
		n = tn.dark
	}
	r := ui.Row(c).Key("tone-"+tn.id).Height(30).Padding(0, 12, 0, 6).Gap(8).AlignItems(ui.Center).Radius(15).Cursor(ui.CursorPointer).Focusable().Label(tn.name).BorderWidth(1)
	switch {
	case sel:
		r.BorderColor(t.Accent).Background(pal.selected)
	case r.Hovered():
		r.BorderColor(pal.hairline).Background(pal.hover)
	default:
		r.BorderColor(pal.hairline)
	}
	r.Children(func() {
		ui.Row(c).Size(20, 20).Radius(10).Clip().Border(1, pal.hairline).Children(func() {
			ui.Box(c).Size(10, 20).Background(ui.Hex(n.surface))
			ui.Box(c).Size(10, 20).Background(ui.Hex(n.muted))
		})
		ui.Text(c, tn.name).FontSize(12.5)
	})
	return r.Clicked()
}

// swatch is a round color to choose, ringed when chosen.
func swatch(c *ui.Context, pal *palette, key, name string, col ui.Color, sel bool) bool {
	ring := ui.Row(c).Key(key).Size(26, 26).Radius(13).AlignItems(ui.Center).Justify(ui.Center).Cursor(ui.CursorPointer).Focusable().Label(name).Tooltip(name).BorderWidth(2)
	if sel {
		ring.BorderColor(col)
	} else if ring.Hovered() {
		ring.BorderColor(pal.hairline)
	} else {
		ring.BorderColor(ui.Transparent)
	}
	ring.Children(func() {
		ui.Box(c).Size(18, 18).Radius(9).Background(col)
	})
	return ring.Clicked()
}

// customAccent is the swatch of the user's own accent: a click opens a
// color picker under it. The window shows the color while it is picked
// and saves it when the picker closes.
func (s *Shell) customAccent(c *ui.Context, pal *palette, ap app.Appearance) {
	t := c.Theme()
	cur, custom := parseHex(ap.Accent)
	if !custom {
		cur = s.prefs.custom
	}
	ring := ui.Row(c).Key("accent-custom").Height(26).Padding(0, 10, 0, 4).Gap(6).AlignItems(ui.Center).Radius(13).Cursor(ui.CursorPointer).Focusable().Label("自定义主题色").BorderWidth(1)
	switch {
	case custom:
		ring.BorderColor(cur).Background(pal.selected)
	case ring.Hovered() || s.prefs.picking:
		ring.BorderColor(pal.hairline).Background(pal.hover)
	default:
		ring.BorderColor(pal.hairline)
	}
	ring.Children(func() {
		dotc := ui.Row(c).Size(18, 18).Radius(9).AlignItems(ui.Center).Justify(ui.Center)
		if cur == (ui.Color{}) {
			dotc.Border(1, t.TextMuted).Children(func() { ui.Icon(c, icPlus).Size(11, 11).TextColor(t.TextMuted) })
		} else {
			dotc.Background(cur).Children(func() {})
		}
		ui.Text(c, "自定义").FontSize(12.5)
	})
	if ring.Clicked() {
		s.prefs.picking = !s.prefs.picking
		if s.prefs.custom == (ui.Color{}) {
			s.prefs.custom = ui.Hex("#2f6fe4")
			if custom {
				s.prefs.custom = cur
			}
		}
	}
	ui.Popover(c, ring, &s.prefs.picking, func() {
		ui.Column(c).Width(288).Padding(14).Children(func() {
			if ui.ColorPicker(c, &s.prefs.custom).Changed() {
				col := s.prefs.custom
				col.A = 255
				s.settings.Appearance.Accent = hexOf(col)
				s.prefs.picked = true
			}
		})
	})
	if !s.prefs.picking && s.prefs.picked {
		s.prefs.picked = false
		s.saveSettings(s.settings)
	}
}
