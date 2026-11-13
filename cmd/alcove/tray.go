package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"sync/atomic"

	"github.com/egoist/mygo"
)

// background keeps Alcove running in the tray (Windows) or menu bar (macOS)
// while a plugin that holds background is installed: closing the window
// hides it, and the tray menu reopens it or quits (SPEC 3.2 常驻).
type background struct {
	win      *mygo.Window
	on       atomic.Bool
	quitting atomic.Bool
	tray     *mygo.Tray
}

func newBackground(win *mygo.Window) *background {
	b := &background{win: win}
	win.OnClose(func(e *mygo.CloseEvent) {
		if b.on.Load() && !b.quitting.Load() {
			e.PreventDefault()
			win.Hide()
		}
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { b.quitting.Store(true) })
	return b
}

// set is the shell's OnBackground hook (UI thread).
func (b *background) set(on bool) {
	b.on.Store(on)
	switch {
	case on && b.tray == nil:
		t, err := mygo.NewTray(mygo.TrayOptions{
			ID: "alcove", Icon: trayIcon(), IconIsTemplate: true, ToolTip: "Alcove",
			Menu: mygo.NewMenu([]*mygo.MenuItem{
				{Label: "打开 Alcove", Click: func(*mygo.MenuItem, *mygo.Window) { b.show() }},
				{Type: mygo.MenuItemSeparator},
				{Label: "退出 Alcove", Click: func(*mygo.MenuItem, *mygo.Window) { b.quit() }},
			}),
		})
		if err != nil {
			log.Printf("tray: %v", err)
			b.on.Store(false) // without a tray, closing must still quit
			return
		}
		b.tray = t
	case !on && b.tray != nil:
		// The window may be hidden: bring it back before the tray, the
		// only way to reopen it, goes away.
		b.show()
		b.tray.Destroy()
		b.tray = nil
	}
}

func (b *background) show() {
	b.win.Show()
	b.win.Focus()
}

func (b *background) quit() {
	b.quitting.Store(true)
	mygo.App.Quit()
}

// trayIcon draws Alcove's mark, the nook of its logo with a cushion on the
// floor, as a 36×36 black-on-transparent template image: the menu bar shows
// it 18pt high, so this is its 2x size. Edges are 4×4 supersampled.
func trayIcon() []byte {
	const n, ss = 36, 4
	nook := [][2]float64{{8.5, 28}, {8.5, 13.5}, {18, 8.5}, {27.5, 13.5}, {27.5, 28}}
	ink := func(x, y float64) bool {
		// The nook's wall: a 2.5-wide band inside the pentagon rounded by 4.
		if d := polyDist(x, y, nook) - 4; d <= 0 && d >= -2.5 {
			return true
		}
		// The cushion, a rounded rectangle resting on the floor.
		cx, cy := min(max(x, 14.6), 21.4), min(max(y, 25.1), 25.9)
		return math.Hypot(x-cx, y-cy) <= 3.6
	}
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			hit := 0
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					if ink(float64(x)+(float64(i)+0.5)/ss, float64(y)+(float64(j)+0.5)/ss) {
						hit++
					}
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{A: uint8(hit * 255 / (ss * ss))})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// polyDist is the signed distance from (x, y) to a convex polygon given
// clockwise in image coordinates: negative inside.
func polyDist(x, y float64, poly [][2]float64) float64 {
	inside, d := true, math.Inf(1)
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		ex, ey, px, py := b[0]-a[0], b[1]-a[1], x-a[0], y-a[1]
		if ex*py-ey*px < 0 {
			inside = false
		}
		t := min(max((px*ex+py*ey)/(ex*ex+ey*ey), 0), 1)
		d = min(d, math.Hypot(px-t*ex, py-t*ey))
	}
	if inside {
		return -d
	}
	return d
}
