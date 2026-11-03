package shell

import (
	"github.com/egoist/mygo/ui"
)

// palette is the window's look beyond what ui.Theme names. The look is
// quiet and monochrome, like a native Mac app: warm neutrals, hairlines
// instead of boxes, ink-colored primary actions, and color kept for state
// (running, waiting on you, done, failed).
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
}

var (
	lightTheme, darkTheme     = workbenchTheme(false), workbenchTheme(true)
	lightPalette, darkPalette = workbenchPalette(false), workbenchPalette(true)
)

func workbenchTheme(dark bool) *ui.Theme {
	if dark {
		t := ui.DarkTheme()
		t.Background = ui.Hex("#1c1c1e")
		t.Surface = ui.Hex("#2a2a2d")
		t.SurfaceHover = ui.Hex("#323236")
		t.SurfacePressed = ui.Hex("#3a3a3f")
		t.Border = ui.Hex("#38383c")
		t.Text = ui.Hex("#ececed")
		t.TextMuted = ui.Hex("#9b9ba1")
		t.Accent = ui.Hex("#ececed")
		t.AccentHover = ui.Hex("#ffffff")
		t.AccentPressed = ui.Hex("#d4d4d6")
		t.AccentText = ui.Hex("#1c1c1e")
		t.Warning = ui.Hex("#e5a54b")
		t.Success = ui.Hex("#5fbf86")
		t.Danger = ui.Hex("#f17a72")
		t.Selection = ui.RGBA(100, 150, 255, 0.35)
		t.Focus = ui.RGBA(120, 160, 255, 0.55)
		t.Radius = 8
		t.FontSize = 13.5
		return t
	}
	t := ui.LightTheme()
	t.Background = ui.Hex("#ffffff")
	t.Surface = ui.Hex("#f3f2ef")
	t.SurfaceHover = ui.Hex("#ebeae6")
	t.SurfacePressed = ui.Hex("#e2e1dc")
	t.Border = ui.Hex("#e4e3df")
	t.Text = ui.Hex("#1f1f1d")
	t.TextMuted = ui.Hex("#73726d")
	t.Accent = ui.Hex("#1f1f1d")
	t.AccentHover = ui.Hex("#3a3a37")
	t.AccentPressed = ui.Hex("#000000")
	t.AccentText = ui.Hex("#ffffff")
	t.Warning = ui.Hex("#b5680f")
	t.Success = ui.Hex("#2f8a57")
	t.Danger = ui.Hex("#c4433b")
	t.Selection = ui.RGBA(56, 116, 255, 0.2)
	t.Focus = ui.RGBA(56, 116, 255, 0.45)
	t.Radius = 8
	t.FontSize = 13.5
	return t
}

func workbenchPalette(dark bool) *palette {
	if dark {
		return &palette{
			content:  ui.Hex("#1c1c1e"),
			sidebar:  ui.Hex("#232325"),
			selected: ui.RGBA(255, 255, 255, 0.09),
			hover:    ui.RGBA(255, 255, 255, 0.05),
			userMsg:  ui.Hex("#2c2c2f"),
			code:     ui.Hex("#232326"),
			hairline: ui.RGBA(255, 255, 255, 0.08),
			subtle:   ui.Hex("#76767c"),
			running:  ui.Hex("#6ea2ff"),
			waiting:  ui.Hex("#e5a54b"),
			done:     ui.Hex("#5fbf86"),
			shadow:   ui.RGBA(0, 0, 0, 0.35),
			avatar:   ui.Hex("#3a3a3f"),
		}
	}
	return &palette{
		content:  ui.Hex("#ffffff"),
		sidebar:  ui.Hex("#f6f5f2"),
		selected: ui.RGBA(31, 31, 29, 0.07),
		hover:    ui.RGBA(31, 31, 29, 0.04),
		userMsg:  ui.Hex("#f3f2ef"),
		code:     ui.Hex("#f8f7f5"),
		hairline: ui.RGBA(31, 31, 29, 0.08),
		subtle:   ui.Hex("#a3a29c"),
		running:  ui.Hex("#2f6fe4"),
		waiting:  ui.Hex("#c47812"),
		done:     ui.Hex("#2f8a57"),
		shadow:   ui.RGBA(31, 31, 29, 0.07),
		avatar:   ui.Hex("#e9e7e1"),
	}
}

// useTheme switches the frame to Alcove's theme for the system's appearance
// and returns the matching palette. Over a window material (macOS
// vibrancy) the root stays clear, so the sidebar shows it.
func useTheme(c *ui.Context) *palette {
	pal := lightPalette
	if c.Theme().Dark {
		c.SetTheme(darkTheme)
		pal = darkPalette
	} else {
		c.SetTheme(lightTheme)
	}
	if c.Vibrancy() {
		c.Root().Background(ui.Transparent)
	}
	return pal
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
