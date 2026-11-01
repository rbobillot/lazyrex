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
func privateDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "lzr")
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
	if len(d) > 40 {
		t.Skipf("temporary directory %s is too long for socket paths", d)
	}
	return d
}

// TestPrepareRuntimeDir (1m).
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
	for _, bad := range []string{"", "run/user/1000", "/run/user/../user/1000", "/tmp/a:b", "/tmp/a%b", "/tmp/a b", "/tmp/a~b", "/tmp/a\nb",
		"/" + strings.Repeat("x", 60)} {
		if dir, err := rex.PrepareRuntimeDir(bad); err == nil {
			t.Errorf("PrepareRuntimeDir(%q) = %q", bad, dir)
		}
	}
	t.Run("parent 0755", func(t *testing.T) {
		xdg := privateDir(t)
		if err := os.Chmod(xdg, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := rex.PrepareRuntimeDir(xdg); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("parent symlinked", func(t *testing.T) {
		target := privateDir(t)
		link := filepath.Join(privateDir(t), "l")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := rex.PrepareRuntimeDir(link); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("lazyrex symlink", func(t *testing.T) {
		xdg := privateDir(t)
		if err := os.Symlink(privateDir(t), filepath.Join(xdg, "lazyrex")); err != nil {
			t.Fatal(err)
		}
		if _, err := rex.PrepareRuntimeDir(xdg); err == nil {
			t.Fatal("accepted")
		}
	})
	t.Run("lazyrex 0755", func(t *testing.T) {
		xdg := privateDir(t)
		if err := os.Mkdir(filepath.Join(xdg, "lazyrex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(xdg, "lazyrex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := rex.PrepareRuntimeDir(xdg); err == nil {
			t.Fatal("accepted")
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
