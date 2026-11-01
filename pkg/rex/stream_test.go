package rex_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

type streamRec struct {
	mu     sync.Mutex
	events []rex.Event
	bad    [][]byte
	states []rex.StreamState
}

func (r *streamRec) supervisor(c *rex.Client, clk rex.Clock) *rex.Supervisor {
	return &rex.Supervisor{
		Client: c, Clock: clk, Min: time.Second, Max: 30 * time.Second,
		OnEvent:   func(ev rex.Event) { r.mu.Lock(); r.events = append(r.events, ev); r.mu.Unlock() },
		OnBadLine: func(l []byte, _ error) { r.mu.Lock(); r.bad = append(r.bad, l); r.mu.Unlock() },
		OnState:   func(s rex.StreamState) { r.mu.Lock(); r.states = append(r.states, s); r.mu.Unlock() },
	}
}

func (r *streamRec) snapshot() ([]rex.Event, [][]byte, []rex.StreamState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events), slices.Clone(r.bad), slices.Clone(r.states)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(rextest.Timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSupervisorSkipsMalformedLine(t *testing.T) {
	fr := rextest.NewFakeRunner()
	lines := strings.Split(strings.TrimSpace(rextest.FixtureString("events.ndjson")), "\n")
	fr.OnStream("events", "--all", "--json").Lines(lines...).Hold()
	rec := &streamRec{}
	clk := rextest.NewFakeClock(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rec.supervisor(localClient(fr), clk).Run(ctx); close(done) }()
	waitFor(t, "all events", func() bool { ev, _, _ := rec.snapshot(); return len(ev) == len(lines)-1 })
	cancel()
	rextest.Recv(t, done, "the supervisor to return after cancel")
	events, bad, states := rec.snapshot()
	if len(bad) != 1 || string(bad[0]) != "this line is not json" {
		t.Fatalf("bad lines = %q", bad)
	}
	if events[len(events)-1].Inner != "mystery_inner" || events[3].Inner != "block_event" {
		t.Fatalf("events after the malformed line were lost: %+v", events)
	}
	if len(states) == 0 || !states[0].Up {
		t.Fatalf("states = %+v", states)
	}
	argv := fr.Calls()[0]
	if !slices.Equal(argv, split("rex --autostart=false --timeout=10s events --all --json")) {
		t.Fatalf("stream argv = %q", argv)
	}
}

func TestSupervisorReconnect(t *testing.T) {
	fr := rextest.NewFakeRunner()
	stopped := rextest.FixtureString("stderr_whoami_stopped.txt")
	fr.OnStream("events").Exit(4, stopped)
	fr.OnStream("events").Exit(4, stopped)
	fr.OnStream("events").Lines(`{"type":"session_created"}`, `{"type":"session_destroyed"}`).Hold()
	rec := &streamRec{}
	clk := rextest.NewFakeClock(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rec.supervisor(localClient(fr), clk).Run(ctx); close(done) }()

	clk.BlockUntilWaiters(1)
	clk.Advance(time.Second)
	clk.BlockUntilWaiters(2)
	clk.Advance(2 * time.Second)
	waitFor(t, "resumed stream", func() bool { ev, _, _ := rec.snapshot(); return len(ev) == 2 })
	cancel()
	rextest.Recv(t, done, "the supervisor to return after cancel")

	_, _, states := rec.snapshot()
	if len(states) < 3 {
		t.Fatalf("states = %+v", states)
	}
	for i := 0; i < 2; i++ {
		if states[i].Up || states[i].Code != 4 || states[i].Kind != rex.ExitUnreachable {
			t.Fatalf("state %d = %+v", i, states[i])
		}
	}
	if !states[2].Up {
		t.Fatalf("stream not marked up: %+v", states[2])
	}
	if w := clk.Waits(); !slices.Equal(w, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("waits = %v", w)
	}
	if n := len(fr.CallsContaining("events")); n != 3 {
		t.Fatalf("stream started %d times", n)
	}
}

func TestSupervisorBackoffCap(t *testing.T) {
	fr := rextest.NewFakeRunner()
	for i := 0; i < 7; i++ {
		fr.OnStream("events").Exit(4, "stopped")
	}
	rec := &streamRec{}
	clk := rextest.NewFakeClock(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rec.supervisor(localClient(fr), clk).Run(ctx); close(done) }()
	for i := 1; i <= 7; i++ {
		clk.BlockUntilWaiters(i)
		w := clk.Waits()
		clk.Advance(w[len(w)-1])
	}
	waitFor(t, "eighth stream", func() bool { return len(fr.CallsContaining("events")) == 8 })
	cancel()
	rextest.Recv(t, done, "the supervisor to return after cancel")
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for i := range want {
		want[i] *= time.Second
	}
	if w := clk.Waits(); !slices.Equal(w, want) {
		t.Fatalf("waits = %v", w)
	}
}
