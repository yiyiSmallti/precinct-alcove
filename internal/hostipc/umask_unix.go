//go:build !windows

package hostipc

import "syscall"

func umask(mask int) int { return syscall.Umask(mask) }
