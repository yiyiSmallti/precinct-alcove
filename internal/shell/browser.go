package shell

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// Alcove's browser: each workspace has one page, shown in a pane beside its
// tasks. The user browses in it, and a plugin with the browser capability
// drives it for the workspace's Agent (sidecar.Browser), which the user
// watches and can take over at any time: the page is a real one, the same
// for both. Pages keep their sign-ins in a data store of their own, apart
// from the user's browsers.

// browserTab is a workspace's page.
type browserTab struct {
	view    *mygo.WebView
	address string    // the address field
	title   string    // the page's title
	failed  string    // why the last load failed
	agentAt time.Time // when a plugin last acted on the page
	// held: the user took the page over; plugins' calls are refused until
	// they hand it back.
	held bool
}

// errHeld is what a plugin hears while the user holds the page. It goes
// on to the Agent, in English, as its tools' errors do.
var errHeld = errors.New("the user has taken over the browser and is using it; wait until they hand it back (ask them if needed), then continue")

// agentShown is how long the pane says the Agent is at work after its last
// action.
const agentShown = 4 * time.Second

// agentLately is how long after the Agent's last action the pane offers
// to take the page over from it.
const agentLately = 2 * time.Minute

// loadTimeout bounds how long an op waits for its page to load; the page
// goes on loading after.
const loadTimeout = 30 * time.Second

var errNoBrowser = errors.New("浏览器不可用")

// browserTab returns the workspace's page, making it if create is set.
// It must be called without mu: making a page waits for the UI thread,
// which takes mu to draw.
func (s *Shell) browserTab(ws string, create bool) (*browserTab, error) {
	s.mu.Lock()
	tab := s.browsers[ws]
	known := s.find(ws) != nil
	s.mu.Unlock()
	if !known {
		// A removed workspace's page is no one's to drive any more.
		if tab != nil {
			s.closeBrowser(ws)
		}
		if create {
			return nil, fmt.Errorf("no workspace %s", ws)
		}
		return nil, nil
	}
	if tab != nil || !create {
		return tab, nil
	}
	if s.NewWebView == nil {
		return nil, errNoBrowser
	}
	view, err := s.NewWebView()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoBrowser, err)
	}
	s.mu.Lock()
	if prev := s.browsers[ws]; prev != nil {
		s.mu.Unlock()
		view.Destroy()
		return prev, nil
	}
	tab = &browserTab{view: view}
	s.browsers[ws] = tab
	s.mu.Unlock()
	page := view.Page()
	page.OnDidNavigate(func(u string) {
		s.post(func() { tab.address, tab.failed = shownURL(u), "" })
	})
	page.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		s.post(func() { tab.title = e.Title })
	})
	page.OnDidFailLoad(func(e *mygo.LoadError) {
		s.post(func() { tab.failed = e.Description })
	})
	// The toolbar shows loading: redraw as it starts and stops.
	page.OnWillNavigate(func(*mygo.NavigateEvent) { s.post(func() {}) })
	page.OnDidFinishLoad(func() { s.post(func() {}) })
	// Links that open a new window open in the page instead.
	page.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if web(req.URL) {
			go page.LoadURL(req.URL)
		}
		return nil
	})
	// No camera, microphone, location or notifications.
	page.SetPermissionHandler(func(mygo.PermissionRequest) bool { return false })
	return tab, nil
}

// web reports whether u is a page the browser opens: http(s) and about:blank.
func web(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != "" || u == "about:blank"
}

// shownURL is what the address field shows for u.
func shownURL(u string) string {
	if u == "about:blank" {
		return ""
	}
	return u
}

// typedURL is the page for what the user typed in the address field: an
// address, with https:// added when it has no scheme, or else a search.
func typedURL(text string) string {
	text = strings.TrimSpace(text)
	if web(text) {
		return text
	}
	if !strings.ContainsAny(text, " \t") && strings.Contains(text, ".") && !strings.Contains(text, "://") {
		if u := "https://" + text; web(u) {
			return u
		}
	}
	return "https://duckduckgo.com/?q=" + url.QueryEscape(text)
}

