package shell

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

// palette is the window's look beyond what ui.Theme names. The window is a
// desk by a window: the theme's painting fills it, the sidebar is glass
// over the painting, and the page is a sheet of paper laid on it, opaque so
// that reading is easy. Color is kept for state (running, waiting on you,
// done, failed) and one accent for primary actions. Settings → 外观 changes
// the light or dark mode, the neutrals' tone, the accent, the fonts and the
// window's zoom.
type palette struct {
	content  ui.Color // the sheet's paper
	canvas   ui.Color // the window's ground where there is no painting
	sidebar  ui.Color // the sidebar's ground where there is no glass
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
	rim      ui.Color // the lit edge of glass and of the alcove's arch
	term     ui.Color // a command's output, as a terminal shows it
	termText ui.Color
	link     ui.Color // links in replies: the accent, or ink blue when the accent is the ink

	// inkAccent is an accent that is the tone's ink (石墨): black on paper,
	// white in the dark. What the accent would light (a link, the agent at
	// work in the browser, a glow under a button) takes another color then.
	inkAccent bool
	md        *markdown.Options // how replies render: their links in link

	mono    string     // the code font
	display string     // the serif of titles and the wordmark
	vibrant bool       // the side columns show the window's material
	wall    *wallpaper // the theme's painting behind the window, or nil
	win     ui.Rect    // where the painting lies, in window coordinates
}

// displayFont is the serif of page titles, the new-task question, empty
// states and the wordmark: Latin from Georgia, Chinese from Songti.
const displayFont = "Georgia, Songti SC, STSong, Source Han Serif SC, Noto Serif CJK SC, Noto Serif SC, SimSun, serif"

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
	// Paper and ink, the default: white paper, black ink, and the grays
	// between them with the least chill of zinc, so they read as black and
	// white rather than blue. Its accent is the ink itself, so color is
	// left to what means something: running, waiting on you, done, failed,
	// and links. By night the paper is ink-dark on a near-black desk.
	{"zinc", "纸墨",
		neutrals{"#ffffff", "#f4f4f5", "#ececee", "#e4e4e7", "#e4e4e7", "#18181b", "#6b6b74", "#f3f3f4", "#9d9da6", "#f6f6f7", "#ebebed", "#f0f0f2"},
		neutrals{"#161618", "#1f1f22", "#27272a", "#2f2f33", "#2a2a2e", "#ececee", "#a1a1aa", "#0c0c0d", "#77777f", "#111113", "#28282c", "#232326"}, "graphite"},
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
	{"forest", "青苔",
		neutrals{"#fcfcf8", "#eef0e7", "#e6e9de", "#dce0d3", "#e0e4d8", "#1b231d", "#636d5f", "#f3f5ee", "#9aa292", "#f6f7f1", "#e2e8da", "#eef1e6"},
		neutrals{"#121915", "#1d2620", "#252f29", "#2d3831", "#28332c", "#e8ede5", "#99a596", "#0d130f", "#6c786a", "#171f1a", "#2a362e", "#1f2a23"}, "burgundy"},
}

// accent is a preset accent color, for light and for dark.
type accent struct {
	id, name    string
	light, dark string // "" is the tone's ink
}

