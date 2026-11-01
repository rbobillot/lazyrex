package rex_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func TestMapExit(t *testing.T) {
	ssh := rex.SSHExec{Host: "remote-host"}
	cases := []struct {
		code int
		t    rex.Transport
		ctx  error
		want rex.ExitKind
	}{
		{0, rex.Local{}, nil, rex.ExitOK},
		{1, rex.Local{}, nil, rex.ExitFailed},
		{2, rex.Local{}, nil, rex.ExitUsage},
		{3, rex.Local{}, nil, rex.ExitNotFound},
		{4, rex.Local{}, nil, rex.ExitUnreachable},
		{130, rex.Local{}, nil, rex.ExitInterrupted},
		{255, rex.Local{}, nil, rex.ExitFailed},
		{255, ssh, nil, rex.ExitSSH},
		{4, ssh, nil, rex.ExitUnreachable},
		{-1, rex.Local{}, context.Canceled, rex.ExitInterrupted},
		{-1, rex.Local{}, context.DeadlineExceeded, rex.ExitTimeout},
		{-1, rex.Local{}, nil, rex.ExitStartError},
		{77, rex.Local{}, nil, rex.ExitFailed},
	}
	for _, c := range cases {
		if got := rex.MapExit(c.code, c.t, c.ctx); got != c.want {
			t.Errorf("MapExit(%d, %s, %v) = %v, want %v", c.code, c.t.Name(), c.ctx, got, c.want)
		}
	}
}

func TestExit4MultilineToast(t *testing.T) {
	fr := rextest.NewFakeRunner()
	stderr := rextest.FixtureString("stderr_whoami_stopped.txt")
	fr.On("whoami", "--json").Return("", stderr, 4)
	c := localClient(fr)
	var logs []rex.LogEntry
	c.Log = func(e rex.LogEntry) { logs = append(logs, e) }
	out, err := c.Exec(context.Background(), cmds.Whoami(), false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != rex.ExitUnreachable {
		t.Fatalf("kind = %v", out.Kind)
	}
	if strings.ContainsAny(out.Toast, "\n\r\t") || !strings.HasPrefix(out.Toast, "Error: no Rex server is running at") {
		t.Fatalf("toast = %q", out.Toast)
	}
	if len(logs) != 1 {
		t.Fatalf("log entries = %d", len(logs))
	}
	e := logs[0]
	if e.Code != 4 || strings.Count(e.Stderr, "\n") < 2 || !strings.Contains(e.Stderr, "\ttried unix://") {
		t.Fatalf("log entry = %+v", e)
	}
	if e.Display != "rex --autostart=false --timeout=10s whoami --json" {
		t.Fatalf("display = %q", e.Display)
	}
}

func TestToastLineCapsAndSanitizes(t *testing.T) {
	got := rex.ToastLine([]byte("\n\n  \x1b[31mboom\x1b[0m\r\nsecond"))
	if got != "boom" {
		t.Fatalf("toast = %q", got)
	}
	long := rex.ToastLine([]byte(strings.Repeat("é", 500)))
	if n := len([]rune(long)); n > 200 {
		t.Fatalf("toast has %d runes", n)
	}
}

func TestExecDeadline(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On("ls").Return("{}", "", 0)
	c := localClient(fr)
	c.Timeout = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := c.Exec(ctx, cmds.Ls(), false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != rex.ExitInterrupted || out.Toast != "" {
		t.Fatalf("cancelled call = %+v", out)
	}
}
