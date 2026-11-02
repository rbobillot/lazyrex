// Package rextest holds test doubles for package rex. Only test files import it.
package rextest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

// Fixture reads testdata/<name> from the repository root.
func Fixture(name string) []byte {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("rextest: no caller information")
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", name))
	if err != nil {
		panic(err)
	}
	return b
}

// FixtureString is Fixture as a string.
func FixtureString(name string) string { return string(Fixture(name)) }

// Response is a canned result for argv containing a token run.
type Response struct {
	tokens []string
	result rex.Result
	once   bool
	used   bool
}

// Return sets the canned stdout, stderr and exit code.
func (r *Response) Return(stdout, stderr string, code int) *Response {
	r.result = rex.Result{Stdout: []byte(stdout), Stderr: []byte(stderr), Code: code}
	return r
}

// Once makes the response match a single call.
func (r *Response) Once() *Response {
	r.once = true
	return r
}

// FakeStream is a canned event stream.
type FakeStream struct {
	tokens []string
	lines  []string
	code   int
	stderr string
	hold   bool
	until  <-chan struct{}
	ended  chan struct{}
}

// Lines sets the lines the stream delivers.
func (s *FakeStream) Lines(lines ...string) *FakeStream {
	s.lines = append(s.lines, lines...)
	return s
}

// Exit sets the exit code and stderr after the last line.
func (s *FakeStream) Exit(code int, stderr string) *FakeStream {
	s.code, s.stderr = code, stderr
	return s
}

// Hold keeps the stream open after its lines until its context ends.
func (s *FakeStream) Hold() *FakeStream {
	s.hold = true
	return s
}

// Until keeps the stream open after its lines until ch closes, then exits
// with the code and stderr set by Exit; the context still ends it first.
func (s *FakeStream) Until(ch <-chan struct{}) *FakeStream {
	s.until = ch
	return s
}

// FakeRunner records every argv and answers with canned responses. The most
// recently registered matching response wins. Unmatched Run calls exit 2.
// Unmatched Stream calls stay open until their context ends.
type FakeRunner struct {
	mu         sync.Mutex
	calls      [][]string
	unexpected [][]string
	responses  []*Response
	streams    []*FakeStream
	// OnCall, if set, runs (outside the lock) before each Run call returns.
	OnCall func(argv []string)
}

// NewFakeRunner returns an empty FakeRunner.
func NewFakeRunner() *FakeRunner { return &FakeRunner{} }

// On registers a response for argv containing tokens contiguously.
func (f *FakeRunner) On(tokens ...string) *Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &Response{tokens: tokens}
	f.responses = append(f.responses, r)
	return r
}

// OnStream queues a stream for argv containing tokens contiguously.
func (f *FakeRunner) OnStream(tokens ...string) *FakeStream {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &FakeStream{tokens: tokens, ended: make(chan struct{})}
	f.streams = append(f.streams, s)
	return s
}

// Ended closes once the stream started for s has ended.
func (s *FakeStream) Ended() <-chan struct{} { return s.ended }

// Calls returns every argv seen by Run and Stream, in order.
func (f *FakeRunner) Calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = slices.Clone(c)
	}
	return out
}

// CallsContaining returns the recorded argv containing tokens contiguously.
func (f *FakeRunner) CallsContaining(tokens ...string) [][]string {
	var out [][]string
	for _, c := range f.Calls() {
		if Contains(c, tokens...) {
			out = append(out, c)
		}
	}
	return out
}

// Unexpected returns the Run argv no response matched.
func (f *FakeRunner) Unexpected() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.unexpected)
}

// Reset forgets the recorded calls, keeping responses.
func (f *FakeRunner) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.unexpected = nil, nil
}

// Contains reports whether argv contains tokens as a contiguous run.
func Contains(argv []string, tokens ...string) bool {
	if len(tokens) == 0 {
		return true
	}
	for i := 0; i+len(tokens) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(tokens)], tokens) {
			return true
		}
	}
	return false
}

