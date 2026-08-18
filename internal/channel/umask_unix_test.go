//go:build !windows

package channel

import "syscall"

func syscallUmask(m int) int { return syscall.Umask(m) }
