//go:build unix

package rexhosts

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

// Test hooks.
var (
	beforeRename = func() {}
	lockBusy     = func() {}
	lockWait     = 2 * time.Second
	lockPoll     = 50 * time.Millisecond
	// ownerOf is the owner of the "dir" or the "file".
	ownerOf = func(what string, fi fs.FileInfo) int { return statOwner(fi) }
)

func statOwner(fi fs.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// openDir opens dir, following a symlink to it once, and checks what it
// opened: a directory of the user's that nobody else can write.
func openDir(dir string) (*os.Root, *os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	d, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	fi, err := d.Stat()
	if err == nil && (!fi.IsDir() || ownerOf("dir", fi) != os.Getuid() || fi.Mode().Perm()&0o022 != 0) {
		err = fmt.Errorf("%s is %w (owned by another user or writable by others)", dir, ErrUnsafeDir)
	}
	if err != nil {
		d.Close()
		root.Close()
		return nil, nil, err
	}
	return root, d, nil
}

func foreign(why string) (Snapshot, error) { return Snapshot{State: Foreign, Why: why}, nil }

// inspect reads lazyrex's file in root without following a symlink at its
// name or blocking on a FIFO.
func inspect(root *os.Root) (Snapshot, error) {
	fi, err := root.Lstat(rex.HostsFileName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Snapshot{State: Absent}, nil
	case err != nil:
		return Snapshot{}, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return foreign("a symlink")
	case !fi.Mode().IsRegular():
		return foreign("not a regular file")
	}
	f, err := root.OpenFile(rex.HostsFileName, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	ffi, err := f.Stat()
	switch {
	case err != nil:
		return Snapshot{}, err
	case !os.SameFile(fi, ffi):
		return Snapshot{}, ErrChanged
	case !ffi.Mode().IsRegular():
		return foreign("not a regular file")
	case ownerOf("file", ffi) != os.Getuid():
		return foreign("not yours")
	case ffi.Size() > maxSize:
		return foreign("larger than 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) > maxSize {
		return foreign("larger than 1 MiB")
	}
	if first, _, _ := bytes.Cut(data, []byte("\n")); string(first) != rex.HostsFileMarker {
		return foreign("its first line is not lazyrex's header")
	}
	return Snapshot{State: Ours, Sum: sha256.Sum256(data)}, nil
}

// Read says whose file is at p.Hosts. A missing directory is an absent
// file; Read never creates anything.
func Read(p Paths) (Snapshot, error) {
	if _, err := os.Lstat(p.Dir); errors.Is(err, fs.ErrNotExist) {
		return Snapshot{State: Absent}, nil
	}
	root, d, err := openDir(p.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{State: Absent}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer root.Close()
	defer d.Close()
	// Without the lock, a writer's rename can land between Lstat and open.
	for range 10 {
		if snap, err := inspect(root); !errors.Is(err, ErrChanged) {
			return snap, err
		}
	}
	return Snapshot{}, ErrChanged
}

// lock takes the flock of the directory, which every lazyrex takes to write
// the file, waiting up to lockWait.
func lock(d *os.File) error {
	deadline := time.Now().Add(lockWait)
	for {
		err := tryLock(d)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("cannot lock %s: %w", d.Name(), err)
		}
		lockBusy()
		if time.Now().After(deadline) {
			return ErrBusy
		}
		time.Sleep(lockPoll)
	}
}

// compare checks that the file is still what seen says.
func compare(root *os.Root, seen Snapshot) error {
	cur, err := inspect(root)
	switch {
	case err != nil:
		return err
	case cur.State == Foreign:
		return &ForeignError{Why: cur.Why}
	case cur.State != seen.State, cur.Sum != seen.Sum:
		return ErrChanged
	}
	return nil
}

func createTemp(root *os.Root) (*os.File, string, error) {
	for range 10 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, "", err
		}
		name := ".lazyrex-hosts-" + hex.EncodeToString(b[:]) + ".tmp"
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			return f, name, err
		}
	}
	return nil, "", errors.New("cannot create a temporary file")
}

// Write replaces the file at p.Hosts with data if it is still what seen
// says, checked under the directory's lock right before the rename. It
// creates a missing directory at 0700 and writes the file at 0600.
func Write(p Paths, data []byte, seen Snapshot) error {
	if seen.State == Foreign {
		return &ForeignError{Why: seen.Why}
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	root, d, err := openDir(p.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	defer d.Close()
	if err := lock(d); err != nil {
		return err
	}
	if err := compare(root, seen); err != nil {
		return err
	}
	f, tmp, err := createTemp(root)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			root.Remove(tmp)
		}
	}()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	beforeRename()
	if err := compare(root, seen); err != nil {
		return err
	}
	if err := root.Rename(tmp, rex.HostsFileName); err != nil {
		return err
	}
	renamed = true
	_ = d.Sync()
	return nil
}
