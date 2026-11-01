package rextest

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTB records failures instead of failing; Fatalf ends its goroutine as
// FailNow does, and runCleanups stands for the end of the test.
type fakeTB struct {
	testing.TB
	mu       sync.Mutex
	fatals   []string
	errors   []string
	cleanups []func()
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.mu.Lock()
	f.fatals = append(f.fatals, fmt.Sprintf(format, args...))
	f.mu.Unlock()
	runtime.Goexit()
}

func (f *fakeTB) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func (f *fakeTB) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }

func (f *fakeTB) runCleanups() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

func (f *fakeTB) failures() (fatals, errors []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fatals...), append([]string(nil), f.errors...)
}

// onGoroutine runs f with a fakeTB on its own goroutine, so a Fatalf ends
// only that goroutine, and returns once f has ended, with how long it took.
func onGoroutine(t *testing.T, f func(tb *fakeTB)) (*fakeTB, time.Duration) {
	t.Helper()
	tb := &fakeTB{}
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		f(tb)
	}()
	Recv(t, done, "the helper under test to return")
	return tb, time.Since(start)
}

func wantFatal(t *testing.T, tb *fakeTB, want string) {
	t.Helper()
	fatals, errors := tb.failures()
	if len(fatals) != 1 || !strings.Contains(fatals[0], want) || len(errors) != 0 {
		t.Fatalf("fatals %q, errors %q, want one fatal containing %q", fatals, errors, want)
	}
}

func wantClean(t *testing.T, tb *fakeTB) {
	t.Helper()
	if fatals, errors := tb.failures(); len(fatals) != 0 || len(errors) != 0 {
		t.Fatalf("fatals %q, errors %q", fatals, errors)
	}
}

func TestRecvReady(t *testing.T) {
	ch := make(chan int, 1)
	ch <- 7
	var got int
	tb, _ := onGoroutine(t, func(tb *fakeTB) { got = recvWithin(tb, ch, "the thing", time.Hour) })
	wantClean(t, tb)
	if got != 7 {
		t.Fatalf("got %d, want 7", got)
	}
}

func TestRecvClosed(t *testing.T) {
	ch := make(chan struct{})
	close(ch)
	tb, _ := onGoroutine(t, func(tb *fakeTB) { recvWithin(tb, ch, "the thing", time.Hour) })
	wantClean(t, tb)
}

func TestRecvTimesOut(t *testing.T) {
	ch := make(chan struct{})
	tb, d := onGoroutine(t, func(tb *fakeTB) { recvWithin(tb, ch, "the thing", 20*time.Millisecond) })
	wantFatal(t, tb, "timed out after 20ms waiting for the thing")
	if d < 20*time.Millisecond || d > time.Second {
		t.Fatalf("failed after %v", d)
	}
}

func TestSend(t *testing.T) {
	ch := make(chan int)
	got := make(chan int, 1)
	go func() { got <- <-ch }()
	tb, _ := onGoroutine(t, func(tb *fakeTB) { sendWithin(tb, ch, 3, "the receiver", time.Hour) })
	wantClean(t, tb)
	if v := Recv(t, got, "the value sent"); v != 3 {
		t.Fatalf("received %d, want 3", v)
	}
	tb, d := onGoroutine(t, func(tb *fakeTB) { sendWithin(tb, make(chan int), 3, "the receiver", 20*time.Millisecond) })
	wantFatal(t, tb, "timed out after 20ms waiting for the receiver")
	if d > time.Second {
		t.Fatalf("failed after %v", d)
	}
}

func TestWaitGroup(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go wg.Done()
	tb, _ := onGoroutine(t, func(tb *fakeTB) { waitGroupWithin(tb, &wg, "the workers", time.Hour) })
	wantClean(t, tb)

	var stuck sync.WaitGroup
	stuck.Add(1)
	defer stuck.Done()
	tb, d := onGoroutine(t, func(tb *fakeTB) { waitGroupWithin(tb, &stuck, "the stuck workers", 20*time.Millisecond) })
	wantFatal(t, tb, "timed out after 20ms waiting for the stuck workers")
	if d > time.Second {
		t.Fatalf("failed after %v", d)
	}
}

