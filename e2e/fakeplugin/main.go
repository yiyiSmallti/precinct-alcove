// Command fakeplugin is a scripted Alcove sidecar for the e2e suite. It
// speaks host extension protocol v1 on stdio.
//
//	FAKE_PLUGIN_NAME   name it reports in ready (default @precinct/alcove-e2e)
//	FAKE_PLUGIN_MODE   comma-separated: crash (exits soon after ready),
//	                   nohello (never answers hello), slowhello (answers
//	                   after 1 s), stubborn (ignores shutdown), child (leaves
//	                   a child process holding stdout); stubborn implies child
//	FAKE_PLUGIN_CALLS  JSON array of {method, params}; each is called after
//	                   ready and its outcome sent back as a log line
//	                   "result <i> ok <json>" or "result <i> err <code>"
//	FAKE_PLUGIN_UI     JSON array of uiContributions for ready
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/jsonrpc"
)

type handler struct {
	conn  *jsonrpc.Conn
	mode  string
	ready chan struct{}
}

func (h *handler) Request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "hello":
		if h.is("nohello") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if h.is("slowhello") {
			time.Sleep(time.Second)
		}
		name := os.Getenv("FAKE_PLUGIN_NAME")
		if name == "" {
			name = "@precinct/alcove-e2e"
		}
		ui := json.RawMessage(os.Getenv("FAKE_PLUGIN_UI"))
		if len(ui) == 0 {
			ui = json.RawMessage("[]")
		}
		defer close(h.ready)
		return map[string]any{"name": name, "version": "1.0.0", "uiContributions": ui, "hello": params}, nil
	case "launchHook":
		return map[string]any{"env": map[string]string{"FAKE_PLUGIN_TOKEN": "from-hook"}, "profilePatch": []any{}}, nil
	case "onUiAction":
		h.conn.Notify(ctx, "log", map[string]string{"level": "info", "msg": "uiAction " + string(params)})
		return map[string]any{}, nil
	case "onInboxInput":
		h.conn.Notify(ctx, "log", map[string]string{"level": "info", "msg": "inboxInput " + string(params)})
		return map[string]any{}, nil
	case "shutdown":
		if h.is("stubborn") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		go func() { time.Sleep(50 * time.Millisecond); os.Exit(0) }()
		return map[string]any{}, nil
	}
	return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: method}
}

func (h *handler) is(mode string) bool {
	for _, m := range strings.Split(h.mode, ",") {
		if m == mode {
			return true
		}
	}
	return false
}

func (h *handler) Notify(method string, params json.RawMessage) {
	if method == "onWorkspaceEvent" {
		var e struct {
			Kind      string `json:"kind"`
			Workspace struct {
				ID string `json:"id"`
			} `json:"workspace"`
		}
		json.Unmarshal(params, &e)
		h.conn.Notify(context.Background(), "log", map[string]string{"level": "info", "msg": "workspace " + e.Kind + " " + e.Workspace.ID})
	}
}

func main() {
	h := &handler{mode: os.Getenv("FAKE_PLUGIN_MODE"), ready: make(chan struct{})}
	h.conn = jsonrpc.New(os.Stdin, os.Stdout, h)
	if h.is("stubborn") || h.is("child") {
		// A child that inherits stdout, like a tool the plugin started.
		var c *exec.Cmd
		if runtime.GOOS == "windows" {
			c = exec.Command("ping", "-n", "600", "127.0.0.1")
		} else {
			c = exec.Command("sleep", "600")
		}
		c.Stdout = os.Stdout
		c.Start()
		fmt.Fprintf(os.Stderr, "child %d\n", c.Process.Pid)
	}
	go func() {
		<-h.ready
		ctx := context.Background()
		var calls []struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal([]byte(os.Getenv("FAKE_PLUGIN_CALLS")), &calls)
		for i, c := range calls {
			var out json.RawMessage
			err := h.conn.Call(ctx, c.Method, c.Params, &out)
			msg := fmt.Sprintf("result %d ok %s", i, out)
			var je *jsonrpc.Error
			if errors.As(err, &je) {
				msg = fmt.Sprintf("result %d err %d", i, je.Code)
			} else if err != nil {
				msg = fmt.Sprintf("result %d err %v", i, err)
			}
			h.conn.Notify(ctx, "log", map[string]string{"level": "info", "msg": msg})
		}
		h.conn.Notify(ctx, "log", map[string]string{"level": "info", "msg": "calls done"})
		if h.is("crash") {
			time.Sleep(100 * time.Millisecond)
			fmt.Fprintln(os.Stderr, "fake crash")
			os.Exit(3)
		}
	}()
	h.conn.Serve(context.Background())
}
