//go:build !unix

package rex

import (
	"errors"
	"os/exec"
	"testing"
)

type lease struct{}

// leaseGrandchildren skips t: the lease needs flock.
func leaseGrandchildren(t *testing.T) *lease {
	t.Helper()
	t.Skip("the linger grandchild lease needs flock, on unix only")
	return nil
}

func shareLease(*exec.Cmd) error { return errors.New("no lease off unix") }

func holdLeaseLock() error { return errors.New("no lease off unix") }
