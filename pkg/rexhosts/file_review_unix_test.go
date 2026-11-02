//go:build unix && !solaris && !illumos && !aix

package rexhosts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

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
