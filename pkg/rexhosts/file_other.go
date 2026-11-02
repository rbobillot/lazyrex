//go:build !unix

package rexhosts

import "errors"

var errUnsupported = errors.New("not supported on Windows (no SSH targets)")

func Read(Paths) (Snapshot, error) { return Snapshot{}, errUnsupported }

func Write(Paths, []byte, Snapshot) error { return errUnsupported }
