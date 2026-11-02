//go:build unix && !solaris && !illumos && !aix

package rexhosts

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "rex")
	return Paths{Dir: dir, Hosts: filepath.Join(dir, rex.HostsFileName), Init: filepath.Join(dir, "init.lua")}
}

func mkdir(t *testing.T, p Paths) Paths {
	t.Helper()
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func ours(body string) []byte { return []byte(rex.HostsFileMarker + "\n" + body) }

func put(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hook sets *v to x for the test.
func hook[T any](t *testing.T, v *T, x T) {
	old := *v
	*v = x
	t.Cleanup(func() { *v = old })
}

func otherOwner(t *testing.T, which string) {
	hook(t, &ownerOf, func(what string, fi fs.FileInfo) int {
		if what == which {
			return os.Getuid() + 1
		}
		return statOwner(fi)
	})
}

// TestRead (H15, H65, A11, G-SF3, M-N1, M-B5.3): only an exact v1 first line
// in a regular file of the user's own makes the file lazyrex's; a missing
// directory is an absent file; an unsafe directory is an error.
func TestRead(t *testing.T) {
	big := append(ours(""), make([]byte, 2<<20)...)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, p Paths)
		state State
		why   string
	}{
		{"absent", func(*testing.T, Paths) {}, Absent, ""},
		{"exact v1", func(t *testing.T, p Paths) { put(t, p.Hosts, ours("rex.host({})\n"), 0o600) }, Ours, ""},
		{"marker without newline", func(t *testing.T, p Paths) { put(t, p.Hosts, []byte(rex.HostsFileMarker), 0o600) }, Ours, ""},
		{"edited body", func(t *testing.T, p Paths) { put(t, p.Hosts, ours("print('mine')\n"), 0o644) }, Ours, ""},
		{"v2", func(t *testing.T, p Paths) {
			put(t, p.Hosts, []byte(strings.Replace(rex.HostsFileMarker, "v1", "v2", 1)+"\n"), 0o600)
		}, Foreign, "first line"},
		{"virus", func(t *testing.T, p Paths) { put(t, p.Hosts, []byte("-- lazyrex-hosts virus\n"), 0o600) }, Foreign, "first line"},
		{"view", func(t *testing.T, p Paths) { put(t, p.Hosts, []byte("-- lazyrex-hosts view\n"), 0o600) }, Foreign, "first line"},
		{"crlf", func(t *testing.T, p Paths) { put(t, p.Hosts, []byte(rex.HostsFileMarker+"\r\n"), 0o600) }, Foreign, "first line"},
		{"empty", func(t *testing.T, p Paths) { put(t, p.Hosts, nil, 0o600) }, Foreign, "first line"},
		{"human's", func(t *testing.T, p Paths) { put(t, p.Hosts, []byte("rex.host({ label = \"x\" })\n"), 0o600) }, Foreign, "first line"},
		{"symlink to ours", func(t *testing.T, p Paths) {
			put(t, p.Hosts+".real", ours(""), 0o600)
			if err := os.Symlink(p.Hosts+".real", p.Hosts); err != nil {
				t.Fatal(err)
			}
		}, Foreign, "symlink"},
		{"directory", func(t *testing.T, p Paths) { mkdir(t, Paths{Dir: p.Hosts}) }, Foreign, "not a regular file"},
		{"fifo", func(t *testing.T, p Paths) {
			if err := syscall.Mkfifo(p.Hosts, 0o600); err != nil {
				t.Fatal(err)
			}
		}, Foreign, "not a regular file"},
		{"other owner", func(t *testing.T, p Paths) { put(t, p.Hosts, ours(""), 0o600); otherOwner(t, "file") }, Foreign, "not yours"},
		{"2 MiB", func(t *testing.T, p Paths) { put(t, p.Hosts, big, 0o600) }, Foreign, "larger than 1 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mkdir(t, testPaths(t))
			tc.setup(t, p)
			done := make(chan struct{})
			var snap Snapshot
			var err error
			go func() {
				defer close(done)
				snap, err = Read(p)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Read blocked")
			}
			if err != nil || snap.State != tc.state || !strings.Contains(snap.Why, tc.why) {
				t.Fatalf("Read = %+v, %v, want %v with %q", snap, err, tc.state, tc.why)
			}
			if tc.state == Ours {
				if want := sha256.Sum256([]byte(read(t, p.Hosts))); snap.Sum != want {
					t.Fatalf("sum %x, want %x", snap.Sum, want)
				}
			}
		})
	}
	t.Run("missing directory", func(t *testing.T) {
		p := testPaths(t)
		if snap, err := Read(p); err != nil || snap.State != Absent {
			t.Fatalf("Read = %+v, %v", snap, err)
		}
		if _, err := os.Lstat(p.Dir); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read created the directory: %v", err)
		}
	})
	t.Run("directory open to others", func(t *testing.T) {
		p := mkdir(t, testPaths(t))
		if err := os.Chmod(p.Dir, 0o775); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(p); !errors.Is(err, ErrUnsafeDir) {
			t.Fatalf("Read = %v", err)
		}
	})
	t.Run("directory not yours", func(t *testing.T) {
		p := mkdir(t, testPaths(t))
		otherOwner(t, "dir")
		if _, err := Read(p); !errors.Is(err, ErrUnsafeDir) {
			t.Fatalf("Read = %v", err)
		}
	})
	t.Run("directory symlinked to a private one", func(t *testing.T) {
		real := mkdir(t, testPaths(t))
		put(t, real.Hosts, ours(""), 0o600)
		link := filepath.Join(filepath.Dir(filepath.Dir(real.Dir)), "linked")
		if err := os.Symlink(real.Dir, link); err != nil {
			t.Fatal(err)
		}
		p := Paths{Dir: link, Hosts: filepath.Join(link, rex.HostsFileName)}
		if snap, err := Read(p); err != nil || snap.State != Ours {
			t.Fatalf("Read = %+v, %v", snap, err)
		}
	})
}

