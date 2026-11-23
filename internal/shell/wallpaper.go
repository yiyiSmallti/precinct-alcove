package shell

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sync"

	"github.com/egoist/mygo/ui"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

// A theme is a painting behind the window with the tone and accent made
// for it, and the outfit Index wears in it (Charter docs/brand/index).
// Each scene is painted twice, by day and by night, and shows the one that
// matches the mode, so a light window never sits on a night sky.
type scene struct {
	id, name     string
	tone, accent string
	outfit       string
}

// The first is the default: a clean sky that keeps out of the way.
var scenes = []scene{
	{"clean", "默认", "zinc", "", "default"},
	{"afternoon", "午后", "linen", "terracotta", "default"},
	{"harbor", "夜航", "slate", "cyan", "midnight"},
	{"library", "书院", "forest", "burgundy", "academia"},
	{"circuit", "机能", "neutral", "lime", "techwear"},
	{"summer", "夏日", "cool", "teal", "summer"},
}

// customScene is the user's own picture, by day and by night alike; the
// tone and accent stay as they were.
var customScene = scene{id: "custom", name: "自定义", outfit: "default"}

// noWallpaper is the plain window, without a painting.
const noWallpaper = "none"

//go:embed wallpapers/*.jpg
var paintings embed.FS

// sceneFor is the scene an appearance shows: "" is the default one, and
// none for noWallpaper or an id it does not know.
func sceneFor(ap app.Appearance) (scene, bool) {
	switch ap.Wallpaper {
	case "":
		return scenes[0], true
	case customScene.id:
		return customScene, true
	}
	return sceneOf(ap.Wallpaper)
}

func sceneOf(id string) (scene, bool) {
	for _, s := range scenes {
		if s.id == id {
			return s, true
		}
	}
	return scene{}, false
}

// wallpaper is one painting, sharp and as frosted glass.
type wallpaper struct {
	sharp, frost *ui.Bitmap
}

var (
	wallpapers sync.Map // "id-day", "id-night" or "custom" → *wallpaper, once shown
	thumbs     sync.Map // the same keys → *ui.Bitmap, for settings
)

// customKey is the user's picture in those caches, dropped when it changes.
const customKey = "custom"

func paintingKey(id string, dark bool) string {
	if dark {
		return id + "-night"
	}
	return id + "-day"
}

// decodePainting reads a painting out of the binary, as RGBA so it can be
// read pixel by pixel quickly (draw has a fast path from JPEG's YCbCr).
func decodePainting(key string) (*image.RGBA, bool) {
	data, err := paintings.ReadFile("wallpapers/" + key + ".jpg")
	if err != nil {
		return nil, false
	}
	return decodeRGBA(data)
}

func decodeRGBA(data []byte) (*image.RGBA, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	m := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(m, m.Rect, img, img.Bounds().Min, draw.Src)
	return m, true
}

// thumbOf is a painting's small copy, made beforehand, for its card in
// settings: decoding the five paintings themselves would stall the page.
func thumbOf(id string, dark bool) *ui.Bitmap {
	key := paintingKey(id, dark)
	if b, ok := thumbs.Load(key); ok {
		return b.(*ui.Bitmap)
	}
	data, err := paintings.ReadFile("wallpapers/" + key + "-thumb.jpg")
	if err != nil {
		return nil
	}
	bm, err := ui.DecodeBitmap(data)
	if err != nil {
		return nil
	}
	b, _ := thumbs.LoadOrStore(key, bm)
	return b.(*ui.Bitmap)
}

// wallpaperFor is the painting of an appearance in light or dark, or nil
// for none; custom is where the user's own picture is kept. It is decoded
// once and kept.
func wallpaperFor(ap app.Appearance, dark bool, custom string) *wallpaper {
	sc, ok := sceneFor(ap)
	switch {
	case !ok:
		return nil
	case sc.id == customScene.id:
		return customPainting(custom)
	}
	return paintingOf(sc.id, dark)
}

// customPainting is the user's picture, or nil when there is none: then
// the window is plain until they pick one.
func customPainting(path string) *wallpaper {
	if w, ok := wallpapers.Load(customKey); ok {
		return w.(*wallpaper)
	}
	var w *wallpaper
	if data, err := os.ReadFile(path); err == nil && path != "" {
		if img, ok := decodeRGBA(data); ok {
			w = &wallpaper{sharp: ui.NewBitmap(img), frost: ui.NewBitmap(frost(img))}
		}
	}
	v, _ := wallpapers.LoadOrStore(customKey, w)
	return v.(*wallpaper)
}

