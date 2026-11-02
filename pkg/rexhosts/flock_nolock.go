//go:build solaris || illumos || aix

package rexhosts

import (
	"errors"
	"os"
)

func tryLock(*os.File) error { return errors.New("flock is not available on this system") }
