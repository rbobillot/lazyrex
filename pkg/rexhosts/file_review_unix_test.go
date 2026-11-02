//go:build unix && !solaris && !illumos && !aix

package rexhosts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWriteDirSync: a failed sync of the directory after the rename is no
// success, though the file is already replaced; a filesystem that cannot
// sync a directory at all is not a failure.
func TestWriteDirSync(t *testing.T) {
	for _, tc := range []struct {
		err     error
		synced  bool
		wantErr bool
	}{
		{syscall.EIO, false, true},
		{syscall.EINVAL, true, false},
		{syscall.ENOTSUP, true, false},
	} {
		p := mkdir(t, testPaths(t))
		hook(t, &syncDir, func(*os.File) error { return &fs.PathError{Op: "sync", Path: p.Dir, Err: tc.err} })
		err := Write(p, ours("b\n"), Snapshot{State: Absent})
		if got := errors.Is(err, ErrNotSynced); got != tc.wantErr || (err != nil) != tc.wantErr {
			t.Fatalf("sync error %v: Write = %v", tc.err, err)
		}
		if read(t, p.Hosts) != string(ours("b\n")) {
			t.Fatalf("sync error %v: file = %q", tc.err, read(t, p.Hosts))
		}
		if tmp := temps(t, p.Dir); len(tmp) != 0 {
			t.Fatalf("sync error %v: temporary files left: %q", tc.err, tmp)
		}
	}
}

// TestWriteSymlinkSwap: a symlink planted at the file between its Lstat and
// its open is never followed, even to a hard link of the very file, which
// would pass the identity and hash checks.
func TestWriteSymlinkSwap(t *testing.T) {
	for _, at := range []int{1, 2} {
		p := mkdir(t, testPaths(t))
		put(t, p.Hosts, ours("a\n"), 0o600)
		snap := mustRead(t, p)
		other := filepath.Join(p.Dir, "other.lua")
		opens := 0
		hook(t, &beforeOpen, func() {
			if opens++; opens != at {
				return
			}
			if err := os.Link(p.Hosts, other); err != nil {
				t.Error(err)
			}
			if err := os.Remove(p.Hosts); err != nil {
				t.Error(err)
			}
			if err := os.Symlink("other.lua", p.Hosts); err != nil {
				t.Error(err)
			}
		})
		if err := Write(p, ours("b\n"), snap); !errors.Is(err, ErrChanged) {
			t.Fatalf("swap at open %d: Write = %v", at, err)
		}
		if fi, err := os.Lstat(p.Hosts); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Fatalf("swap at open %d: the symlink was replaced: %v, %v", at, fi, err)
		}
		if read(t, other) != string(ours("a\n")) {
			t.Fatalf("swap at open %d: target = %q", at, read(t, other))
		}
		if tmp := temps(t, p.Dir); len(tmp) != 0 {
			t.Fatalf("swap at open %d: temporary files left: %q", at, tmp)
		}
		if s, err := Read(p); err != nil || s.State != Foreign {
			t.Fatalf("swap at open %d: Read = %+v, %v", at, s, err)
		}
	}
}
