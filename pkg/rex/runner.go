package rex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Result is the outcome of one process. Code is -1 when the process did not
// exit normally (killed by a signal or by its context).
type Result struct {
	Stdout, Stderr []byte
	Code           int
	Duration       time.Duration
}

// LineStream delivers stdout lines of a long-lived process. Lines is closed
// when the process ends; Wait then returns its result.
type LineStream interface {
	Lines() <-chan []byte
	Wait() Result
}

// Runner runs argv without a shell.
type Runner interface {
	// Run returns an error only when the process could not start.
	Run(ctx context.Context, argv []string) (Result, error)
	Stream(ctx context.Context, argv []string) (LineStream, error)
}

// ExecRunner runs processes with os/exec.
type ExecRunner struct {
	// MaxLine is the longest stream line kept (default 4 MiB); longer lines
	// are discarded without stopping the stream.
	MaxLine int
}

const (
	defaultMaxLine = 4 << 20
	maxStderr      = 1 << 20
)

// contextVars are the variables rex reads as the client, session or block a
// command means when it does not name one. lazyrex always names them, and
// inheriting its own pane's values could aim a call at that pane or client.
// REX_SERVER stays: it selects the endpoint.
var contextVars = []string{"REX_CLIENT", "REX_SESSION", "REX_BLOCK"}

func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(contextVars, name) {
			out = append(out, kv)
		}
	}
	return out
}

// waitDelay bounds how long a finished process's lingering pipes (an ssh
// ControlPersist master keeps them open) can hold Wait.
var waitDelay = 2 * time.Second

func command(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = nil
	cmd.Env = childEnv(os.Environ())
	cmd.WaitDelay = waitDelay
	return cmd
}

// waitCode is the exit code of a waited process. exec.ErrWaitDelay only says
// its pipes outlived it: the process itself exited with ps's code.
func waitCode(err error, ps interface{ ExitCode() int }) int {
	if errors.Is(err, exec.ErrWaitDelay) && ps != nil {
		return ps.ExitCode()
	}
	return exitCode(err)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, argv []string) (Result, error) {
	if len(argv) == 0 {
		return Result{Code: -1}, errors.New("empty argv")
	}
	start := time.Now()
	cmd := command(ctx, argv)
	var stdout bytes.Buffer
	stderr := &cappedBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := cmd.Start(); err != nil {
		return Result{Code: -1, Stderr: []byte(err.Error())}, err
	}
	err := cmd.Wait()
	return Result{Stdout: stdout.Bytes(), Stderr: stderr.buf.Bytes(), Code: waitCode(err, cmd.ProcessState), Duration: time.Since(start)}, nil
}

type execStream struct {
	lines chan []byte
	done  chan struct{}
	res   Result
}

func (s *execStream) Lines() <-chan []byte { return s.lines }

func (s *execStream) Wait() Result {
	<-s.done
	return s.res
}

// Stream implements Runner.
func (r ExecRunner) Stream(ctx context.Context, argv []string) (LineStream, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty argv")
	}
	maxLine := r.MaxLine
	if maxLine <= 0 {
		maxLine = defaultMaxLine
	}
	start := time.Now()
	cmd := command(ctx, argv)
	// stdout goes through os/exec's copying goroutine, which WaitDelay
	// bounds, so a child holding the pipe cannot keep the stream open after
	// the process exited. Wait runs while lines are read, as that goroutine
	// blocks until they are.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	stderr := &cappedBuffer{max: maxStderr}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &execStream{lines: make(chan []byte), done: make(chan struct{})}
	waited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		waited <- err
	}()
	go func() {
		defer close(s.done)
		readLines(ctx, pr, maxLine, s.lines)
		close(s.lines)
		_, _ = io.Copy(io.Discard, pr)
		err := <-waited
		s.res = Result{Stderr: stderr.buf.Bytes(), Code: waitCode(err, cmd.ProcessState), Duration: time.Since(start)}
	}()
	return s, nil
}

func readLines(ctx context.Context, r io.Reader, maxLine int, out chan<- []byte) {
	br := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 && !tooLong {
			if len(line)+len(chunk) > maxLine+1 {
				tooLong = true
				line = line[:0]
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if !tooLong && len(line) > 0 {
			l := bytes.TrimRight(line, "\r\n")
			if len(l) > 0 {
				select {
				case out <- bytes.Clone(l):
				case <-ctx.Done():
					_, _ = io.Copy(io.Discard, r)
					return
				}
			}
		}
		line = line[:0]
		tooLong = false
		if err != nil {
			return
		}
	}
}
