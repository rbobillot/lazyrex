package rex

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rbobillot/lazyrex/pkg/render"
)

// LinkOptions configures an SSHLink.
type LinkOptions struct {
	Base  *Client
	Name  string
	Spec  SSHSpec
	Env   *SSHEnv
	Clock Clock
	Group *TunnelGroup
	// OnTunnel runs, off the caller's goroutine, when the tunnel goes up or
	// down.
	OnTunnel func(up bool)
}

type linkMode int

const (
	modeUnresolved linkMode = iota
	modeForward
	modeExec
)

type tunnelExit struct {
	code  int
	line  string
	retry time.Duration
}

// SSHLink is the connection to an active SSH target: its probes, its mode
// and, in forward mode, the tunnel kept up until its context ends. As a
// Router it routes each command to the tunnel or to ssh exec.
type SSHLink struct {
	o        LinkOptions
	ctx      context.Context
	dir      string
	setupErr error

	mu        sync.Mutex
	probe     *SSHProbe
	mode      linkMode
	fallback  bool
	fellBack  bool
	remote    string
	loop      uint64
	stopLoop  context.CancelFunc
	socket    string
	up        bool
	exit      *tunnelExit
	routeGen  uint64
	statusGen uint64
}

var errNoSSH = errors.New("SSH targets are not available")

// NewSSHLink returns the link of an SSH target, its tunnel bound to ctx. A
// setup error (no runtime directory) is kept: Status reports it and every
// command is refused with it.
func NewSSHLink(ctx context.Context, o LinkOptions) *SSHLink {
	if o.Clock == nil {
		o.Clock = RealClock()
	}
	if o.Group == nil {
		o.Group = &TunnelGroup{}
	}
	l := &SSHLink{o: o, ctx: ctx}
	if o.Env == nil {
		l.setupErr = errNoSSH
		return l
	}
	dir, err := o.Env.Dir()
	if err != nil {
		l.setupErr = fmt.Errorf("SSH targets need a private runtime directory: %v", err)
		return l
	}
	l.dir = dir
	return l
}

func (l *SSHLink) Name() string { return "ssh" }

// Host is the ssh destination.
func (l *SSHLink) Host() string { return l.o.Spec.Host }

func (l *SSHLink) Remote() bool { return l.defaultRoute().Remote() }
func (l *SSHLink) SSH() bool    { return l.defaultRoute().SSH() }

func (l *SSHLink) Argv(binary string, globals, args []string) []string {
	return l.defaultRoute().Argv(binary, globals, args)
}

func (l *SSHLink) defaultRoute() Transport {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mode == modeForward && l.up {
		return l.forwardLocked()
	}
	return l.execLocked()
}

func (l *SSHLink) forwardLocked() SSHForward {
	return SSHForward{Host: l.o.Spec.Host, Socket: l.socket}
}

func (l *SSHLink) execLocked() SSHExec {
	e := SSHExec{Host: l.o.Spec.Host, RexPath: l.o.Spec.RexPath, ControlDir: l.dir, NonPOSIX: true}
	if l.probe != nil {
		e.RexPath, e.NonPOSIX = l.probe.RexPath, !l.probe.POSIX
	}
	return e
}

func (l *SSHLink) nonPOSIXLocked() error {
	shell := "unknown"
	if l.probe != nil && l.probe.Shell != "" {
		shell = render.Label(l.probe.Shell)
	}
	return fmt.Errorf("exec mode needs a POSIX login shell (sh, bash, zsh...) on %s; its shell is %s", l.o.Spec.Host, shell)
}

// RouteFor implements Router.
func (l *SSHLink) RouteFor(cmd Cmd) (Transport, uint64, error) {
	if l.setupErr != nil {
		return nil, 0, l.setupErr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	host := l.o.Spec.Host
	switch {
	case l.probe == nil:
		return nil, l.routeGen, fmt.Errorf("ssh %s: not connected yet", host)
	case cmd.Kind == KindServerStart:
		// rex -S unix://<local socket> server start would act on this machine.
		return l.execLocked(), l.routeGen, nil
	case l.mode == modeForward && l.up:
		return l.forwardLocked(), l.routeGen, nil
	case l.mode == modeForward:
		return nil, l.routeGen, fmt.Errorf("ssh tunnel to %s is down", host)
	case l.mode == modeExec && !l.probe.POSIX:
		return nil, l.routeGen, l.nonPOSIXLocked()
	case l.mode == modeExec:
		return l.execLocked(), l.routeGen, nil
	}
	return nil, l.routeGen, fmt.Errorf("ssh %s: not connected (server stopped or unreachable)", host)
}

// RouteGen implements Router.
func (l *SSHLink) RouteGen() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.routeGen
}

