package rex_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
}

// run runs argv through r, cancelling it and failing t after
// rextest.Timeout.
func run(t *testing.T, r rex.Runner, what string, argv ...string) (rex.Result, error) {
	t.Helper()
	type out struct {
		res rex.Result
		err error
	}
	o := rextest.Bounded(t, what, func(ctx context.Context) out {
		res, err := r.Run(ctx, argv)
		return out{res, err}
	})
	return o.res, o.err
}

// runStream streams argv through r, hands its lines to read, reads what read
// left and waits for the result. Start, reads and Wait run under one
// rextest.Bounded: past rextest.Timeout the stream's context is cancelled,
// which kills the direct child and so closes its pipes, its end is awaited
// up to rextest.StopTimeout, and t fails. read may cancel the stream early;
// it must return once lines is closed.
func runStream(t *testing.T, r rex.Runner, what string, argv []string, read func(lines <-chan []byte, cancel context.CancelFunc) []string) ([]string, rex.Result, error) {
	t.Helper()
	type out struct {
		lines []string
		res   rex.Result
		err   error
	}
	o := rextest.Bounded(t, what, func(ctx context.Context) out {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		ls, err := r.Stream(ctx, argv)
		if err != nil {
			return out{err: err}
		}
		lines := ls.Lines()
		got := read(lines, cancel)
		for range lines {
		}
		return out{lines: got, res: ls.Wait()}
	})
	return o.lines, o.res, o.err
}

func readAll(lines <-chan []byte, _ context.CancelFunc) []string {
	var got []string
	for l := range lines {
		got = append(got, string(l))
	}
	return got
}

// fakeStream holds its lines in a closed channel, and records whether Wait
// found it read to its close.
type fakeStream struct {
	lines     chan []byte
	waits     int
	drainedAt bool
}

func (s *fakeStream) Lines() <-chan []byte { return s.lines }

func (s *fakeStream) Wait() rex.Result {
	s.waits++
	// lines is closed, so this never blocks: ok is false once it was read
	// to its close.
	_, ok := <-s.lines
	s.drainedAt = !ok
	return rex.Result{Code: 7}
}

type fakeStreamRunner struct{ s *fakeStream }

func (fakeStreamRunner) Run(context.Context, []string) (rex.Result, error) {
	return rex.Result{Code: -1}, errors.New("not a stream")
}

func (f fakeStreamRunner) Stream(context.Context, []string) (rex.LineStream, error) {
	return f.s, nil
}

func newFakeStream(lines ...string) *fakeStream {
	s := &fakeStream{lines: make(chan []byte, len(lines))}
	for _, l := range lines {
		s.lines <- []byte(l)
	}
	close(s.lines)
	return s
}

// TestRunStreamDrains: a read that returns early still leaves runStream to
// read the stream to its end before it waits for the result.
func TestRunStreamDrains(t *testing.T) {
	s := newFakeStream("one", "two", "three")
	lines, res, err := runStream(t, fakeStreamRunner{s}, "the fake stream to end", []string{"fake"},
		func(lines <-chan []byte, _ context.CancelFunc) []string { return []string{string(<-lines)} })
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "one" || res.Code != 7 {
		t.Fatalf("lines %q, result %+v", lines, res)
	}
	if s.waits != 1 || !s.drainedAt {
		t.Fatalf("Wait called %d times, after the last line was read: %v", s.waits, s.drainedAt)
	}
}

func TestExecRunnerRun(t *testing.T) {
	requireSh(t)
	r := rex.ExecRunner{}
	res, err := run(t, r, "the sh command to exit", "sh", "-c", "printf out; printf err >&2; exit 3")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "out" || string(res.Stderr) != "err" || res.Code != 3 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := run(t, r, "the missing binary to fail", "/nonexistent/lazyrex-binary"); err == nil {
		t.Fatalf("missing binary did not fail to start")
	}
}

// TestRunnerDropsRexClient: rex reads the receiving client, block and session
// from REX_CLIENT, REX_BLOCK and REX_SESSION when a command omits them, so a
// child process must not inherit lazyrex's own pane context.
func TestRunnerDropsRexClient(t *testing.T) {
	if _, err := exec.LookPath("env"); err != nil {
		t.Skip("no env")
	}
	t.Setenv("REX_CLIENT", "client:x")
	t.Setenv("REX_BLOCK", "block:01self")
	t.Setenv("REX_SESSION", "session:01self")
	t.Setenv("REX_SERVER", "unix:///tmp/lazyrex-test.sock")
	t.Setenv("LAZYREX_TEST_KEEP", "kept")
	check := func(out string) {
		t.Helper()
		for _, v := range []string{"REX_CLIENT=", "REX_BLOCK=", "REX_SESSION="} {
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, v) {
					t.Errorf("child inherited %s", line)
				}
			}
		}
		for _, v := range []string{"REX_SERVER=unix:///tmp/lazyrex-test.sock", "LAZYREX_TEST_KEEP=kept", "PATH="} {
			if !strings.Contains(out, v) {
				t.Errorf("child lost %s", v)
			}
		}
	}
	res, err := run(t, rex.ExecRunner{}, "env to exit", "env")
	if err != nil || res.Code != 0 {
		t.Fatalf("env: %v %+v", err, res)
	}
	check(string(res.Stdout))
	lines, _, err := runStream(t, rex.ExecRunner{}, "the env stream to end", []string{"env"}, readAll)
	if err != nil {
		t.Fatal(err)
	}
	check(strings.Join(lines, "\n"))
}

func TestExecRunnerStreamSkipsLongLines(t *testing.T) {
	requireSh(t)
	r := rex.ExecRunner{MaxLine: 16}
	got, res, err := runStream(t, r, "the long-line stream to end",
		[]string{"sh", "-c", "echo one; echo " + strings.Repeat("x", 100) + "; echo two; exit 4"}, readAll)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "one,two" || res.Code != 4 {
		t.Fatalf("lines %q, result %+v", got, res)
	}
}

func TestExecRunnerStreamCancel(t *testing.T) {
	requireSh(t)
	lines, res, err := runStream(t, rex.ExecRunner{}, "the first line and the cancelled stream to end",
		[]string{"sh", "-c", "echo up; exec sleep 30"},
		func(lines <-chan []byte, cancel context.CancelFunc) []string {
			defer cancel()
			if l, ok := <-lines; ok {
				return []string{string(l)}
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "up" {
		t.Fatalf("first line %q", lines)
	}
	if res.Code == 0 {
		t.Fatalf("cancelled stream exited 0")
	}
}