// Browse is the sidecars' browser (sidecar.Browser).
func (s *Shell) Browse(ctx context.Context, plugin, ws string, op sidecar.BrowserOp) (any, error) {
	switch op.Op {
	case "state":
		tab, err := s.browserTab(ws, false)
		if err != nil || tab == nil {
			return sidecar.BrowserState{}, err
		}
		return s.browserState(tab), nil
	case "close":
		tab, err := s.browserTab(ws, false)
		if err != nil {
			return nil, err
		}
		if tab != nil {
			s.mu.Lock()
			held := tab.held
			s.mu.Unlock()
			if held {
				return nil, errHeld // the user's page until they hand it back
			}
		}
		s.closeBrowser(ws)
		return sidecar.BrowserState{}, nil
	}
	if op.Op == "navigate" && !web(op.URL) {
		return nil, fmt.Errorf("browser.navigate: only http and https pages open, not %q", op.URL)
	}
	tab, err := s.browserTab(ws, true)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	held := tab.held
	s.mu.Unlock()
	if held {
		return nil, errHeld
	}
	s.agentActed(ws, tab)
	page := tab.view.Page()
	switch op.Op {
	case "eval":
		v, err := page.EvalContext(ctx, op.Code)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v}, nil
	case "capture":
		png, err := tab.view.CapturePage()
		if err != nil {
			return nil, err
		}
		return map[string]any{"png": base64.StdEncoding.EncodeToString(png)}, nil
	case "navigate":
		s.post(func() { tab.address, tab.failed = shownURL(op.URL), "" })
		if err := page.LoadURL(op.URL); err != nil {
			return nil, err
		}
	case "back":
		page.GoBack()
	case "forward":
		page.GoForward()
	case "reload":
		page.Reload()
	default:
		return nil, fmt.Errorf("unknown browser op %q", op.Op)
	}
	waitLoaded(ctx, page)
	return s.browserState(tab), nil
}

