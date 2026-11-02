//go:build unix

package rex

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fallbackTail is what runtimeDir appends to the resolved temporary root.
func fallbackTail() string { return "/lazyrex-" + strconv.Itoa(os.Getuid()) + "/run" }

// shortDir returns a fresh directory in /tmp, symlinks resolved, with mode,
// skipped when the fallback below it would not fit a socket path.
func shortDir(t *testing.T, mode fs.FileMode) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "l")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	if d, err = filepath.EvalSymlinks(d); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, mode); err != nil {
		t.Fatal(err)
	}
	if len(d)+len(fallbackTail())+len(longestControlName) > maxBindPath {
		t.Skipf("temporary directory %s is too long for socket paths", d)
	}
	return d
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode()
}

func present(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func wantErrContains(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one containing %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("error %q does not contain %q", err, p)
		}
	}
}

// TestControlNameDrift: the length budget counts the name execOpts gives the
// control socket, plus the ".<16 random characters>" a ControlMaster binds
// before renaming it into place.
func TestControlNameDrift(t *testing.T) {
	if !slices.Contains(execOpts("/d"), "ControlPath=/d/ssh-%C") {
		t.Fatalf("execOpts = %q", execOpts("/d"))
	}
	if !strings.HasPrefix(longestControlName, "/ssh-") || len(longestControlName) != len("/ssh-")+40+1+16 {
		t.Fatalf("longestControlName = %q (%d)", longestControlName, len(longestControlName))
	}
	if maxBindPath < 100 {
		t.Fatalf("maxBindPath = %d", maxBindPath)
	}
}

// TestRuntimeDirControlBoundary: the longest XDG_RUNTIME_DIR accepted leaves
// room for the ControlMaster temporary socket, one more character is refused
// before anything is created.
func TestRuntimeDirControlBoundary(t *testing.T) {
	root := shortDir(t, 0o700)
	limit := maxBindPath - len(longestControlName) - len("/lazyrex")
	name := strings.Repeat("x", limit-len(root)-1)
	ok, tooLong := filepath.Join(root, name), filepath.Join(root, name+"y")
	for _, d := range []string{ok, tooLong} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if len(ok) != limit {
		t.Fatalf("len(ok) = %d, want %d", len(ok), limit)
	}
	if dir, err := runtimeDir(ok, "/nonexistent"); err != nil || dir != ok+"/lazyrex" {
		t.Fatalf("at the limit = %q, %v", dir, err)
	}
	_, err := runtimeDir(tooLong, "/nonexistent")
	if err == nil || err.Error() != `XDG_RUNTIME_DIR "`+tooLong+`" is too long for socket paths` {
		t.Fatalf("one over the limit: %v", err)
	}
	if present(tooLong + "/lazyrex") {
		t.Fatal("created a directory too long for the control socket")
	}
}

// TestRuntimeDirXDGWins: a set XDG_RUNTIME_DIR is used, or refused, and
// never falls back to the temporary root.
func TestRuntimeDirXDGWins(t *testing.T) {
	base := shortDir(t, 0o777|fs.ModeSticky)
	parent := filepath.Join(base, filepath.Dir(fallbackTail()))

	xdg := shortDir(t, 0o700)
	if dir, err := runtimeDir(xdg, base); err != nil || dir != xdg+"/lazyrex" {
		t.Fatalf("valid XDG = %q, %v", dir, err)
	}
	missing := filepath.Join(xdg, "missing")
	_, err := runtimeDir(missing, base)
	wantErrContains(t, err, "XDG_RUNTIME_DIR", missing)

	open := shortDir(t, 0o755)
	_, err = runtimeDir(open, base)
	wantErrContains(t, err, open, "accessible to other users")

	if present(parent) {
		t.Fatal("fell back to the temporary root")
	}
}

