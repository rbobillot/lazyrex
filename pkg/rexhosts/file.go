package rexhosts

import "errors"

// State says whose file is at Paths.Hosts.
type State int

const (
	Absent State = iota
	// Ours is a regular file of the user's whose first line is exactly
	// rex.HostsFileMarker; lazyrex replaces it whole.
	Ours
	// Foreign is anything else, which lazyrex never touches.
	Foreign
)

// Snapshot is what Read found: Why says why a file is Foreign, Sum is the
// sha256 of an Ours file.
type Snapshot struct {
	State State
	Why   string
	Sum   [32]byte
}

var (
	ErrForeign   = errors.New("not a file lazyrex wrote")
	ErrChanged   = errors.New("changed on disk since lazyrex read it")
	ErrBusy      = errors.New("another lazyrex is writing it")
	ErrUnsafeDir = errors.New("not a private directory of yours")
)

// ForeignError is ErrForeign with the reason.
type ForeignError struct{ Why string }

func (e *ForeignError) Error() string { return "not a file lazyrex wrote (" + e.Why + ")" }
func (e *ForeignError) Unwrap() error { return ErrForeign }

const maxSize = 1 << 20
