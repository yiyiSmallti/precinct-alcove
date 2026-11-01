//go:build windows

package hostipc

import (
	"net"

	"github.com/Microsoft/go-winio"
)

// listen opens \\.\pipe\<name>. The security descriptor grants full access to
// the pipe's owner (the current user) only, protected from inheritance.
func listen(name string) (net.Listener, string, error) {
	path := `\\.\pipe\` + name
	ln, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;OW)",
		MessageMode:        false,
	})
	return ln, path, err
}
