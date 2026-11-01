// Package hostipc implements the host side of the host-callback convention
// (Charter protocols.md, "宿主回连约定"): the host listens on a per-user local
// endpoint, a bundle inside a dsh process dials it and opens with
// host.hello { protocol, token }, then both sides exchange JSON-RPC 2.0 frames
// one per line.
//
// Endpoints: a named pipe \\.\pipe\<name> readable only by the current user on
// Windows (hostipc_windows.go), a 0600 Unix socket elsewhere (hostipc_unix.go).
package hostipc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/jsonrpc"
)

// Error codes shared by every protocol on this convention.
const (
	CodeAlreadyAttached  = -32010
	CodeProtocolMismatch = -32011
	CodeUnauthorized     = -32012
	CodeNotReady         = -32013
	CodeRetryLater       = -32020
	CodeUnsupported      = -32021
)

// Hello is the first frame a bundle sends.
type Hello struct {
	Protocol string `json:"protocol"`
	Token    string `json:"token"`
	PID      int    `json:"pid,omitempty"`
}

// Attachment is one authenticated connection for one launch.
type Attachment struct {
	Conn   *jsonrpc.Conn
	Target string // what the token was issued for, e.g. a runtime id
	Hello  Hello
}

// Handler serves requests on attached connections.
type Handler interface {
	// OnAttach is called once the handshake succeeded.
	OnAttach(a *Attachment)
	// Request serves a bundle → host request other than host.hello.
	Request(ctx context.Context, a *Attachment, method string, params json.RawMessage) (any, error)
	// Notify receives a bundle → host notification.
	Notify(a *Attachment, method string, params json.RawMessage)
	// OnDetach is called when the connection closes.
	OnDetach(a *Attachment)
}

// Server listens on one endpoint for one protocol family ("alcove-bridge").
type Server struct {
	// Protocol is the name before the slash; Majors the supported major versions.
	Protocol string
	Majors   []string
	Handler  Handler

	ln       net.Listener
	endpoint string

	mu       sync.Mutex
	tokens   map[string]string // token → target
	attached map[string]*Attachment
}

// Listen opens the endpoint name (a pipe name on Windows, a socket path elsewhere).
func (s *Server) Listen(name string) error {
	ln, endpoint, err := listen(name)
	if err != nil {
		return err
	}
	s.ln, s.endpoint = ln, endpoint
	s.tokens = map[string]string{}
	s.attached = map[string]*Attachment{}
	return nil
}

// Endpoint is the address bundles dial (the <NAME>_HOST value).
func (s *Server) Endpoint() string { return s.endpoint }

// Issue returns a fresh one-time token for target (the <NAME>_HOST_TOKEN value).
func (s *Server) Issue(target string) string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	s.tokens[tok] = target
	s.mu.Unlock()
	return tok
}

// Revoke forgets every unused token for target.
func (s *Server) Revoke(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for t, tg := range s.tokens {
		if tg == target {
			delete(s.tokens, t)
		}
	}
}

// Attachment returns the live attachment for target, if any.
func (s *Server) Attachment(target string) *Attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attached[target]
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve(ctx context.Context) error {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.serveConn(ctx, c)
	}
}

// Close stops listening; attached connections stay until they close.
func (s *Server) Close() error { return s.ln.Close() }

type connHandler struct {
	s     *Server
	nc    net.Conn
	mu    sync.Mutex
	att   *Attachment
	ended bool // serveConn finished; no more attaching
	conn  *jsonrpc.Conn
}

func (s *Server) serveConn(ctx context.Context, c net.Conn) {
	h := &connHandler{s: s, nc: c}
	h.conn = jsonrpc.New(c, c, h)
	// Requests run under a context that ends with the connection, so a
	// handler that starts after the peer left still sees it gone.
	cctx, cancel := context.WithCancel(ctx)
	h.conn.Serve(cctx)
	cancel()
	c.Close()
	// Under s.mu (as hello registers), mark the connection ended so a
	// handshake still in flight cannot attach a dead connection.
	s.mu.Lock()
	h.mu.Lock()
	att := h.att
	h.ended = true
	h.mu.Unlock()
	if att != nil && s.attached[att.Target] == att {
		delete(s.attached, att.Target)
	}
	s.mu.Unlock()
	if att != nil {
		s.Handler.OnDetach(att)
	}
}

func (h *connHandler) Request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	h.mu.Lock()
	att := h.att
	h.mu.Unlock()
	if method == "host.hello" {
		if att != nil {
			return nil, &jsonrpc.Error{Code: CodeAlreadyAttached, Message: "already attached"}
		}
		return h.hello(params)
	}
	if att == nil {
		return nil, &jsonrpc.Error{Code: CodeUnauthorized, Message: "host.hello first"}
	}
	return h.s.Handler.Request(ctx, att, method, params)
}

func (h *connHandler) Notify(method string, params json.RawMessage) {
	h.mu.Lock()
	att := h.att
	h.mu.Unlock()
	if att != nil {
		h.s.Handler.Notify(att, method, params)
	}
}

func (h *connHandler) hello(params json.RawMessage) (any, error) {
	var hello Hello
	if err := json.Unmarshal(params, &hello); err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()}
	}
	s := h.s
	s.mu.Lock()
	var target string
	found := false
	for tok, tg := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(hello.Token)) == 1 {
			target, found = tg, true
			delete(s.tokens, tok) // one-time
			break
		}
	}
	if !found {
		s.mu.Unlock()
		// Answer, then disconnect.
		time.AfterFunc(200*time.Millisecond, func() { h.nc.Close() })
		return nil, &jsonrpc.Error{Code: CodeUnauthorized, Message: "unauthorized"}
	}
	name, major, _ := strings.Cut(hello.Protocol, "/")
	supported := false
	for _, m := range s.Majors {
		if name == s.Protocol && major == m {
			supported = true
		}
	}
	if !supported {
		s.mu.Unlock()
		var list []string
		for _, m := range s.Majors {
			list = append(list, s.Protocol+"/"+m)
		}
		data, _ := json.Marshal(map[string]any{"supported": list})
		return nil, &jsonrpc.Error{Code: CodeProtocolMismatch, Message: fmt.Sprintf("protocol %q not supported", hello.Protocol), Data: data}
	}
	if s.attached[target] != nil {
		s.mu.Unlock()
		return nil, &jsonrpc.Error{Code: CodeAlreadyAttached, Message: "already attached"}
	}
	h.mu.Lock()
	if h.ended {
		h.mu.Unlock()
		s.mu.Unlock()
		return nil, jsonrpc.ErrClosed
	}
	att := &Attachment{Conn: h.conn, Target: target, Hello: hello}
	s.attached[target] = att
	h.att = att
	h.mu.Unlock()
	s.mu.Unlock()
	s.Handler.OnAttach(att)
	return map[string]any{"protocol": hello.Protocol}, nil
}
