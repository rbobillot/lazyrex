package rex_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

const (
	S = rex.SessionID("session:01aaaa")
	W = rex.WindowID("window:01w1")
	B = rex.BlockID("block:01b1")
	C = rex.ClientID("client:02app")
)

var cmds rex.Commands

func localClient(fr *rextest.FakeRunner) *rex.Client {
	return &rex.Client{Runner: fr, Transport: rex.Local{}, Binary: "rex", Timeout: 10 * time.Second}
}

func split(s string) []string { return strings.Fields(s) }

// samples has one entry per Commands method; TestArgvInvariants fails when a
// builder is added without one.
func samples() map[string]rex.Cmd {
	return map[string]rex.Cmd{
		"Version":        cmds.Version(),
		"Whoami":         cmds.Whoami(),
		"ServerStatus":   cmds.ServerStatus(),
		"ServerStart":    cmds.ServerStart(),
		"ServerStop":     cmds.ServerStop(),
		"Ls":             cmds.Ls(),
		"New":            cmds.New("api", "/tmp/x"),
		"SessionInspect": cmds.SessionInspect(S),
		"SessionRename":  cmds.SessionRename(S, "renamed"),
		"Kill":           cmds.Kill(S),
		"BlockLs":        cmds.BlockLs(S),
		"WindowNew":      cmds.WindowNew(S, "w1"),
		"WindowRename":   cmds.WindowRename(S, W, "n"),
		"WindowClose":    cmds.WindowClose(S, W),
		"WindowFocus":    cmds.WindowFocus(S, W),
		"BlockInspect":   cmds.BlockInspect(S, B),
		"BlockCall":      cmds.BlockCall(S, B, "process"),
		"Capture":        cmds.Capture(S, B, "vt"),
		"Send":           cmds.Send(S, B, "hi"),
		"SendKey":        cmds.SendKey(S, B, "enter"),
		"Split":          cmds.Split(S, B, rex.SplitOpts{Dir: "right", Ratio: 30, Label: "x", Command: []string{"htop"}}),
		"RunPane":        cmds.RunPane(S, B, rex.RunOpts{Dir: "right", Command: []string{"make", "test"}}),
		"BlockRename":    cmds.BlockRename(S, B, "n"),
		"BlockClose":     cmds.BlockClose(S, B),
		"BlockFocus":     cmds.BlockFocus(S, B),
		"Zoom":           cmds.Zoom(S, B, true),
		"ClientLs":       cmds.ClientLs(),
		"ClientInspect":  cmds.ClientInspect(C),
		"Events":         cmds.Events(),
		"HostsList":      cmds.HostsList(),
		"HostsCheck":     cmds.HostsCheck("studio"),
		"APICall":        cmds.APICall(S, "session.view", "{}"),
		"Do":             cmds.Do("x", "k=v"),
		"DoJSON":         cmds.DoJSON("x", `{"a":1}`),
		"ClientDo":       cmds.ClientDo(C, "closeWindow", "id=1"),
		"ClientDoJSON":   cmds.ClientDoJSON(C, "openFile", `{"path":"/x"}`),
		"Keymap":         cmds.Keymap(),
		"Actions":        cmds.Actions(),
		"ClientActions":  cmds.ClientActions(C),
		"APIDescribe":    cmds.APIDescribe("session.resize_pane"),
		"APIList":        cmds.APIList(),
		"Raw":            cmds.Raw("server", "status", "--json"),
		"ConfigCheck":    cmds.ConfigCheck("/tmp/x.lua"),
		"ConfigReload":   cmds.ConfigReload(),
	}
}

