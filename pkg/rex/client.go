package rex

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rbobillot/lazyrex/pkg/render"
)

// LogEntry is one command-log line: the redacted, sanitized argv, how it
// ended, and its sanitized stderr. A Note is a line about no command.
type LogEntry struct {
	Note     string
	Display  string
	Duration time.Duration
	Code     int
	Stderr   string
	Refused  string
	Stream   bool
}

// Outcome is a finished call as the UI sees it.
type Outcome struct {
	Result
	Kind    ExitKind
	Toast   string
	Display string
}

// DeniedError reports a command the policy or the caller's guard refused;
// nothing ran.
type DeniedError struct{ Reason, Display string }

func (e *DeniedError) Error() string { return "refused: " + e.Reason }

// NeedsConfirmError reports a command that runs only once confirmed.
type NeedsConfirmError struct{ Display string }

func (e *NeedsConfirmError) Error() string { return "needs confirmation: " + e.Display }

// RouteError reports a stream with no route yet (a tunnel still starting);
// it is not logged, so retries stay quiet.
type RouteError struct{ Reason string }

func (e *RouteError) Error() string { return e.Reason }

// Client runs commands on one target through the safety policy, and logs them.
type Client struct {
	Runner    Runner
	Transport Transport
	Binary    string
	Timeout   time.Duration
	Policy    Policy
	Log       func(LogEntry)
}

// Prepared is a command checked once: the policy's verdict, the route, and
// the argv and display that go with it. RunPrepared runs exactly that argv.
type Prepared struct {
	Cmd      Cmd
	Decision Decision
	Display  string
	argv     []string
	route    Transport
	owner    Transport
	gen      uint64
}

// Touching returns p with ids added to the blocks its command affects; the
// argv does not change.
func (p *Prepared) Touching(ids ...BlockID) *Prepared {
	cp := *p
	cp.Cmd = p.Cmd.Touching(ids...)
	return &cp
}

func (c *Client) globals(cmd Cmd) []string {
	t := "--timeout=" + c.Timeout.String()
	if cmd.NoAutostart {
		return []string{t}
	}
	return []string{"--autostart=false", t}
}

// With returns a copy of c reaching another transport.
func (c *Client) With(t Transport) *Client {
	cp := *c
	cp.Transport = t
	return &cp
}

func (c *Client) route(cmd Cmd) (Transport, uint64, error) {
	if r, ok := c.Transport.(Router); ok {
		return r.RouteFor(cmd)
	}
	return c.Transport, 0, nil
}

// routeOrDefault is the route of cmd, else the transport's default form.
func (c *Client) routeOrDefault(cmd Cmd) Transport {
	if t, _, err := c.route(cmd); err == nil {
		return t
	}
	return c.Transport
}

// Argv returns the real argv of cmd.
func (c *Client) Argv(cmd Cmd) []string {
	return c.routeOrDefault(cmd).Argv(c.Binary, c.globals(cmd), cmd.Args)
}

// Display returns the argv of cmd as shown to the user: payloads redacted,
// controls removed.
func (c *Client) Display(cmd Cmd) string { return c.displayOn(c.routeOrDefault(cmd), cmd) }

func (c *Client) displayOn(t Transport, cmd Cmd) string {
	return displayArgv(t.Argv(c.Binary, c.globals(cmd), Redacted(cmd.Args, cmd.Redact)))
}

func displayArgv(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		if a = render.LogText(a); a != "" {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, " ")
}

// Decide returns the policy's verdict for cmd. A transport that fails its
// own Check denies every command.
func (c *Client) Decide(cmd Cmd) Decision {
	if cmd.Err != nil {
		return Decision{Verdict: Deny, Reason: cmd.Err.Error()}
	}
	if IsLocalOnly(cmd) {
		if _, local := c.Transport.(Local); !local || namesServer(cmd.Args) {
			return Decision{Verdict: Deny, Reason: "runs on this machine's server only, never through -S or ssh"}
		}
		if _, ok := c.Runner.(EnvRunner); !ok {
			return Decision{Verdict: Deny, Reason: "runs on this machine's server only, and this runner cannot leave REX_SERVER out"}
		}
	}
	if t, ok := c.Transport.(interface{ Check() error }); ok {
		if err := t.Check(); err != nil {
			return Decision{Verdict: Deny, Reason: err.Error()}
		}
	}
	return c.Policy.Check(cmd)
}

// Refuse logs that cmd was refused.
func (c *Client) Refuse(cmd Cmd, reason string) {
	display := ""
	if cmd.Err == nil {
		display = c.Display(cmd)
	}
	c.log(LogEntry{Display: display, Refused: render.Label(reason)})
}

