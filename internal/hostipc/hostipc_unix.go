//go:build !windows

package hostipc

import (
	"net"
	"os"
	"path/filepath"
)

// listen opens a Unix socket at name (an absolute path, or a file name placed
// in the temp directory) with mode 0600. A stale socket file is replaced.
func listen(name string) (net.Listener, string, error) {
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(os.TempDir(), name+".sock")
	}
	os.Remove(path)
	old := umask(0o177)
	ln, err := net.Listen("unix", path)
	umask(old)
	if err != nil {
		return nil, "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, "", err
	}
	return ln, path, nil
}