// Run implements rex.Runner.
func (f *FakeRunner) Run(ctx context.Context, argv []string) (rex.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(argv))
	var res rex.Result
	matched := false
	for i := len(f.responses) - 1; i >= 0; i-- {
		r := f.responses[i]
		if r.once && r.used {
			continue
		}
		if Contains(argv, r.tokens...) {
			r.used = true
			res = r.result
			matched = true
			break
		}
	}
	if !matched {
		f.unexpected = append(f.unexpected, slices.Clone(argv))
		res = rex.Result{Code: 2, Stderr: []byte("fake runner: unexpected argv " + strings.Join(argv, " "))}
	}
	hook := f.OnCall
	f.mu.Unlock()
	if hook != nil {
		hook(argv)
	}
	if err := ctx.Err(); err != nil {
		return rex.Result{Code: -1}, nil
	}
	res.Stdout = slices.Clone(res.Stdout)
	res.Stderr = slices.Clone(res.Stderr)
	return res, nil
}

// Stream implements rex.Runner.
func (f *FakeRunner) Stream(ctx context.Context, argv []string) (rex.LineStream, error) {
	f.mu.Lock()
	f.calls = append(f.calls, slices.Clone(argv))
	var spec *FakeStream
	for i, s := range f.streams {
		if Contains(argv, s.tokens...) {
			spec = s
			f.streams = append(f.streams[:i], f.streams[i+1:]...)
			break
		}
	}
	f.mu.Unlock()
	if spec == nil {
		spec = &FakeStream{hold: true, ended: make(chan struct{})}
	}
	ls := &lineStream{lines: make(chan []byte), done: make(chan struct{})}
	go func() {
		defer close(spec.ended)
		defer close(ls.done)
		defer close(ls.lines)
		for _, l := range spec.lines {
			select {
			case ls.lines <- []byte(l):
			case <-ctx.Done():
				ls.res = rex.Result{Code: 130}
				return
			}
		}
		if spec.hold {
			<-ctx.Done()
			ls.res = rex.Result{Code: 130}
			return
		}
		if spec.until != nil {
			select {
			case <-spec.until:
			case <-ctx.Done():
				ls.res = rex.Result{Code: 130}
				return
			}
		}
		ls.res = rex.Result{Code: spec.code, Stderr: []byte(spec.stderr)}
	}()
	return ls, nil
}

type lineStream struct {
	lines chan []byte
	done  chan struct{}
	res   rex.Result
}

func (l *lineStream) Lines() <-chan []byte { return l.lines }

func (l *lineStream) Wait() rex.Result {
	<-l.done
	return l.res
}

// FakeClock is a manually advanced rex.Clock.
type FakeClock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	timers  []*fakeTimer
	waits   []time.Duration
	waiters int
}

type fakeTimer struct {
	clock   *FakeClock
	at      time.Time
	fn      func()
	ch      chan time.Time
	stopped bool
	fired   bool
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}

// NewFakeClock returns a FakeClock set to t.
func NewFakeClock(t time.Time) *FakeClock {
	c := &FakeClock{now: t}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Now implements rex.Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc implements rex.Clock.
func (c *FakeClock) AfterFunc(d time.Duration, fn func()) rex.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), fn: fn}
	c.timers = append(c.timers, t)
	return t
}

// After implements rex.Clock.
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	c.waits = append(c.waits, d)
	c.waiters++
	c.cond.Broadcast()
	return t.ch
}

// Waits returns the durations passed to After, in order.
func (c *FakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.waits)
}

// BlockUntilWaiters waits until After has been called n times in total.
func (c *FakeClock) BlockUntilWaiters(n int) {
	deadline := time.Now().Add(Timeout)
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.waiters < n {
		if time.Now().After(deadline) {
			panic("rextest: timed out waiting for clock waiters")
		}
		c.mu.Unlock()
		time.Sleep(time.Millisecond)
		c.mu.Lock()
	}
}

// Advance moves the clock and fires due timers in deadline order.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*fakeTimer
	keep := c.timers[:0]
	for _, t := range c.timers {
		switch {
		case t.stopped:
		case !t.at.After(now):
			t.fired = true
			due = append(due, t)
		default:
			keep = append(keep, t)
		}
	}
	c.timers = keep
	c.mu.Unlock()
	sort.SliceStable(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		if t.fn != nil {
			t.fn()
		} else {
			t.ch <- now
		}
	}
}

