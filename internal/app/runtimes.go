package app

import (
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// RunningRuntimes lists the running runtimes, for the runtime panel.
func (a *App) RunningRuntimes() []alrt.Info { return a.Runtimes.List() }

// StopRuntime stops one runtime, whatever it is doing; its sessions can be
// resumed on the next message.
func (a *App) StopRuntime(k alrt.Key) error { return a.Runtimes.Stop(k) }

// PinRuntime keeps k's runtime from exiting when idle, until unpinned or
// Alcove quits.
func (a *App) PinRuntime(k alrt.Key, on bool) { a.Runtimes.Pin(k, on) }
