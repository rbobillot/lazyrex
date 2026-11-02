package rex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// endpoint, or an error when the endpoint cannot be forwarded.
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
	// ssh's -L expands '%' and '${', drops '\' as an escape and splits on
	// ':'; a space survives. Other spaces would read as one on screen.
	case strings.ContainsRune(p, ':'):
		return "", fmt.Errorf("endpoint path %q contains ':'", render.Label(p))
	case strings.ContainsRune(p, '\\'):
		return "", fmt.Errorf("endpoint path %q contains '\\'", render.Label(p))
	case strings.IndexFunc(p, func(r rune) bool { return r != ' ' && unicode.IsSpace(r) }) >= 0:
		return "", fmt.Errorf("endpoint path %q contains whitespace other than a space", render.Label(p))
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
		"-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-L", local + ":" + remote, "--", host}
}

const (
	stablePrefix = "gui-"
	maxSlug      = 14
	stableHex    = 16
)

var (
	// ErrSocketHeld is the claim of a stable socket that another lazyrex
	// holds.
	ErrSocketHeld = errors.New("held by another lazyrex")
	// ErrLiveSocket refuses to remove a socket that accepts connections.
	ErrLiveSocket = errors.New("a live socket is at the path")
)

// stableSocketName names the socket the Rex app can reach a target through:
// the same across runs for one target identity, distinct across identities.
// Its v1 form is a contract with the hosts a human added, so never change it.
func stableSocketName(name string, spec SSHSpec) string {
	sum := sha256.Sum256([]byte("v1\x00" + foldKey(name) + "\x00" + spec.Host + "\x00" + spec.RexPath))
	return stablePrefix + slug(name) + "_" + hex.EncodeToString(sum[:stableHex/2]) + ".sock"
}

// foldKey maps s to one string per strings.EqualFold class, which is how
// target names are unique.
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		m := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			m = min(m, f)
		}
		b.WriteRune(m)
	}
	return b.String()
}

// slug is the readable part of a stable socket name, possibly empty.
func slug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
		case !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	return strings.TrimRight(s[:min(len(s), maxSlug)], "-")
}

// SocketURI is the endpoint of a local socket, as rex -S and the Rex app take
// it.
func SocketURI(path string) string { return (&url.URL{Scheme: "unix", Path: path}).String() }

// HostsAddHint is the command a human runs for the Rex app; lazyrex shows it
// and never runs it.
func HostsAddHint(label, uri string) string {
	words := []string{"rex", "hosts", "add", label, uri}
	for i, w := range words {
		if !plainWord(w) {
			words[i] = ShellQuote(w)
		}
	}
	return strings.Join(words, " ")
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

// TunnelSockets creates and removes the local sockets of tunnels. None of
// them ever removes a socket that accepts connections.
type TunnelSockets interface {
	// Claim takes the stable socket at path in dir for this process until
	// release or exit; ErrSocketHeld when another process holds it.
	Claim(dir, path string) (release func(), err error)
	// Prepare revalidates dir and removes a dead socket at path; it refuses
	// any other file there, and a live socket with ErrLiveSocket.
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
	// seq numbers the private tunnel sockets of every link, so an old
	// link's cleanup never removes a newer link's socket.
	seq atomic.Uint64
	// claims holds, per stable socket, the loop using it in this process
	// until that loop has returned, after its last Cleanup.
	claimMu sync.Mutex
	claims  map[string]chan struct{}
}

func (e *SSHEnv) socketPath(dir string) string {
	return fmt.Sprintf("%s/fwd-%d-%d.sock", dir, e.PID, e.seq.Add(1))
}

// claim waits until no other loop of this process holds path, then holds it
// until release; it reports false when ctx ends first.
func (e *SSHEnv) claim(ctx context.Context, path string) (release func(), ok bool) {
	for {
		e.claimMu.Lock()
		prev, held := e.claims[path]
		if !held {
			done := make(chan struct{})
			if e.claims == nil {
				e.claims = map[string]chan struct{}{}
			}
			e.claims[path] = done
			e.claimMu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					e.claimMu.Lock()
					delete(e.claims, path)
					e.claimMu.Unlock()
					close(done)
				})
			}, true
		}
		e.claimMu.Unlock()
		select {
		case <-prev:
		case <-ctx.Done():
			return nil, false
		}
	}
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
