package shell

import (
	"testing"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

func TestAppearanceLooks(t *testing.T) {
	if !isDark(app.Appearance{}, true) || isDark(app.Appearance{Mode: "light"}, true) || !isDark(app.Appearance{Mode: "dark"}, false) {
		t.Fatal("mode")
	}
	if c, ink := accentColor(app.Appearance{}, false); !ink || hexOf(c) != "#1f1f1d" {
		t.Fatalf("graphite: %s %v", hexOf(c), ink)
	}
	if c, ink := accentColor(app.Appearance{Accent: "#123456"}, false); ink || hexOf(c) != "#123456" {
		t.Fatalf("custom: %s", hexOf(c))
	}
	l := lookFor(app.Appearance{Tone: "cool", Accent: "violet", CodeFont: "Menlo"}, true)
	if l.theme.Accent != l.pal.running || l.pal.mono != "Menlo" || hexOf(l.pal.content) != "#1a1d23" {
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
