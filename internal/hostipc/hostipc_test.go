//go:build !windows

package hostipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"
)

type ctxHandler struct{ got chan context.Context }

func (h ctxHandler) OnAttach(*Attachment) {}
func (h ctxHandler) Request(ctx context.Context, _ *Attachment, _ string, _ json.RawMessage) (any, error) {
	time.Sleep(100 * time.Millisecond) // starts work after the peer is gone
	h.got <- ctx
	return nil, nil
}
func (h ctxHandler) Notify(*Attachment, string, json.RawMessage) {}
func (h ctxHandler) OnDetach(*Attachment)                        {}

// A request whose peer disconnects runs under a cancelled context.
func TestRequestContextEndsWithConnection(t *testing.T) {
	h := ctxHandler{got: make(chan context.Context, 1)}
	s := &Server{Protocol: "p", Majors: []string{"1"}, Handler: h}
	if err := s.Listen(filepath.Join(t.TempDir(), "s.sock")); err != nil {
		t.Fatal(err)
	}
	go s.Serve(context.Background())
	defer s.Close()
	tok := s.Issue("rt")
	c, err := net.Dial("unix", s.Endpoint())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, `{"jsonrpc":"2.0","id":1,"method":"host.hello","params":{"protocol":"p/1","token":%q}}`+"\n", tok)
	bufio.NewReader(c).ReadBytes('\n')
	fmt.Fprint(c, `{"jsonrpc":"2.0","id":2,"method":"approval.ask","params":{}}`+"\n")
	c.Close()
	select {
	case ctx := <-h.got:
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("request context still live after disconnect")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request never ran")
	}
}