// Describe says how the link reaches its server.
func (l *SSHLink) Describe() string {
	host := l.o.Spec.Host
	if l.setupErr != nil {
		return "ssh " + host + ", not available"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.mode == modeForward && l.up:
		return "ssh " + host + ", forward mode, tunnel up"
	case l.mode == modeForward:
		return "ssh " + host + ", forward mode, tunnel down"
	case l.mode == modeExec && l.fallback:
		return "ssh " + host + ", exec mode (endpoint cannot be forwarded)"
	case l.mode == modeExec:
		return "ssh " + host + ", exec mode"
	}
	return "ssh " + host + ", not connected"
}

// BeginStatus numbers a status read. Only the latest read started changes
// the link, and ApplyStatus reports, on the goroutine applying results,
// whether read g is still that one.
func (l *SSHLink) BeginStatus() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statusGen++
	return l.statusGen
}

// ApplyStatus: see BeginStatus.
func (l *SSHLink) ApplyStatus(g uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return g == l.statusGen
}

// Status starts a status read and runs it: see StatusAt.
func (l *SSHLink) Status(ctx context.Context, version *Outcome) TargetStatus {
	return l.StatusAt(ctx, l.BeginStatus(), version)
}

// StatusAt runs status read g: it probes the target once, reads its server
// over the tunnel when it is up, else over ssh exec, and settles the mode:
// forward (starting the tunnel) or exec. A tunnel still starting gives
// StateUnknown, as does a read that another one started since superseded,
// which changes nothing.
func (l *SSHLink) StatusAt(ctx context.Context, g uint64, version *Outcome) TargetStatus {
	if l.setupErr != nil {
		return TargetStatus{State: StateError, Detail: l.setupErr.Error()}
	}
	p, ok := probeWith(ctx, l.o.Base, l.o.Name, l.o.Spec, l.o.Env)
	if !ok {
		return TargetStatus{}
	}
	if p.Fail != nil {
		if !l.setProbe(g, nil) {
			return TargetStatus{}
		}
		return *p.Fail
	}
	if !l.setProbe(g, &p) {
		return TargetStatus{}
	}
	base := l.o.Base
	l.mu.Lock()
	fwd, fwdUp := l.forwardLocked(), l.mode == modeForward && l.up
	exec := l.execLocked()
	l.mu.Unlock()
	var fwdFailed *Outcome
	if fwdUp {
		who, err := base.With(fwd).Exec(ctx, Commands{}.Whoami(), false)
		switch {
		case err == nil && who.Kind == ExitOK:
			server, _ := base.With(fwd).Exec(ctx, Commands{}.ServerStatus(), false)
			return DeriveStatus(fwd, who, &server, version)
		case err == nil && who.Kind == ExitInterrupted:
			return TargetStatus{}
		case err != nil:
			who.Toast = err.Error()
		}
		fwdFailed = &who
	}
	who, err := base.With(exec).Exec(ctx, Commands{}.Whoami(), false)
	if st, failed := whoamiFailed(exec, who, err); failed {
		return st
	}
	w, perr := ParseWhoami(who.Stdout)
	if perr != nil {
		return TargetStatus{State: StateError, Detail: "whoami: " + perr.Error()}
	}
	var remote string
	var unforwardable error
	if l.o.Spec.forward() {
		remote, unforwardable = NormalizeEndpoint(w.Endpoint)
	}
	if !l.o.Spec.forward() || unforwardable != nil {
		if !l.useExec(g, unforwardable) {
			return TargetStatus{}
		}
		if !p.POSIX {
			l.mu.Lock()
			err := l.nonPOSIXLocked()
			l.mu.Unlock()
			return TargetStatus{State: StateError, Detail: err.Error()}
		}
		server, _ := base.With(exec).Exec(ctx, Commands{}.ServerStatus(), false)
		return DeriveStatus(exec, who, &server, nil)
	}
	if !l.useForward(g, remote) {
		return TargetStatus{}
	}
	if fwdFailed != nil {
		return TargetStatus{State: StateUnreachable, Detail: fwdFailed.Toast}
	}
	l.mu.Lock()
	exit := l.exit
	l.mu.Unlock()
	if exit != nil {
		st := TargetStatus{State: StateUnreachable, Detail: fmt.Sprintf("ssh tunnel: %s (retry in %s)", exit.line, exit.retry)}
		if exit.code == 255 {
			st.State = StateSSHUnreachable
		}
		return st
	}
	return TargetStatus{}
}

