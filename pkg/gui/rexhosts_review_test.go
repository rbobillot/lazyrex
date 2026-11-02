package gui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rexhosts"
)

// TestRexHostsReloadMalformed: an exit 0 reload whose output misses a
// required field is unreadable, and a hosts list after it still says S10,
// not that the reload failed to load the file.
func TestRexHostsReloadMalformed(t *testing.T) {
	for _, tc := range []struct{ stdout, toast, state string }{
		{`{}`, "the local Rex server read its config again (output unreadable)", "up to date, not loaded yet"},
		{`{"generation": 2}`, "the local Rex server read its config again (output unreadable)", "up to date, not loaded yet"},
		{`{"diagnostics": null}`, "the local Rex server read its config again (output unreadable)", "up to date, not loaded yet"},
		{`{"generation": 2, "diagnostics": null}`, "the local Rex server read its config again", "up to date, still not loaded after the reload"},
	} {
		t.Run(tc.stdout, func(t *testing.T) {
			r := newRexHarness(t, rexSetup{file: golden})
			r.fr.On("config", "reload", "--json").Return(tc.stdout, "", 0)
			r.w()
			r.expectConfirm("Reload the local Rex server")
			r.accept()
			r.expectToast(tc.toast)
			if got := r.status("edge"); !strings.HasPrefix(got, r.head("edge-lazyrex", tc.state)+"\n") {
				t.Fatalf("status %q, want %q", got, tc.state)
			}
		})
	}
}

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
