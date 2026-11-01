package rex

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rbobillot/lazyrex/pkg/render"
)

// SSHMode is how an SSH target reaches its server.
type SSHMode string

const (
	// SSHModeForward forwards the remote Rex socket and runs the local rex
	// against it. It is the default.
	SSHModeForward SSHMode = "forward"
	// SSHModeExec runs the remote rex through ssh for every call.
	SSHModeExec SSHMode = "exec"
)

// SSHSpec is the static configuration of an SSH target.
type SSHSpec struct {
	Host    string
	Mode    SSHMode
	RexPath string
}

func (s SSHSpec) forward() bool { return s.Mode != SSHModeExec }

// CheckSSHHost validates an ssh destination: it stays one argv element after
// "--", and a leading '-' or whitespace could still be read as an option or
// split by ssh_config tooling.
func CheckSSHHost(s string) error {
	if err := checkText(s, 255, false); err != nil {
		return err
	}
	if strings.IndexFunc(s, unicode.IsSpace) >= 0 {
		return errors.New("contains whitespace")
	}
	return nil
}

// CheckRexPath validates the path of rex on an SSH target.
func CheckRexPath(s string) error {
	switch {
	case s == "":
		return errors.New("empty")
	case !utf8.ValidString(s):
		return errors.New("not valid UTF-8")
	case len(s) > 1024:
		return errors.New("longer than 1024 bytes")
	case hasControl(s):
		return errors.New("contains control characters")
	case !strings.HasPrefix(s, "/"):
		return errors.New("must be an absolute path")
	}
	return nil
}

const (
	maxEndpointPath = 104
	maxEndpoint     = len("unix://") + 3*maxEndpointPath
)

// NormalizeEndpoint returns the percent-decoded socket path of a `unix://`
// endpoint, or an error when the endpoint cannot be forwarded. ssh expands
// '%' and '${' in -L, so a decoded path holding either cannot be forwarded.
func NormalizeEndpoint(endpoint string) (string, error) {
	if len(endpoint) > maxEndpoint {
		return "", fmt.Errorf("endpoint is longer than %d bytes", maxEndpoint)
	}
	raw, ok := strings.CutPrefix(endpoint, "unix://")
	switch {
	case !ok:
		return "", fmt.Errorf("endpoint %q is not a unix:// socket", render.Label(endpoint))
	case strings.ContainsAny(raw, "?#"):
		return "", fmt.Errorf("endpoint %q has a query or fragment", render.Label(endpoint))
	case !strings.HasPrefix(raw, "/"):
		return "", fmt.Errorf("endpoint path %q is not absolute", render.Label(raw))
	}
	p, err := url.PathUnescape(raw)
	if err != nil {
		return "", fmt.Errorf("endpoint path %q has a malformed percent escape", render.Label(raw))
	}
	switch {
	case !utf8.ValidString(p):
		return "", errors.New("endpoint path is not valid UTF-8")
	case p == "/":
		return "", errors.New("endpoint path is /")
	case len(p) > maxEndpointPath:
		return "", fmt.Errorf("endpoint path is longer than %d bytes", maxEndpointPath)
	case strings.ContainsRune(p, ':'):
		return "", fmt.Errorf("endpoint path %q contains ':'", render.Label(p))
	case strings.IndexFunc(p, unicode.IsSpace) >= 0:
		return "", fmt.Errorf("endpoint path %q contains whitespace", render.Label(p))
	case hasControl(p):
		return "", errors.New("endpoint path contains control characters")
	case strings.ContainsRune(p, '%'):
		return "", fmt.Errorf("endpoint path %q contains '%%'", render.Label(p))
	case strings.Contains(p, "${"):
		return "", fmt.Errorf("endpoint path %q contains '${'", render.Label(p))
	}
	return p, nil
}

var posixShells = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "ksh93": true,
	"mksh": true, "ash": true, "yash": true}

// posixShell reports whether the `echo "$SHELL"` output names a POSIX shell.
func posixShell(out string) bool {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	line = strings.TrimSpace(line)
	return line != "" && posixShells[path.Base(line)]
}

// rexPaths are the paths the probe lists when `command -v rex` fails, in
// order of preference.
var rexPaths = []string{"/usr/local/bin/rex", "/opt/homebrew/bin/rex", "/Applications/Rex.app/Contents/Resources/bin/rex",
	"/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"}

// parseRexPath returns the rex path of the path probe output: the most
// preferred of rexPaths it lists, whatever order ls sorts them in, else its
// first valid line.
func parseRexPath(out []byte) string {
	first, best := "", len(rexPaths)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if CheckRexPath(line) != nil {
			continue
		}
		if first == "" {
			first = line
		}
		if i := slices.Index(rexPaths, line); i >= 0 && i < best {
			best = i
		}
	}
	if best < len(rexPaths) {
		return rexPaths[best]
	}
	return first
}

var plainWordRE = regexp.MustCompile(`^[A-Za-z0-9._+@/=:,-]+$`)

// plainWord reports a word every login shell family reads literally, quoted
// or not.
func plainWord(s string) bool { return plainWordRE.MatchString(s) }

