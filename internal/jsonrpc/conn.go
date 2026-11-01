// Package jsonrpc is a newline-delimited JSON-RPC 2.0 connection over a pair
// of byte streams, the framing dsh's SDK server and Alcove sidecars use: one
// message per line; a frame with id and method is a request, id alone a
// response, method alone a notification. Malformed lines are ignored.
package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Error is a JSON-RPC error response.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

// Standard error codes.
const (
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// ErrClosed is returned for calls on a closed connection and for calls still
// pending when it closes.
var ErrClosed = errors.New("jsonrpc: connection closed")

// Handler answers requests and receives notifications from the peer. A nil
// Handler answers every request with method-not-found.
type Handler interface {
	// Request returns the result (marshalled to JSON) or an error; an *Error
	// is sent as is, any other error as CodeInternal.
	Request(ctx context.Context, method string, params json.RawMessage) (any, error)
	Notify(method string, params json.RawMessage)
}

type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// Conn is one connection. Create it with New and start reading with Serve.
type Conn struct {
	w       io.Writer
	r       io.Reader
	handler Handler

	wq      chan writeReq // to the single writer goroutine
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan message
	closed  bool
	done    chan struct{}
	err     error
}

type writeReq struct {
	b   []byte
	res chan error
}

// New wraps r and w. Nothing is read until Serve runs. Writes go through one
// goroutine so a peer that stops reading cannot block callers past their
// context; closing the transport ends a stuck write.
func New(r io.Reader, w io.Writer, h Handler) *Conn {
	c := &Conn{r: r, w: w, handler: h, pending: map[int64]chan message{}, done: make(chan struct{}), wq: make(chan writeReq)}
	go c.writer()
	return c
}

// writer serves writes until the connection stops reading. A write stuck in
// the transport keeps it until the transport is closed.
func (c *Conn) writer() {
	var failed error
	for {
		select {
		case req := <-c.wq:
			if failed == nil {
				_, failed = c.w.Write(req.b)
			}
			req.res <- failed
		case <-c.done:
			return
		}
	}
}

// Serve reads until r ends or fails, then fails every pending call. It
// returns nil on a clean EOF.
func (c *Conn) Serve(ctx context.Context) error {
	sc := bufio.NewScanner(c.r)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		switch {
		case m.ID != nil && m.Method != "":
			go c.answer(ctx, m)
		case m.ID != nil:
			var id int64
			if json.Unmarshal(*m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.Method != "":
			if c.handler != nil {
				c.handler.Notify(m.Method, m.Params)
			}
		}
	}
	err := sc.Err()
	c.close(err)
	return err
}

// Done is closed when the connection stops reading.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) close(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	close(c.done)
}

func (c *Conn) answer(ctx context.Context, m message) {
	resp := message{JSONRPC: "2.0", ID: m.ID}
	if c.handler == nil {
		resp.Error = &Error{Code: CodeMethodNotFound, Message: "method not found: " + m.Method}
	} else if res, err := c.handler.Request(ctx, m.Method, m.Params); err != nil {
		var je *Error
		if !errors.As(err, &je) {
			je = &Error{Code: CodeInternal, Message: err.Error()}
		}
		resp.Error = je
	} else {
		b, err := json.Marshal(res)
		if err != nil {
			resp.Error = &Error{Code: CodeInternal, Message: err.Error()}
		} else {
			resp.Result = b
		}
	}
	c.write(ctx, resp)
}

func (c *Conn) write(ctx context.Context, m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req := writeReq{b: append(b, '\n'), res: make(chan error, 1)}
	select {
	case c.wq <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}
	select {
	case err := <-req.res:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Call sends a request and decodes the result into out (which may be nil).
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	raw := json.RawMessage(fmt.Sprint(id))
	req := message{ID: &raw, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	if err := c.write(ctx, req); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Notify sends a notification; ctx bounds the write.
func (c *Conn) Notify(ctx context.Context, method string, params any) error {
	m := message{Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		m.Params = b
	}
	return c.write(ctx, m)
}