// TestRuntimeDirFallback: with XDG_RUNTIME_DIR unset, lazyrex uses
// <resolved tmp>/lazyrex-UID/run, both levels private, and only in a
// temporary root nobody else can rename things in.
func TestRuntimeDirFallback(t *testing.T) {
	t.Run("sticky root", func(t *testing.T) {
		base := shortDir(t, 0o777|fs.ModeSticky)
		dir, err := runtimeDir("", base)
		if err != nil || dir != base+fallbackTail() {
			t.Fatalf("runtimeDir = %q, %v", dir, err)
		}
		for _, d := range []string{dir, filepath.Dir(dir)} {
			if m := mode(t, d); !m.IsDir() || m.Perm() != 0o700 {
				t.Fatalf("%s: mode %v", d, m)
			}
		}
		if again, err := runtimeDir("", base); err != nil || again != dir {
			t.Fatalf("second call = %q, %v", again, err)
		}
	})
	t.Run("private root", func(t *testing.T) {
		base := shortDir(t, 0o700)
		if dir, err := runtimeDir("", base); err != nil || dir != base+fallbackTail() {
			t.Fatalf("runtimeDir = %q, %v", dir, err)
		}
	})
	for _, m := range []fs.FileMode{0o777, 0o775, 0o757} {
		t.Run("root "+m.String(), func(t *testing.T) {
			base := shortDir(t, m)
			_, err := runtimeDir("", base)
			wantErrContains(t, err, "XDG_RUNTIME_DIR is not set and the fallback in "+base+" failed", "writable by others and not sticky")
			if present(base + filepath.Dir(fallbackTail())) {
				t.Fatal("created a directory in an unsafe root")
			}
		})
	}
	t.Run("symlinked root", func(t *testing.T) {
		base := shortDir(t, 0o777|fs.ModeSticky)
		link := filepath.Join(shortDir(t, 0o700), "t")
		if err := os.Symlink(base, link); err != nil {
			t.Fatal(err)
		}
		dir, err := runtimeDir("", link)
		if err != nil || dir != base+fallbackTail() {
			t.Fatalf("runtimeDir = %q, %v", dir, err)
		}
		if real, err := filepath.EvalSymlinks(dir); err != nil || real != dir {
			t.Fatalf("%s resolves to %q, %v", dir, real, err)
		}
	})
	t.Run("missing root", func(t *testing.T) {
		missing := filepath.Join(shortDir(t, 0o700), "missing")
		_, err := runtimeDir("", missing)
		wantErrContains(t, err, "XDG_RUNTIME_DIR is not set and the fallback in "+missing+" failed")
	})
	t.Run("root too long", func(t *testing.T) {
		base := shortDir(t, 0o700)
		deep := filepath.Join(base, strings.Repeat("d", maxBindPath-len(longestControlName)-len(fallbackTail())-len(base)))
		if err := os.Mkdir(deep, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := runtimeDir("", deep)
		wantErrContains(t, err, "too long for socket paths")
		if present(deep + filepath.Dir(fallbackTail())) {
			t.Fatal("created a directory too long for socket paths")
		}
	})
	t.Run("recreated after removal", func(t *testing.T) {
		base := shortDir(t, 0o777|fs.ModeSticky)
		dir, err := runtimeDir("", base)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Dir(dir)); err != nil {
			t.Fatal(err)
		}
		if again, err := runtimeDir("", base); err != nil || again != dir || mode(t, dir).Perm() != 0o700 {
			t.Fatalf("after removal = %q, %v", again, err)
		}
	})
}

