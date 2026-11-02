//go:build unix

package rex_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

// privateDir returns a short private temporary directory without symlinks.
// It lives in /tmp rather than $TMPDIR, which is too long for socket paths on
// macOS.
func privateDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "lzr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	if d, err = filepath.EvalSymlinks(d); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if len(d) > 33 {
		t.Skipf("temporary directory %s is too long for socket paths", d)
	}
	return d
}

// wantErr fails unless err's text is exactly want.
func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// TestPrepareRuntimeDir (1m): a set XDG_RUNTIME_DIR is used as is or
// refused, with the same messages as before the /tmp fallback existed.
func TestPrepareRuntimeDir(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		xdg := privateDir(t)
		dir, err := rex.PrepareRuntimeDir(xdg)
		if err != nil || dir != filepath.Join(xdg, "lazyrex") {
			t.Fatalf("PrepareRuntimeDir = %q, %v", dir, err)
		}
		if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Fatalf("lazyrex dir: %v %v", st, err)
		}
		if again, err := rex.PrepareRuntimeDir(xdg); err != nil || again != dir {
			t.Fatalf("second call = %q, %v", again, err)
		}
	})
	long := "/" + strings.Repeat("x", 60)
	for _, tc := range []struct{ xdg, want string }{
		{"run/user/1000", `XDG_RUNTIME_DIR "run/user/1000" is not a clean absolute path`},
		{"/run/user/../user/1000", `XDG_RUNTIME_DIR "/run/user/../user/1000" is not a clean absolute path`},
		{"/tmp/a:b", `XDG_RUNTIME_DIR "/tmp/a:b" contains a character ssh options cannot hold`},
		{"/tmp/a%b", `XDG_RUNTIME_DIR "/tmp/a%b" contains a character ssh options cannot hold`},
		{"/tmp/a b", `XDG_RUNTIME_DIR "/tmp/a b" contains a character ssh options cannot hold`},
		{"/tmp/a~b", `XDG_RUNTIME_DIR "/tmp/a~b" contains a character ssh options cannot hold`},
		{"/tmp/a\nb", `XDG_RUNTIME_DIR "/tmp/a\nb" contains a character ssh options cannot hold`},
		{long, `XDG_RUNTIME_DIR "` + long + `" is too long for socket paths`},
	} {
		dir, err := rex.PrepareRuntimeDir(tc.xdg)
		if err == nil || err.Error() != tc.want {
			t.Errorf("PrepareRuntimeDir(%q) = %q, %v, want %q", tc.xdg, dir, err, tc.want)
		}
	}
	t.Run("parent 0755", func(t *testing.T) {
		xdg := privateDir(t)
		if err := os.Chmod(xdg, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := rex.PrepareRuntimeDir(xdg)
		wantErr(t, err, xdg+" is accessible to other users (mode 0755)")
	})
	t.Run("parent symlinked", func(t *testing.T) {
		target := privateDir(t)
		link := filepath.Join(privateDir(t), "l")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		_, err := rex.PrepareRuntimeDir(link)
		wantErr(t, err, `XDG_RUNTIME_DIR "`+link+`" is or goes through a symlink`)
	})
	t.Run("parent missing", func(t *testing.T) {
		missing := filepath.Join(privateDir(t), "missing")
		_, err := rex.PrepareRuntimeDir(missing)
		wantErr(t, err, `XDG_RUNTIME_DIR "`+missing+`" is or goes through a symlink`)
	})
	t.Run("lazyrex symlink", func(t *testing.T) {
		xdg := privateDir(t)
		if err := os.Symlink(privateDir(t), filepath.Join(xdg, "lazyrex")); err != nil {
			t.Fatal(err)
		}
		_, err := rex.PrepareRuntimeDir(xdg)
		wantErr(t, err, xdg+"/lazyrex is a symlink")
	})
	t.Run("lazyrex 0755", func(t *testing.T) {
		xdg := privateDir(t)
		if err := os.Mkdir(filepath.Join(xdg, "lazyrex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(xdg, "lazyrex"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := rex.PrepareRuntimeDir(xdg)
		wantErr(t, err, xdg+"/lazyrex is accessible to other users (mode 0755)")
	})
	t.Run("lazyrex regular file", func(t *testing.T) {
		xdg := privateDir(t)
		if err := os.WriteFile(filepath.Join(xdg, "lazyrex"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := rex.PrepareRuntimeDir(xdg)
		wantErr(t, err, xdg+"/lazyrex is not a directory")
	})
	t.Run("recreated after removal", func(t *testing.T) {
		xdg := privateDir(t)
		dir, err := rex.PrepareRuntimeDir(xdg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if again, err := rex.PrepareRuntimeDir(xdg); err != nil || again != dir || !exists(dir) {
			t.Fatalf("after removal = %q, %v", again, err)
		}
	})
}

// staleSocket leaves a socket file at path with no listener.
func staleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// TestOSSockets (decision 7): only a socket of the current user in a valid
// runtime directory is ever removed.
func TestOSSockets(t *testing.T) {
	dir, err := rex.PrepareRuntimeDir(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var s rex.OSSockets
	sock := filepath.Join(dir, "fwd-42-1.sock")

	staleSocket(t, sock)
	if err := s.Prepare(dir, sock); err != nil || exists(sock) {
		t.Fatalf("stale socket: %v, exists %v", err, exists(sock))
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(dir, sock); err == nil {
		t.Fatal("Prepare replaced a regular file")
	}
	s.Cleanup(sock)
	if !exists(sock) {
		t.Fatal("Cleanup removed a regular file")
	}
	os.Remove(sock)
	if err := s.Prepare(dir, "/tmp/elsewhere.sock"); err == nil {
		t.Fatal("Prepare accepted a socket outside the directory")
	}

	staleSocket(t, sock)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.Cleanup(sock)
	if !exists(sock) {
		t.Fatal("Cleanup removed a socket from a directory that failed revalidation")
	}
	if err := s.Prepare(dir, sock); err == nil {
		t.Fatal("Prepare accepted a directory open to others")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s.Cleanup(sock)
	if exists(sock) {
		t.Fatal("Cleanup left its own socket")
	}

	exited := make(chan struct{})
	close(exited)
	if s.WaitReady(context.Background(), sock, exited) {
		t.Fatal("ready without a socket")
	}
	staleSocket(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !s.WaitReady(ctx, sock, make(chan struct{})) {
		t.Fatal("not ready with a socket")
	}
}
