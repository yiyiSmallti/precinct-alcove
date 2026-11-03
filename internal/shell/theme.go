package shell

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

// palette is the window's look beyond what ui.Theme names. The default
// look is quiet, like a native Mac app: clean neutrals, hairlines instead
// of boxes, one accent for primary actions, and color kept for state
// (running, waiting on you, done, failed). Settings → 外观 changes the
// light or dark mode, the neutrals' tone, the accent, the fonts and the
// window's zoom.
type palette struct {
	content  ui.Color // the middle column's ground
	sidebar  ui.Color // the side columns' ground where there is no vibrancy
	selected ui.Color // the chosen row in a list
	hover    ui.Color // a row under the pointer
	userMsg  ui.Color // the user's message bubble
	code     ui.Color // tool details
	hairline ui.Color // dividers between columns and rows
	subtle   ui.Color // the faintest text: times, counts
	running  ui.Color // a running task
	waiting  ui.Color // a task waiting on the user
	done     ui.Color // a finished task
	shadow   ui.Color // under floating cards and the composer
	avatar   ui.Color // the workspace's letter tile

	mono    string // the code font
	vibrant bool   // the side columns show the window's material
}

// tone is one set of neutrals, for light and for dark, and the accent it
// comes with until the user picks another.
type tone struct {
	id, name    string
	light, dark neutrals
	accent      string
}

type neutrals struct {
	bg, surface, hover, pressed, border, text, muted, sidebar, subtle, code, avatar, userMsg string
}

// The tones differ most where the eye rests: the sidebar, the user's
// bubbles, controls and the muted text; the middle column stays near white
// (or near black) so reading is easy. The first is the default.
var tones = []tone{
	{"zinc", "雪白",
		neutrals{"#ffffff", "#f4f4f5", "#ececee", "#e4e4e7", "#e4e4e7", "#18181b", "#71717a", "#f8f8f9", "#a1a1aa", "#f8f8f9", "#ececef", "#f4f4f5"},
		neutrals{"#131316", "#1f1f23", "#27272a", "#2f2f33", "#2a2a2e", "#f4f4f5", "#a1a1aa", "#0e0e10", "#71717a", "#18181b", "#2a2a2e", "#232327"}, "indigo"},
	{"slate", "石板",
		neutrals{"#ffffff", "#f1f5f9", "#e9eef4", "#e2e8f0", "#e2e8f0", "#0f172a", "#64748b", "#f6f8fb", "#94a3b8", "#f8fafc", "#e2e8f0", "#eef3fa"},
		neutrals{"#0d1424", "#172136", "#1d2940", "#24314b", "#1f2a3f", "#e2e8f0", "#94a3b8", "#090f1c", "#64748b", "#111a2c", "#1f2a3f", "#18243c"}, "blue"},
	{"linen", "米白",
		neutrals{"#fdfcfa", "#f2efea", "#ebe7e0", "#e3ded5", "#e7e2da", "#1f1d1a", "#706a62", "#f6f4f0", "#a59f96", "#f7f5f1", "#ece6dc", "#f3eee7"},
		neutrals{"#1a1816", "#272421", "#302c28", "#38342f", "#33302b", "#efebe5", "#a59e94", "#141210", "#7a746b", "#211f1c", "#36312b", "#2b2723"}, "terracotta"},
	{"warm", "暖灰",
		neutrals{"#fffdfa", "#f1ece3", "#e9e3d8", "#dfd8cb", "#e5ded2", "#26221d", "#7a7064", "#f3eee5", "#a69d90", "#f8f5ef", "#e8e0d2", "#f1ebe1"},
		neutrals{"#1e1b17", "#2d2923", "#36312a", "#3f3931", "#3a352e", "#ede8e0", "#a39a8d", "#27231e", "#7b7368", "#25221d", "#3d372f", "#2f2a24"}, "graphite"},
	{"neutral", "中性",
		neutrals{"#ffffff", "#f2f2f2", "#e9e9e9", "#dedede", "#e2e2e2", "#1c1c1c", "#6f6f6f", "#f4f4f4", "#a3a3a3", "#f7f7f7", "#e6e6e6", "#f0f0f0"},
		neutrals{"#1b1b1b", "#2a2a2a", "#333333", "#3c3c3c", "#373737", "#ececec", "#9c9c9c", "#222222", "#767676", "#232323", "#383838", "#2b2b2b"}, "graphite"},
	{"cool", "冷灰",
		neutrals{"#fbfcfe", "#e9eef6", "#dfe6f0", "#d3dce9", "#d9e1ec", "#18202e", "#5d6a80", "#edf1f7", "#93a0b4", "#f3f6fa", "#dce4f0", "#e6edf7"},
		neutrals{"#141922", "#212836", "#29313f", "#323b4b", "#2e3646", "#e4e9f2", "#93a0b6", "#1a202b", "#6c788c", "#1b212c", "#323b4c", "#232b39"}, "graphite"},
}