func mustRead(t *testing.T, p Paths) Snapshot {
	t.Helper()
	snap, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func temps(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestWrite (H16, M-B5.4, G-SF2): the file is replaced only when it is still
// what lazyrex read, through a private temporary file, in a directory of the
// user's own.
func TestWrite(t *testing.T) {
	data := ours("rex.host({ label = \"edge-lazyrex\", endpoint = \"unix:///x.sock\" })\n")
	t.Run("absent, missing directory", func(t *testing.T) {
		p := testPaths(t)
		if err := Write(p, data, mustRead(t, p)); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p.Dir)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir %v, %v", fi, err)
		}
		fi, err = os.Lstat(p.Hosts)
		if err != nil || fi.Mode() != 0o600 || read(t, p.Hosts) != string(data) {
			t.Fatalf("file %v, %v", fi, err)
		}
		if tmp := temps(t, p.Dir); len(tmp) != 0 {
			t.Fatalf("temporary files left: %q", tmp)
		}
	})
	t.Run("ours 0644", func(t *testing.T) {
		p := mkdir(t, testPaths(t))
		put(t, p.Hosts, ours("old\n"), 0o644)
		if err := Write(p, data, mustRead(t, p)); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Lstat(p.Hosts); fi.Mode() != 0o600 || read(t, p.Hosts) != string(data) {
			t.Fatalf("file %v %q", fi.Mode(), read(t, p.Hosts))
		}
	})
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, p Paths) Snapshot
		target error
	}{
		{"foreign", func(t *testing.T, p Paths) Snapshot {
			put(t, p.Hosts, []byte("mine\n"), 0o600)
			return mustRead(t, p)
		}, ErrForeign},
		{"foreign, forged snapshot", func(t *testing.T, p Paths) Snapshot {
			put(t, p.Hosts, []byte("mine\n"), 0o600)
			return Snapshot{State: Ours, Sum: sha256.Sum256([]byte("mine\n"))}
		}, ErrForeign},
		{"symlink", func(t *testing.T, p Paths) Snapshot {
			put(t, p.Hosts+".real", ours(""), 0o600)
			if err := os.Symlink(p.Hosts+".real", p.Hosts); err != nil {
				t.Fatal(err)
			}
			return Snapshot{State: Ours, Sum: sha256.Sum256(ours(""))}
		}, ErrForeign},
		{"sum changed", func(t *testing.T, p Paths) Snapshot {
			put(t, p.Hosts, ours("a\n"), 0o600)
			snap := mustRead(t, p)
			put(t, p.Hosts, ours("b\n"), 0o600)
			return snap
		}, ErrChanged},
		{"appeared since absent", func(t *testing.T, p Paths) Snapshot {
			snap := mustRead(t, p)
			put(t, p.Hosts, ours("b\n"), 0o600)
			return snap
		}, ErrChanged},
		{"gone since read", func(t *testing.T, p Paths) Snapshot {
			put(t, p.Hosts, ours("a\n"), 0o600)
			snap := mustRead(t, p)
			os.Remove(p.Hosts)
			return snap
		}, ErrChanged},
		{"dangling symlink", func(t *testing.T, p Paths) Snapshot {
			if err := os.Symlink(filepath.Join(p.Dir, "nowhere"), p.Hosts); err != nil {
				t.Fatal(err)
			}
			return Snapshot{State: Absent}
		}, ErrForeign},
		{"directory 0775", func(t *testing.T, p Paths) Snapshot {
			if err := os.Chmod(p.Dir, 0o775); err != nil {
				t.Fatal(err)
			}
			return Snapshot{State: Absent}
		}, ErrUnsafeDir},
		{"directory not yours", func(t *testing.T, p Paths) Snapshot {
			otherOwner(t, "dir")
			return Snapshot{State: Absent}
		}, ErrUnsafeDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mkdir(t, testPaths(t))
			seen := tc.setup(t, p)
			before, _ := os.ReadDir(p.Dir)
			err := Write(p, data, seen)
			if !errors.Is(err, tc.target) {
				t.Fatalf("Write = %v, want %v", err, tc.target)
			}
			after, _ := os.ReadDir(p.Dir)
			if len(after) != len(before) {
				t.Fatalf("directory changed: %v -> %v", before, after)
			}
			if b, err := os.ReadFile(p.Hosts); err == nil && string(b) == string(data) {
				t.Fatal("the file was written")
			}
			if _, err := os.Lstat(filepath.Join(p.Dir, "nowhere")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("wrote through the dangling symlink")
			}
		})
	}
	t.Run("foreign why", func(t *testing.T) {
		p := mkdir(t, testPaths(t))
		put(t, p.Hosts, []byte("mine\n"), 0o600)
		var fe *ForeignError
		if err := Write(p, data, Snapshot{State: Ours}); !errors.As(err, &fe) || !strings.Contains(fe.Why, "first line") {
			t.Fatalf("Write = %v", err)
		}
	})
	t.Run("directory symlinked to a private one", func(t *testing.T) {
		real := mkdir(t, testPaths(t))
		link := filepath.Join(filepath.Dir(filepath.Dir(real.Dir)), "linked")
		if err := os.Symlink(real.Dir, link); err != nil {
			t.Fatal(err)
		}
		p := Paths{Dir: link, Hosts: filepath.Join(link, rex.HostsFileName)}
		if err := Write(p, data, mustRead(t, p)); err != nil {
			t.Fatal(err)
		}
		if read(t, real.Hosts) != string(data) {
			t.Fatal("not written in the real directory")
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Fatalf("link replaced: %v, %v", fi, err)
		}
	})
}

