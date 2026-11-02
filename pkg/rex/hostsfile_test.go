package rex_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// labelRE is the label regex the Rex binary holds; hostLabelRE what lazyrex
// itself declares.
var (
	labelRE     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)
	hostLabelRE = regexp.MustCompile(`^[a-z0-9-]+-lazyrex$`)
)

// TestLuaString (H1): every string is one double-quoted literal; bytes
// outside printable ASCII are 3-digit decimal escapes, so a digit after one
// cannot extend it; NUL and invalid UTF-8 are refused.
func TestLuaString(t *testing.T) {
	for in, want := range map[string]string{
		"edge":   `"edge"`,
		`a"b`:    `"a\"b"`,
		`a\b`:    `"a\\b"`,
		"a\nb":   `"a\010b"`,
		"\n1":    `"\0101"`,
		"é":      `"\195\169"`,
		"\u202e": `"\226\128\174"`,
		"]]":     `"]]"`,
	} {
		if got, err := rex.LuaString(in); err != nil || got != want {
			t.Errorf("LuaString(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"a\x00b", "\xff"} {
		if got, err := rex.LuaString(bad); err == nil {
			t.Errorf("LuaString(%q) = %q, want an error", bad, got)
		}
	}
}

// TestHostLabel (H2, G-B1, M-B1): one [a-z0-9-] segment ending in -lazyrex,
// at most 63 bytes, so it fits both the binary's segment regex and the
// hosts.list limit of 64 characters.
func TestHostLabel(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	for in, want := range map[string]string{
		"edge":                        "edge-lazyrex",
		"Edge":                        "edge-lazyrex",
		"My Box":                      "my-box-lazyrex",
		"edge.prod":                   "edge-prod-lazyrex",
		".edge":                       "edge-lazyrex",
		"edge.":                       "edge-lazyrex",
		"a..b":                        "a-b-lazyrex",
		"a_._b":                       "a-b-lazyrex",
		"\u212aedge":                  "kedge-lazyrex",
		a(54):                         a(54) + "-lazyrex",
		a(55):                         a(55) + "-lazyrex",
		a(56):                         a(55) + "-lazyrex",
		a(64):                         a(55) + "-lazyrex",
		a(54) + ".bcdef":              a(54) + "-lazyrex",
		strings.Repeat("ab.", 20):     strings.Repeat("ab-", 18) + "a-lazyrex",
		"x" + strings.Repeat(".", 59): "x-lazyrex",
	} {
		got, err := rex.HostLabel(in)
		if err != nil || got != want {
			t.Errorf("HostLabel(%q) = %q, %v, want %q", in, got, err, want)
			continue
		}
		if len(got) > 63 || !hostLabelRE.MatchString(got) || !labelRE.MatchString(got) {
			t.Errorf("HostLabel(%q) = %q (%d bytes) breaks a label rule", in, got, len(got))
		}
	}
	for _, n := range []int{54, 55, 56, 64} {
		got, _ := rex.HostLabel(a(n))
		if want := min(n, 55) + len("-lazyrex"); len(got) != want {
			t.Errorf("HostLabel(%d chars) is %d bytes, want %d", n, len(got), want)
		}
	}
	for _, bad := range []string{"日本", "...", "-", "__"} {
		if got, err := rex.HostLabel(bad); err == nil {
			t.Errorf("HostLabel(%q) = %q, want an error", bad, got)
		}
	}
}

func sshTarget(name, host string, mode rex.SSHMode, src rex.TargetSource) rex.Target {
	return rex.Target{Name: name, Kind: rex.TargetSSH, SSH: rex.SSHSpec{Host: host, Mode: mode}, Source: src}
}

// TestHostsFileEntries (H3): only forward-mode config SSH targets, sorted by
// label; a target without a label, and every target sharing one, skipped.
func TestHostsFileEntries(t *testing.T) {
	long := strings.Repeat("b", 55)
	ts := []rex.Target{
		{Name: "local", Kind: rex.TargetLocal, Source: rex.SourceBuiltin},
		{Name: "studio", Kind: rex.TargetNative, Server: "studio", Source: rex.SourceConfig},
		sshTarget("remote-host", "remote-host", "", rex.SourceConfig),
		sshTarget("edge", "user@edge", rex.SSHModeForward, rex.SourceConfig),
		sshTarget("box", "box", rex.SSHModeExec, rex.SourceConfig),
		sshTarget("flagged", "flagged", "", rex.SourceFlag),
		sshTarget("a", "a", "", rex.SourceConfig),
		sshTarget("a-", "a2", "", rex.SourceConfig),
		sshTarget("日本", "jp", "", rex.SourceConfig),
		sshTarget(long+"one", "l1", "", rex.SourceConfig),
		sshTarget(long+"two", "l2", "", rex.SourceConfig),
	}
	es, skipped := rex.HostsFileEntries(runDir, ts)
	want := []rex.HostEntry{
		{Target: "edge", Label: "edge-lazyrex", Endpoint: "unix://" + runDir + "/gui-edge_ce63845b8c961e37.sock"},
		{Target: "remote-host", Label: "remote-host-lazyrex", Endpoint: "unix://" + runDir + "/gui-remote-host_996d7034ffff31a3.sock"},
	}
	if !slices.Equal(es, want) {
		t.Fatalf("entries\n got %+v\nwant %+v", es, want)
	}
	for _, e := range es {
		i := slices.IndexFunc(ts, func(t rex.Target) bool { return t.Name == e.Target })
		if e.Endpoint != rex.SocketURI(rex.StableSocketPath(runDir, ts[i].Name, ts[i].SSH)) {
			t.Errorf("%s: endpoint %s", e.Target, e.Endpoint)
		}
	}
	why := map[string]string{}
	for _, s := range skipped {
		why[s.Target] = s.Why
	}
	if len(why) != 5 {
		t.Fatalf("skipped = %+v", skipped)
	}
	if w := why["日本"]; w != `"日本" gives no Rex host label (it needs letters or digits); rename the target.` {
		t.Errorf("日本: %q", w)
	}
	if w := why["a"]; w != `targets "a" and "a-" would share the label a-lazyrex; rename one.` {
		t.Errorf("a: %q", w)
	}
	if w := why["a-"]; w != `targets "a-" and "a" would share the label a-lazyrex; rename one.` {
		t.Errorf("a-: %q", w)
	}
	for _, n := range []string{long + "one", long + "two"} {
		if !strings.Contains(why[n], "would share the label "+long+"-lazyrex") {
			t.Errorf("%s: %q", n, why[n])
		}
	}
}

func goldenEntries() []rex.HostEntry {
	return []rex.HostEntry{
		{Target: "edge", Label: "edge-lazyrex", Endpoint: "unix://" + runDir + "/gui-edge_ce63845b8c961e37.sock"},
		{Target: "remote-host", Label: "remote-host-lazyrex", Endpoint: "unix://" + runDir + "/gui-remote-host_996d7034ffff31a3.sock"},
	}
}

// TestRenderHostsFile (H4): the byte-exact golden file, whatever the order of
// the entries; no entries leave the two header lines.
func TestRenderHostsFile(t *testing.T) {
	golden := rextest.Fixture("lazyrex_hosts.lua")
	es := goldenEntries()
	for _, order := range [][]rex.HostEntry{es, {es[1], es[0]}} {
		got, err := rex.RenderHostsFile(order)
		if err != nil || string(got) != string(golden) {
			t.Errorf("RenderHostsFile = %v\n%s\nwant\n%s", err, got, golden)
		}
	}
	got, err := rex.RenderHostsFile(nil)
	want := rex.HostsFileMarker + "\n-- Each host reaches the stable socket of a forward-mode SSH target while lazyrex runs with it active.\n\n"
	if err != nil || string(got) != want {
		t.Errorf("empty = %q, %v, want %q", got, err, want)
	}
	if rex.HostsFileMarker != "-- lazyrex-hosts v1: generated by lazyrex, which replaces this whole file. Do not edit it." {
		t.Errorf("marker %q", rex.HostsFileMarker)
	}
	bad := append(goldenEntries(), rex.HostEntry{Target: "x", Label: "Edge", Endpoint: es[0].Endpoint})
	if _, err := rex.RenderHostsFile(bad); err == nil {
		t.Error("rendered an invalid label")
	}
}

// TestHostLine (H5): an endpoint that tries to close the literal stays one
// escaped literal; a label or endpoint lazyrex would never write is refused.
func TestHostLine(t *testing.T) {
	got, err := rex.HostLine(rex.HostEntry{Label: "edge-lazyrex", Endpoint: `unix:///a");os.execute("x")--`})
	want := `rex.host({ label = "edge-lazyrex", endpoint = "unix:///a\");os.execute(\"x\")--" })`
	if err != nil || got != want {
		t.Errorf("HostLine = %q, %v\nwant %q", got, err, want)
	}
	ok := "unix://" + runDir + "/gui-edge_ce63845b8c961e37.sock"
	for _, e := range []rex.HostEntry{
		{Label: "Edge", Endpoint: ok},
		{Label: strings.Repeat("a", 56) + "-lazyrex", Endpoint: ok},
		{Label: "edge", Endpoint: ok},
		{Label: "edge-lazyrex", Endpoint: "tcp://10.0.0.9:7297"},
		{Label: "edge-lazyrex", Endpoint: "unix:///a\nb"},
	} {
		if got, err := rex.HostLine(e); err == nil {
			t.Errorf("HostLine(%+v) = %q, want an error", e, got)
		}
	}
}

// TestIncludeLine (H6): the line for init.lua names the absolute path as one
// literal and keeps f out of the human's scope.
func TestIncludeLine(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/x/.config/rex/lazyrex-hosts.lua": `do local f = loadfile("/Users/x/.config/rex/lazyrex-hosts.lua"); if f then f() end end`,
		"/a\"b\nc.lua":                           `do local f = loadfile("/a\"b\010c.lua"); if f then f() end end`,
	} {
		if got, err := rex.IncludeLine(in); err != nil || got != want {
			t.Errorf("IncludeLine(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
}

// TestParseConfigCheck (H7, M-B5.1): exists and diagnostics are pointers, so
// a missing field is told apart from an empty one.
func TestParseConfigCheck(t *testing.T) {
	clean, err := rex.ParseConfigCheck(rextest.Fixture("config_check_clean.json"))
	if err != nil || clean.Path != "/tmp/c1.lua" || clean.Exists == nil || !*clean.Exists || clean.Diagnostics == nil || len(*clean.Diagnostics) != 0 {
		t.Fatalf("clean = %+v, %v", clean, err)
	}
	errs, err := rex.ParseConfigCheck(rextest.Fixture("config_check_errors.json"))
	if err != nil || errs.Diagnostics == nil || len(*errs.Diagnostics) != 2 {
		t.Fatalf("errors = %+v, %v", errs, err)
	}
	if d := (*errs.Diagnostics)[1]; d.Severity != "error" || d.Source != "c2.lua:2" || d.Message != "boom" {
		t.Fatalf("errors[1] = %+v", d)
	}
	for in, check := range map[string]func(rex.ConfigCheckOutput) bool{
		`{}`:                                 func(o rex.ConfigCheckOutput) bool { return o.Exists == nil && o.Diagnostics == nil },
		`{"exists":false,"diagnostics":[]}`:  func(o rex.ConfigCheckOutput) bool { return o.Exists != nil && !*o.Exists },
		`{"exists":true}`:                    func(o rex.ConfigCheckOutput) bool { return o.Diagnostics == nil },
		`{"exists":true,"diagnostics":null}`: func(o rex.ConfigCheckOutput) bool { return o.Diagnostics == nil },
	} {
		if o, err := rex.ParseConfigCheck([]byte(in)); err != nil || !check(o) {
			t.Errorf("ParseConfigCheck(%s) = %+v, %v", in, o, err)
		}
	}
	for _, bad := range []string{"garbage", "", "null", "[]", `"x"`} {
		if _, err := rex.ParseConfigCheck([]byte(bad)); err == nil {
			t.Errorf("ParseConfigCheck(%q) accepted", bad)
		}
	}
}

func homeClient(fr *rextest.FakeRunner) *rex.Client {
	p, _ := rex.NewPolicy("", nil)
	return &rex.Client{Runner: fr, Transport: rex.Local{}, Binary: "rex", Timeout: 10 * time.Second, Policy: p}
}

func checkOutput(fixture, path string) string {
	return strings.Replace(rextest.FixtureString(fixture), "/tmp/c1.lua", path, 1)
}

// TestCheckHostsFile (H8): the copy in the runtime directory is checked by
// this machine's rex; only a clean report for that very file passes, and the
// copy never outlives the check.
func TestCheckHostsFile(t *testing.T) {
	dir := rextest.PrivateDir(t)
	path := filepath.Join(dir, "hosts-check-42.lua")
	data := rextest.Fixture("lazyrex_hosts.lua")
	for _, tc := range []struct {
		name, stdout, errPart string
		code                  int
	}{
		{"clean", checkOutput("config_check_clean.json", path), "", 0},
		{"other path", rextest.FixtureString("config_check_clean.json"), "/tmp/c1.lua", 0},
		{"errors", rextest.FixtureString("config_check_errors.json"), "boom", 1},
		{"warning", `{"path":"` + path + `","exists":true,"diagnostics":[{"severity":"warning","source":"x.lua:1","message":"careful"}]}`, "careful", 0},
		{"empty object", `{}`, "", 0},
		{"missing file", `{"path":"` + path + `","exists":false,"diagnostics":[]}`, "", 0},
		{"null diagnostics", `{"path":"` + path + `","exists":true,"diagnostics":null}`, "", 0},
		{"garbage", `not json`, "", 0},
		{"usage", ``, "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := rextest.NewFakeRunner()
			fr.On("config", "check").Return(tc.stdout, "Error: the configuration has errors\n", tc.code)
			if err := os.WriteFile(path, []byte("leftover"), 0o644); err != nil {
				t.Fatal(err)
			}
			var mode os.FileMode
			var seen []byte
			fr.OnCall = func([]string) {
				if fi, err := os.Lstat(path); err == nil {
					mode = fi.Mode()
				}
				seen, _ = os.ReadFile(path)
			}
			err := rex.CheckHostsFile(context.Background(), homeClient(fr), dir, 42, data)
			if (err == nil) != (tc.name == "clean") {
				t.Fatalf("CheckHostsFile = %v", err)
			}
			if err != nil && tc.errPart != "" && !strings.Contains(err.Error(), tc.errPart) {
				t.Errorf("error %q does not mention %q", err, tc.errPart)
			}
			calls := fr.Calls()
			want := []string{"rex", "--autostart=false", "--timeout=10s", "config", "check", "--json", path}
			if len(calls) != 1 || !slices.Equal(calls[0], want) {
				t.Fatalf("calls = %q, want %q", calls, want)
			}
			if d := fr.Drops("config", "check"); len(d) != 1 || !slices.Equal(d[0], []string{"REX_SERVER"}) {
				t.Errorf("drops = %q", d)
			}
			if mode != 0o600 || string(seen) != string(data) {
				t.Errorf("copy mode %v, %d bytes, want 0600 and the exact %d bytes", mode, len(seen), len(data))
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("copy left behind: %v", err)
			}
		})
	}
	t.Run("leftover symlink", func(t *testing.T) {
		victim := filepath.Join(dir, "victim")
		if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
		fr := rextest.NewFakeRunner()
		fr.On("config", "check").Return(checkOutput("config_check_clean.json", path), "", 0)
		if err := rex.CheckHostsFile(context.Background(), homeClient(fr), dir, 42, data); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(victim); string(b) != "keep" {
			t.Fatalf("followed the leftover symlink: %q", b)
		}
	})
}

// TestConfigBuilders (H9).
func TestConfigBuilders(t *testing.T) {
	c := localClient(rextest.NewFakeRunner())
	G := "rex --autostart=false --timeout=10s "
	for cmd, want := range map[*rex.Cmd]string{
		ptr(cmds.ConfigCheck("/x/y z.lua")): G + "config check --json /x/y z.lua",
		ptr(cmds.ConfigReload()):            G + "config reload --json",
	} {
		if cmd.Err != nil || !cmd.LocalOnly || strings.Join(c.Argv(*cmd), " ") != want {
			t.Errorf("%q (local only %v, %v), want %q", c.Argv(*cmd), cmd.LocalOnly, cmd.Err, want)
		}
	}
	if r := cmds.ConfigReload(); r.Kind != rex.KindAPI || r.Name != "server.config_reload" {
		t.Errorf("reload = %+v", r)
	}
	if r := cmds.ConfigCheck("/x.lua"); r.Kind != rex.KindRead {
		t.Errorf("check = %+v", r)
	}
	for _, bad := range []string{"", "rel.lua", "-x.lua", "/a\nb.lua", "/a\x1b.lua"} {
		if cmds.ConfigCheck(bad).Err == nil {
			t.Errorf("ConfigCheck(%q) accepted", bad)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// TestLocalOnly (H10, M-B3.1): whatever builds it, a command that reloads or
// checks the config runs only against this machine's server, never through
// -S or ssh.
func TestLocalOnly(t *testing.T) {
	localOnly := map[string]rex.Cmd{
		"ConfigReload":  cmds.ConfigReload(),
		"ConfigCheck":   cmds.ConfigCheck("/tmp/x.lua"),
		"APICall":       cmds.APICall("", "server.config_reload", ""),
		"APICall {}":    cmds.APICall("", "server.config_reload", "{}"),
		"Do":            cmds.Do("server.config_reload"),
		"DoJSON":        cmds.DoJSON("server.config_reload", "{}"),
		"ClientDo":      cmds.ClientDo(C, "config.reload"),
		"camelCase":     cmds.Do("configReload"),
		"Raw reload":    cmds.Raw("config", "reload", "--json"),
		"Raw check":     cmds.Raw("config", "check", "--json", "/x.lua"),
		"Raw -S reload": cmds.Raw("-S", "srv", "config", "reload"),
	}
	for name, cmd := range localOnly {
		if cmd.Err != nil {
			t.Fatalf("%s: %v", name, cmd.Err)
		}
		if !rex.IsLocalOnly(cmd) {
			t.Errorf("%s: not local only", name)
		}
	}
	for name, cmd := range map[string]rex.Cmd{
		"HostsList": cmds.HostsList(), "Ls": cmds.Ls(), "status": cmds.APICall("", "server.status", ""),
		"reload": cmds.Do("reload"), "Raw": cmds.Raw("server", "status", "--json"),
	} {
		if rex.IsLocalOnly(cmd) {
			t.Errorf("%s: local only", name)
		}
	}
	link := rex.NewSSHLink(context.Background(), rex.LinkOptions{Name: "edge", Spec: rex.SSHSpec{Host: "edge"}})
	for _, tr := range []rex.Transport{rex.Native{Server: "srv"}, rex.SSHForward{Host: "edge", Socket: "/tmp/s.sock"},
		rex.SSHExec{Host: "edge"}, link} {
		c, fr := policyClient(t, tr)
		for name, cmd := range localOnly {
			label := tr.Name() + " " + name
			d := c.Decide(cmd)
			if d.Verdict != rex.Deny {
				t.Errorf("%s: verdict %v", label, d.Verdict)
				continue
			}
			if _, err := c.Prepare(cmd); !isDenied(err) {
				t.Errorf("%s: Prepare = %v", label, err)
			}
			assertDenied(t, c, fr, cmd, label)
		}
	}
	c, fr := policyClient(t, rex.Local{})
	for name, cmd := range localOnly {
		want := rex.Confirm
		switch name {
		case "ConfigCheck":
			want = rex.Allow
		case "Raw -S reload":
			want = rex.Deny
		}
		if d := c.Decide(cmd); d.Verdict != want {
			t.Errorf("local %s: verdict %v (%s), want %v", name, d.Verdict, d.Reason, want)
		}
	}
	for _, raw := range [][]string{{"--server", "srv", "config", "reload"}, {"--server=srv", "config", "reload"}, {"-S=srv", "config", "check"}} {
		assertDenied(t, c, fr, cmds.Raw(raw...), strings.Join(raw, " "))
	}
	if _, err := c.Exec(context.Background(), cmds.ConfigReload(), true); err != nil {
		t.Fatal(err)
	}
	if d := fr.Drops("config", "reload"); len(d) != 1 || !slices.Equal(d[0], []string{"REX_SERVER"}) {
		t.Errorf("drops = %q", d)
	}
	if _, err := c.Exec(context.Background(), cmds.Ls(), false); err != nil {
		t.Fatal(err)
	}
	if d := fr.Drops("ls"); len(d) != 1 || d[0] != nil {
		t.Errorf("ls drops = %q", d)
	}
}

func isDenied(err error) bool {
	var denied *rex.DeniedError
	return errors.As(err, &denied)
}

// plainRunner is a runner without RunEnv.
type plainRunner struct{ rex.Runner }

// TestLocalOnlyNeedsEnvRunner (H11, H64, A10): a runner that cannot drop
// REX_SERVER refuses a local-only command before any confirmation.
func TestLocalOnlyNeedsEnvRunner(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On().Return("{}", "", 0)
	c := homeClient(fr)
	c.Runner = plainRunner{fr}
	for _, cmd := range []rex.Cmd{cmds.ConfigReload(), cmds.ConfigCheck("/x.lua"), cmds.APICall("", "server.config_reload", "")} {
		if d := c.Decide(cmd); d.Verdict != rex.Deny {
			t.Errorf("%q: verdict %v", cmd.Args, d.Verdict)
		}
		p, err := c.Prepare(cmd)
		if !isDenied(err) {
			t.Errorf("%q: Prepare = %v", cmd.Args, err)
		}
		if _, err := c.RunPrepared(context.Background(), p, true, nil); !isDenied(err) {
			t.Errorf("%q: RunPrepared = %v", cmd.Args, err)
		}
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("ran %q", fr.Calls())
	}
	if _, err := c.Exec(context.Background(), cmds.Ls(), false); err != nil {
		t.Fatalf("a read without RunEnv: %v", err)
	}
}

// TestExecRunnerRunEnv (H11, G-B2): the dropped variables are gone from the
// real child environment, the rest stays.
func TestExecRunnerRunEnv(t *testing.T) {
	t.Setenv("REX_SERVER", "unix:///tmp/remote.sock")
	t.Setenv("LZR_KEEP", "1")
	t.Setenv("REX_BLOCK", "block:01self")
	env := func(drop []string) string {
		t.Helper()
		var res rex.Result
		var err error
		if drop == nil {
			res, err = rex.ExecRunner{}.Run(context.Background(), []string{"/usr/bin/env"})
		} else {
			res, err = rex.ExecRunner{}.RunEnv(context.Background(), []string{"/usr/bin/env"}, drop)
		}
		if err != nil || res.Code != 0 {
			t.Fatalf("env: %v, exit %d", err, res.Code)
		}
		return "\n" + string(res.Stdout)
	}
	if out := env([]string{"REX_SERVER"}); strings.Contains(out, "\nREX_SERVER=") || strings.Contains(out, "\nREX_BLOCK=") ||
		!strings.Contains(out, "\nLZR_KEEP=1\n") || !strings.Contains(out, "\nPATH=") {
		t.Errorf("RunEnv environment:%s", out)
	}
	if out := env(nil); !strings.Contains(out, "\nREX_SERVER=unix:///tmp/remote.sock\n") || strings.Contains(out, "\nREX_BLOCK=") {
		t.Errorf("Run environment:%s", out)
	}
	var _ rex.EnvRunner = rex.ExecRunner{}
	var _ rex.EnvRunner = rextest.NewFakeRunner()
}

// TestIsOwnHost (H12, A2, A3, A4): declared means a source naming a .lua line
// (spaces allowed); managed means declared in lazyrex-hosts.lua; own means
// declared, a -lazyrex label and only stable gui sockets. Endpoints may be
// empty or null.
func TestIsOwnHost(t *testing.T) {
	v := parseHostsFixture(t, "hosts_list_lazyrex.json")
	want := map[string][3]bool{ // declared, managed, own
		"edge-lazyrex":        {true, true, true},
		"edge":                {false, false, false},
		"remote-host-lazyrex": {false, false, false},
		"old-lazyrex":         {true, false, true},
		"gone-lazyrex":        {true, true, true},
		"studio":              {false, false, false},
		"nulls-lazyrex":       {false, false, false},
		"cfg-lazyrex":         {false, false, false},
		"empty-lazyrex":       {true, true, false},
		"mixed-lazyrex":       {true, true, false},
	}
	if len(v.Hosts) != len(want) {
		t.Fatalf("hosts = %+v", v.Hosts)
	}
	for _, h := range v.Hosts {
		w := want[h.Label]
		if got := [3]bool{rex.IsDeclared(h), rex.IsManaged(h), rex.IsOwnHost(h)}; got != w {
			t.Errorf("%s: declared, managed, own = %v, want %v", h.Label, got, w)
		}
	}
	for src, declared := range map[string]bool{
		"init.lua:12": true, "lazyrex-hosts.lua:4": true, "/Users/Jane Doe/.config/rex/lazyrex-hosts.lua:5": true,
		"rel dir/x.lua:1": true, "stored": false, "config": false, "": false, "init.lua": false, "init.lua:": false,
		"x.lua:1a": false, ".lua:3": false, "lazyrex-hosts.lua.bak:3": false, "init.LUA:3": false,
	} {
		if got := rex.IsDeclared(rex.Host{Label: "x", Source: rex.Loose(src)}); got != declared {
			t.Errorf("IsDeclared(source %q) = %v, want %v", src, got, declared)
		}
	}
	for src, managed := range map[string]bool{
		"lazyrex-hosts.lua:4": true, "/a b/lazyrex-hosts.lua:9": true, "/a/xlazyrex-hosts.lua:1": false,
		"/a/lazyrex-hosts.lua/init.lua:1": false, "init.lua:2": false,
	} {
		if got := rex.IsManaged(rex.Host{Label: "x", Source: rex.Loose(src)}); got != managed {
			t.Errorf("IsManaged(source %q) = %v, want %v", src, got, managed)
		}
	}
	attic := parseHostsFixture(t, "hosts_list_hosts.json").Hosts[1]
	if attic.Label != "attic" || !rex.IsDeclared(attic) || rex.IsManaged(attic) || rex.IsOwnHost(attic) {
		t.Errorf("attic = %+v", attic)
	}
}

// TestParseConfigReload (H13).
func TestParseConfigReload(t *testing.T) {
	o, err := rex.ParseConfigReload(rextest.Fixture("config_reload_warning.json"))
	if err != nil || o.Generation != "2" || len(o.Diagnostics) != 1 || o.Diagnostics[0].Severity != "notice" || o.Diagnostics[0].Message != "x" {
		t.Fatalf("warning = %+v, %v", o, err)
	}
	if o, err := rex.ParseConfigReload([]byte(`{"generation":"7","diagnostics":[]}`)); err != nil || o.Generation != "7" || len(o.Diagnostics) != 0 {
		t.Fatalf("clean = %+v, %v", o, err)
	}
	for _, bad := range []string{"garbage", "null", "[]"} {
		if _, err := rex.ParseConfigReload([]byte(bad)); err == nil {
			t.Errorf("ParseConfigReload(%q) accepted", bad)
		}
	}
}