func (c *Client) log(e LogEntry) {
	if c.Log != nil {
		c.Log(e)
	}
}

type argsChecker interface {
	CheckArgs(globals, args []string) error
}

// checkRoute applies a route's own checks to cmd.
func (c *Client) checkRoute(t Transport, cmd Cmd) error {
	if r, ok := t.(interface{ Check() error }); ok {
		if err := r.Check(); err != nil {
			return err
		}
	}
	if r, ok := t.(argsChecker); ok {
		if err := r.CheckArgs(c.globals(cmd), cmd.Args); err != nil {
			return err
		}
	}
	return nil
}

// Prepare checks cmd against the policy and its route, and fixes its argv
// and display. A refusal returns *DeniedError; Prepare logs nothing.
func (c *Client) Prepare(cmd Cmd) (*Prepared, error) {
	d := c.Decide(cmd)
	p := &Prepared{Cmd: cmd, Decision: d, owner: c.Transport}
	if d.Verdict == Deny {
		return p, &DeniedError{Reason: d.Reason}
	}
	route, gen, err := c.route(cmd)
	if err == nil {
		err = c.checkRoute(route, cmd)
	}
	if err != nil {
		return p, &DeniedError{Reason: err.Error()}
	}
	p.route, p.gen = route, gen
	p.argv = route.Argv(c.Binary, c.globals(cmd), cmd.Args)
	p.Display = c.displayOn(route, cmd)
	return p, nil
}

// Exec runs cmd. A denied command returns *DeniedError and a command needing
// confirmation returns *NeedsConfirmError unless confirmed; neither runs.
func (c *Client) Exec(ctx context.Context, cmd Cmd, confirmed bool) (Outcome, error) {
	return c.ExecGuarded(ctx, cmd, confirmed, nil)
}

// ExecGuarded is Exec with guard, called last before the runner: an error
// from it refuses cmd, which then does not run. Once ctx is done, cmd ends
// interrupted without starting a process.
func (c *Client) ExecGuarded(ctx context.Context, cmd Cmd, confirmed bool, guard func() error) (Outcome, error) {
	p, err := c.Prepare(cmd)
	var denied *DeniedError
	if errors.As(err, &denied) {
		c.Refuse(cmd, denied.Reason)
		return Outcome{}, err
	}
	return c.runPrepared(ctx, p, confirmed, guard, false)
}

// RunPrepared runs p, prepared by c, once its checks still hold: the same
// transport, the policy (which may only refuse more), the same route
// generation, the same argv, then guard. Every refusal is logged.
func (c *Client) RunPrepared(ctx context.Context, p *Prepared, confirmed bool, guard func() error) (Outcome, error) {
	return c.runPrepared(ctx, p, confirmed, guard, true)
}

func (c *Client) runPrepared(ctx context.Context, p *Prepared, confirmed bool, guard func() error, rebuild bool) (Outcome, error) {
	refuse := func(reason string) (Outcome, error) {
		c.log(LogEntry{Display: p.Display, Refused: render.Label(reason)})
		return Outcome{Display: p.Display}, &DeniedError{Reason: reason, Display: p.Display}
	}
	if !sameTransport(p.owner, c.Transport) || p.route == nil {
		return refuse("the target changed since this command was prepared")
	}
	d := c.Decide(p.Cmd)
	switch {
	case d.Verdict == Deny:
		return refuse(d.Reason)
	case (d.Verdict == Confirm || p.Decision.Verdict == Confirm) && !confirmed:
		return Outcome{Display: p.Display}, &NeedsConfirmError{Display: p.Display}
	}
	if r, ok := c.Transport.(Router); ok && r.RouteGen() != p.gen {
		return refuse(fmt.Sprintf("the route to %s changed since this command was prepared; run it again", routeHost(r)))
	}
	if rebuild && !slices.Equal(p.route.Argv(c.Binary, c.globals(p.Cmd), p.Cmd.Args), p.argv) {
		return refuse("the command changed since it was prepared")
	}
	timeout := c.Timeout + 2*time.Second + extraTimeout(p.route)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if guard != nil {
		if err := guard(); err != nil {
			return refuse(err.Error())
		}
	}
	if ctx.Err() != nil {
		return Outcome{Kind: ExitInterrupted, Display: p.Display}, nil
	}
	var drop []string
	if IsLocalOnly(p.Cmd) {
		drop = []string{"REX_SERVER"}
	}
	return c.run(cctx, p.argv, p.Display, p.route, drop), nil
}

// sameTransport compares transports without panicking on a transport type
// that is not comparable.
func sameTransport(a, b Transport) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if !va.IsValid() || !vb.IsValid() || va.Type() != vb.Type() {
		return va.IsValid() == vb.IsValid() && !va.IsValid()
	}
	if !va.Comparable() || !vb.Comparable() {
		return true
	}
	return va.Equal(vb)
}