func TestArgvLocalGolden(t *testing.T) {
	c := localClient(rextest.NewFakeRunner())
	G := "rex --autostart=false --timeout=10s "
	golden := []struct {
		cmd  rex.Cmd
		want string
	}{
		{cmds.Version(), G + "version --json"},
		{cmds.Whoami(), G + "whoami --json"},
		{cmds.ServerStatus(), G + "server status --json"},
		{cmds.ServerStart(), "rex --timeout=10s server start --json"},
		{cmds.ServerStop(), G + "server stop"},
		{cmds.Ls(), G + "ls --json"},
		{cmds.New("api", ""), G + "new api --json"},
		{cmds.New("", ""), G + "new --json"},
		{cmds.New("api", "/tmp/x"), G + "new api --cwd=/tmp/x --json"},
		{cmds.SessionInspect(S), G + "session inspect session:01aaaa --json"},
		{cmds.SessionRename(S, "n"), G + "session rename session:01aaaa n"},
		{cmds.Kill(S), G + "kill session:01aaaa"},
		{cmds.BlockLs(S), G + "block ls -s session:01aaaa --json"},
		{cmds.WindowNew(S, "w1"), G + "window new -s session:01aaaa w1 --focus=false --json"},
		{cmds.WindowNew(S, ""), G + "window new -s session:01aaaa --focus=false --json"},
		{cmds.WindowRename(S, W, "n"), G + "window rename -s session:01aaaa window:01w1 n"},
		{cmds.WindowClose(S, W), G + "window close -s session:01aaaa window:01w1"},
		{cmds.WindowFocus(S, W), G + "window focus -s session:01aaaa window:01w1"},
		{cmds.BlockInspect(S, B), G + "block inspect -s session:01aaaa block:01b1 --json"},
		{cmds.BlockCall(S, B, "process"), G + "block call -s session:01aaaa -b block:01b1 process"},
		{cmds.BlockCall(S, B, "modes"), G + "block call -s session:01aaaa -b block:01b1 modes"},
		{cmds.Capture(S, B, "vt"), G + "capture -s session:01aaaa -b block:01b1 --format=vt"},
		{cmds.Capture(S, B, "text"), G + "capture -s session:01aaaa -b block:01b1 --format=text"},
		{cmds.Send(S, B, "hi"), G + "send -s session:01aaaa -b block:01b1 -- hi"},
		{cmds.SendKey(S, B, "enter"), G + "send-key -s session:01aaaa -b block:01b1 enter"},
		{cmds.Split(S, B, rex.SplitOpts{Dir: "right", Ratio: 30, Label: "x", Command: []string{"htop"}}),
			G + "split -s session:01aaaa -b block:01b1 --split=right --ratio=30 --label=x --focus=false --json -- htop"},
		{cmds.Split(S, B, rex.SplitOpts{Dir: "below"}),
			G + "split -s session:01aaaa -b block:01b1 --split=below --focus=false --json"},
		{cmds.Split(S, B, rex.SplitOpts{Dir: "left", Cwd: "/srv"}),
			G + "split -s session:01aaaa -b block:01b1 --split=left --focus=false --cwd=/srv --json"},
		{cmds.RunPane(S, B, rex.RunOpts{Dir: "right", Command: []string{"make", "test"}}),
			G + "run -s session:01aaaa -b block:01b1 --split=right --focus=false --json -- make test"},
		{cmds.BlockRename(S, B, "n"), G + "block rename -s session:01aaaa block:01b1 n"},
		{cmds.BlockClose(S, B), G + "block close -s session:01aaaa block:01b1"},
		{cmds.BlockFocus(S, B), G + "block focus -s session:01aaaa block:01b1"},
		{cmds.Zoom(S, B, true), G + "zoom -s session:01aaaa -b block:01b1 --on"},
		{cmds.Zoom(S, B, false), G + "zoom -s session:01aaaa -b block:01b1 --off"},
		{cmds.ClientLs(), G + "client ls --all --json"},
		{cmds.ClientInspect(C), G + "client inspect client:02app --json"},
		{cmds.Events(), G + "events --all --json"},
		{cmds.HostsList(), G + "hosts list --json"},
		{cmds.HostsCheck(), G + "hosts check --json"},
		{cmds.HostsCheck("studio"), G + "hosts check studio --json"},
		{cmds.APICall(S, "session.view", "{}"), G + "api call -s session:01aaaa session.view {}"},
		{cmds.APICall("", "server.status", "{}"), G + "api call server.status {}"},
		{cmds.Do("x", "k=v"), G + "do x k=v"},
		{cmds.DoJSON("x", `{"a":1}`), G + `do x --args {"a":1}`},
		{cmds.ClientDo(C, "closeWindow", "id=1"), G + "-C client:02app do closeWindow id=1"},
		{cmds.Raw("server", "status"), G + "server status"},
		{cmds.Keymap(), G + "keymap --json"},
		{cmds.Actions(), G + "actions --json"},
		{cmds.ClientActions(C), G + "-C client:02app actions --json"},
		{cmds.APIDescribe("session.resize_pane"), G + "api describe session.resize_pane --json"},
		{cmds.APIList(), G + "api list --quiet"},
		{cmds.APICall("", "server.status", ""), G + "api call server.status"},
		{cmds.APICall(S, "session.view", ""), G + "api call -s session:01aaaa session.view"},
		{cmds.ClientDoJSON(C, "openFile", `{"path":"/x"}`), G + `-C client:02app do openFile --args {"path":"/x"}`},
	}
	for _, g := range golden {
		if g.cmd.Err != nil {
			t.Errorf("%s: unexpected error %v", g.want, g.cmd.Err)
			continue
		}
		if got := c.Argv(g.cmd); !slices.Equal(got, split(g.want)) {
			t.Errorf("argv\n got %q\nwant %q", got, split(g.want))
		}
	}
}

