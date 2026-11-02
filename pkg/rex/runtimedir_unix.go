//go:build unix

package rex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// maxSocketPath keeps a tunnel socket under every platform's sun_path limit.
const maxSocketPath = 100

// longestSocketName is the longest name SSHEnv gives a tunnel socket.
const longestSocketName = "/fwd-4294967295-18446744073709551615.sock"

// maxBindPath is the longest path ssh can bind a socket at here: sun_path
// less its NUL (103 on macOS and the BSDs, 107 on Linux).
const maxBindPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// longestControlName is the longest name ssh binds for the exec ControlPath
// ssh-%C: a ControlMaster binds ssh-%C.<16 random characters> first, then
// renames it into place. %C is 40 hex digits.
const longestControlName = "/ssh-0123456789abcdef0123456789abcdef01234567.0123456789abcdef"

// PrepareRuntimeDir returns $XDG_RUNTIME_DIR/lazyrex, created with mode 0700.
// Both directories must be real directories (no symlink anywhere) owned by
// the current user with no group or other permissions, because ssh control
// sockets and forwarded Rex sockets live there.
func PrepareRuntimeDir(xdg string) (string, error) {
	switch {
	case xdg == "":
		return "", errors.New("XDG_RUNTIME_DIR is not set")
	case !filepath.IsAbs(xdg) || filepath.Clean(xdg) != xdg:
		return "", fmt.Errorf("XDG_RUNTIME_DIR %q is not a clean absolute path", xdg)
	case strings.ContainsAny(xdg, `:%~'"\`) || strings.IndexFunc(xdg, unicode.IsSpace) >= 0 || hasControl(xdg):
		return "", fmt.Errorf("XDG_RUNTIME_DIR %q contains a character ssh options cannot hold", xdg)
	}
	dir := filepath.Join(xdg, "lazyrex")
	if len(dir)+len(longestSocketName) > maxSocketPath || len(dir)+len(longestControlName) > maxBindPath {
		return "", fmt.Errorf("XDG_RUNTIME_DIR %q is too long for socket paths", xdg)
	}
	if real, err := filepath.EvalSymlinks(xdg); err != nil || real != xdg {
		return "", fmt.Errorf("XDG_RUNTIME_DIR %q is or goes through a symlink", xdg)
	}
	if err := checkPrivateDir(xdg); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if err := checkPrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// checkPrivateDir requires a directory, not a symlink, owned by the current
// user with no group or other permissions.
func checkPrivateDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink", dir)
	case !st.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case st.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is accessible to other users (mode %04o)", dir, st.Mode().Perm())
	case !ownedByMe(st):
		return fmt.Errorf("%s is not owned by the current user", dir)
	}
	return nil
}

// checkRuntimeDir revalidates the runtime directory and its parent.
func checkRuntimeDir(dir string) error {
	parent := filepath.Dir(dir)
	if real, err := filepath.EvalSymlinks(parent); err != nil || real != parent {
		return fmt.Errorf("%s is or goes through a symlink", parent)
	}
	if err := checkPrivateDir(parent); err != nil {
		return err
	}
	return checkPrivateDir(dir)
}

func ownedByMe(st fs.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(s.Uid) == os.Getuid()
}

func myLiveSocket(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode()&fs.ModeSocket != 0 && ownedByMe(st)
}

// OSSockets manages tunnel sockets on the real filesystem.
type OSSockets struct{}

// Prepare implements TunnelSockets.
func (OSSockets) Prepare(dir, path string) error {
	if filepath.Dir(path) != dir || len(path) > maxSocketPath {
		return fmt.Errorf("socket path %q is not a short path in %s", path, dir)
	}
	if err := checkRuntimeDir(dir); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case st.Mode()&fs.ModeSocket == 0 || !ownedByMe(st):
		return fmt.Errorf("refusing to replace %s: not a socket of the current user", path)
	}
	return os.Remove(path)
}

// WaitReady implements TunnelSockets.
func (OSSockets) WaitReady(ctx context.Context, path string, exited <-chan struct{}) bool {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	limit := time.NewTimer(15 * time.Second)
	defer limit.Stop()
	for {
		if myLiveSocket(path) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-exited:
			return myLiveSocket(path)
		case <-limit.C:
			return false
		case <-tick.C:
		}
	}
}

// Cleanup implements TunnelSockets.
func (OSSockets) Cleanup(path string) {
	if checkRuntimeDir(filepath.Dir(path)) != nil || !myLiveSocket(path) {
		return
	}
	_ = os.Remove(path)
}
