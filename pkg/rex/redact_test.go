package rex_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func TestRedactionLocalAndSSH(t *testing.T) {
	transports := []rex.Transport{rex.Local{}, rex.SSHExec{Host: "remote-host", RexPath: "rex", ControlDir: "/tmp/lz"}}
	cases := []struct {
		cmd     rex.Cmd
		want    []string
		secrets []string
	}{
		{cmds.Send(S, B, "pw\n1"), []string{"<redacted 4 bytes>"}, []string{"pw"}},
		{cmds.APICall(S, "session.new_block", `{"a":1}`), []string{"session.new_block", "<redacted 7 bytes>"}, []string{`"a"`}},
		{cmds.APICall(S, "session.view", "@file"), []string{"<redacted 5 bytes>"}, []string{"@file"}},
		{cmds.APICall(S, "session.view", "-"), []string{"session.view", "<redacted 1 bytes>"}, nil},
		{cmds.Do("deploy", "k=secret", "other=x y"), []string{"deploy", "k=<redacted 6 bytes>", "other=<redacted 3 bytes>"}, []string{"secret", "x y"}},
		{cmds.DoJSON("deploy", `{"token":"hunter2"}`), []string{"deploy", "--args", "<redacted 19 bytes>"}, []string{"hunter2"}},
		{cmds.ClientDo(C, "session.select", "k=v1"), []string{"-C", "client:02app", "session.select", "k=<redacted 2 bytes>"}, []string{"v1"}},
	}
	for _, tr := range transports {
		fr := rextest.NewFakeRunner()
		fr.On().Return("", "", 0)
		c := &rex.Client{Runner: fr, Transport: tr, Binary: "rex", Timeout: 10 * time.Second}
		var logs []rex.LogEntry
		c.Log = func(e rex.LogEntry) { logs = append(logs, e) }
		for _, tc := range cases {
			display := c.Display(tc.cmd)
			for _, w := range tc.want {
				if !strings.Contains(display, w) {
					t.Errorf("%s: display %q lacks %q", tr.Name(), display, w)
				}
			}
			for _, s := range tc.secrets {
				if strings.Contains(display, s) {
					t.Errorf("%s: display %q leaks %q", tr.Name(), display, s)
				}
			}
			argv := c.Argv(tc.cmd)
			joined := strings.Join(argv, " ")
			for _, s := range tc.secrets {
				if !strings.Contains(joined, s) {
					t.Errorf("%s: real argv lost %q: %q", tr.Name(), s, argv)
				}
			}
			logs = nil
			if _, err := c.Exec(context.Background(), tc.cmd, true); err != nil {
				t.Fatalf("%s: exec: %v", tr.Name(), err)
			}
			if len(logs) != 1 || logs[0].Display != display {
				t.Errorf("%s: logged %+v, want display %q", tr.Name(), logs, display)
			}
		}
	}
}