func routeHost(r Router) string {
	if h, ok := r.(interface{ Host() string }); ok {
		return h.Host()
	}
	return r.Name()
}

func extraTimeout(t Transport) time.Duration {
	if e, ok := t.(interface{ ExtraTimeout() time.Duration }); ok {
		return e.ExtraTimeout()
	}
	return 0
}

// run runs argv under cctx, without the variables in drop, and logs it;
// route maps the exit code.
func (c *Client) run(cctx context.Context, argv []string, display string, route Transport, drop []string) Outcome {
	start := time.Now()
	var res Result
	var err error
	if drop != nil {
		er, ok := c.Runner.(EnvRunner)
		if !ok {
			return Outcome{Kind: ExitStartError, Result: Result{Code: -1}, Display: display, Toast: "this runner cannot leave REX_SERVER out"}
		}
		res, err = er.RunEnv(cctx, argv, drop)
	} else {
		res, err = c.Runner.Run(cctx, argv)
	}
	if res.Duration == 0 {
		res.Duration = time.Since(start)
	}
	out := Outcome{Result: res, Display: display}
	switch {
	case err != nil:
		out.Kind, out.Code = ExitStartError, -1
		out.Toast = render.Label(err.Error())
	case res.Code == 0:
		out.Kind = ExitOK
	default:
		out.Kind = MapExit(res.Code, route, cctx.Err())
		switch out.Kind {
		case ExitInterrupted:
		case ExitTimeout:
			out.Toast = fmt.Sprintf("timed out after %s", c.Timeout)
		default:
			out.Toast = ToastLine(res.Stderr)
			if out.Toast == "" {
				out.Toast = fmt.Sprintf("exit %d", res.Code)
			}
		}
	}
	c.log(LogEntry{Display: display, Duration: res.Duration, Code: out.Code, Stderr: render.LogText(string(res.Stderr))})
	return out
}

// runArgv runs an argv lazyrex builds itself, not a rex command (the ssh
// probes): it is logged and sanitized, and exit 255 is ssh's.
func (c *Client) runArgv(ctx context.Context, argv []string, timeout time.Duration) Outcome {
	if ctx.Err() != nil {
		return Outcome{Kind: ExitInterrupted, Display: displayArgv(argv)}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.run(cctx, argv, displayArgv(argv), SSHExec{}, nil)
}

// streamArgv starts a long-lived argv lazyrex builds itself (the ssh tunnel).
func (c *Client) streamArgv(ctx context.Context, argv []string) (LineStream, error) {
	return c.startStream(ctx, argv, displayArgv(argv), SSHExec{})
}

// Stream starts a long-lived command (the event stream) through the policy.
// It is logged when it ends.
func (c *Client) Stream(ctx context.Context, cmd Cmd) (LineStream, error) {
	d := c.Decide(cmd)
	if d.Verdict == Allow && IsLocalOnly(cmd) {
		d = Decision{Verdict: Deny, Reason: "a local-only command does not stream"}
	}
	if d.Verdict != Allow {
		c.Refuse(cmd, d.Reason)
		return nil, &DeniedError{Reason: d.Reason}
	}
	route, _, err := c.route(cmd)
	if err == nil {
		if err = c.checkRoute(route, cmd); err != nil {
			c.Refuse(cmd, err.Error())
			return nil, &DeniedError{Reason: err.Error()}
		}
	} else {
		return nil, &RouteError{Reason: err.Error()}
	}
	return c.startStream(ctx, route.Argv(c.Binary, c.globals(cmd), cmd.Args), c.displayOn(route, cmd), route)
}

func (c *Client) startStream(ctx context.Context, argv []string, display string, route Transport) (LineStream, error) {
	ls, err := c.Runner.Stream(ctx, argv)
	if err != nil {
		c.log(LogEntry{Display: display, Code: -1, Stderr: render.LogText(err.Error()), Stream: true})
		return nil, err
	}
	return &loggedStream{LineStream: ls, c: c, display: display, route: route, start: time.Now()}, nil
}

type loggedStream struct {
	LineStream
	c       *Client
	display string
	route   Transport
	start   time.Time
	once    sync.Once
	res     Result
}

// Route is the transport the stream runs on.
func (s *loggedStream) Route() Transport { return s.route }

func (s *loggedStream) Wait() Result {
	s.once.Do(func() {
		s.res = s.LineStream.Wait()
		s.c.log(LogEntry{Display: s.display, Duration: time.Since(s.start), Code: s.res.Code, Stderr: render.LogText(string(s.res.Stderr)), Stream: true})
	})
	return s.res
}