// whoamiFailed maps a whoami run over ssh exec that did not succeed.
func whoamiFailed(exec SSHExec, who Outcome, err error) (TargetStatus, bool) {
	switch {
	case err != nil:
		var denied *DeniedError
		if errors.As(err, &denied) {
			return TargetStatus{State: StateError, Detail: "refused: " + denied.Reason}, true
		}
		return TargetStatus{State: StateError, Detail: err.Error()}, true
	case who.Kind != ExitOK:
		return DeriveStatus(exec, who, nil, nil), true
	}
	return TargetStatus{}, false
}

// setProbe, useExec and useForward change the link for status read g only
// while it is the latest one, and report whether it was.
func (l *SSHLink) setProbe(g uint64, p *SSHProbe) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if g != l.statusGen {
		return false
	}
	if (l.probe == nil) != (p == nil) || (p != nil && *l.probe != *p) {
		l.probe = p
		l.routeGen++
	}
	return true
}

// useExec settles exec mode, configured or, with why set, as the fallback
// for an endpoint that cannot be forwarded; it stops the tunnel.
func (l *SSHLink) useExec(g uint64, why error) bool {
	l.mu.Lock()
	if g != l.statusGen {
		l.mu.Unlock()
		return false
	}
	fallback := why != nil
	if l.mode != modeExec || l.fallback != fallback {
		l.mode, l.fallback = modeExec, fallback
		l.routeGen++
	}
	l.stopLoopLocked()
	note := fallback && !l.fellBack
	l.fellBack = l.fellBack || fallback
	l.mu.Unlock()
	if note {
		l.o.Base.log(LogEntry{Note: fmt.Sprintf("%s: %v: cannot be forwarded: using exec mode", l.o.Spec.Host, why)})
	}
	return true
}

// useForward settles forward mode and keeps a tunnel to remote running.
func (l *SSHLink) useForward(g uint64, remote string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if g != l.statusGen {
		return false
	}
	if l.mode != modeForward {
		l.mode, l.fallback = modeForward, false
		l.routeGen++
	}
	if l.stopLoop == nil || l.remote != remote {
		l.startLoopLocked(remote)
	}
	return true
}

func (l *SSHLink) stopLoopLocked() {
	if l.stopLoop != nil {
		l.stopLoop()
		l.stopLoop = nil
		l.loop++
		l.up, l.exit, l.socket = false, nil, ""
		l.routeGen++
	}
}

// startLoopLocked replaces the tunnel loop with one forwarding a new local
// socket to remote. Only the newest loop publishes state.
func (l *SSHLink) startLoopLocked(remote string) {
	l.stopLoopLocked()
	l.loop++
	g := l.loop
	sock := l.o.Env.socketPath(l.dir)
	l.remote, l.socket, l.up, l.exit = remote, sock, false, nil
	l.routeGen++
	ctx, cancel := context.WithCancel(l.ctx)
	if l.o.Group.Go(func() { l.runLoop(ctx, g, sock, remote) }) {
		l.stopLoop = cancel
	} else {
		cancel()
	}
}

// publish applies f to the link's state if loop g is still the current one
// and the link still lives.
func (l *SSHLink) publish(g uint64, f func()) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if g != l.loop || l.ctx.Err() != nil {
		return false
	}
	f()
	l.routeGen++
	return true
}

func (l *SSHLink) notify(up bool) {
	if l.o.OnTunnel != nil {
		l.o.OnTunnel(up)
	}
}

// runLoop keeps the tunnel up until ctx ends, restarting it with backoff
// (Min doubling up to Max, reset once it was ready).
func (l *SSHLink) runLoop(ctx context.Context, g uint64, sock, remote string) {
	env := l.o.Env
	backoff := env.Min
	for ctx.Err() == nil {
		ready, res := l.once(ctx, g, sock, remote)
		if ready {
			backoff = env.Min
		}
		if ctx.Err() != nil {
			return
		}
		line := ToastLine(res.Stderr)
		if line == "" {
			line = fmt.Sprintf("exit %d", res.Code)
		}
		retry := backoff
		if l.publish(g, func() { l.up, l.exit = false, &tunnelExit{code: res.Code, line: line, retry: retry} }) {
			l.notify(false)
		}
		select {
		case <-ctx.Done():
			return
		case <-l.o.Clock.After(backoff):
		}
		backoff = min(backoff*2, env.Max)
	}
}

