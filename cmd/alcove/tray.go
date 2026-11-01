package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
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

// trayIcon draws Alcove's mark, an arch, as a 32×32 black-on-transparent
// template image.
func trayIcon() []byte {
	const n = 32
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	ink := color.NRGBA{A: 255}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-15.5, float64(y)-14.0
			outer := (y >= 14 && y <= 28 && x >= 5 && x <= 26) || (y < 14 && dx*dx+dy*dy <= 10.5*10.5)
			inner := (y >= 14 && y <= 28 && x >= 10 && x <= 21) || (y < 14 && dx*dx+dy*dy <= 6*6)
			if outer && !inner {
				img.SetNRGBA(x, y, ink)
			}
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}