// accent is a preset accent color, for light and for dark.
type accent struct {
	id, name    string
	light, dark string // "" is the tone's ink
}

var accents = []accent{
	{"graphite", "石墨", "", ""},
	{"indigo", "靛蓝", "#4f46e5", "#818cf8"},
	{"blue", "海蓝", "#2563eb", "#60a5fa"},
	{"violet", "紫藤", "#6e56cf", "#9d8bf0"},
	{"teal", "青碧", "#0f8a83", "#3cc0b5"},
	{"green", "松绿", "#2f8a57", "#55b97e"},
	{"amber", "琥珀", "#c9620c", "#f0913a"},
	{"rose", "玫红", "#c2416b", "#ef6f96"},
	{"terracotta", "赤陶", "#c4532d", "#e07a55"},
}

func toneOf(id string) tone {
	for _, t := range tones {
		if t.id == id {
			return t
		}
	}
	return tones[0]
}

// accentID is the preset accent an appearance shows: the chosen one, or
// the tone's own.
func accentID(ap app.Appearance) string {
	if ap.Accent != "" {
		return ap.Accent
	}
	return toneOf(ap.Tone).accent
}

// accentColor is the accent of the appearance in light or dark, and
// whether it is the tone's ink (graphite).
func accentColor(ap app.Appearance, dark bool) (ui.Color, bool) {
	if c, ok := parseHex(ap.Accent); ok {
		if dark {
			c = c.Mix(ui.RGB(255, 255, 255), 0.18)
		}
		return c, false
	}
	id := accentID(ap)
	for _, a := range accents {
		if a.id == id && a.light != "" {
			if dark {
				return ui.Hex(a.dark), false
			}
			return ui.Hex(a.light), false
		}
	}
	n := toneOf(ap.Tone).light
	if dark {
		n = toneOf(ap.Tone).dark
	}
	return ui.Hex(n.text), true
}

// parseHex reads a "#rrggbb" color.
func parseHex(s string) (ui.Color, bool) {
	var r, g, b uint8
	if len(s) != 7 || s[0] != '#' {
		return ui.Color{}, false
	}
	if _, err := fmt.Sscanf(strings.ToLower(s[1:]), "%02x%02x%02x", &r, &g, &b); err != nil {
		return ui.Color{}, false
	}
	return ui.RGB(r, g, b), true
}

func hexOf(c ui.Color) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// onAccent is the text color that reads on an accent.
func onAccent(c ui.Color) ui.Color {
	l := 0.2126*float32(c.R) + 0.7152*float32(c.G) + 0.0722*float32(c.B)
	if l > 160 {
		return ui.Hex("#141414")
	}
	return ui.RGB(255, 255, 255)
}

type look struct {
	theme *ui.Theme
	pal   palette
}

var looks sync.Map // look key → *look

// lookFor builds (once) the theme and palette of an appearance in light
// or dark.
func lookFor(ap app.Appearance, dark bool) *look {
	key := fmt.Sprintf("%s|%s|%s|%s|%v", ap.Tone, ap.Accent, ap.Font, ap.CodeFont, dark)
	if l, ok := looks.Load(key); ok {
		return l.(*look)
	}
	n := toneOf(ap.Tone).light
	t := ui.LightTheme()
	if dark {
		n = toneOf(ap.Tone).dark
		t = ui.DarkTheme()
	}
	hx := ui.Hex
	t.Background, t.Surface, t.SurfaceHover, t.SurfacePressed = hx(n.bg), hx(n.surface), hx(n.hover), hx(n.pressed)
	t.Border, t.Text, t.TextMuted = hx(n.border), hx(n.text), hx(n.muted)
	acc, ink := accentColor(ap, dark)
	t.Accent, t.AccentText = acc, onAccent(acc)
	if dark {
		t.AccentHover, t.AccentPressed = acc.Mix(ui.RGB(255, 255, 255), 0.12), acc.Mix(ui.RGB(0, 0, 0), 0.1)
		t.Warning, t.Success, t.Danger = hx("#e5a54b"), hx("#5fbf86"), hx("#f17a72")
	} else {
		t.AccentHover, t.AccentPressed = acc.Mix(ui.RGB(255, 255, 255), 0.14), acc.Mix(ui.RGB(0, 0, 0), 0.15)
		t.Warning, t.Success, t.Danger = hx("#b5680f"), hx("#2f8a57"), hx("#c4433b")
	}
	focus := acc
	if ink {
		focus = hx("#3874ff")
	}
	t.Selection, t.Focus = focus.Alpha(0.25), focus.Alpha(0.5)
	t.Radius, t.FontSize, t.Font = 8, 13.5, ap.Font

	text := hx(n.text)
	p := palette{
		content: hx(n.bg), sidebar: hx(n.sidebar),
		selected: text.Alpha(0.07), hover: text.Alpha(0.04),
		userMsg: hx(n.userMsg), code: hx(n.code), hairline: text.Alpha(0.08),
		subtle: hx(n.subtle), avatar: hx(n.avatar),
		running: hx("#2f6fe4"), waiting: hx("#c47812"), done: hx("#2f8a57"),
		shadow: text.Alpha(0.07), mono: ap.CodeFont,
	}
	if dark {
		p.selected, p.hover, p.hairline = text.Alpha(0.09), text.Alpha(0.05), text.Alpha(0.08)
		p.running, p.waiting, p.done, p.shadow = hx("#6ea2ff"), hx("#e5a54b"), hx("#5fbf86"), ui.RGBA(0, 0, 0, 0.35)
	}
	if !ink {
		// A colored accent tints what is chosen and what runs.
		p.selected, p.running = acc.Alpha(0.14), acc
		p.userMsg = acc.Alpha(0.1).Over(hx(n.bg))
		p.avatar = acc.Alpha(0.18).Over(hx(n.bg))
	}
	if p.mono == "" {
		p.mono = "monospace"
	}
	l, _ := looks.LoadOrStore(key, &look{theme: t, pal: p})
	return l.(*look)
}

