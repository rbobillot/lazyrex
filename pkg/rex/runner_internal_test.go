package rex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a test: the tests below run the test binary as a
// child process through it.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("HELPER_MODE") {
	case "exit3":
		os.Exit(3)
	case "linger", "linger-die":
		// A grandchild inherits stdout and stderr and outlives this process,
		// as an ssh ControlPersist master does. It holds the test's lease
		// from before its start.
		gc := helperCommand(context.Background(), "hold")
		gc.Stdout, gc.Stderr = os.Stdout, os.Stderr
		if err := shareLease(gc); err != nil {
			os.Exit(10)
		}
		if err := gc.Start(); err != nil {
			os.Exit(9)
		}
		if os.Getenv("HELPER_MODE") == "linger-die" {
			// As a kill right after the start would, before naming it.
			os.Exit(0)
		}
		fmt.Println("ready", gc.Process.Pid)
		os.Exit(0)
	case "hold":
		// Until the test's cleanup removes the lease, for at most 60s, past
		// lingerWait. A lease already gone ends it at once.
		lease := os.Getenv("HELPER_LEASE")
		for end := time.Now().Add(60 * time.Second); lease != "" && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(lease); err != nil {
				break
			}
		}
	}
	os.Exit(0)
}

// checkReady checks the "ready PID" line of the linger helper.
func checkReady(t *testing.T, line string) {
	t.Helper()
	var pid int
	if _, err := fmt.Sscanf(line, "ready %d", &pid); err != nil {
		t.Fatalf("line %q: %v", line, err)
	}
}

func helperCommand(ctx context.Context, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "HELPER_MODE="+mode, "GORACE="+helperRace())
	return cmd
}

func helperArgv(t *testing.T, mode string) []string {
	t.Helper()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("HELPER_MODE", mode)
	t.Setenv("GORACE", helperRace())
	return []string{os.Args[0], "-test.run=^TestHelperProcess$"}
}

// helperRace drops the race detector's 1s sleep at exit from the helpers,
// whose exits these tests wait for: under -race it would hold each linger
// test, and the lease, for a second.
func helperRace() string {
	return strings.TrimSpace(os.Getenv("GORACE") + " atexit_sleep_ms=0")
}

// lingerWait bounds the waits of these tests: past the 10s the linger tests
// allow, so their own check reports a slow return first. stopWait bounds the
// wait for cancelled work to return.
const (
	lingerWait = 20 * time.Second
	stopWait   = 2 * time.Second
)

// within runs f on its own goroutine. Past lingerWait it cancels f's
// context, waits up to stopWait for f to return, and fails t, naming what it
// waited for. rextest imports rex, so its Bounded is out of reach here.
func within(t *testing.T, what string, f func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(ctx)
	}()
	timer := time.NewTimer(lingerWait)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
	}
	cancel()
	stop := time.NewTimer(stopWait)
	defer stop.Stop()
	select {
	case <-done:
		t.Fatalf("timed out after %v waiting for %s; it returned once cancelled", lingerWait, what)
	case <-stop.C:
		t.Fatalf("timed out after %v waiting for %s; still running %v after its cancel", lingerWait, what, stopWait)
	}
}

func shortWaitDelay(t *testing.T) {
	old := waitDelay
	waitDelay = 100 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })
}

type exitCoder int

func (c exitCoder) ExitCode() int { return int(c) }

// TestWaitCode (1p).
func TestWaitCode(t *testing.T) {
	if got := waitCode(fmt.Errorf("wait: %w", exec.ErrWaitDelay), exitCoder(0)); got != 0 {
		t.Errorf("ErrWaitDelay after exit 0 = %d", got)
	}
	var cmd *exec.Cmd
	var err error
	within(t, "the exit3 helper to exit", func(ctx context.Context) {
		cmd = helperCommand(ctx, "exit3")
		err = cmd.Run()
	})
	if got := waitCode(err, cmd.ProcessState); got != 3 {
		t.Errorf("exit 3 = %d (%v)", got, err)
	}
	if got := waitCode(errors.New("boom"), nil); got != -1 {
		t.Errorf("other error = %d", got)
	}
}

// TestRunKeepsCodePastWaitDelay (decision 6): a grandchild holding the pipes
// past WaitDelay does not turn a successful exit into a failure.
func TestRunKeepsCodePastWaitDelay(t *testing.T) {
	shortWaitDelay(t)
	leaseGrandchildren(t)
	argv := helperArgv(t, "linger")
	start := time.Now()
	var res Result
	var err error
	within(t, "Run to return past the lingering grandchild", func(ctx context.Context) { res, err = ExecRunner{}.Run(ctx, argv) })
	if err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	if !strings.HasPrefix(string(res.Stdout), "ready ") {
		t.Fatalf("result = %d %q %q", res.Code, res.Stdout, res.Stderr)
	}
	checkReady(t, strings.TrimSpace(string(res.Stdout)))
	if res.Code != 0 {
		t.Fatalf("code = %d, stderr %q", res.Code, res.Stderr)
	}
	if d > 10*time.Second {
		t.Fatalf("Run waited %s for the grandchild", d)
	}
}

// TestStreamKeepsCodePastWaitDelay (decision 6).
func TestStreamKeepsCodePastWaitDelay(t *testing.T) {
	shortWaitDelay(t)
	leaseGrandchildren(t)
	argv := helperArgv(t, "linger")
	start := time.Now()
	var lines []string
	var res Result
	var err error
	within(t, "the stream to end past the lingering grandchild", func(ctx context.Context) {
		var ls LineStream
		if ls, err = (ExecRunner{}).Stream(ctx, argv); err != nil {
			return
		}
		for l := range ls.Lines() {
			lines = append(lines, string(l))
		}
		res = ls.Wait()
	})
	if err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	if len(lines) == 0 {
		t.Fatalf("no line; code %d, stderr %q", res.Code, res.Stderr)
	}
	checkReady(t, lines[0])
	if res.Code != 0 {
		t.Fatalf("code = %d, lines %q, stderr %q", res.Code, lines, res.Stderr)
	}
	if d > 10*time.Second {
		t.Fatalf("the stream stayed open %s for the grandchild", d)
	}
}