func TestRedactionPhase2(t *testing.T) {
	transports := []rex.Transport{rex.Local{}, rex.SSHExec{Host: "remote-host", RexPath: "rex", ControlDir: "/tmp/lz"}}
	resize := `{"block_id":"block:01b1","delta":5,"direction":"right","session_id":"session:01aaaa"}`
	cases := []struct {
		cmd     rex.Cmd
		want    []string
		secrets []string
	}{
		{cmds.ClientDoJSON(C, "openFile", `{"token":"hunter2"}`), []string{"-C", "client:02app", "openFile", "--args", "<redacted 19 bytes>"}, []string{"hunter2"}},
		{cmds.APICall(S, "session.resize_pane", resize), []string{"session.resize_pane", fmt.Sprintf("<redacted %d bytes>", len(resize))}, []string{"delta"}},
		{cmds.APICall("", "target.complete", `{"prefix":"secret"}`), []string{"target.complete", "<redacted 19 bytes>"}, []string{"secret"}},
		{cmds.ClientDo(C, "session.select", "session_id="+string(S)), []string{"session.select", "session_id=<redacted 14 bytes>"}, []string{string(S)}},
		{cmds.DoJSON("split_right", `{"ratio":30}`), []string{"split_right", "--args", "<redacted 12 bytes>"}, []string{"ratio"}},
	}
	for _, tr := range transports {
		fr := rextest.NewFakeRunner()
		fr.On().Return("", "", 0)
		c := &rex.Client{Runner: fr, Transport: tr, Binary: "rex", Timeout: 10 * time.Second}
		var logs []rex.LogEntry
		c.Log = func(e rex.LogEntry) { logs = append(logs, e) }
		for _, tc := range cases {
			if tc.cmd.Err != nil {
				t.Fatalf("%q: %v", tc.cmd.Args, tc.cmd.Err)
			}
			display := c.Display(tc.cmd)
			for _, w := range tc.want {
				if !strings.Contains(display, w) {
					t.Errorf("%s: display %q lacks %q", tr.Name(), display, w)
				}
			}
			for _, s := range tc.secrets {
				if strings.Contains(display, s) {
					t.Errorf("%s: display %q leaks %q", tr.Name(), display, s)
				}
				if !strings.Contains(strings.Join(c.Argv(tc.cmd), " "), s) {
					t.Errorf("%s: real argv lost %q", tr.Name(), s)
				}
			}
			logs = nil
			if _, err := c.Exec(context.Background(), tc.cmd, true); err != nil {
				t.Fatalf("%s: exec: %v", tr.Name(), err)
			}
			if len(logs) != 1 || logs[0].Display != display {
				t.Errorf("%s: logged %+v, want display %q", tr.Name(), logs, display)
			}
		}
	}
}

func TestRedacted(t *testing.T) {
	args := []string{"do", "x", "k=v", "plain", "noeq"}
	got := rex.Redacted(args, []rex.Redaction{{Index: 2, ValueOnly: true}, {Index: 3}, {Index: 4, ValueOnly: true}})
	want := []string{"do", "x", "k=<redacted 1 bytes>", "<redacted 5 bytes>", "<redacted 4 bytes>"}
	if !slices.Equal(got, want) {
		t.Fatalf("Redacted = %q", got)
	}
	if args[2] != "k=v" {
		t.Fatalf("Redacted modified its input")
	}
}

func TestLogSanitizesArgvAndStderr(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On("session", "rename").Return("", "line one\x1b[31m red\x1b[0m\r\n\tline two\x1b]0;title\x07\x01 end\n", 1)
	c := localClient(fr)
	var logs []rex.LogEntry
	c.Log = func(e rex.LogEntry) { logs = append(logs, e) }
	out, err := c.Exec(context.Background(), cmds.SessionRename(S, "ok"), false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != rex.ExitFailed || out.Toast != "line one red" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(logs) != 1 || logs[0].Stderr != "line one red\n\tline two end\n" {
		t.Fatalf("stderr logged as %q", logs[0].Stderr)
	}
}

func TestDisplayDropsControlsFromArgs(t *testing.T) {
	c := localClient(rextest.NewFakeRunner())
	cmd := cmds.Raw("server", "status\x1b]0;x\x07\r")
	if got := c.Display(cmd); got != "rex --autostart=false --timeout=10s server status" {
		t.Fatalf("display = %q", got)
	}
}

func TestRefusedIsLogged(t *testing.T) {
	c, _ := policyClient(t, rex.Local{})
	var logs []rex.LogEntry
	c.Log = func(e rex.LogEntry) { logs = append(logs, e) }
	_, _ = c.Exec(context.Background(), cmds.APICall("", "hosts.put", `{"x":1}`), true)
	if len(logs) != 1 || logs[0].Refused == "" || strings.Contains(logs[0].Display, `"x"`) {
		t.Fatalf("refusal logged as %+v", logs)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"'; rm -rf ~'": `''\''; rm -rf ~'\'''`,
		"$(id)":        `'$(id)'`,
		"a`b`":         "'a`b`'",
		"a\nb":         "'a\nb'",
		"":             "''",
		"plain":        "'plain'",
	}
	for in, want := range cases {
		if got := rex.ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