// once runs one tunnel process and reports whether its socket became ready.
func (l *SSHLink) once(ctx context.Context, g uint64, sock, remote string) (bool, Result) {
	sockets := l.o.Env.Sockets
	if err := sockets.Prepare(l.dir, sock); err != nil {
		return false, Result{Code: -1, Stderr: []byte(err.Error())}
	}
	ls, err := l.o.Base.streamArgv(ctx, tunnelArgv(l.o.Spec.Host, sock, remote))
	if err != nil {
		sockets.Cleanup(sock)
		return false, Result{Code: -1, Stderr: []byte(err.Error())}
	}
	exited := make(chan struct{})
	go func() {
		for range ls.Lines() {
		}
		close(exited)
	}()
	ready := sockets.WaitReady(ctx, sock, exited)
	if ready && l.publish(g, func() { l.up, l.exit = true, nil }) {
		l.notify(true)
	}
	<-exited
	res := ls.Wait()
	sockets.Cleanup(sock)
	return ready, res
}

// ProbeSSH reads the status of an SSH target that is not active: its probes,
// then whoami and server status over ssh exec. It never starts a tunnel.
func ProbeSSH(ctx context.Context, base *Client, name string, spec SSHSpec, env *SSHEnv) TargetStatus {
	if env == nil {
		return TargetStatus{State: StateError, Detail: errNoSSH.Error()}
	}
	dir, err := env.Dir()
	if err != nil {
		return TargetStatus{State: StateError, Detail: fmt.Sprintf("SSH targets need a private runtime directory: %v", err)}
	}
	p, ok := probeWith(ctx, base, name, spec, env)
	switch {
	case !ok:
		return TargetStatus{}
	case p.Fail != nil:
		return *p.Fail
	}
	exec := SSHExec{Host: spec.Host, RexPath: p.RexPath, ControlDir: dir, NonPOSIX: !p.POSIX}
	who, err := base.With(exec).Exec(ctx, Commands{}.Whoami(), false)
	if st, failed := whoamiFailed(exec, who, err); failed {
		return st
	}
	if spec.Mode == SSHModeExec && !p.POSIX {
		shell := "unknown"
		if p.Shell != "" {
			shell = render.Label(p.Shell)
		}
		return TargetStatus{State: StateError, Detail: fmt.Sprintf("exec mode needs a POSIX login shell (sh, bash, zsh...) on %s; its shell is %s", spec.Host, shell)}
	}
	server, _ := base.With(exec).Exec(ctx, Commands{}.ServerStatus(), false)
	return DeriveStatus(exec, who, &server, nil)
}

func probeWith(ctx context.Context, base *Client, name string, spec SSHSpec, env *SSHEnv) (SSHProbe, bool) {
	run := func() (SSHProbe, bool) { return runProbes(ctx, base, spec) }
	if env == nil || env.Probes == nil {
		return run()
	}
	return env.Probes.Do(ProbeKey(name, spec), run)
}

// runProbes asks the target for its login shell and, unless configured,
// where rex is. It reports false when interrupted.
func runProbes(ctx context.Context, base *Client, spec SSHSpec) (SSHProbe, bool) {
	timeout := base.Timeout + 8*time.Second
	unreachable := func(o Outcome) SSHProbe {
		return SSHProbe{Fail: &TargetStatus{State: StateSSHUnreachable, Detail: o.Toast}}
	}
	var p SSHProbe
	out := base.runArgv(ctx, shellProbeArgv(spec.Host), timeout)
	switch {
	case out.Kind == ExitInterrupted:
		return p, false
	case out.Kind == ExitSSH:
		return unreachable(out), true
	case out.Kind == ExitOK:
		line, _, _ := strings.Cut(strings.TrimSpace(string(out.Stdout)), "\n")
		p.Shell, p.POSIX = strings.TrimSpace(line), posixShell(line)
	}
	p.RexPath = spec.RexPath
	if p.RexPath != "" {
		return p, true
	}
	out = base.runArgv(ctx, pathProbeArgv(spec.Host), timeout)
	switch {
	case out.Kind == ExitInterrupted:
		return p, false
	case out.Kind == ExitSSH:
		return unreachable(out), true
	}
	if p.RexPath = parseRexPath(out.Stdout); p.RexPath == "" {
		return SSHProbe{Fail: &TargetStatus{State: StateError, Detail: fmt.Sprintf("rex not found on %s (set rex_path)", spec.Host)}}, true
	}
	return p, true
}
