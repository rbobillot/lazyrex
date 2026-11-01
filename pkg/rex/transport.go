package rex

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rbobillot/lazyrex/pkg/render"
)

// Transport turns a rex invocation into the argv that reaches a server.
type Transport interface {
	Name() string
	// Remote reports whether a stopped server is another machine's.
	Remote() bool
	SSH() bool
	Argv(binary string, globals, args []string) []string
}

// Local runs the local rex binary against the local server.
type Local struct{}

func (Local) Name() string { return "local" }
func (Local) Remote() bool { return false }
func (Local) SSH() bool    { return false }

func (Local) Argv(binary string, globals, args []string) []string {
	argv := make([]string, 0, 1+len(globals)+len(args))
	argv = append(argv, binary)
	argv = append(argv, globals...)
	return append(argv, args...)
}

// Native reaches a Rex server through `rex -S <label|address>`.
type Native struct{ Server string }

func (Native) Name() string { return "native" }
func (Native) Remote() bool { return true }
func (Native) SSH() bool    { return false }

// Check refuses a server that rex could read as a flag.
func (n Native) Check() error {
	if err := CheckServer(n.Server); err != nil {
		return fmt.Errorf("server %q: %v", n.Server, err)
	}
	return nil
}

func (n Native) Argv(binary string, globals, args []string) []string {
	g := append(append([]string{}, globals...), "-S", n.Server)
	return Local{}.Argv(binary, g, args)
}

// Router is a transport whose route depends on each command and on the
// state of a connection. The route generation changes with that state.
type Router interface {
	Transport
	RouteFor(Cmd) (Transport, uint64, error)
	RouteGen() uint64
}

// SSHExec runs rex on a remote machine through ssh. ssh joins its arguments
// into one string for the remote login shell, so every remote argument is
// POSIX-single-quoted. NonPOSIX marks a login shell that is not known to be
// POSIX: the words then go unquoted, and CheckArgs accepts plain words only.
type SSHExec struct {
	Host, RexPath, ControlDir string
	NonPOSIX                  bool
}

func (SSHExec) Name() string { return "ssh" }

// Remote is false: rex runs on the server's own machine, where exit 4 means
// that server is stopped.
func (SSHExec) Remote() bool { return false }
func (SSHExec) SSH() bool    { return true }

// ExtraTimeout is the time ssh may take to connect before rex's own timeout
// starts.
func (SSHExec) ExtraTimeout() time.Duration { return 6 * time.Second }

func (s SSHExec) rexPath() string {
	if s.RexPath == "" {
		return "rex"
	}
	return s.RexPath
}

// Check refuses a destination ssh could read as an option, and paths ssh
// options or the remote shell cannot hold.
func (s SSHExec) Check() error {
	if err := CheckSSHHost(s.Host); err != nil {
		return fmt.Errorf("ssh host %q: %v", s.Host, err)
	}
	if s.RexPath != "" && (!utf8.ValidString(s.RexPath) || hasControl(s.RexPath)) {
		return fmt.Errorf("rex path %q: contains control characters or invalid UTF-8", s.RexPath)
	}
	if d := s.ControlDir; d != "" && (!strings.HasPrefix(d, "/") || hasControl(d) || strings.IndexFunc(d, unicode.IsSpace) >= 0) {
		return fmt.Errorf("control directory %q: not an absolute path without whitespace", d)
	}
	return nil
}

// CheckArgs refuses, for a shell not known to be POSIX, any remote word
// that is not a plain word.
func (s SSHExec) CheckArgs(globals, args []string) error {
	if !s.NonPOSIX {
		return nil
	}
	for _, w := range append(append([]string{s.rexPath()}, globals...), args...) {
		if !plainWord(w) {
			return fmt.Errorf("the login shell on %s is not POSIX: lazyrex sends it plain words only, and %q is not one", s.Host, render.Label(w))
		}
	}
	return nil
}

func (s SSHExec) Argv(_ string, globals, args []string) []string {
	quote := ShellQuote
	if s.NonPOSIX {
		quote = func(w string) string { return w }
	}
	argv := sshArgv(execOpts(s.ControlDir), s.Host, quote(s.rexPath()))
	for _, a := range globals {
		argv = append(argv, quote(a))
	}
	for _, a := range args {
		argv = append(argv, quote(a))
	}
	return argv
}

// SSHForward runs the local rex against a remote server's socket forwarded
// by an ssh tunnel to Socket.
type SSHForward struct{ Host, Socket string }

func (SSHForward) Name() string { return "ssh" }
func (SSHForward) Remote() bool { return true }
func (SSHForward) SSH() bool    { return false }

func (f SSHForward) Argv(binary string, globals, args []string) []string {
	return Native{Server: "unix://" + f.Socket}.Argv(binary, globals, args)
}

// ShellQuote returns s as one POSIX shell word.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