func TestBoundedReturns(t *testing.T) {
	var got int
	tb, _ := onGoroutine(t, func(tb *fakeTB) {
		got = boundedWithin(tb, "the work", time.Hour, time.Hour, func(context.Context) int { return 5 })
	})
	wantClean(t, tb)
	if got != 5 {
		t.Fatalf("got %d, want 5", got)
	}
}

// schedSlack is how late past its intervals a bounded wait may fail. Timers
// never fire early, so its lower bounds take none.
const schedSlack = 500 * time.Millisecond

func TestBoundedCancels(t *testing.T) {
	var cause error
	tb, d := onGoroutine(t, func(tb *fakeTB) {
		boundedWithin(tb, "the work", 20*time.Millisecond, time.Hour, func(ctx context.Context) int {
			select {
			case <-ctx.Done():
			case <-time.After(Timeout):
			}
			cause = ctx.Err()
			return 0
		})
	})
	wantFatal(t, tb, "timed out after 20ms waiting for the work; it returned once cancelled")
	if cause == nil || d > 20*time.Millisecond+schedSlack {
		t.Fatalf("context error %v, failed after %v", cause, d)
	}
	if d < 20*time.Millisecond {
		t.Fatalf("failed after %v, before its 20ms timeout", d)
	}
}

func TestBoundedStuck(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	tb, d := onGoroutine(t, func(tb *fakeTB) {
		boundedWithin(tb, "the work", 20*time.Millisecond, 20*time.Millisecond, func(context.Context) int {
			select {
			case <-release:
			case <-time.After(Timeout):
			}
			return 0
		})
	})
	wantFatal(t, tb, "timed out after 20ms waiting for the work; still running 20ms after its cancel")
	if d > 40*time.Millisecond+schedSlack {
		t.Fatalf("failed after %v", d)
	}
	if d < 40*time.Millisecond {
		t.Fatalf("failed after %v, before its 20ms timeout and 20ms stop", d)
	}
}

func TestHoldsReleased(t *testing.T) {
	tb := &fakeTB{}
	h := newHolds(tb, time.Hour)
	ch := make(chan struct{})
	close(ch)
	h.Wait(ch, "the release")
	tb.runCleanups()
	wantClean(t, tb)
}

func TestHoldsTimesOut(t *testing.T) {
	tb := &fakeTB{}
	h := newHolds(tb, 20*time.Millisecond)
	start := time.Now()
	h.Wait(make(chan struct{}), "the forgotten release")
	if d := time.Since(start); d < 20*time.Millisecond || d > time.Second {
		t.Fatalf("returned after %v", d)
	}
	fatals, errors := tb.failures()
	if len(fatals) != 0 || len(errors) != 1 || !strings.Contains(errors[0], "held call timed out after 20ms waiting for the forgotten release") {
		t.Fatalf("fatals %q, errors %q", fatals, errors)
	}
	tb.runCleanups()
}

func TestHoldsEndReleases(t *testing.T) {
	tb := &fakeTB{}
	h := newHolds(tb, time.Hour)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Wait(make(chan struct{}), "a release the test never sends")
	}()
	tb.runCleanups()
	Recv(t, done, "the held call to return at the end of the test")
	wantClean(t, tb)
}

func TestHoldsSilentAfterEnd(t *testing.T) {
	tb := &fakeTB{}
	h := newHolds(tb, time.Nanosecond)
	tb.runCleanups()
	for range 100 {
		h.Wait(make(chan struct{}), "a release after the end")
	}
	wantClean(t, tb)
}

// TestRecvMismatchedCall: a fake runner hook that never matches its call
// fails the wait that expected it, instead of hanging.
func TestRecvMismatchedCall(t *testing.T) {
	fr := NewFakeRunner()
	entered := make(chan struct{})
	fr.OnCall = func(argv []string) {
		if Contains(argv, "'whoami'") {
			close(entered)
		}
	}
	tb, d := onGoroutine(t, func(tb *fakeTB) {
		if _, err := fr.Run(context.Background(), []string{"ssh", "remote-host", "'whoami-nomatch'"}); err != nil {
			t.Error(err)
		}
		recvWithin(tb, entered, "the whoami call", 50*time.Millisecond)
	})
	wantFatal(t, tb, "waiting for the whoami call")
	if d > time.Second {
		t.Fatalf("failed after %v", d)
	}
}