const shellProbe = `echo "$SHELL"`

// pathProbe single-quotes a path that is not a plain word, which POSIX
// shells and fish read as one word. QUOTING_STYLE=literal keeps GNU ls
// from quoting names with spaces.
var pathProbe = func() string {
	words := []string{"command -v rex || env QUOTING_STYLE=literal ls"}
	for _, p := range rexPaths {
		if !plainWord(p) {
			p = ShellQuote(p)
		}
		words = append(words, p)
	}
	return strings.Join(words, " ")
}()

func probeOpts() []string { return []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5"} }

func execOpts(dir string) []string {
	opts := probeOpts()
	if dir != "" {
		opts = append(opts, "-o", "ControlMaster=auto", "-o", "ControlPath="+dir+"/ssh-%C", "-o", "ControlPersist=60s")
	}
	return opts
}

func sshArgv(opts []string, host string, remote ...string) []string {
	argv := append([]string{"ssh"}, opts...)
	argv = append(argv, "--", host)
	return append(argv, remote...)
}

func shellProbeArgv(host string) []string { return sshArgv(probeOpts(), host, shellProbe) }
func pathProbeArgv(host string) []string  { return sshArgv(probeOpts(), host, pathProbe) }

func tunnelArgv(host, local, remote string) []string {
	return []string{"ssh", "-N", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-L", local + ":" + remote, "--", host}
}

// SSHProbe is what lazyrex learns of an SSH target once: its login shell
// and where rex is. Fail holds the status of a probe that failed.
type SSHProbe struct {
	Shell   string
	POSIX   bool
	RexPath string
	Fail    *TargetStatus
}

// SSHProbes caches probes per target identity, failures included, and runs
// concurrent probes of one target once.
type SSHProbes struct {
	mu       sync.Mutex
	done     map[string]SSHProbe
	inflight map[string]*probeCall
}

type probeCall struct {
	wait chan struct{}
	res  SSHProbe
	ok   bool
}

// ProbeKey identifies the probes of a target.
func ProbeKey(name string, spec SSHSpec) string {
	return strings.ToLower(name) + "|" + spec.Host + "|" + spec.RexPath
}

// Do returns the cached probe of key, or runs f once for every concurrent
// caller. A result f reports as not ok (interrupted) is not cached.
func (p *SSHProbes) Do(key string, f func() (SSHProbe, bool)) (SSHProbe, bool) {
	p.mu.Lock()
	if r, ok := p.done[key]; ok {
		p.mu.Unlock()
		return r, true
	}
	if call, ok := p.inflight[key]; ok {
		p.mu.Unlock()
		<-call.wait
		return call.res, call.ok
	}
	call := &probeCall{wait: make(chan struct{})}
	if p.inflight == nil {
		p.inflight = map[string]*probeCall{}
	}
	p.inflight[key] = call
	p.mu.Unlock()

	call.res, call.ok = f()
	p.mu.Lock()
	if p.inflight[key] == call {
		delete(p.inflight, key)
		if call.ok {
			if p.done == nil {
				p.done = map[string]SSHProbe{}
			}
			p.done[key] = call.res
		}
	}
	p.mu.Unlock()
	close(call.wait)
	return call.res, call.ok
}

// Forget drops the probe of key, so the next Do probes again.
func (p *SSHProbes) Forget(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.done, key)
	delete(p.inflight, key)
}

// TunnelSockets creates and removes the local sockets of tunnels.
type TunnelSockets interface {
	// Prepare revalidates dir and removes a stale socket at path; it refuses
	// any other file there.
	Prepare(dir, path string) error
	// WaitReady reports whether the socket at path appeared before exited
	// closed, ctx ended or the wait gave up.
	WaitReady(ctx context.Context, path string, exited <-chan struct{}) bool
	Cleanup(path string)
}

// SSHEnv is what SSH targets need from the machine lazyrex runs on.
type SSHEnv struct {
	// Dir returns the private runtime directory of sockets and ssh control
	// paths.
	Dir      func() (string, error)
	Sockets  TunnelSockets
	PID      int
	Probes   *SSHProbes
	Min, Max time.Duration
	// seq numbers the tunnel sockets of every link, so an old link's
	// cleanup never removes a newer link's socket.
	seq atomic.Uint64
}

func (e *SSHEnv) socketPath(dir string) string {
	return fmt.Sprintf("%s/fwd-%d-%d.sock", dir, e.PID, e.seq.Add(1))
}

// TunnelGroup tracks the tunnel goroutines, so quitting can wait for them.
// Registration closes before the wait, as sync.WaitGroup requires.
type TunnelGroup struct {
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
}

// Go runs f on a goroutine unless the group was stopped.
func (g *TunnelGroup) Go(f func()) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped {
		return false
	}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		f()
	}()
	return true
}

// Stop refuses every later Go.
func (g *TunnelGroup) Stop() {
	g.mu.Lock()
	g.stopped = true
	g.mu.Unlock()
}

// Wait waits up to d for the running goroutines and reports whether they
// ended. Call it after Stop.
func (g *TunnelGroup) Wait(d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
