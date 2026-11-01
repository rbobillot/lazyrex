package rextest

import "testing"

func TestSocketsCleanupGate(t *testing.T) {
	tb := &fakeTB{}
	s := &FakeSockets{}
	gate := make(chan struct{})
	s.GateCleanup(tb, "/run/a.sock", gate)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Cleanup("/run/a.sock")
	}()
	close(gate)
	Recv(t, done, "Cleanup once its gate closed")
	s.Cleanup("/run/b.sock")
	if got := s.Cleaned(); len(got) != 2 || got[0] != "/run/a.sock" || got[1] != "/run/b.sock" {
		t.Fatalf("cleaned %q", got)
	}
	tb.runCleanups()
	wantClean(t, tb)
}

func TestSocketsCleanupGateEnd(t *testing.T) {
	tb := &fakeTB{}
	s := &FakeSockets{}
	s.GateCleanup(tb, "/run/a.sock", make(chan struct{}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Cleanup("/run/a.sock")
	}()
	tb.runCleanups()
	Recv(t, done, "the gated Cleanup to return at the end of the test")
	wantClean(t, tb)
}