// isDark reports whether an appearance shows dark, given the system's.
func isDark(ap app.Appearance, system bool) bool {
	switch ap.Mode {
	case "light":
		return false
	case "dark":
		return true
	}
	return system
}

// useTheme switches the frame to the user's look and returns its palette.
// Over a window material (macOS vibrancy) the root stays clear, so the
// sidebar shows it, while the look's mode is the system's (the material
// follows the system).
func useTheme(c *ui.Context, ap app.Appearance) *palette {
	system := c.Theme().Dark
	dark := isDark(ap, system)
	l := lookFor(ap, dark)
	c.SetTheme(l.theme)
	z := ap.Zoom
	if z == 0 {
		z = 1
	}
	if c.Zoom() != z {
		c.SetZoom(z)
	}
	pal := l.pal
	pal.vibrant = c.Vibrancy() && dark == system
	if pal.vibrant {
		c.Root().Background(ui.Transparent)
	}
	return &pal
}

func icon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

// Icons, drawn in the text color.
var (
	icPlus     = icon(`<path d="M12 5v14M5 12h14"/>`)
	icFolder   = icon(`<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`)
	icChevron  = icon(`<path d="m7 10 5 5 5-5"/>`)
	icGear     = icon(`<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>`)
	icPuzzle   = icon(`<path d="M10 4a2 2 0 1 1 4 0v1h3a1 1 0 0 1 1 1v3h1a2 2 0 1 1 0 4h-1v4a1 1 0 0 1-1 1h-4v-1a2 2 0 1 0-4 0v1H5a1 1 0 0 1-1-1v-4h1a2 2 0 1 0 0-4H4V6a1 1 0 0 1 1-1h5z"/>`)
	icStop     = icon(`<rect x="7" y="7" width="10" height="10" rx="1.5" fill="currentColor" stroke="none"/>`)
	icSend     = icon(`<path d="M12 18V6M7 11l5-5 5 5"/>`)
	icCheck    = icon(`<path d="m5 12 4.5 4.5L19 7"/>`)
	icX        = icon(`<path d="M6 6l12 12M18 6 6 18"/>`)
	icCloud    = icon(`<path d="M7 18a4.5 4.5 0 0 1-.6-9A6 6 0 0 1 18 8.5a4.5 4.5 0 0 1-.5 9.5z"/>`)
	icChat     = icon(`<path d="M5 18.5V6a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H8.5z"/>`)
	icMore     = icon(`<circle cx="6" cy="12" r="1" fill="currentColor"/><circle cx="12" cy="12" r="1" fill="currentColor"/><circle cx="18" cy="12" r="1" fill="currentColor"/>`)
	icInbox    = icon(`<path d="M4 13h4l1.5 2.5h5L16 13h4"/><path d="M5.5 6.5 4 13v5a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-5l-1.5-6.5a1 1 0 0 0-1-.8H6.5a1 1 0 0 0-1 .8z"/>`)
	icUpDown   = icon(`<path d="m8 9 4-4 4 4M8 15l4 4 4-4"/>`)
	icFile     = icon(`<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>`)
	icPanel    = icon(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M15 4v16"/>`)
	icTerminal = icon(`<path d="m5 8 4 4-4 4M12 17h7"/>`)
	icAlert    = icon(`<path d="M12 4 2.5 20h19z"/><path d="M12 10v4M12 17h.01"/>`)
)
