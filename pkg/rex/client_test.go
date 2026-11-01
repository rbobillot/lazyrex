package rex_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// argvHook is rex.Local calling hook on every Argv, Display's included.
type argvHook struct {
	rex.Local
	hook func(argv []string)
}

func (a argvHook) Argv(binary string, globals, args []string) []string {
	argv := a.Local.Argv(binary, globals, args)
	if a.hook != nil {
		a.hook(argv)
	}
	return argv
}

func guardClient(tr rex.Transport) (*rex.Client, *rextest.FakeRunner, *[]rex.LogEntry) {
	fr := rextest.NewFakeRunner()
	fr.On().Return("{}", "", 0)
	policy, _ := rex.NewPolicy("", nil)
	var log []rex.LogEntry
	c := &rex.Client{Runner: fr, Transport: tr, Binary: "rex", Timeout: 10 * time.Second, Policy: policy,
		Log: func(e rex.LogEntry) { log = append(log, e) }}
	return c, fr, &log
}

// TestExecGuardedRefuses (R1): a guard error refuses the command, which
// does not run, and is logged as refused with its display.
func TestExecGuardedRefuses(t *testing.T) {
	c, fr, log := guardClient(rex.Local{})
	cmd := cmds.Do("mystery")
	out, err := c.ExecGuarded(context.Background(), cmd, true, func() error { return errors.New("x") })
	var denied *rex.DeniedError
	if !errors.As(err, &denied) || denied.Reason != "x" {
		t.Fatalf("err = %v, want DeniedError x", err)
	}
	if want := c.Display(cmd); denied.Display != want || out.Display != want || want == "" {
		t.Fatalf("display = %q / %q, want %q", denied.Display, out.Display, want)
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("runner called %d times: %q", n, fr.Calls())
	}
	if len(*log) != 1 || (*log)[0].Refused != "x" || (*log)[0].Display != c.Display(cmd) {
		t.Fatalf("log = %+v", *log)
	}
}

// TestExecGuardedAfterRunnerArgv (R2): the guard runs after the argv the
// runner gets is built, not only after Display's: the guard fails once the
// second Argv call happened, so a guard run between the two lets it run.
func TestExecGuardedAfterRunnerArgv(t *testing.T) {
	var events []string
	valid := true
	tr := argvHook{hook: func([]string) {
		events = append(events, "argv")
		if len(events) == 2 {
			valid = false
		}
	}}
	c, fr, _ := guardClient(tr)
	fr.OnCall = func([]string) { events = append(events, "run") }
	guard := func() error {
		events = append(events, "guard")
		if !valid {
			return errors.New("changed")
		}
		return nil
	}
	_, err := c.ExecGuarded(context.Background(), cmds.Do("mystery"), true, guard)
	var denied *rex.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want DeniedError", err)
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("runner called %d times", n)
	}
	if want := []string{"argv", "argv", "guard"}; !slices.Equal(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
}

// TestExecGuardedPasses (R3): a nil guard result runs the command once,
// the guard right before the runner and after both Argv calls.
func TestExecGuardedPasses(t *testing.T) {
	var events []string
	argvs := 0
	tr := argvHook{hook: func([]string) { argvs++ }}
	c, fr, _ := guardClient(tr)
	fr.OnCall = func([]string) { events = append(events, "run") }
	seen := -1
	guard := func() error {
		seen = argvs
		events = append(events, "guard")
		return nil
	}
	out, err := c.ExecGuarded(context.Background(), cmds.Do("mystery"), true, guard)
	if err != nil || out.Kind != rex.ExitOK {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	if n := len(fr.Calls()); n != 1 {
		t.Fatalf("runner called %d times", n)
	}
	if seen != 2 {
		t.Fatalf("guard saw %d Argv calls, want 2", seen)
	}
	if want := []string{"guard", "run"}; !slices.Equal(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
}

// TestExecGuardedAfterPolicy (R4, R5): a denied command and an unconfirmed
// one never reach the guard; an allowed one calls it once.
func TestExecGuardedAfterPolicy(t *testing.T) {
	cases := []struct {
		name      string
		cmd       rex.Cmd
		confirmed bool
		guards    int
		runs      int
		check     func(error) bool
	}{
		{"denied", cmds.APICall("", "hosts.put", "{}"), true, 0, 0, func(err error) bool {
			var d *rex.DeniedError
			return errors.As(err, &d)
		}},
		{"needs confirmation", cmds.Do("mystery"), false, 0, 0, func(err error) bool {
			var n *rex.NeedsConfirmError
			return errors.As(err, &n)
		}},
		{"allowed", cmds.Whoami(), false, 1, 1, func(err error) bool { return err == nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fr, _ := guardClient(rex.Local{})
			guards := 0
			_, err := c.ExecGuarded(context.Background(), tc.cmd, tc.confirmed, func() error { guards++; return nil })
			if !tc.check(err) {
				t.Fatalf("err = %v", err)
			}
			if guards != tc.guards || len(fr.Calls()) != tc.runs {
				t.Fatalf("guard called %d times, runner %d times; want %d, %d", guards, len(fr.Calls()), tc.guards, tc.runs)
			}
		})
	}
}

// TestExecUnguarded (R6): Exec runs as before.
func TestExecUnguarded(t *testing.T) {
	c, fr, _ := guardClient(rex.Local{})
	out, err := c.Exec(context.Background(), cmds.Whoami(), false)
	if err != nil || out.Kind != rex.ExitOK || len(fr.Calls()) != 1 {
		t.Fatalf("out = %+v, err = %v, calls %q", out, err, fr.Calls())
	}
}
