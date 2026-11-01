//go:build unix

package config

import (
	"os"
	"syscall"
	"testing"
)

// TestEditDirSyncUnsupportedErrno (6e): the errnos of a filesystem that
// cannot sync a directory are not errors.
func TestEditDirSyncUnsupportedErrno(t *testing.T) {
	for name, e := range map[string]syscall.Errno{"ENOTSUP": syscall.ENOTSUP, "EOPNOTSUPP": syscall.EOPNOTSUPP, "ENOSYS": syscall.ENOSYS} {
		t.Run(name, func(t *testing.T) {
			expectSyncIgnored(t, &os.PathError{Op: "sync", Path: "lazyrex", Err: e})
		})
	}
}
