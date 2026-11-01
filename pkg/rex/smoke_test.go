//go:build smoke

package rex_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

// TestSmokeReadOnly runs read-only commands against the real rex binary. It
// never creates sessions, sends input, starts servers or uses SSH.
func TestSmokeReadOnly(t *testing.T) {
	if os.Getenv("LAZYREX_SMOKE") != "1" {
		t.Skip("set LAZYREX_SMOKE=1 to run")
	}
	bin := "/usr/local/bin/rex"
	if _, err := os.Stat(bin); err != nil {
		p, err := exec.LookPath("rex")
		if err != nil {
			t.Skip("rex not installed")
		}
		bin = p
	}
	c := &rex.Client{Runner: rex.ExecRunner{}, Transport: rex.Local{}, Binary: bin, Timeout: 5 * time.Second}
	checks := []struct {
		name  string
		cmd   rex.Cmd
		parse func([]byte) error
	}{
		{"version", cmds.Version(), func(b []byte) error { _, err := rex.ParseVersion(b); return err }},
		{"whoami", cmds.Whoami(), func(b []byte) error { _, err := rex.ParseWhoami(b); return err }},
		{"ls", cmds.Ls(), func(b []byte) error { _, err := rex.ParseLs(b); return err }},
		{"server status", cmds.ServerStatus(), func(b []byte) error { _, err := rex.ParseServerStatus(b); return err }},
		{"hosts list", cmds.HostsList(), func(b []byte) error { _, err := rex.ParseHosts(b); return err }},
	}
	var ls []byte
	for _, ch := range checks {
		argv := c.Argv(ch.cmd)
		if !slices.Contains(argv, "--autostart=false") {
			t.Fatalf("%s: argv without --autostart=false: %q", ch.name, argv)
		}
		if slices.Contains(argv, "-S") || slices.Contains(argv, "check") {
			t.Fatalf("%s: the smoke test never uses -S or hosts check: %q", ch.name, argv)
		}
		out, err := c.Exec(context.Background(), ch.cmd, false)
		if err != nil {
			t.Fatalf("%s: %v", ch.name, err)
		}
		switch {
		case out.Code == 0:
			if err := ch.parse(out.Stdout); err != nil {
				t.Errorf("%s: output does not parse: %v", ch.name, err)
			}
			if ch.name == "ls" {
				ls = out.Stdout
			}
		case out.Code == 4:
			t.Logf("%s: server unreachable (exit 4): %s", ch.name, out.Toast)
		case out.Code == 1 && ch.name == "server status" && rex.IsNotRunning(out):
			t.Logf("%s: server not running", ch.name)
		default:
			t.Errorf("%s: exit %d: %s", ch.name, out.Code, out.Toast)
		}
	}
	if ls != nil {
		smokeContainment(t, c, ls)
	}
}

// smokeContainment reads every existing session with the strict containment
// readers, so that real Rex output they refuse shows up here rather than as
// every kill and window close refused.
func smokeContainment(t *testing.T, c *rex.Client, ls []byte) {
	var v struct {
		Sessions *[]rex.SessionEntry `json:"sessions"`
	}
	if err := json.Unmarshal(ls, &v); err != nil {
		t.Errorf("ls: %v", err)
		return
	}
	if v.Sessions == nil {
		t.Errorf("ls: no sessions array")
		return
	}
	if len(*v.Sessions) == 0 {
		t.Log("ls: no sessions, containment readers not checked")
		return
	}
	for _, s := range *v.Sessions {
		reads := []struct {
			name string
			cmd  rex.Cmd
			read func([]byte) error
		}{
			{"session inspect", cmds.SessionInspect(s.SessionID), func(b []byte) error {
				_, err := rex.ContainedBlocks(b, s.SessionID, "")
				return err
			}},
			{"block ls", cmds.BlockLs(s.SessionID), func(b []byte) error { _, err := rex.ListedBlocks(b); return err }},
		}
		for _, r := range reads {
			name := r.name + " " + string(s.SessionID)
			argv := c.Argv(r.cmd)
			if !slices.Contains(argv, "--autostart=false") {
				t.Fatalf("%s: argv without --autostart=false: %q", name, argv)
			}
			out, err := c.Exec(context.Background(), r.cmd, false)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if out.Code != 0 {
				t.Logf("%s: exit %d: %s", name, out.Code, out.Toast)
				continue
			}
			if err := r.read(out.Stdout); err != nil {
				t.Errorf("%s: strict reader refuses the output: %v", name, err)
			}
		}
	}
}