// FakeSockets is a rex.TunnelSockets that touches no file. Like flock, a
// path claimed and not released yet cannot be claimed again.
type FakeSockets struct {
	mu       sync.Mutex
	prepared []string
	cleaned  []string
	events   []string
	claimed  map[string]bool
	waits    int
	gates    map[string]<-chan struct{}
	holds    *Holds
	// Ready reports whether the socket of the nth WaitReady call (from 1)
	// appears; when it does not, WaitReady waits for the tunnel to exit. Nil
	// means every socket appears.
	Ready func(n int) bool
	// PrepareErr, if set, is returned by Prepare.
	PrepareErr error
	// PrepareFunc, if set, is called by Prepare instead of returning
	// PrepareErr.
	PrepareFunc func(path string) error
	// Held reports whether another process holds the claim of path.
	Held func(path string) bool
	// ClaimErr, if set, is returned by Claim.
	ClaimErr error
}

// Claim implements rex.TunnelSockets.
func (s *FakeSockets) Claim(dir, path string) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "claim:"+path)
	switch {
	case s.ClaimErr != nil:
		return nil, s.ClaimErr
	case s.claimed[path] || (s.Held != nil && s.Held(path)):
		return nil, rex.ErrSocketHeld
	}
	if s.claimed == nil {
		s.claimed = map[string]bool{}
	}
	s.claimed[path] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.claimed, path)
			s.events = append(s.events, "release:"+path)
		})
	}, nil
}

// Events returns the claim, release, prepare and cleanup calls in order,
// each as "claim:PATH", "release:PATH", "prepare:PATH" or "cleanup:PATH".
func (s *FakeSockets) Events() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

// GateCleanup holds Cleanup of path until gate closes, the end of t, or
// HoldTimeout, which fails t.
func (s *FakeSockets) GateCleanup(t testing.TB, path string, gate <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holds == nil {
		s.holds = NewHolds(t)
	}
	if s.gates == nil {
		s.gates = map[string]<-chan struct{}{}
	}
	s.gates[path] = gate
}

// Prepare implements rex.TunnelSockets.
func (s *FakeSockets) Prepare(dir, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepared = append(s.prepared, path)
	s.events = append(s.events, "prepare:"+path)
	if s.PrepareFunc != nil {
		return s.PrepareFunc(path)
	}
	return s.PrepareErr
}

// WaitReady implements rex.TunnelSockets.
func (s *FakeSockets) WaitReady(ctx context.Context, path string, exited <-chan struct{}) bool {
	s.mu.Lock()
	s.waits++
	ready := s.Ready == nil || s.Ready(s.waits)
	s.mu.Unlock()
	if ready {
		return ctx.Err() == nil
	}
	select {
	case <-exited:
	case <-ctx.Done():
	}
	return false
}

// Cleanup implements rex.TunnelSockets.
func (s *FakeSockets) Cleanup(path string) {
	s.mu.Lock()
	gate, holds := s.gates[path], s.holds
	s.mu.Unlock()
	if gate != nil {
		holds.Wait(gate, "the cleanup gate of "+path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleaned = append(s.cleaned, path)
	s.events = append(s.events, "cleanup:"+path)
}

// Prepared returns the paths passed to Prepare, in order.
func (s *FakeSockets) Prepared() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.prepared)
}

// Cleaned returns the paths passed to Cleanup, in order.
func (s *FakeSockets) Cleaned() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.cleaned)
}

// SSHEnv returns an SSH environment for tests: runtime directory dir, pid
// 42, these sockets and a fresh probe cache, with 1s to 30s backoff.
func SSHEnv(dir string, sockets *FakeSockets) *rex.SSHEnv {
	return &rex.SSHEnv{
		Dir:     func() (string, error) { return dir, nil },
		Sockets: sockets,
		PID:     42,
		Probes:  &rex.SSHProbes{},
		Min:     time.Second,
		Max:     30 * time.Second,
	}
}
