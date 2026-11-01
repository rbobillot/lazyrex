package gui

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// fatalTB records a Fatalf instead of failing, and ends its goroutine as
// FailNow does.
type fatalTB struct {
	testing.TB
	msg string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// waitWorkersOn runs waitWorkers with a fatalTB on its own goroutine; done
// closes once it has returned or failed.
func waitWorkersOn(ui *goUI) (tb *fatalTB, done chan struct{}) {
	tb, done = &fatalTB{}, make(chan struct{})
	go func() {
		defer close(done)
		ui.waitWorkers(tb)
	}()
	return tb, done
}

// TestGoUIWaitWorkersBlocks: waitWorkers does not return while a worker
// runs.
func TestGoUIWaitWorkersBlocks(t *testing.T) {
	ui := &goUI{}
	holds := rextest.NewHolds(t)
	block := make(chan struct{})
	ui.Go(func() { holds.Wait(block, "the release of the worker") })
	tb, done := waitWorkersOn(ui)
	select {
	case <-done:
		t.Fatalf("waitWorkers returned while a worker ran: %q", tb.msg)
	case <-time.After(50 * time.Millisecond):
	}
	close(block)
	rextest.Recv(t, done, "waitWorkers once the worker returned")
	if tb.msg != "" {
		t.Fatal(tb.msg)
	}
}

// TestGoUISettleFollowsNewWorkers: settle waits for a worker an update
// starts, and runs that worker's update.
func TestGoUISettleFollowsNewWorkers(t *testing.T) {
	ui := &goUI{}
	ran := false
	ui.Go(func() {
		ui.Update(func() {
			ui.Go(func() {
				time.Sleep(20 * time.Millisecond)
				ui.Update(func() { ran = true })
			})
		})
	})
	ui.settle(t)
	if !ran {
		t.Fatal("settle returned before the worker an update started")
	}
}

// TestGoUIWaitWorkersTimesOut: a worker that never returns fails the wait,
// naming it.
func TestGoUIWaitWorkersTimesOut(t *testing.T) {
	ui := &goUI{wait: 20 * time.Millisecond}
	holds := rextest.NewHolds(t)
	block := make(chan struct{})
	defer close(block)
	ui.Go(func() { holds.Wait(block, "the release of the stuck worker") })
	start := time.Now()
	tb, done := waitWorkersOn(ui)
	rextest.Recv(t, done, "waitWorkers to give up")
	if d := time.Since(start); d > time.Second {
		t.Fatalf("gave up after %v", d)
	}
	if want := "timed out after 20ms waiting for the Go workers (1 running)"; !strings.Contains(tb.msg, want) {
		t.Fatalf("message %q, want %q", tb.msg, want)
	}
}