func TestArgvSendKeepsTextAsOneArgument(t *testing.T) {
	c := localClient(rextest.NewFakeRunner())
	got := c.Argv(cmds.Send(S, B, "-la; rm -rf ~\n"))
	want := []string{"rex", "--autostart=false", "--timeout=10s", "send", "-s", string(S), "-b", string(B), "--", "-la; rm -rf ~\n"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestArgvRejectsInvalid(t *testing.T) {
	bad := map[string]rex.Cmd{
		"short session":       cmds.Capture("abcd", B, "vt"),
		"short block":         cmds.Send(S, "1234", "x"),
		"wrong prefix":        cmds.Capture(S, rex.BlockID("window:01w1"), "vt"),
		"empty block":         cmds.Capture(S, "", "vt"),
		"bad format":          cmds.Capture(S, B, "html; x"),
		"empty send":          cmds.Send(S, B, ""),
		"dash name":           cmds.SessionRename(S, "-x"),
		"control in name":     cmds.WindowNew(S, "a\nb"),
		"dash new":            cmds.New("-x", ""),
		"relative dir":        cmds.New("a", "rel/dir"),
		"bad split dir":       cmds.Split(S, B, rex.SplitOpts{Dir: "diagonal"}),
		"bad ratio":           cmds.Split(S, B, rex.SplitOpts{Dir: "right", Ratio: 100}),
		"control label":       cmds.Split(S, B, rex.SplitOpts{Dir: "right", Label: "a\x1b[2J"}),
		"dash key":            cmds.SendKey(S, B, "-x"),
		"bad key":             cmds.SendKey(S, B, "ctrl+c; ls"),
		"empty method":        cmds.APICall(S, "", "{}"),
		"run without command": cmds.RunPane(S, B, rex.RunOpts{Dir: "right"}),
		"bad client":          cmds.ClientInspect("app"),
		"space in id":         cmds.Kill(rex.SessionID("session:01 aa")),
		"dash id":             cmds.Kill(rex.SessionID("-session:01aa")),
		"client actions id":   cmds.ClientActions("app"),
		"describe dash":       cmds.APIDescribe("-x"),
		"describe empty":      cmds.APIDescribe(""),
		"client do json args": cmds.ClientDoJSON(C, "x", ""),
		"client do json id":   cmds.ClientDoJSON("app", "x", `{}`),
	}
	for name, cmd := range bad {
		t.Run(name, func(t *testing.T) {
			if cmd.Err == nil {
				t.Fatalf("expected a validation error")
			}
			fr := rextest.NewFakeRunner()
			_, err := localClient(fr).Exec(context.Background(), cmd, true)
			if err == nil {
				t.Fatalf("Exec accepted an invalid command")
			}
			if n := len(fr.Calls()); n != 0 {
				t.Fatalf("runner called %d times", n)
			}
		})
	}
}

// TestActionNameScripts: rex do runs a Lua script for a name with a slash or
// a .lua suffix, reads stdin for -, and takes -e, @FILE and flags, so no
// action builder may produce any of them.
func TestActionNameScripts(t *testing.T) {
	bad := map[string]rex.Cmd{}
	for _, name := range []string{"./x.lua", "x.lua", "X.LUA", "a/b", `a\b`, "-", "-e", "@f", "", "a b", "a\x1b[2J"} {
		bad["Do "+name] = cmds.Do(name)
		bad["DoJSON "+name] = cmds.DoJSON(name, `{}`)
		bad["ClientDo "+name] = cmds.ClientDo(C, name)
		bad["ClientDoJSON "+name] = cmds.ClientDoJSON(C, name, `{}`)
	}
	for _, kv := range []string{"@k=v", "-e=print(1)", "--action=y", "--for=1s", "--all=true", "-", "@f", "noeq"} {
		bad["Do kv "+kv] = cmds.Do("x", kv)
		bad["ClientDo kv "+kv] = cmds.ClientDo(C, "x", kv)
	}
	for _, args := range []string{"-", "@f", "[1]", `"s"`, "{", "1"} {
		bad["DoJSON args "+args] = cmds.DoJSON("x", args)
		bad["ClientDoJSON args "+args] = cmds.ClientDoJSON(C, "x", args)
	}
	for name, cmd := range bad {
		t.Run(name, func(t *testing.T) {
			if cmd.Err == nil {
				t.Fatalf("built %q", cmd.Args)
			}
			fr := rextest.NewFakeRunner()
			if _, err := localClient(fr).Exec(context.Background(), cmd, true); err == nil {
				t.Fatalf("Exec accepted it")
			}
			if n := len(fr.Calls()); n != 0 {
				t.Fatalf("runner called %d times", n)
			}
		})
	}
	for _, ok := range []rex.Cmd{cmds.Do("split_right"), cmds.Do("com.superlogical.terminal.reset"), cmds.Do("x", "k=a/b.lua"),
		cmds.DoJSON("x", ` {"a":1}`), cmds.ClientDo(C, "pane.split", "direction=right")} {
		if ok.Err != nil {
			t.Errorf("%q refused: %v", ok.Args, ok.Err)
		}
	}
}

func TestArgvInvariants(t *testing.T) {
	c := localClient(rextest.NewFakeRunner())
	all := samples()
	typ := reflect.TypeOf(rex.Commands{})
	for i := 0; i < typ.NumMethod(); i++ {
		if _, ok := all[typ.Method(i).Name]; !ok {
			t.Errorf("Commands.%s has no sample in this test", typ.Method(i).Name)
		}
	}
	needsSB := map[string]bool{"capture": true, "send": true, "send-key": true, "split": true, "run": true, "call": true}
	needsNoFocus := map[string]bool{"split": true, "run": true}
	for name, cmd := range all {
		if cmd.Err != nil {
			t.Errorf("%s: sample invalid: %v", name, cmd.Err)
			continue
		}
		argv := c.Argv(cmd)
		if argv[0] != "rex" {
			t.Errorf("%s: argv[0] = %q", name, argv[0])
		}
		hasTimeout := slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "--timeout=") })
		if !hasTimeout {
			t.Errorf("%s: no --timeout= in %q", name, argv)
		}
		hasNoAutostart := slices.Contains(argv, "--autostart=false")
		if name == "ServerStart" {
			if hasNoAutostart {
				t.Errorf("server start must not carry --autostart=false: %q", argv)
			}
		} else if !hasNoAutostart {
			t.Errorf("%s: no --autostart=false in %q", name, argv)
		}
		for _, a := range argv {
			if a == "--version" || a == "-A" || a == "--all" && slices.Contains(argv, "ls") && !slices.Contains(argv, "client") {
				t.Errorf("%s: forbidden argument %q in %q", name, a, argv)
			}
		}
		sub := subcommand(argv)
		if needsSB[sub] {
			assertFlagValue(t, name, argv, "-s", "session:")
			assertFlagValue(t, name, argv, "-b", "block:")
		}
		if needsNoFocus[sub] || name == "WindowNew" {
			if !slices.Contains(argv, "--focus=false") {
				t.Errorf("%s: no --focus=false in %q", name, argv)
			}
		}
		if name == "New" && slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "--focus") }) {
			t.Errorf("new must not carry --focus: %q", argv)
		}
	}
}

