// Package update checks for and installs new versions of Alcove (SPEC A5).
//
// Releases are what `mygo build` publishes: a signed archive per platform,
// delta updates from earlier versions, and update-<platform>.json. Installed
// apps check the feed built into them (updates in mygo.json); an
// organization can point Alcove at a mirror of those files instead (the
// 更新源 setting). Either way the archive must be signed with the key the
// app was built with, so a mirror can serve updates but not change them.
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// Release is a newer version of Alcove.
type Release struct {
	Version string
	Notes   string // Markdown
	Date    time.Time
}

// Updater is what the settings page drives.
type Updater interface {
	// Enabled reports whether this build can update itself: a release
	// build, with updates configured, installed where the user can write.
	Enabled() bool
	// SetSource makes checks read the mirror at base (an https directory
	// holding the files of a release); "" goes back to the built-in feed.
	SetSource(base string) error
	// Check returns the newer release, or nil when Alcove is up to date.
	Check(ctx context.Context) (*Release, error)
	// Install downloads and installs the release Check last returned; it
	// runs at the next start (Relaunch).
	Install(ctx context.Context, progress func(done, total int64)) error
	Relaunch()
}

// ErrDisabled is returned by Check in builds that cannot update themselves.
var ErrDisabled = mygo.ErrUpdatesDisabled

// MyGo is the Updater of a real build.
type MyGo struct {
	mu   sync.Mutex
	last *mygo.Update
}

func (m *MyGo) Enabled() bool { return mygo.Updater.Enabled() }

func (m *MyGo) SetSource(base string) error {
	if base == "" {
		mygo.Updater.SetSource(nil)
		return nil
	}
	src, err := NewMirror(base)
	if err != nil {
		return err
	}
	mygo.Updater.SetSource(src)
	return nil
}

func (m *MyGo) Check(ctx context.Context) (*Release, error) {
	up, err := mygo.Updater.Check(ctx)
	if err != nil || up == nil {
		return nil, err
	}
	m.mu.Lock()
	m.last = up
	m.mu.Unlock()
	return &Release{Version: up.Version, Notes: up.Notes, Date: up.Date}, nil
}

func (m *MyGo) Install(ctx context.Context, progress func(done, total int64)) error {
	m.mu.Lock()
	up := m.last
	m.mu.Unlock()
	if up == nil {
		return errors.New("no update to install; check first")
	}
	return up.Install(ctx, progress)
}

func (m *MyGo) Relaunch() { mygo.App.Relaunch() }

// Mirror serves a release from a directory other than the one the app was
// built with: update-<platform>.json from base, and every file the
// manifest names from base under its file name, so a mirror is a plain copy
// of the release's files.
type Mirror struct {
	Base   string
	Target string // e.g. darwin-arm64; default the running platform
	Client *http.Client
}

// NewMirror checks base: an https URL (http only for this machine, for
// tests).
func NewMirror(base string) (*Mirror, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("更新源不是有效的网址：%q", base)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("更新源须为 https 网址：%q", base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return &Mirror{Base: u.String()}, nil
}

func (m *Mirror) Manifest(ctx context.Context, _ bool) (io.ReadCloser, error) {
	t := m.Target
	if t == "" {
		t = runtime.GOOS + "-" + runtime.GOARCH
	}
	return m.get(ctx, m.Base+"/update-"+t+".json")
}

func (m *Mirror) Open(ctx context.Context, fileURL string) (io.ReadCloser, error) {
	u, err := url.Parse(fileURL)
	if err != nil {
		return nil, err
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		return nil, fmt.Errorf("manifest names no file: %q", fileURL)
	}
	return m.get(ctx, m.Base+"/"+url.PathEscape(name))
}

func (m *Mirror) get(ctx context.Context, u string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c := m.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return resp.Body, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
