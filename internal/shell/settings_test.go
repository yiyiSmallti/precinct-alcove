package shell

import (
	"testing"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

func TestAppearanceLooks(t *testing.T) {
	if !isDark(app.Appearance{}, true) || isDark(app.Appearance{Mode: "light"}, true) || !isDark(app.Appearance{Mode: "dark"}, false) {
		t.Fatal("mode")
	}
	if c, ink := accentColor(app.Appearance{}, false); ink || hexOf(c) != "#4f46e5" {
		t.Fatalf("default: %s %v", hexOf(c), ink)
	}
	if c, ink := accentColor(app.Appearance{Tone: "warm"}, false); !ink || hexOf(c) != "#26221d" {
		t.Fatalf("graphite: %s %v", hexOf(c), ink)
	}
	if c, _ := accentColor(app.Appearance{Tone: "linen", Accent: "graphite"}, false); hexOf(c) != "#1f1d1a" {
		t.Fatalf("chosen graphite: %s", hexOf(c))
	}
	if c, ink := accentColor(app.Appearance{Accent: "#123456"}, false); ink || hexOf(c) != "#123456" {
		t.Fatalf("custom: %s", hexOf(c))
	}
	l := lookFor(app.Appearance{Tone: "cool", Accent: "violet", CodeFont: "Menlo"}, true)
	if l.theme.Accent != l.pal.running || l.pal.mono != "Menlo" || hexOf(l.pal.content) != "#141922" {
		t.Fatal("look")
	}
	if lookFor(app.Appearance{}, false).pal.mono != "monospace" {
		t.Fatal("default mono")
	}
}

func TestAppearanceSaved(t *testing.T) {
	s, tt := newShell(t)
	until(t, tt, s, func() bool { return s.page == PageSettings })
	tt.Click("外观")
	tt.Frame()
	tt.Click("深色")
	tt.Frame()
	tt.Click("紫藤")
	tt.Frame()
	until(t, tt, s, func() bool {
		st, err := s.App.Settings()
		return err == nil && st.Appearance.Mode == "dark" && st.Appearance.Accent == "violet"
	})
}
