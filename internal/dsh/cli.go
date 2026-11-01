// Package dsh runs the bundled dsh command line: version queries, profile
// package operations (`dsh plugin`) and `--dump-config` checks.
//
// Every call runs non-interactively (stdin closed), so dsh never prompts for
// build-script approval or version exemptions; a refusal comes back as an
// error that carries dsh's own diagnostic lines for the UI.
package dsh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CLI locates the bundled runtime. Node is the node executable, Bin the dsh
// entry script (node_modules/@deepseek-ai/dsh/lib/bin.js) and Home the dsh home
// (DSH_HOME). ToolDirs are prepended to PATH for every call; the full build puts
// the bundled pnpm there, because `dsh plugin` runs the `pnpm` found on PATH.
type CLI struct {
	Node     string
	Bin      string
	Home     string
	ToolDirs []string
	// Env holds extra KEY=VALUE pairs for every call.
	Env []string
	// Timeout bounds each call; zero means 5 minutes.
	Timeout time.Duration
}

// Result is one finished dsh call.
type Result struct {
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
}

// Error is a dsh call that exited non-zero.
type Error struct {
	Result
}

func (e *Error) Error() string {
	msg := strings.Join(e.Diagnostics(), "; ")
	if msg == "" {
		msg = lastLines(e.Stderr, 3)
	} else {
		// dsh's own lines can be as vague as "plugin command failed"; keep
		// the last stderr lines that are not repeats of them.
		var extra []string
		for _, l := range e.StderrTail(3) {
			if !strings.HasPrefix(l, "dsh: ") {
				extra = append(extra, l)
			}
		}
		// pnpm prints its error code (ERR_PNPM_...) on stdout, which dsh
		// passes through instead of logging.
		for _, l := range strings.Split(e.Stdout, "\n") {
			if l = strings.TrimSpace(l); strings.Contains(l, "ERR_PNPM_") {
				extra = append(extra, l)
				break
			}
		}
		if len(extra) > 0 {
			msg += " | " + strings.Join(extra, " | ")
		}
	}
	return fmt.Sprintf("dsh %s: exit %d: %s", strings.Join(e.Args, " "), e.ExitCode, msg)
}

// Diagnostics returns dsh's own "dsh: ..." lines, which explain a refusal in
// words a user can act on (for example an incompatible engines.dsh range).
func (r Result) Diagnostics() []string {
	var out []string
	for _, line := range strings.Split(r.Stdout+"\n"+r.Stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "dsh: ") {
			out = append(out, strings.TrimPrefix(line, "dsh: "))
		}
	}
	return out
}

// StderrTail returns the last n meaningful stderr lines (stack frames and the
// Node.js version banner dropped), for failures dsh does not explain with its
// own "dsh: ..." lines.
func (r Result) StderrTail(n int) []string {
	var lines []string
	for _, l := range strings.Split(r.Stderr, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "at ") || strings.HasPrefix(l, "Node.js v") || l == "^" {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// IncompatibleVersion reports whether dsh refused a package because its
// engines.dsh (or dsh peer) range does not cover the runtime version.
func (r Result) IncompatibleVersion() bool {
	for _, d := range r.Diagnostics() {
		if strings.Contains(d, "is incompatible with dsh") {
			return true
		}
	}
	return false
}

// Run executes dsh with args and returns its result; a non-zero exit is an *Error.
func (c *CLI) Run(ctx context.Context, args ...string) (Result, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Node, append([]string{c.Bin}, args...)...)
	cmd.Env = c.Environ()
	cmd.Stdin = nil // closed: dsh must not prompt
	// On timeout, end the whole process tree (dsh runs pnpm), and stop
	// waiting for output pipes a stray descendant may still hold.
	OwnProcessGroup(cmd)
	cmd.Cancel = func() error { return KillTree(cmd) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Args: args, Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			res.ExitCode = exit.ExitCode()
			return res, &Error{res}
		}
		return res, fmt.Errorf("dsh %s: %w", strings.Join(args, " "), err)
	}
	return res, nil
}

// Environ is the environment every dsh process gets: PATH with the bundled
// tools first, DSH_HOME, and Env.
func (c *CLI) Environ() []string {
	env := os.Environ()
	path := os.Getenv("PATH")
	if len(c.ToolDirs) > 0 {
		path = strings.Join(c.ToolDirs, string(os.PathListSeparator)) + string(os.PathListSeparator) + path
	}
	env = setEnv(env, "PATH", path)
	env = setEnv(env, "DSH_HOME", c.Home)
	// pnpm's update notice is noise in captured output.
	env = setEnv(env, "NO_UPDATE_NOTIFIER", "1")
	env = setEnv(env, "npm_config_update_notifier", "false")
	for _, kv := range c.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env = setEnv(env, k, v)
		}
	}
	return env
}

// Version returns the runtime version `dsh --version` prints, the version
// engines.dsh ranges are checked against.
func (c *CLI) Version(ctx context.Context) (string, error) {
	res, err := c.Run(ctx, "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// PluginAdd installs spec (here always an absolute .tgz path inside Alcove's
// package store) into profile, creating the profile on first use.
func (c *CLI) PluginAdd(ctx context.Context, profile, spec string) (Result, error) {
	return c.Run(ctx, "plugin", "--profile", profile, "add", spec)
}

// PluginRemove removes the named package from profile.
func (c *CLI) PluginRemove(ctx context.Context, profile, name string) (Result, error) {
	return c.Run(ctx, "plugin", "--profile", profile, "remove", name)
}

// DumpConfig composes profile without booting it; an error means the profile
// would not start.
func (c *CLI) DumpConfig(ctx context.Context, profile string) (Result, error) {
	return c.Run(ctx, "--profile", profile, "--dump-config")
}

// ProfileDir is the directory of a profile under the dsh home.
func (c *CLI) ProfileDir(profile string) string {
	return filepath.Join(c.Home, "profiles", profile)
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(strings.ToUpper(kv), strings.ToUpper(prefix)) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
