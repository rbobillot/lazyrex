//go:build unix

package rex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// leaseWait bounds the wait for the linger grandchildren to end once their
// lease is gone; they look for it every 20ms.
const leaseWait = 2 * time.Second

// lease ends the linger helper's grandchildren without naming them: the
// helper takes the lock file shared and hands that open lock to its
// grandchild before starting it, which holds it until the lease file is
// gone. No pid is read, so none can be reused.
type lease struct{ alive, lock string }

// leaseGrandchildren leases the linger helper's grandchildren to t, and
// releases them when t ends, whichever way it ends.
func leaseGrandchildren(t *testing.T) *lease {
	t.Helper()
	dir := t.TempDir()
	l := &lease{alive: filepath.Join(dir, "alive"), lock: filepath.Join(dir, "lock")}
	for _, p := range []string{l.alive, l.lock} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HELPER_LEASE", l.alive)
	t.Setenv("HELPER_LOCK", l.lock)
	// Registered after TempDir, so it runs before the directory goes.
	t.Cleanup(func() {
		if err := l.release(); err != nil {
			t.Error(err)
		}
	})
	return l
}

// shareLease takes the lease lock shared and hands it to cmd, so the lock
// is held from before cmd starts until cmd and this process have both ended.
func shareLease(cmd *exec.Cmd) error {
	f, err := os.Open(os.Getenv("HELPER_LOCK"))
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		f.Close()
		return err
	}
	cmd.ExtraFiles = []*os.File{f}
	return nil
}

// leaseLock keeps the lock holdLeaseLock took open for the life of the
// process.
var leaseLock *os.File

// holdLeaseLock takes the lease lock shared itself, as a process that
// inherits no lock from its parent must.
func holdLeaseLock() error {
	f, err := os.Open(os.Getenv("HELPER_LOCK"))
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		f.Close()
		return err
	}
	leaseLock = f
	return nil
}

// held reports whether a grandchild still holds the lease.
func (l *lease) held() (bool, error) {
	f, err := os.Open(l.lock)
	if err != nil {
		return false, err
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}

// release removes the lease, then waits up to leaseWait for the lock to be
// free, which it is only once every grandchild holding it has exited.
func (l *lease) release() error {
	if err := os.Remove(l.alive); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	deadline := time.Now().Add(leaseWait)
	for {
		held, err := l.held()
		if err != nil || !held {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("a linger grandchild still held its lease %v after the lease was removed", leaseWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLeaseEndsOrphanedGrandchild: a linger helper that dies right after
// starting its grandchild, before naming it, leaves it to the lease, which
// still ends it.
func TestLeaseEndsOrphanedGrandchild(t *testing.T) {
	shortWaitDelay(t)
	l := leaseGrandchildren(t)
	argv := helperArgv(t, "linger-die")
	var res Result
	var err error
	within(t, "Run to return past the orphaned grandchild", func(ctx context.Context) { res, err = ExecRunner{}.Run(ctx, argv) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != 0 || len(res.Stdout) != 0 {
		t.Fatalf("result = %d %q %q", res.Code, res.Stdout, res.Stderr)
	}
	if held, err := l.held(); err != nil || !held {
		t.Fatalf("lease held %v (%v): no orphaned grandchild", held, err)
	}
	if err := l.release(); err != nil {
		t.Fatal(err)
	}
}