// customThumb is the small copy of the user's picture saved beside it.
func customThumb(path string) *ui.Bitmap {
	if b, ok := thumbs.Load(customKey); ok {
		return b.(*ui.Bitmap)
	}
	var bm *ui.Bitmap
	if data, err := os.ReadFile(thumbPath(path)); err == nil && path != "" {
		bm, _ = ui.DecodeBitmap(data)
	}
	b, _ := thumbs.LoadOrStore(customKey, bm)
	return b.(*ui.Bitmap)
}

func thumbPath(path string) string {
	return path[:len(path)-len(filepath.Ext(path))] + "-thumb.jpg"
}

// saveWallpaper keeps a copy of the picture at src as dst, a JPEG just
// large enough to cover a 1280×800 window at 2x, with its thumbnail: it
// loads fast, and stays when the original moves or goes.
func saveWallpaper(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("读不了这张图片（支持 JPEG、PNG、WebP、GIF）：%w", err)
	}
	if err := writeJPEG(dst, coverScale(img, 2560, 1600), 90); err != nil {
		return err
	}
	return writeJPEG(thumbPath(dst), coverScale(img, 256, 160), 85)
}

// coverScale shrinks img, if it is larger, to the smallest size that still
// covers w×h.
func coverScale(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	f := max(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	if f >= 1 {
		return img
	}
	out := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(b.Dx())*f+0.5)), max(1, int(float64(b.Dy())*f+0.5))))
	xdraw.CatmullRom.Scale(out, out.Rect, img, b, draw.Src, nil)
	return out
}

// writeJPEG writes beside path and renames, so a reader never sees half.
func writeJPEG(path string, img image.Image, q int) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: q}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// customWallpaper is where the user's picture is kept.
func (s *Shell) customWallpaper() string {
	if s.App == nil {
		return ""
	}
	return s.App.Layout.Wallpaper()
}

// pickWallpaper asks for a picture and makes it the wallpaper.
func (s *Shell) pickWallpaper() {
	path := s.customWallpaper()
	if s.PickImage == nil || path == "" {
		return
	}
	go func() {
		src, err := s.PickImage()
		if err == nil && src == "" {
			return
		}
		if err == nil {
			err = saveWallpaper(src, path)
		}
		s.post(func() {
			if err != nil {
				s.notice = "无法换壁纸：" + describe(err)
				return
			}
			wallpapers.Delete(customKey)
			thumbs.Delete(customKey)
			ap := s.settings.Appearance
			ap.Wallpaper = customScene.id
			s.setAppearance(ap)
		})
	}()
}

func paintingOf(id string, dark bool) *wallpaper {
	key := paintingKey(id, dark)
	if w, ok := wallpapers.Load(key); ok {
		return w.(*wallpaper)
	}
	img, ok := decodePainting(key)
	if !ok {
		return nil
	}
	w := &wallpaper{sharp: ui.NewBitmap(img), frost: ui.NewBitmap(frost(img))}
	v, _ := wallpapers.LoadOrStore(key, w)
	return v.(*wallpaper)
}

// frost is the painting as seen through frosted glass: shrunk 12 times and
// box-blurred three times (close to a Gaussian), so the window's ground
// keeps the painting's light and colors without its detail. The GPU
// stretches it back smoothly.
func frost(img *image.RGBA) image.Image {
	m := shrink(img, 12)
	for range 3 {
		m = boxBlur(m, 3)
	}
	return m
}

// shrink averages each f×f block into one pixel.
func shrink(img *image.RGBA, f int) *image.RGBA {
	w, h := img.Rect.Dx()/f, img.Rect.Dy()/f
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	n := f * f
	for y := range h {
		for x := range w {
			var s [3]int
			for dy := range f {
				row := img.Pix[(y*f+dy)*img.Stride+x*f*4:]
				for dx := range f {
					s[0], s[1], s[2] = s[0]+int(row[dx*4]), s[1]+int(row[dx*4+1]), s[2]+int(row[dx*4+2])
				}
			}
			o := out.Pix[y*out.Stride+x*4:]
			o[0], o[1], o[2], o[3] = uint8(s[0]/n), uint8(s[1]/n), uint8(s[2]/n), 255
		}
	}
	return out
}