// waitLoaded waits for the page to start loading, briefly, and then to
// finish, at most loadTimeout.
func waitLoaded(ctx context.Context, page *mygo.Page) {
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	started := false
	for i := 0; ; i++ {
		loading := page.IsLoading()
		started = started || loading
		if !loading && (started || i >= 5) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Shell) browserState(tab *browserTab) sidecar.BrowserState {
	page := tab.view.Page()
	st := sidecar.BrowserState{Open: true, URL: page.URL(), Loading: page.IsLoading(),
		CanGoBack: page.CanGoBack(), CanGoForward: page.CanGoForward()}
	s.mu.Lock()
	st.Title, st.Error = tab.title, tab.failed
	s.mu.Unlock()
	return st
}

// agentActed shows the pane in the workspace and says the Agent is at
// work in it, for agentShown.
func (s *Shell) agentActed(ws string, tab *browserTab) {
	s.post(func() {
		tab.agentAt = time.Now()
		if v := s.views[ws]; v != nil {
			v.browsing = true
		}
	})
	time.AfterFunc(agentShown+50*time.Millisecond, func() { s.post(func() {}) })
	time.AfterFunc(agentLately+50*time.Millisecond, func() { s.post(func() {}) })
}

// closeBrowser drops the workspace's page and hides its pane.
func (s *Shell) closeBrowser(ws string) {
	s.mu.Lock()
	tab := s.browsers[ws]
	delete(s.browsers, ws)
	if v := s.views[ws]; v != nil {
		v.browsing = false
	}
	s.mu.Unlock()
	if tab != nil {
		tab.view.Destroy()
	}
}

// browserToggle shows or hides the workspace's browser pane.
func (s *Shell) browserToggle(c *ui.Context, pal *palette, v *wsView) {
	label := "打开浏览器"
	if v.browsing {
		label = "隐藏浏览器"
	}
	if iconButton(c, pal, icGlobe, label, v.browsing).Clicked() {
		v.browsing = !v.browsing
		if v.browsing && s.browsers[v.ws.ID] == nil {
			ws := v.ws.ID
			go func() {
				if _, err := s.browserTab(ws, true); err != nil {
					s.post(func() { s.notice = err.Error() })
				}
			}()
		}
	}
}

// browserPane is the workspace's page, on a sheet of its own beside the
// conversation's, under its toolbar: back, forward, reload, the address,
// and while a plugin acts on the page, a note that the Agent is at work,
// which the page's accent ring repeats.
func (s *Shell) browserPane(c *ui.Context, pal *palette, v *wsView) {
	t := c.Theme()
	tab := s.browsers[v.ws.ID]
	agent := tab != nil && time.Since(tab.agentAt) < agentShown
	lately := tab != nil && time.Since(tab.agentAt) < agentLately
	// The agent's hand on the page is the accent, or the blue of work in
	// progress when the accent is the ink: a black ring would read as a
	// frame, not as someone at work.
	hand := t.Accent
	if pal.inkAccent {
		hand = pal.running
	}
	sheet(pal, ui.Column(c).Grow(1.3).Basis(0).MinWidth(420)).Children(func() {
		r := ui.Row(c).Height(52).Shrink(0).Padding(0, 10).Gap(4).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline)
		if c.TitleBar().Height > 0 {
			r.DragWindow()
		}
		r.Children(func() {
			var page *mygo.Page
			if tab != nil {
				page = tab.view.Page()
			}
			nav := func(ic *ui.SVG, label string, ok bool, fn func(*mygo.Page)) {
				b := iconButton(c, pal, ic, label, false)
				if !ok {
					b.Opacity(0.4)
				}
				if b.Clicked() && ok {
					go fn(page)
				}
			}
			nav(icBack, "后退", page != nil && page.CanGoBack(), (*mygo.Page).GoBack)
			nav(icForward, "前进", page != nil && page.CanGoForward(), (*mygo.Page).GoForward)
			loading := page != nil && page.IsLoading()
			if loading {
				nav(icClose, "停止加载", true, (*mygo.Page).Stop)
			} else {
				nav(icReload, "重新加载", page != nil, (*mygo.Page).Reload)
			}
			if tab == nil {
				ui.Text(c, "正在打开…").FontSize(13).TextColor(t.TextMuted).Grow(1)
			} else {
				in := ui.TextInput(c, &tab.address).Placeholder("输入网址或搜索").Height(30).Grow(1).MinWidth(0).Label("网址")
				if in.Submitted() && strings.TrimSpace(tab.address) != "" {
					u := typedURL(tab.address)
					tab.address, tab.failed = u, ""
					go page.LoadURL(u)
				}
			}
			switch {
			case tab != nil && tab.held:
				if pill(c, "交还给 Agent", t.Text, pal.selected, "让 Agent 继续使用浏览器").Clicked() {
					tab.held = false
				}
			case lately:
				if agent {
					ui.Row(c).Height(24).Padding(0, 10).Gap(6).AlignItems(ui.Center).Radius(12).Background(hand.Alpha(0.14)).Shrink(0).Children(func() {
						ui.Row(c).Size(7, 7).Radius(4).Background(hand)
						ui.Text(c, "Agent 正在操作").FontSize(12).FontWeight(600).TextColor(hand)
					})
				}
				if pill(c, "接手", t.AccentText, t.Accent, "暂停 Agent 使用浏览器，由你来操作").Clicked() {
					tab.held, tab.agentAt = true, time.Time{}
				}
			}
			if iconButton(c, pal, icHide, "隐藏浏览器", false).Clicked() {
				v.browsing = false
			}
		})
		if tab != nil && tab.held {
			ui.Row(c).Padding(6, 14).Gap(8).AlignItems(ui.Center).Background(pal.hover).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
				ui.Text(c, "你在操作浏览器，Agent 暂时不能用它；需要它继续时点“交还给 Agent”。").FontSize(12).TextColor(t.TextMuted).Grow(1)
			})
		}
		if tab != nil && tab.failed != "" {
			ui.Row(c).Padding(6, 14).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
				ui.Icon(c, icAlert).Size(14, 14).TextColor(t.Danger)
				ui.Text(c, "打不开这个页面："+tab.failed).FontSize(12).TextColor(t.Danger).Grow(1).Selectable()
			})
		}
		// A thin bar under the toolbar while the page loads.
		bar := ui.Row(c).Height(2).Shrink(0)
		if tab != nil && tab.view.Page().IsLoading() {
			bar.Background(hand.Alpha(0.8))
		}
		// The page is a hole through the window, rounded only as it says:
		// its foot as round as the sheet's, inside the ring.
		body := ui.Column(c).Grow(1).Radius(0, 0, sheetR, sheetR)
		inner := float32(sheetR)
		if agent {
			body.Border(2, hand.Alpha(0.7))
			inner = sheetR - 2
		}
		body.Children(func() {
			if tab != nil {
				ui.WebView(c, tab.view).Grow(1).Radius(0, 0, inner, inner)
			}
		})
	})
}

// pill is a small rounded button of the browser's toolbar.
func pill(c *ui.Context, label string, fg, bg ui.Color, tip string) ui.Element {
	b := ui.Row(c).Height(24).Padding(0, 10).AlignItems(ui.Center).Radius(12).Background(bg).Shrink(0).
		Cursor(ui.CursorPointer).Focusable().Label(label).Tooltip(tip).Transition(fade)
	if b.Hovered() {
		b.Opacity(0.85)
	}
	b.Children(func() { ui.Text(c, label).FontSize(12).FontWeight(600).TextColor(fg) })
	return b
}