var accents = []accent{
	{"graphite", "石墨", "", ""},
	{"ink", "墨蓝", "#2c4fc0", "#8ea6ff"},
	{"indigo", "靛蓝", "#4f46e5", "#818cf8"},
	{"blue", "海蓝", "#2563eb", "#60a5fa"},
	{"violet", "紫藤", "#6e56cf", "#9d8bf0"},
	{"teal", "青碧", "#0f8a83", "#3cc0b5"},
	{"green", "松绿", "#2f8a57", "#55b97e"},
	{"amber", "琥珀", "#c9620c", "#f0913a"},
	{"rose", "玫红", "#c2416b", "#ef6f96"},
	{"terracotta", "赤陶", "#c4532d", "#e07a55"},
	{"cyan", "海青", "#0e7490", "#38c9e6"},
	{"burgundy", "酒红", "#9b2c45", "#e0758c"},
	{"lime", "荧绿", "#4f7a12", "#a6e23c"},
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
	// State colors: 藤黄 for what waits on the user, pine for done,
	// vermilion for failures.
	white, black := ui.RGB(255, 255, 255), ui.RGB(0, 0, 0)
	if dark {
		t.AccentHover, t.AccentPressed = acc.Mix(white, 0.12), acc.Mix(black, 0.1)
		t.Warning, t.Success, t.Danger = hx("#e8a94e"), hx("#62c08a"), hx("#f2806f")
	} else {
		t.AccentHover, t.AccentPressed = acc.Mix(white, 0.14), acc.Mix(black, 0.15)
		t.Warning, t.Success, t.Danger = hx("#b86e00"), hx("#2e7d55"), hx("#c4382b")
	}
	// Ink can go no blacker, nor white whiter: a black button lightens
	// under the pointer and a white one dims, and pressing comes back
	// part of the way.
	switch {
	case ink && dark:
		t.AccentHover, t.AccentPressed = acc.Mix(black, 0.08), acc.Mix(black, 0.16)
	case ink:
		t.AccentHover, t.AccentPressed = acc.Mix(white, 0.14), acc.Mix(white, 0.06)
	}
	t.Radius, t.FontSize, t.Font = 8, 13.5, ap.Font

	// Shadows are the tone's ink, faint, and a command's output is set in
	// a terminal of the same ink: warm under 米白, black under 纸墨.
	text := hx(n.text)
	tone := toneOf(ap.Tone)
	p := palette{
		content: hx(n.bg), canvas: hx(n.sidebar), sidebar: hx(n.sidebar),
		selected: text.Alpha(0.075), hover: text.Alpha(0.045),
		userMsg: hx(n.userMsg), code: hx(n.code), hairline: text.Alpha(0.09),
		subtle: hx(n.subtle), avatar: hx(n.avatar),
		running: hx("#2f6fe4"), waiting: t.Warning, done: t.Success,
		shadow: text.Alpha(0.09), rim: ui.RGBA(255, 255, 255, 0.55),
		term: hx(tone.light.text).Mix(ui.RGB(255, 255, 255), 0.04), termText: hx(tone.dark.text),
		link: acc, inkAccent: ink,
		mono: ap.CodeFont, display: displayFont,
	}
	if dark {
		p.selected, p.hover, p.hairline = text.Alpha(0.09), text.Alpha(0.05), text.Alpha(0.1)
		p.running, p.shadow, p.rim = hx("#6ea2ff"), ui.RGBA(0, 0, 0, 0.42), ui.RGBA(255, 255, 255, 0.14)
		p.term = hx(n.bg).Mix(ui.RGB(0, 0, 0), 0.38)
	}
	if ink {
		// Black (or white) links would be lost among the words: they take
		// 墨蓝, and the focus ring the blue of what runs.
		for _, a := range accents {
			if a.id == "ink" {
				p.link = hx(a.light)
				if dark {
					p.link = hx(a.dark)
				}
			}
		}
	} else {
		// A colored accent tints what is chosen and what runs.
		p.selected, p.running = acc.Alpha(0.13), acc
		p.userMsg = acc.Alpha(0.09).Over(hx(n.bg))
		p.avatar = acc.Alpha(0.16).Over(hx(n.bg))
	}
	focus := p.running
	t.Selection, t.Focus = focus.Alpha(0.25), focus.Alpha(0.5)
	p.md = &markdown.Options{Theme: &markdown.Theme{Link: p.link}}
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
func useTheme(c *ui.Context, ap app.Appearance, custom string) *palette {
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
	// A painting takes the place of the window's material.
	pal.wall = wallpaperFor(ap, dark, custom)
	pal.vibrant = c.Vibrancy() && dark == system && pal.wall == nil
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
	icGlobe    = icon(`<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>`)
	icBack     = icon(`<path d="m15 18-6-6 6-6"/>`)
	icForward  = icon(`<path d="m9 18 6-6-6-6"/>`)
	icReload   = icon(`<path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/>`)
	icHide     = icon(`<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M15 4v16M8 10l2 2-2 2"/>`)
	icClose    = icon(`<path d="M6 6l12 12M18 6 6 18"/>`)
	icAlert    = icon(`<path d="M12 4 2.5 20h19z"/><path d="M12 10v4M12 17h.01"/>`)

	// The kinds of a task's steps, on the thread of its work.
	icRead    = icon(`<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h4"/>`)
	icPencil  = icon(`<path d="M4 20h4L19 9a2.8 2.8 0 0 0-4-4L4 16z"/><path d="m13.5 6.5 4 4"/>`)
	icNewFile = icon(`<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M12 11v6M9 14h6"/>`)
	icSearch  = icon(`<circle cx="11" cy="11" r="6.5"/><path d="m20 20-4.2-4.2"/>`)
	icAgent   = icon(`<circle cx="12" cy="5.5" r="2.5"/><circle cx="5.5" cy="18.5" r="2.5"/><circle cx="18.5" cy="18.5" r="2.5"/><path d="M12 8v3.5M12 11.5 6.8 16.3M12 11.5l5.2 4.8"/>`)
	icPointer = icon(`<path d="m5 3 14 7-6 2-2 6z"/>`)
	icBulb    = icon(`<path d="M9 18h6M10 21h4"/><path d="M12 3a6 6 0 0 0-3.6 10.8c.7.6 1.1 1.3 1.1 2.2h5c0-.9.4-1.6 1.1-2.2A6 6 0 0 0 12 3z"/>`)
	icFold    = icon(`<path d="M4 12h16M9 4l3 3 3-3M9 20l3-3 3 3"/>`)

	// The window's own.
	icCompose = icon(`<path d="M11 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-5"/><path d="M18.4 2.6a2 2 0 0 1 3 3L12 15l-4 1 1-4z"/>`)
	icArch    = icon(`<path d="M5 21V11a7 7 0 0 1 14 0v10"/><circle cx="12" cy="12" r="2.25"/><path d="M8.5 21a3.5 3.5 0 0 1 7 0"/>`)
	icOpen    = icon(`<path d="M7 17 17 7M9 7h8v8"/>`)
	icTrash   = icon(`<path d="M4 7h16M10 11v6M14 11v6M6 7l1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12M9 7V4h6v3"/>`)
	icHome    = icon(`<path d="M4 11 12 4l8 7v8a1 1 0 0 1-1 1h-4v-6h-6v6H5a1 1 0 0 1-1-1z"/>`)
	icSparkle = icon(`<path d="M12 3c.6 4.4 2.6 6.4 7 7-4.4.6-6.4 2.6-7 7-.6-4.4-2.6-6.4-7-7 4.4-.6 6.4-2.6 7-7z"/>`)
)

// stepIcon is the mark of a tool call on the thread of a task's work.
func stepIcon(tool string) *ui.SVG {
	switch toolVerb(tool) {
	case "运行命令":
		return icTerminal
	case "读取文件":
		return icRead
	case "写入文件":
		return icNewFile
	case "修改文件":
		return icPencil
	case "查看目录":
		return icFolder
	case "搜索":
		return icSearch
	case "子代理":
		return icAgent
	case "点击", "输入", "按键", "滚动页面":
		return icPointer
	}
	if strings.HasPrefix(strings.ToLower(tool), "browser_") || toolVerb(tool) == "访问网页" {
		return icGlobe
	}
	return icSparkle
}