// TestWriteTwoWriters (H17, G-B4): two writers that read the same snapshot
// are serialized by the lock; the second one sees the first one's file and
// refuses.
func TestWriteTwoWriters(t *testing.T) {
	p := mkdir(t, testPaths(t))
	a, b := ours("a\n"), ours("b\n")
	snap := mustRead(t, p)
	waiting := make(chan struct{})
	var once sync.Once
	hook(t, &lockBusy, func() { once.Do(func() { close(waiting) }) })
	errB := make(chan error, 1)
	var rename sync.Once
	hook(t, &beforeRename, func() {
		rename.Do(func() {
			go func() { errB <- Write(p, b, snap) }()
			select {
			case <-waiting:
			case <-time.After(5 * time.Second):
				t.Error("the second writer never waited on the lock")
			}
		})
	})
	if err := Write(p, a, snap); err != nil {
		t.Fatalf("first writer: %v", err)
	}
	if err := <-errB; !errors.Is(err, ErrChanged) {
		t.Fatalf("second writer: %v", err)
	}
	if read(t, p.Hosts) != string(a) {
		t.Fatalf("file = %q", read(t, p.Hosts))
	}
}

// TestWriteBusy (H18): a lock held longer than lockWait gives ErrBusy.
func TestWriteBusy(t *testing.T) {
	if lockWait != 2*time.Second || lockPoll != 50*time.Millisecond {
		t.Fatalf("lock wait %v, poll %v", lockWait, lockPoll)
	}
	hook(t, &lockWait, 300*time.Millisecond)
	p := mkdir(t, testPaths(t))
	d, err := os.Open(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := syscall.Flock(int(d.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = Write(p, ours(""), mustRead(t, p))
	if !errors.Is(err, ErrBusy) || time.Since(start) < lockWait {
		t.Fatalf("Write = %v after %v", err, time.Since(start))
	}
	if _, err := os.Lstat(p.Hosts); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("written while locked")
	}
}

// TestWriteRace (H19): concurrent writers and readers; a reader only ever
// sees a whole v1 file, and no temporary file is left.
func TestWriteRace(t *testing.T) {
	p := mkdir(t, testPaths(t))
	var valid []string
	for i := range 8 {
		valid = append(valid, string(ours(fmt.Sprintf("-- writer %d\n%s", i, strings.Repeat("x", 4096)))))
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := range 8 {
		wg.Go(func() {
			for range 20 {
				snap, err := Read(p)
				if err != nil {
					t.Error(err)
					return
				}
				if err := Write(p, []byte(valid[i]), snap); err != nil && !errors.Is(err, ErrChanged) && !errors.Is(err, ErrBusy) {
					t.Error(err)
					return
				}
			}
		})
	}
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				b, err := os.ReadFile(p.Hosts)
				if err == nil && !slices.Contains(valid, string(b)) {
					t.Errorf("read a partial file of %d bytes", len(b))
					return
				}
				if snap, err := Read(p); err != nil || snap.State == Foreign {
					t.Errorf("Read = %+v, %v", snap, err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	if tmp := temps(t, p.Dir); len(tmp) != 0 {
		t.Fatalf("temporary files left: %q", tmp)
	}
}

// TestWriteExternalReplace (H61, A7, G2-S1): an editor replacing the file
// while lazyrex writes is caught right before the rename.
func TestWriteExternalReplace(t *testing.T) {
	p := mkdir(t, testPaths(t))
	put(t, p.Hosts, ours("a\n"), 0o600)
	snap := mustRead(t, p)
	edited := ours("edited by hand\n")
	hook(t, &beforeRename, func() {
		put(t, p.Hosts+".edit", edited, 0o600)
		if err := os.Rename(p.Hosts+".edit", p.Hosts); err != nil {
			t.Error(err)
		}
	})
	if err := Write(p, ours("b\n"), snap); !errors.Is(err, ErrChanged) {
		t.Fatalf("Write = %v", err)
	}
	if read(t, p.Hosts) != string(edited) {
		t.Fatalf("file = %q", read(t, p.Hosts))
	}
	if tmp := temps(t, p.Dir); len(tmp) != 0 {
		t.Fatalf("temporary files left: %q", tmp)
	}
}

// TestWriteTemp (H63, A9, G2-S3): the temporary file has a random name, so a
// leftover of a crashed run never blocks a write.
func TestWriteTemp(t *testing.T) {
	p := mkdir(t, testPaths(t))
	leftovers := []string{".lazyrex-hosts-42.tmp", ".lazyrex-hosts-" + strings.Repeat("0", 16) + ".tmp"}
	for _, l := range leftovers {
		put(t, filepath.Join(p.Dir, l), []byte("crash"), 0o600)
	}
	name := regexp.MustCompile(`^\.lazyrex-hosts-[0-9a-f]{16}\.tmp$`)
	var during []string
	hook(t, &beforeRename, func() { during = temps(t, p.Dir) })
	if err := Write(p, ours(""), mustRead(t, p)); err != nil {
		t.Fatal(err)
	}
	fresh := slices.DeleteFunc(during, func(n string) bool { return slices.Contains(leftovers, n) })
	if len(fresh) != 1 || !name.MatchString(fresh[0]) {
		t.Fatalf("temporary files during the write: %q", during)
	}
	if left := temps(t, p.Dir); len(left) != len(leftovers) {
		t.Fatalf("temporary files after: %q", left)
	}
}
