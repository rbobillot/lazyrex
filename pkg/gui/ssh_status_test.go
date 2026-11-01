package gui

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// runQueued runs the updates queued so far, without waiting for workers.
func runQueued(t *testing.T, u *goUI, want int) {
	t.Helper()
	deadline := time.Now().Add(rextest.Timeout)
	for {
		u.mu.Lock()
		fs := u.updates
		if len(fs) >= want {
			u.updates = nil
		}
		u.mu.Unlock()
		if len(fs) >= want {
			for _, f := range fs {
				f()
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d updates queued, want %d", len(fs), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// runUntil runs the queued updates, without waiting for workers, until cond
// holds.
func runUntil(t *testing.T, u *goUI, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(rextest.Timeout)
	for {
		u.mu.Lock()
		fs := u.updates
		u.updates = nil
		u.mu.Unlock()
		for _, f := range fs {
			f()
		}
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// newGoSSH starts on the exec SSH target remote-host, with workers on
// goroutines, and settles it online.
func newGoSSH(t *testing.T, fr *rextest.FakeRunner) (*harness, *goUI) {
	t.Helper()
	ui := &goUI{}
	env := rextest.SSHEnv(sshRunDir, &rextest.FakeSockets{})
	h := newHarness(t, withRunner(fr), withUI(ui), withSSH(env), withConfig(sshExecConfig), withInitial("remote-host"))
	ui.settle(t)
	if h.st.Status.State != rex.StateOnline {
		t.Fatalf("status %v %q", h.st.Status.State, h.st.Status.Detail)
	}
	return h, ui
}

// poll lets a poll interval pass and ticks, the stream being down.
func (h *harness) poll() {
	h.clk.Advance(5 * time.Second)
	h.ctrl.Tick()
}

func execWhoami(fr *rextest.FakeRunner) int {
	return len(fr.CallsContaining(quoted("whoami", "--json")...))
}

// TestStatusSwitchDuringRead: a switch while a status read runs, with a
// request pending, drops both; the new target reads its own status at once.
func TestStatusSwitchDuringRead(t *testing.T) {
	fr := sshRunner("/bin/bash")
	h, ui := newGoSSH(t, fr)
	var armed atomic.Bool
	entered, release := make(chan struct{}, 8), make(chan struct{})
	holds := rextest.NewHolds(t)
	fr.OnCall = func(argv []string) {
		if !rextest.Contains(argv, "'whoami'") {
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		if armed.CompareAndSwap(true, false) {
			holds.Wait(release, "the release of the held whoami")
		}
	}
	armed.Store(true)
	h.poll()
	rextest.Recv(t, entered, "the poll's whoami")
	goes := ui.goes.Load()
	h.poll()
	if ui.goes.Load() != goes {
		// A read the poll started runs its whoami before the switch.
		rextest.Recv(t, entered, "the whoami of the read the second poll started")
	}
	localBefore := h.count("whoami --json")
	h.activateTarget("local")
	runUntil(t, ui, "local online", func() bool {
		return h.st.TargetName == "local" && h.st.Status.State == rex.StateOnline && h.count("whoami --json") > localBefore
	})
	n := execWhoami(fr)
	close(release)
	ui.settle(t)
	if got := execWhoami(fr); got != n {
		t.Fatalf("exec whoami %d -> %d after the switch; calls:\n%s", n, got, h.dumpCalls())
	}
	if h.st.TargetName != "local" || h.st.Status.State != rex.StateOnline {
		t.Fatalf("target %q status %v", h.st.TargetName, h.st.Status.State)
	}
}

// stillAt fails if n() differs from want within a short while.
func stillAt(t *testing.T, what string, n func() int, want int) {
	t.Helper()
	for deadline := time.Now().Add(50 * time.Millisecond); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if got := n(); got != want {
			t.Fatalf("%s = %d, want %d", what, got, want)
		}
	}
}

// TestSSHStatusPollsCoalesced: polls during a slow status read start no
// read; once it lands, its result applies and one refresh runs for them.
func TestSSHStatusPollsCoalesced(t *testing.T) {
	fr := sshRunner("/bin/bash")
	h, ui := newGoSSH(t, fr)
	fr.On(quoted("whoami", "--json")...).Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	before := execWhoami(fr)
	var armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	holds := rextest.NewHolds(t)
	fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, "'whoami'") && armed.CompareAndSwap(true, false) {
			close(entered)
			holds.Wait(release, "the release of the held whoami")
		}
	}
	armed.Store(true)
	h.poll()
	rextest.Recv(t, entered, "the held whoami")
	for range 4 {
		h.poll()
	}
	stillAt(t, "exec whoami during the read", func() int { return execWhoami(fr) }, before+1)
	if cn := h.ctrl.conn.Load(); !cn.statusInflight || cn.statusDirty != statusRefresh {
		t.Fatalf("inflight %v, dirty %v during the read", cn.statusInflight, cn.statusDirty)
	}
	close(release)
	runUntil(t, ui, "the held read to land", func() bool { return h.st.Status.State == rex.StateStopped })
	ui.settle(t)
	if n := execWhoami(fr); n != before+2 {
		t.Fatalf("exec whoami = %d, want %d; calls:\n%s", n, before+2, h.dumpCalls())
	}
	if cn := h.ctrl.conn.Load(); cn.statusInflight || cn.statusDirty != statusNone {
		t.Fatalf("inflight %v, dirty %v after settling", cn.statusInflight, cn.statusDirty)
	}
}

// TestSSHStatusLandsUnderFastPolls: when every read outlasts the poll
// interval, reads still land, one at a time.
func TestSSHStatusLandsUnderFastPolls(t *testing.T) {
	fr := sshRunner("/bin/bash")
	h, ui := newGoSSH(t, fr)
	fr.On(quoted("whoami", "--json")...).Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	before := execWhoami(fr)
	var running, most atomic.Int32
	entered, release := make(chan struct{}, 8), make(chan struct{})
	holds := rextest.NewHolds(t)
	fr.OnCall = func(argv []string) {
		if !rextest.Contains(argv, "'whoami'") {
			return
		}
		n := running.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		entered <- struct{}{}
		holds.Wait(release, "a round's release of the held whoami")
		running.Add(-1)
	}
	h.poll()
	rextest.Recv(t, entered, "the first read's whoami")
	for round := 1; round <= 3; round++ {
		h.poll()
		stillAt(t, "exec whoami before the release", func() int { return execWhoami(fr) }, before+round)
		rextest.Send(t, release, struct{}{}, fmt.Sprintf("round %d's release to reach the held read", round))
		runUntil(t, ui, "the next read", func() bool { return len(entered) > 0 })
		rextest.Recv(t, entered, "the next read's whoami")
		if h.st.Status.State != rex.StateStopped {
			t.Fatalf("round %d: status %v, want stopped", round, h.st.Status.State)
		}
	}
	close(release)
	ui.settle(t)
	if m := most.Load(); m != 1 {
		t.Fatalf("%d status reads at once, want 1", m)
	}
	if n := execWhoami(fr); n != before+4 {
		t.Fatalf("exec whoami = %d, want %d", n, before+4)
	}
}

// TestSSHStatusRequestWaitsGUI (b): a status request during a read runs once
// that read lands: the older result applies first, the newer one last.
func TestSSHStatusRequestWaitsGUI(t *testing.T) {
	fr := sshRunner("/bin/bash")
	h, ui := newGoSSH(t, fr)
	fr.On(quoted("whoami", "--json")...).Return("", rextest.FixtureString("stderr_ssh_255.txt"), 255)
	fr.On(quoted("whoami", "--json")...).Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4).Once()
	var calls atomic.Int32
	olderIn, newerIn := make(chan struct{}), make(chan struct{})
	releaseOlder, releaseNewer := make(chan struct{}), make(chan struct{})
	holds := rextest.NewHolds(t)
	fr.OnCall = func(argv []string) {
		if !rextest.Contains(argv, "'whoami'") {
			return
		}
		switch calls.Add(1) {
		case 1:
			close(olderIn)
			holds.Wait(releaseOlder, "the release of the older read")
		case 2:
			close(newerIn)
			holds.Wait(releaseNewer, "the release of the newer read")
		}
	}
	h.ctrl.checkTarget()
	rextest.Recv(t, olderIn, "the older read's whoami")
	h.ctrl.checkTarget()
	if cn := h.ctrl.conn.Load(); calls.Load() != 1 || !cn.statusInflight || cn.statusDirty != statusCheck {
		t.Fatalf("whoami %d, inflight %v, dirty %v during the older read", calls.Load(), cn.statusInflight, cn.statusDirty)
	}
	close(releaseOlder)
	runUntil(t, ui, "the older status", func() bool { return h.st.Status.State == rex.StateStopped })
	rextest.Recv(t, newerIn, "the newer read's whoami")
	if h.st.Status.State != rex.StateStopped {
		t.Fatalf("status = %v once the newer read runs, want the older stopped", h.st.Status.State)
	}
	close(releaseNewer)
	ui.settle(t)
	if h.st.Status.State != rex.StateSSHUnreachable {
		t.Fatalf("status = %v, want the newer unreachable (ssh)", h.st.Status.State)
	}
}
