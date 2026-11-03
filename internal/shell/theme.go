package shell

import (
	"github.com/egoist/mygo/ui"
)

// palette is the window's look beyond what ui.Theme names: the sidebar's
// ground, the soft tints behind user messages, selected rows and cards that
// need the user.
type palette struct {
	sidebar    ui.Color // the task list's ground
	selected   ui.Color // the chosen row in the sidebar
	hover      ui.Color // a row under the pointer
	userMsg    ui.Color // the user's message bubble
	code       ui.Color // tool details, paths
	warnSoft   ui.Color // cards waiting for the user
	accentSoft ui.Color
	okSoft     ui.Color
	running    ui.Color // the dot of a running task
}

var (
	lightTheme, darkTheme     = workbenchTheme(false), workbenchTheme(true)
	lightPalette, darkPalette = workbenchPalette(false), workbenchPalette(true)
)

// workbenchTheme is MyGo's theme in Alcove's colors: cool neutrals and an
// indigo accent, a little more room and rounder corners.
func workbenchTheme(dark bool) *ui.Theme {
	if dark {
		t := ui.DarkTheme()
		t.Background = ui.Hex("#16171b")
		t.Surface = ui.Hex("#23252b")
		t.SurfaceHover = ui.Hex("#2b2e35")
		t.SurfacePressed = ui.Hex("#33363e")
		t.Border = ui.Hex("#2f323a")
		t.Text = ui.Hex("#eceef3")
		t.TextMuted = ui.Hex("#959aa8")
		t.Accent = ui.Hex("#7d87ff")
		t.AccentHover = ui.Hex("#939bff")
		t.AccentPressed = ui.Hex("#6871f0")
		t.AccentText = ui.Hex("#0f1020")
		t.Warning = ui.Hex("#e7a64a")
		t.Success = ui.Hex("#5cc48c")
		t.Danger = ui.Hex("#f07575")
		t.Selection = ui.RGBA(125, 135, 255, 0.35)
		t.Focus = ui.RGBA(125, 135, 255, 0.6)
		t.Radius = 8
		return t
	}
	t := ui.LightTheme()
	t.Background = ui.Hex("#ffffff")
	t.Surface = ui.Hex("#f2f3f6")
	t.SurfaceHover = ui.Hex("#e9ebf0")
	t.SurfacePressed = ui.Hex("#e0e3e9")
	t.Border = ui.Hex("#e2e4ea")
	t.Text = ui.Hex("#17181d")
	t.TextMuted = ui.Hex("#6a6f7d")
	t.Accent = ui.Hex("#4b55d6")
	t.AccentHover = ui.Hex("#3f48c4")
	t.AccentPressed = ui.Hex("#363eaf")
	t.AccentText = ui.Hex("#ffffff")
	t.Warning = ui.Hex("#b86e0b")
	t.Success = ui.Hex("#22894f")
	t.Danger = ui.Hex("#c93636")
	t.Selection = ui.RGBA(75, 85, 214, 0.22)
	t.Focus = ui.RGBA(75, 85, 214, 0.5)
	t.Radius = 8
	return t
}

func workbenchPalette(dark bool) *palette {
	if dark {
		return &palette{
			sidebar:    ui.Hex("#1b1c21"),
			selected:   ui.Hex("#2a2c34"),
			hover:      ui.Hex("#24262c"),
			userMsg:    ui.Hex("#272a3a"),
			code:       ui.Hex("#1d1f24"),
			warnSoft:   ui.Hex("#33291a"),
			accentSoft: ui.Hex("#252948"),
			okSoft:     ui.Hex("#1a3326"),
			running:    ui.Hex("#7d87ff"),
		}
	}
	return &palette{
		sidebar:    ui.Hex("#f6f7f9"),
		selected:   ui.Hex("#e7e9ef"),
		hover:      ui.Hex("#eef0f4"),
		userMsg:    ui.Hex("#eef0fd"),
		code:       ui.Hex("#f6f7f9"),
		warnSoft:   ui.Hex("#fdf5e8"),
		accentSoft: ui.Hex("#eceefd"),
		okSoft:     ui.Hex("#e6f4ec"),
		running:    ui.Hex("#4b55d6"),
	}
}

// useTheme switches the frame to Alcove's theme for the system's appearance
// and returns the matching palette.
func useTheme(c *ui.Context) *palette {
	if c.Theme().Dark {
		c.SetTheme(darkTheme)
		return darkPalette
	}
	c.SetTheme(lightTheme)
	return lightPalette
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
	icSend     = icon(`<path d="M12 19V5M6 11l6-6 6 6"/>`)
	icCheck    = icon(`<path d="m5 12 4.5 4.5L19 7"/>`)
	icX        = icon(`<path d="M6 6l12 12M18 6 6 18"/>`)
	icCloud    = icon(`<path d="M7 18a4.5 4.5 0 0 1-.6-9A6 6 0 0 1 18 8.5a4.5 4.5 0 0 1-.5 9.5z"/>`)
	icChat     = icon(`<path d="M5 18.5V6a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H8.5z"/>`)
	icMore     = icon(`<circle cx="6" cy="12" r="1" fill="currentColor"/><circle cx="12" cy="12" r="1" fill="currentColor"/><circle cx="18" cy="12" r="1" fill="currentColor"/>`)
	icTerminal = icon(`<path d="m5 8 4 4-4 4M12 17h7"/>`)
	icAlert    = icon(`<path d="M12 4 2.5 20h19z"/><path d="M12 10v4M12 17h.01"/>`)
)