// TestRuntimeDirFallbackSquat: whatever sits at lazyrex-UID or run before
// lazyrex creates it is refused and left as it is.
func TestRuntimeDirFallbackSquat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, parent string)
		want  string
	}{
		{"parent 0755", func(t *testing.T, parent string) { mkdirMode(t, parent, 0o755) }, "accessible to other users (mode 0755)"},
		{"parent symlink", func(t *testing.T, parent string) {
			if err := os.Symlink(shortDir(t, 0o700), parent); err != nil {
				t.Fatal(err)
			}
		}, "is or goes through a symlink"},
		{"parent file", func(t *testing.T, parent string) {
			if err := os.WriteFile(parent, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "is not a directory"},
		{"run symlink", func(t *testing.T, parent string) {
			mkdirMode(t, parent, 0o700)
			if err := os.Symlink(shortDir(t, 0o700), parent+"/run"); err != nil {
				t.Fatal(err)
			}
		}, "/run is a symlink"},
		{"run 0755", func(t *testing.T, parent string) {
			mkdirMode(t, parent, 0o700)
			mkdirMode(t, parent+"/run", 0o755)
		}, "/run is accessible to other users (mode 0755)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := shortDir(t, 0o777|fs.ModeSticky)
			parent := base + filepath.Dir(fallbackTail())
			tc.plant(t, parent)
			before := mode(t, parent)
			_, err := runtimeDir("", base)
			wantErrContains(t, err, "XDG_RUNTIME_DIR is not set and the fallback in "+base+" failed", tc.want)
			if after := mode(t, parent); after != before {
				t.Fatalf("mode changed from %v to %v", before, after)
			}
		})
	}
}

func mkdirMode(t *testing.T, dir string, m fs.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, m); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, m); err != nil {
		t.Fatal(err)
	}
}

// TestTmpDirProblem: owners and modes a test cannot create on disk.
func TestTmpDirProblem(t *testing.T) {
	me := os.Getuid()
	foreign := me + 1
	if foreign == 0 {
		foreign = 2
	}
	for _, tc := range []struct {
		name  string
		mode  fs.FileMode
		owner int
		want  string
	}{
		{"root sticky", fs.ModeDir | fs.ModeSticky | 0o777, 0, ""},
		{"mine sticky", fs.ModeDir | fs.ModeSticky | 0o777, me, ""},
		{"mine private", fs.ModeDir | 0o700, me, ""},
		{"root 0755", fs.ModeDir | 0o755, 0, ""},
		{"foreign sticky", fs.ModeDir | fs.ModeSticky | 0o777, foreign, "/t is not owned by root or the current user"},
		{"foreign private", fs.ModeDir | 0o700, foreign, "/t is not owned by root or the current user"},
		{"unknown owner", fs.ModeDir | 0o700, -1, "/t is not owned by root or the current user"},
		{"root 0777", fs.ModeDir | 0o777, 0, "/t is writable by others and not sticky"},
		{"root 0770", fs.ModeDir | 0o770, 0, "/t is writable by others and not sticky"},
		{"file", 0o600, me, "/t is not a directory"},
		{"symlink", fs.ModeSymlink | 0o777, me, "/t is not a directory"},
	} {
		err := tmpDirProblem("/t", tc.mode, tc.owner)
		if got := errText(err); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPrivateDirProblem: the owner check of every private directory.
func TestPrivateDirProblem(t *testing.T) {
	me := os.Getuid()
	for _, tc := range []struct {
		name  string
		mode  fs.FileMode
		owner int
		want  string
	}{
		{"mine", fs.ModeDir | 0o700, me, ""},
		{"foreign", fs.ModeDir | 0o700, me + 1, "/p is not owned by the current user"},
		{"unknown owner", fs.ModeDir | 0o700, -1, "/p is not owned by the current user"},
		{"group", fs.ModeDir | 0o750, me, "/p is accessible to other users (mode 0750)"},
		{"symlink", fs.ModeSymlink | 0o700, me, "/p is a symlink"},
		{"file", 0o600, me, "/p is not a directory"},
	} {
		if got := errText(privateDirProblem("/p", tc.mode, tc.owner)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestOSSocketsFallback: socket preparation revalidates a fallback directory
// like an XDG one.
func TestOSSocketsFallback(t *testing.T) {
	dir, err := runtimeDir("", shortDir(t, 0o777|fs.ModeSticky))
	if err != nil {
		t.Fatal(err)
	}
	var s OSSockets
	sock := filepath.Join(dir, "fwd-42-1.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if err := s.Prepare(dir, sock); err != nil || present(sock) {
		t.Fatalf("stale socket: %v, present %v", err, present(sock))
	}
	if err := os.Chmod(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(dir, sock); err == nil || !strings.Contains(err.Error(), "accessible to other users") {
		t.Fatalf("Prepare with an open parent: %v", err)
	}
}