// boxBlur averages each pixel with its neighbors within r, clamping at the
// edges.
func boxBlur(m *image.RGBA, r int) *image.RGBA {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	pass := func(src *image.RGBA, horizontal bool) *image.RGBA {
		out := image.NewRGBA(src.Rect)
		for y := range h {
			for x := range w {
				var s [3]int
				for d := -r; d <= r; d++ {
					px, py := x, y
					if horizontal {
						px = min(max(x+d, 0), w-1)
					} else {
						py = min(max(y+d, 0), h-1)
					}
					c := src.RGBAAt(px, py)
					s[0], s[1], s[2] = s[0]+int(c.R), s[1]+int(c.G), s[2]+int(c.B)
				}
				n := 2*r + 1
				out.SetRGBA(x, y, color.RGBA{uint8(s[0] / n), uint8(s[1] / n), uint8(s[2] / n), 255})
			}
		}
		return out
	}
	return pass(pass(m, true), false)
}

// paint draws a bitmap over its parent's whole box, cropped to cover it.
// It is absolute, so it paints under the parent's later absolute children
// and over any in flow.
func paint(c *ui.Context, b *ui.Bitmap) ui.Element {
	return ui.Image(c, b).Fit(ui.Cover).Absolute().Top(0).Left(0).Right(0).Bottom(0).IgnoreInput(true)
}

// withScene is the appearance with a theme's painting, tone and accent.
// The default scene, the first tone and the tone's own accent are stored as
// "", as the settings do.
func withScene(ap app.Appearance, sc scene) app.Appearance {
	ap.Wallpaper, ap.Tone, ap.Accent = sc.id, sc.tone, sc.accent
	if sc.id == scenes[0].id {
		ap.Wallpaper = ""
	}
	if sc.tone == tones[0].id {
		ap.Tone = ""
	}
	if sc.accent == toneOf(sc.tone).accent {
		ap.Accent = ""
	}
	return ap
}

// sceneCard is a theme to choose, as a small window over its painting (its
// thumbnail in the current mode) with the frosted sidebar and the accent on
// the composer. A scene without an id is no painting, drawn as the plain
// window; the user's picture keeps the current look, and is a "+" until
// there is one.
func sceneCard(c *ui.Context, pal *palette, ap app.Appearance, sc scene, sel bool, thumb *ui.Bitmap) bool {
	t := c.Theme()
	key := sc.id
	if key == "" {
		key = "none"
	}
	col := ui.Column(c).Key("scene-" + key).Gap(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Focusable().Label("壁纸：" + sc.name)
	col.Children(func() {
		frame := ui.Box(c).Size(112, 70).Radius(8).Clip().BorderWidth(2)
		switch {
		case sel:
			frame.BorderColor(t.Accent)
		case col.Hovered():
			frame.BorderColor(pal.hairline.Alpha(3))
		default:
			frame.BorderColor(pal.hairline)
		}
		frame.Children(func() {
			if sc.id == "" {
				plain := ap
				plain.Wallpaper = noWallpaper
				miniWindow(c, lookFor(plain, t.Dark), 108)
				return
			}
			if sc.id == customScene.id && thumb == nil {
				ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Background(pal.sidebar).Children(func() {
					ui.Icon(c, icPlus).Size(18, 18).TextColor(t.TextMuted)
				})
				return
			}
			l := lookFor(ap, t.Dark)
			if sc.id != customScene.id {
				l = lookFor(withScene(ap, sc), t.Dark)
			}
			if thumb != nil {
				paint(c, thumb)
			}
			ui.Column(c).Absolute().Top(0).Left(0).Bottom(0).Width(30).Padding(8, 5).Gap(4).Background(l.pal.sidebar.Alpha(0.6)).Children(func() {
				ui.Box(c).Height(4).Radius(2).Background(l.theme.TextMuted.Alpha(0.6))
				ui.Box(c).Height(6).Radius(2).Background(l.pal.selected)
				ui.Box(c).Height(4).Radius(2).Background(l.theme.TextMuted.Alpha(0.45))
			})
			ui.Row(c).Absolute().Right(6).Bottom(6).Left(38).Height(12).Radius(4).Background(l.pal.content.Alpha(0.9)).Justify(ui.End).AlignItems(ui.Center).Padding(0, 2).Children(func() {
				ui.Box(c).Size(7, 7).Radius(3.5).Background(l.theme.Accent)
			})
		})
		txt := ui.Text(c, sc.name).FontSize(12)
		if !sel {
			txt.TextColor(t.TextMuted)
		}
	})
	return col.Clicked()
}
