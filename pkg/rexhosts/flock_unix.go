//go:build unix && !solaris && !illumos && !aix

package rexhosts

import (
	"os"
	"syscall"
)

// tryLock takes an exclusive flock on f without waiting. flock, unlike fcntl
// locks, conflicts between two opens in one process too.
func tryLock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
