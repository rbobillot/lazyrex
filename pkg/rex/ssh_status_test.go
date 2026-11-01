package rex_test

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// TestSSHStaleStatusKeepsTunnel (a): of two overlapping status reads, the
// older finishing last neither replaces nor stops the tunnel the newer set.
func TestSSHStaleStatusKeepsTunnel(t *testing.T) {
	const newerSock = "/run/user/1001/rex/other.sock"
	for name, older := range map[string]string{
		"other endpoint": rextest.FixtureString("whoami_remote.json"),
		"unforwardable":  rextest.FixtureString("whoami_remote_space.json"),
	} {
		t.Run(name, func(t *testing.T) {
			fr := remoteRunner("/bin/bash")
			fr.On("'whoami'", "'--json'").Return(remoteWhoami("unix://"+newerSock), "", 0)
			fr.On("'whoami'", "'--json'").Return(older, "", 0).Once()
			var armed atomic.Bool
			armed.Store(true)
			entered, release := make(chan struct{}), make(chan struct{})
			holds := rextest.NewHolds(t)
			fr.OnCall = func(argv []string) {
				if slices.Contains(argv, "'whoami'") && armed.CompareAndSwap(true, false) {
					close(entered)
					holds.Wait(release, "the release of the older read")
				}
			}
			r := newLinkRig(t, fr, forward)
			done := make(chan struct{})
			go func() {
				r.status()
				close(done)
			}()
			rextest.Recv(t, entered, "the older status read to reach whoami")
			r.status()
			r.waitTunnel(true)
			gen := r.link.RouteGen()
			close(release)
			rextest.Recv(t, done, "the older status read to return")

			tunnels := fr.CallsContaining("-N")
			if len(tunnels) != 1 || !slices.Contains(tunnels[0], fwdSock(1)+":"+newerSock) {
				t.Fatalf("tunnels = %q", tunnels)
			}
			if g := r.link.RouteGen(); g != gen {
				t.Fatalf("route generation %d -> %d", gen, g)
			}
			if d := r.link.Describe(); d != "ssh remote-host, forward mode, tunnel up" {
				t.Fatalf("describe = %q", d)
			}
			r.noTunnelChange()
			for _, e := range r.log.all() {
				if e.Note != "" {
					t.Fatalf("note %q", e.Note)
				}
			}
		})
	}
}

// TestSSHOlderStatusFirstNotApplied (b): an older read finishing while a
// newer one runs is not applied.
func TestSSHOlderStatusFirstNotApplied(t *testing.T) {
	r := newLinkRig(t, remoteRunner("/bin/bash"), forward)
	g1, g2 := r.link.BeginStatus(), r.link.BeginStatus()
	if r.link.ApplyStatus(g1) {
		t.Fatal("older applied while the newer runs")
	}
	if !r.link.ApplyStatus(g2) {
		t.Fatal("newer refused")
	}
}

// TestSSHExplicitExecNoFallback (c): an endpoint that cannot be forwarded
// is a fallback, with its note, in forward mode only.
func TestSSHExplicitExecNoFallback(t *testing.T) {
	for _, c := range []struct {
		spec     rex.SSHSpec
		describe string
		notes    int
	}{
		{execOnly, "ssh remote-host, exec mode", 0},
		{forward, "ssh remote-host, exec mode (endpoint cannot be forwarded)", 1},
	} {
		t.Run(string(c.spec.Mode), func(t *testing.T) {
			fr := remoteRunner("/bin/bash")
			fr.On("'whoami'", "'--json'").Return(rextest.FixtureString("whoami_remote_space.json"), "", 0)
			r := newLinkRig(t, fr, c.spec)
			for range 2 {
				if st := r.status(); st.State != rex.StateOnline {
					t.Fatalf("status = %v %q", st.State, st.Detail)
				}
			}
			if d := r.link.Describe(); d != c.describe {
				t.Fatalf("describe = %q", d)
			}
			notes := 0
			for _, e := range r.log.all() {
				if strings.Contains(e.Note, "cannot be forwarded") {
					notes++
				}
			}
			if notes != c.notes {
				t.Fatalf("%d fallback notes, want %d:\n%s", notes, c.notes, r.log.text())
			}
			if n := len(fr.CallsContaining("-N")); n != 0 {
				t.Fatal("tunnel started")
			}
		})
	}
}
