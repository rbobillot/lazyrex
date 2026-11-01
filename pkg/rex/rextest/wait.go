package rextest

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Timeout bounds every wait of a test, so a broken expectation fails, naming
// what it waited for, instead of hanging until go test's own timeout.
const Timeout = 5 * time.Second

// HoldTimeout bounds a call a callback holds: longer than Timeout, so the
// test goroutine's own wait reports first.
const HoldTimeout = 2 * Timeout

// Recv receives from ch on the test goroutine, failing t after Timeout.
func Recv[T any](t testing.TB, ch <-chan T, what string) T {
	t.Helper()
	return recvWithin(t, ch, what, Timeout)
}

func recvWithin[T any](t testing.TB, ch <-chan T, what string, d time.Duration) T {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
		t.Fatalf("timed out after %v waiting for %s", d, what)
	}
	var zero T
	return zero
}

// Send sends v on ch from the test goroutine, failing t after Timeout.
func Send[T any](t testing.TB, ch chan<- T, v T, what string) {
	t.Helper()
	sendWithin(t, ch, v, what, Timeout)
}

func sendWithin[T any](t testing.TB, ch chan<- T, v T, what string, d time.Duration) {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case ch <- v:
	case <-timer.C:
		t.Fatalf("timed out after %v waiting for %s", d, what)
	}
}

// WaitGroup waits for wg on the test goroutine, failing t after Timeout.
// Every Add must have happened already: a timed out wait leaves its waiter
// running.
func WaitGroup(t testing.TB, wg *sync.WaitGroup, what string) {
	t.Helper()
	waitGroupWithin(t, wg, what, Timeout)
}

func waitGroupWithin(t testing.TB, wg *sync.WaitGroup, what string, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	recvWithin(t, done, what, d)
}

// StopTimeout bounds how long Bounded waits for cancelled work to return. It
// must stay above rex's waitDelay (2s), which a killed process's lingering
// pipes can hold its Wait for.
const StopTimeout = 3 * time.Second

// Bounded runs f on its own goroutine and returns its result. Past Timeout
// it cancels f's context, waits up to StopTimeout for f to return, and fails
// t, naming what it waited for.
func Bounded[T any](t testing.TB, what string, f func(ctx context.Context) T) T {
	t.Helper()
	return boundedWithin(t, what, Timeout, StopTimeout, f)
}

func boundedWithin[T any](t testing.TB, what string, d, stop time.Duration, f func(ctx context.Context) T) T {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan T, 1)
	go func() { ch <- f(ctx) }()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
	}
	cancel()
	stopTimer := time.NewTimer(stop)
	defer stopTimer.Stop()
	select {
	case <-ch:
		t.Fatalf("timed out after %v waiting for %s; it returned once cancelled", d, what)
	case <-stopTimer.C:
		t.Fatalf("timed out after %v waiting for %s; still running %v after its cancel", d, what, stop)
	}
	var zero T
	return zero
}

// Holds bounds the waits of callbacks the code under test runs on its own
// goroutines, which must not call FailNow. The end of the test releases them.
type Holds struct {
	t     testing.TB
	d     time.Duration
	stop  chan struct{}
	mu    sync.Mutex
	ended bool
}

// NewHolds returns the Holds of t, bounded by HoldTimeout.
func NewHolds(t testing.TB) *Holds { return newHolds(t, HoldTimeout) }

func newHolds(t testing.TB, d time.Duration) *Holds {
	h := &Holds{t: t, d: d, stop: make(chan struct{})}
	t.Cleanup(h.end)
	return h
}

// Wait waits for ch. Past the deadline it reports what and returns, so the
// held call goes on; once the test has ended it returns without a report.
func (h *Holds) Wait(ch <-chan struct{}, what string) {
	timer := time.NewTimer(h.d)
	defer timer.Stop()
	select {
	case <-ch:
	case <-h.stop:
	case <-timer.C:
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.ended {
			h.t.Errorf("held call timed out after %v waiting for %s", h.d, what)
		}
	}
}

// end runs as a cleanup of the test, before testing marks it done, so no
// report can follow it.
func (h *Holds) end() {
	h.mu.Lock()
	h.ended = true
	h.mu.Unlock()
	close(h.stop)
}
