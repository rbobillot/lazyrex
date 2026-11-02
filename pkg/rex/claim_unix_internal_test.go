//go:build unix

package rex

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claimDir returns a fresh runtime directory and the stable socket path the
// claim tests use in it.
func claimDir(t *testing.T) (dir, sock string) {
	t.Helper()
	dir, err := PrepareRuntimeDir(shortDir(t, 0o700))
	if err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, stableSocketName("edge", SSHSpec{Host: "edge"}))
}

func lockOf(sock string) string { return strings.TrimSuffix(sock, ".sock") + ".lock" }

// liveSocket listens at path until t ends.
func liveSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
}

func accepts(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// TestOSSocketsDeadCheck (C17): Prepare and Cleanup remove a socket nothing
// serves, never one a dial reaches, and refuse anything but a socket.
func TestOSSocketsDeadCheck(t *testing.T) {
	dir, sock := claimDir(t)
	var s OSSockets

	stale := func() {
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		l.(*net.UnixListener).SetUnlinkOnClose(false)
		l.Close()
	}
	stale()
	if err := s.Prepare(dir, sock); err != nil || present(sock) {
		t.Fatalf("Prepare of a stale socket: %v, present %v", err, present(sock))
	}
	stale()
	s.Cleanup(sock)
	if present(sock) {
		t.Fatal("Cleanup left a stale socket")
	}

	liveSocket(t, sock)
	if err := s.Prepare(dir, sock); !errors.Is(err, ErrLiveSocket) {
		t.Fatalf("Prepare of a live socket = %v, want ErrLiveSocket", err)
	}
	s.Cleanup(sock)
	if !accepts(sock) {
		t.Fatal("the live socket no longer accepts after Prepare and Cleanup")
	}

	file := filepath.Join(dir, "gui-file_0123456789abcdef.sock")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(dir, file); err == nil || errors.Is(err, ErrLiveSocket) {
		t.Fatalf("Prepare of a regular file = %v", err)
	}
	s.Cleanup(file)
	if !present(file) {
		t.Fatal("Cleanup removed a regular file")
	}
}

// TestDeletedLockKeepsLiveSocket (C18): with the lock deleted under a holder,
// a second claimer gets a new lock and binds the stable path; the first
// holder's Prepare and Cleanup then leave that live socket alone.
func TestDeletedLockKeepsLiveSocket(t *testing.T) {
	dir, sock := claimDir(t)
	var s OSSockets
	first, err := s.Claim(dir, sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lockOf(sock)); err != nil {
		t.Fatal(err)
	}
	second, err := s.Claim(dir, sock)
	if err != nil {
		t.Fatalf("Claim after the lock was deleted = %v", err)
	}
	liveSocket(t, sock)
	if err := s.Prepare(dir, sock); !errors.Is(err, ErrLiveSocket) {
		t.Fatalf("Prepare = %v, want ErrLiveSocket", err)
	}
	s.Cleanup(sock)
	if !accepts(sock) {
		t.Fatal("the second claimer's socket no longer accepts")
	}
	first()
	second()
	if !present(lockOf(sock)) {
		t.Fatal("a release removed the lock file")
	}
	if !accepts(sock) {
		t.Fatal("a release removed the live socket")
	}
}

// TestClaimAcrossProcesses (C19): a claim held by another process is
// refused, and freed when that process exits without releasing it.
func TestClaimAcrossProcesses(t *testing.T) {
	dir, sock := claimDir(t)
	cmd := helperCommand(context.Background(), "claim-hold")
	cmd.Env = append(cmd.Env, "HELPER_DIR="+dir, "HELPER_SOCK="+sock)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	var line string
	within(t, "the claim-hold helper to claim", func(context.Context) {
		line, _ = bufio.NewReader(stdout).ReadString('\n')
	})
	if line != "claimed\n" {
		t.Fatalf("helper said %q", line)
	}
	if release, err := (OSSockets{}).Claim(dir, sock); !errors.Is(err, ErrSocketHeld) {
		if release != nil {
			release()
		}
		t.Fatalf("Claim while another process holds it = %v, want ErrSocketHeld", err)
	}
	stdin.Close()
	within(t, "the claim-hold helper to exit", func(context.Context) { cmd.Wait() })
	deadline := time.Now().Add(stopWait)
	for {
		release, err := (OSSockets{}).Claim(dir, sock)
		if err == nil {
			release()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Claim %v after the holder exited = %v", stopWait, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestClaimNotInherited (C20): a process started through ExecRunner.Stream,
// as ssh is, does not inherit the claim of the process that started it.
func TestClaimNotInherited(t *testing.T) {
	l := leaseGrandchildren(t)
	dir, sock := claimDir(t)
	var out []byte
	var err error
	within(t, "the claim-spawn helper to exit", func(ctx context.Context) {
		cmd := helperCommand(ctx, "claim-spawn")
		cmd.Env = append(cmd.Env, "HELPER_DIR="+dir, "HELPER_SOCK="+sock)
		out, err = cmd.Output()
	})
	if err != nil || string(out) != "ready\n" {
		t.Fatalf("helper: %q, %v", out, err)
	}
	deadline := time.Now().Add(lingerWait)
	for {
		held, err := l.held()
		if err != nil {
			t.Fatal(err)
		}
		if held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the grandchild never took its lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release, err := (OSSockets{}).Claim(dir, sock)
	if err != nil {
		t.Fatalf("Claim with the grandchild alive = %v", err)
	}
	release()
}

// TestClaimRefusals (C21).
func TestClaimRefusals(t *testing.T) {
	var s OSSockets
	t.Run("lock is a symlink", func(t *testing.T) {
		dir, sock := claimDir(t)
		if err := os.Symlink(filepath.Join(dir, "elsewhere"), lockOf(sock)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Claim(dir, sock); err == nil {
			t.Fatal("claimed through a symlinked lock")
		}
		if present(filepath.Join(dir, "elsewhere")) {
			t.Fatal("Claim created the symlink's target")
		}
	})
	t.Run("lock is a directory", func(t *testing.T) {
		dir, sock := claimDir(t)
		if err := os.Mkdir(lockOf(sock), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Claim(dir, sock); err == nil {
			t.Fatal("claimed a directory")
		}
	})
	t.Run("dir open to others", func(t *testing.T) {
		dir, sock := claimDir(t)
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Claim(dir, sock); err == nil {
			t.Fatal("claimed in a directory open to others")
		}
		if present(lockOf(sock)) {
			t.Fatal("Claim created a lock in a directory open to others")
		}
	})
	t.Run("outside the dir", func(t *testing.T) {
		dir, _ := claimDir(t)
		other, sock := claimDir(t)
		if _, err := s.Claim(dir, sock); err == nil {
			t.Fatal("claimed a socket outside the directory")
		}
		if present(lockOf(sock)) || other == dir {
			t.Fatal("Claim created a lock outside the directory")
		}
	})
	t.Run("not a .sock", func(t *testing.T) {
		dir, _ := claimDir(t)
		if _, err := s.Claim(dir, filepath.Join(dir, "gui-x")); err == nil {
			t.Fatal("claimed a path without .sock")
		}
	})
	t.Run("twice in one process", func(t *testing.T) {
		dir, sock := claimDir(t)
		release, err := s.Claim(dir, sock)
		if err != nil {
			t.Fatal(err)
		}
		if again, err := s.Claim(dir, sock); !errors.Is(err, ErrSocketHeld) {
			if again != nil {
				again()
			}
			t.Fatalf("second Claim = %v, want ErrSocketHeld", err)
		}
		release()
		again, err := s.Claim(dir, sock)
		if err != nil {
			t.Fatalf("Claim after release = %v", err)
		}
		again()
	})
}
