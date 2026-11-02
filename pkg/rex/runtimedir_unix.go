//go:build unix

package rex

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

// PrepareRuntimeDir returns lazyrex's private runtime directory:
// $XDG_RUNTIME_DIR/lazyrex when xdg is set, else /tmp/lazyrex-UID/run with
// /tmp resolved. It creates the last level, and lazyrex-UID in the fallback,
// with mode 0700, and is safe to call again after a cleaner removed them.
// Every level it uses must be a real directory (no symlink anywhere) owned
// by the current user with no group or other permissions, because ssh
// control sockets and forwarded Rex sockets live there. A set xdg that fails
// a check is an error, never a fallback.
func PrepareRuntimeDir(xdg string) (string, error) { return runtimeDir(xdg, "/tmp") }

func runtimeDir(xdg, tmp string) (string, error) {
	if xdg != "" {
		return prepareIn("XDG_RUNTIME_DIR", xdg, "lazyrex", false)
	}
	base, err := filepath.EvalSymlinks(tmp)
	if err == nil {
		err = checkTmpDir(base)
	}
	var dir string
	if err == nil {
		dir, err = prepareIn("runtime directory", filepath.Join(base, "lazyrex-"+strconv.Itoa(os.Getuid())), "run", true)
	}
	if err != nil {
		return "", fmt.Errorf("XDG_RUNTIME_DIR is not set and the fallback in %s failed: %w", tmp, err)
	}
	return dir, nil
}

// prepareIn returns parent/leaf after checking both, creating parent first
// when create is set. what names parent in its own errors.
func prepareIn(what, parent, leaf string, create bool) (string, error) {
	switch {
	case !filepath.IsAbs(parent) || filepath.Clean(parent) != parent:
		return "", fmt.Errorf("%s %q is not a clean absolute path", what, parent)
	case strings.ContainsAny(parent, `:%~'"\`) || strings.IndexFunc(parent, unicode.IsSpace) >= 0 || hasControl(parent):
		return "", fmt.Errorf("%s %q contains a character ssh options cannot hold", what, parent)
	}
	dir := filepath.Join(parent, leaf)
	if len(dir)+len(longestSocketName) > maxSocketPath || len(dir)+len(longestControlName) > maxBindPath {
		return "", fmt.Errorf("%s %q is too long for socket paths", what, parent)
	}
	if create {
		if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	if real, err := filepath.EvalSymlinks(parent); err != nil || real != parent {
		return "", fmt.Errorf("%s %q is or goes through a symlink", what, parent)
	}
	if err := checkPrivateDir(parent); err != nil {
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

// checkTmpDir requires a directory of root or the current user that others
// cannot write to, or a sticky one: nobody else can then rename or replace
// what lazyrex creates in it.
func checkTmpDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	return tmpDirProblem(dir, st.Mode(), ownerOf(st))
}

func tmpDirProblem(dir string, mode fs.FileMode, owner int) error {
	switch {
	case !mode.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case owner != 0 && owner != os.Getuid():
		return fmt.Errorf("%s is not owned by root or the current user", dir)
	case mode.Perm()&0o022 != 0 && mode&fs.ModeSticky == 0:
		return fmt.Errorf("%s is writable by others and not sticky", dir)
	}
	return nil
}

// checkPrivateDir requires a directory, not a symlink, owned by the current
// user with no group or other permissions.
func checkPrivateDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	return privateDirProblem(dir, st.Mode(), ownerOf(st))
}

func privateDirProblem(dir string, mode fs.FileMode, owner int) error {
	switch {
	case mode&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink", dir)
	case !mode.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case mode.Perm()&0o077 != 0:
		return fmt.Errorf("%s is accessible to other users (mode %04o)", dir, mode.Perm())
	case owner != os.Getuid():
		return fmt.Errorf("%s is not owned by the current user", dir)
	}
	return nil
}

// ownerOf returns the owner's UID, or -1 when the file system does not say.
func ownerOf(st fs.FileInfo) int {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid)
	}
	return -1
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

func ownedByMe(st fs.FileInfo) bool { return ownerOf(st) == os.Getuid() }

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
