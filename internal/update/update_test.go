package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMirror(t *testing.T) {
	files := map[string]string{
		"/rel/update-darwin-arm64.json":         `{"version":"0.2.0"}`,
		"/rel/alcove-0.2.0-darwin-arm64.tar.gz": "archive",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	defer srv.Close()

	m, err := NewMirror(srv.URL + "/rel/")
	if err != nil {
		t.Fatal(err)
	}
	m.Target = "darwin-arm64"
	read := func(rc io.ReadCloser, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		return string(b)
	}
	if got := read(m.Manifest(context.Background(), false)); got != `{"version":"0.2.0"}` {
		t.Fatalf("manifest %q", got)
	}
	// The manifest names the original host; the mirror serves the same file name.
	if got := read(m.Open(context.Background(), "https://github.com/x/y/releases/download/v0.2.0/alcove-0.2.0-darwin-arm64.tar.gz")); got != "archive" {
		t.Fatalf("archive %q", got)
	}
	if _, err := m.Open(context.Background(), "https://example.com/missing.tar.gz"); err == nil {
		t.Fatal("missing file: no error")
	}

	for _, bad := range []string{"", "ftp://x/y", "http://example.com/rel", "not a url"} {
		if _, err := NewMirror(bad); err == nil {
			t.Errorf("NewMirror(%q): no error", bad)
		}
	}
}