// subcommand returns the last word of the command path: "capture", "call", ...
func subcommand(argv []string) string {
	words := []string{}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			break
		}
		if a == "-C" || a == "-S" || a == "-s" || a == "-b" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		words = append(words, a)
	}
	for i, w := range words {
		switch w {
		case "capture", "send", "send-key", "split", "run":
			return w
		case "call":
			if i > 0 && words[i-1] == "block" {
				return w
			}
		}
	}
	return ""
}

func assertFlagValue(t *testing.T, name string, argv []string, flag, prefix string) {
	t.Helper()
	i := slices.Index(argv, flag)
	if i < 0 || i+1 >= len(argv) || !strings.HasPrefix(argv[i+1], prefix) {
		t.Errorf("%s: %s with a full %s ID missing in %q", name, flag, prefix, argv)
	}
}

func TestArgvOtherTransports(t *testing.T) {
	native := &rex.Client{Runner: rextest.NewFakeRunner(), Transport: rex.Native{Server: "alias"}, Binary: "rex", Timeout: 5 * time.Second}
	if got, want := native.Argv(cmds.Ls()), split("rex --autostart=false --timeout=5s -S alias ls --json"); !slices.Equal(got, want) {
		t.Errorf("native argv %q", got)
	}
	ssh := &rex.Client{Runner: rextest.NewFakeRunner(), Transport: rex.SSHExec{Host: "remote-host", RexPath: "/usr/local/bin/rex", ControlDir: "/run/user/1000/lazyrex"}, Binary: "rex", Timeout: 5 * time.Second}
	got := ssh.Argv(cmds.Send(S, B, "'; rm -rf ~"))
	want := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ControlMaster=auto",
		"-o", "ControlPath=/run/user/1000/lazyrex/ssh-%C", "-o", "ControlPersist=60s", "--", "remote-host",
		"'/usr/local/bin/rex'", "'--autostart=false'", "'--timeout=5s'", "'send'", "'-s'", "'session:01aaaa'",
		"'-b'", "'block:01b1'", "'--'", `''\''; rm -rf ~'`}
	if !slices.Equal(got, want) {
		t.Errorf("ssh exec argv\n got %q\nwant %q", got, want)
	}
}
