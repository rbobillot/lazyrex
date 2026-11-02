package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rexhosts"
)

// TestRexHostsWriteNotSynced: a write whose directory sync failed is no
// success: no toast or log line saying it was written, and no reload offer.
func TestRexHostsWriteNotSynced(t *testing.T) {
	r := newRexHarness(t, rexSetup{})
	r.ctrl.writeHosts = func(p rexhosts.Paths, data []byte, seen rexhosts.Snapshot) error {
		if err := rexhosts.Write(p, data, seen); err != nil {
			t.Fatal(err)
		}
		return fmt.Errorf("%w (input/output error)", rexhosts.ErrNotSynced)
	}
	r.w()
	r.expectConfirm("Write the Rex app hosts file")
	r.accept()
	r.expectError(r.paths.Hosts + " written, but its directory was not synced to disk (input/output error); press w again")
	if p := r.ctrl.Popups.Top(); p != nil {
		t.Fatalf("popup %+v open", p)
	}
	if strings.Contains(r.logText(), "wrote ") || r.countContaining("config", "reload") != 0 {
		t.Fatalf("reported as written:\n%s\n%s", r.logText(), r.dumpCalls())
	}
}
