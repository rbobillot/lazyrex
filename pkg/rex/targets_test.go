package rex_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func nativeClient(fr *rextest.FakeRunner, server string) *rex.Client {
	policy, _ := rex.NewPolicy("", nil)
	return &rex.Client{Runner: fr, Transport: rex.Native{Server: server}, Binary: "rex", Timeout: 5 * time.Second, Policy: policy}
}

// TestNativeArgv (1a-1d): a native target puts -S SERVER after the global
// flags, as one argv element even with inner spaces.
func TestNativeArgv(t *testing.T) {
	cases := []struct {
		server string
		cmd    rex.Cmd
		want   []string
	}{
		{"studio", cmds.Ls(), split("rex --autostart=false --timeout=5s -S studio ls --json")},
		{"192.168.1.20:7297", cmds.Whoami(), split("rex --autostart=false --timeout=5s -S 192.168.1.20:7297 whoami --json")},
		{"Studio Mac", cmds.Ls(), []string{"rex", "--autostart=false", "--timeout=5s", "-S", "Studio Mac", "ls", "--json"}},
		{"Studio Mac", cmds.HostsCheck("Studio Mac"), []string{"rex", "--autostart=false", "--timeout=5s", "-S", "Studio Mac", "hosts", "check", "Studio Mac", "--json"}},
		{"studio", cmds.Events(), split("rex --autostart=false --timeout=5s -S studio events --all --json")},
	}
	for _, tc := range cases {
		c := nativeClient(rextest.NewFakeRunner(), tc.server)
		if got := c.Argv(tc.cmd); !slices.Equal(got, tc.want) {
			t.Errorf("Argv = %q, want %q", got, tc.want)
		}
	}
}

// TestHostsCheckCmd (1e, 1f).
func TestHostsCheckCmd(t *testing.T) {
	c := localClient(rextest.NewFakeRunner())
	if got := c.Argv(cmds.HostsCheck()); !slices.Equal(got, split("rex --autostart=false --timeout=10s hosts check --json")) {
		t.Fatalf("HostsCheck() = %q", got)
	}
	if got := c.Argv(cmds.HostsCheck("studio", "attic")); !slices.Equal(got, split("rex --autostart=false --timeout=10s hosts check studio attic --json")) {
		t.Fatalf("HostsCheck(studio, attic) = %q", got)
	}
	if cmd := cmds.HostsCheck(); cmd.Kind != rex.KindRead || cmd.Err != nil {
		t.Fatalf("HostsCheck() = %+v, want a read", cmd)
	}
	for _, bad := range []string{"-x", " a"} {
		if cmd := cmds.HostsCheck(bad); cmd.Err == nil {
			t.Errorf("HostsCheck(%q) accepted", bad)
		}
	}
}

// TestCheckServer (1g, 1h).
func TestCheckServer(t *testing.T) {
	for _, bad := range []string{"", "-oProxyCommand=x", "--server", "a\tb", "a\nb", "x\x1b[2J", "\x00", "\xff", strings.Repeat("a", 256), " a", "a "} {
		if err := rex.CheckServer(bad); err == nil {
			t.Errorf("CheckServer(%q) accepted", bad)
		}
	}
	for _, good := range []string{"studio", "Studio Mac", "studio.tail1234.ts.net", "192.168.1.20:7297", "unix:///tmp/rex.sock", "[::1]:7297", strings.Repeat("a", 255)} {
		if err := rex.CheckServer(good); err != nil {
			t.Errorf("CheckServer(%q): %v", good, err)
		}
	}
}

func TestCheckTargetName(t *testing.T) {
	for _, bad := range []string{"", "-x", " a", "a ", "a\tb", "x\x1b]0;t\x07", "\xff", strings.Repeat("é", 65)} {
		if err := rex.CheckTargetName(bad); err == nil {
			t.Errorf("CheckTargetName(%q) accepted", bad)
		}
	}
	for _, good := range []string{"studio", "Studio Mac", "007", strings.Repeat("é", 64)} {
		if err := rex.CheckTargetName(good); err != nil {
			t.Errorf("CheckTargetName(%q): %v", good, err)
		}
	}
}

// TestClientDeniesUnsafeServer (1i): a native transport whose server could
// be read as a flag is denied before the runner, for calls and streams.
func TestClientDeniesUnsafeServer(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On().Return("{}", "", 0)
	c := nativeClient(fr, "-oProxyCommand=x")
	var log []rex.LogEntry
	c.Log = func(e rex.LogEntry) { log = append(log, e) }
	var denied *rex.DeniedError
	if _, err := c.Exec(context.Background(), cmds.Ls(), false); !errors.As(err, &denied) {
		t.Fatalf("Exec: %v, want DeniedError", err)
	}
	if _, err := c.Stream(context.Background(), cmds.Events()); !errors.As(err, &denied) {
		t.Fatalf("Stream: %v, want DeniedError", err)
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("runner called %d times: %q", n, fr.Calls())
	}
	if len(log) != 2 || log[0].Refused == "" || log[1].Refused == "" {
		t.Fatalf("refusals not logged: %+v", log)
	}
	if d := c.Decide(cmds.Ls()); d.Verdict != rex.Deny {
		t.Fatalf("Decide = %v", d)
	}
}

func TestClientWith(t *testing.T) {
	c := localClient(rextest.NewFakeRunner())
	n := c.With(rex.Native{Server: "studio"})
	if _, ok := c.Transport.(rex.Local); !ok {
		t.Fatalf("With changed the original transport: %T", c.Transport)
	}
	if got := n.Argv(cmds.Ls()); !slices.Equal(got, split("rex --autostart=false --timeout=10s -S studio ls --json")) {
		t.Fatalf("Argv = %q", got)
	}
	if n.Runner != c.Runner || n.Timeout != c.Timeout || n.Binary != c.Binary {
		t.Fatalf("With lost fields: %+v", n)
	}
}

// TestExecGuardedOrder (A5): the guard runs before the cancelled-context
// shortcut, and a cancelled call never starts a process.
func TestExecGuardedOrder(t *testing.T) {
	c, fr, log := guardClient(rex.Local{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changed := errors.New("target changed")
	_, err := c.ExecGuarded(ctx, cmds.Ls(), false, func() error { return changed })
	var denied *rex.DeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "target changed") {
		t.Fatalf("err = %v, want the guard's refusal", err)
	}
	if len(*log) != 1 || !strings.Contains((*log)[0].Refused, "target changed") {
		t.Fatalf("log = %+v", *log)
	}
	guarded := false
	out, err := c.ExecGuarded(ctx, cmds.Ls(), false, func() error { guarded = true; return nil })
	if err != nil || out.Kind != rex.ExitInterrupted || !guarded {
		t.Fatalf("cancelled guarded call: %+v, %v (guard ran %v)", out, err, guarded)
	}
	out, err = c.Exec(ctx, cmds.Ls(), false)
	if err != nil || out.Kind != rex.ExitInterrupted {
		t.Fatalf("cancelled read: %+v, %v", out, err)
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("runner called %d times", n)
	}
}

// TestWithFlag (1j, A2).
func TestWithFlag(t *testing.T) {
	base, err := rex.ConfigTargets([]rex.TargetSpec{{Kind: "local", Name: "home"}, {Kind: "native", Name: "lab", Server: "lab.ts.net"}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		flag    string
		idx     int
		n       int
		kind    rex.TargetKind
		name    string
		wantErr bool
	}{
		{"", 0, 2, rex.TargetLocal, "home", false},
		{"local", 0, 2, rex.TargetLocal, "home", false},
		{"LOCAL", 0, 2, rex.TargetLocal, "home", false},
		{"HOME", 0, 2, rex.TargetLocal, "home", false},
		{"Lab", 1, 2, rex.TargetNative, "lab", false},
		{"studio", 2, 3, rex.TargetNative, "studio", false},
		{"Studio Mac", 2, 3, rex.TargetNative, "Studio Mac", false},
		{"-oProxyCommand=x", 0, 0, "", "", true},
		{" x", 0, 0, "", "", true},
		{"a\x1b[2J", 0, 0, "", "", true},
	}
	for _, tc := range cases {
		got, idx, err := rex.WithFlag(base, tc.flag)
		if tc.wantErr {
			if err == nil {
				t.Errorf("WithFlag(%q) accepted", tc.flag)
			}
			continue
		}
		if err != nil || idx != tc.idx || len(got) != tc.n || got[idx].Kind != tc.kind || got[idx].Name != tc.name {
			t.Errorf("WithFlag(%q) = %+v, %d, %v", tc.flag, got, idx, err)
		}
	}
	got, idx, _ := rex.WithFlag(base, "studio")
	if f := got[idx]; f.Server != "studio" || f.Source != rex.SourceFlag {
		t.Fatalf("ad-hoc target = %+v", f)
	}
	if len(base) != 2 {
		t.Fatalf("WithFlag changed its input: %+v", base)
	}
}

// TestConfigTargets (C1, C3).
func TestConfigTargets(t *testing.T) {
	ts, err := rex.ConfigTargets([]rex.TargetSpec{{Kind: "native", Name: "studio", Server: "studio"}, {Kind: "local", Name: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].Name != "home" || ts[0].Kind != rex.TargetLocal || ts[1].Name != "studio" || ts[1].Source != rex.SourceConfig {
		t.Fatalf("targets = %+v", ts)
	}
	if _, ok := ts[0].Transport().(rex.Local); !ok {
		t.Fatalf("local transport = %T", ts[0].Transport())
	}
	if n, ok := ts[1].Transport().(rex.Native); !ok || n.Server != "studio" {
		t.Fatalf("native transport = %#v", ts[1].Transport())
	}
	none, err := rex.ConfigTargets(nil)
	if err != nil || len(none) != 1 || none[0].Name != rex.LocalName || none[0].Source != rex.SourceBuiltin {
		t.Fatalf("no config targets = %+v, %v", none, err)
	}
	for name, specs := range map[string][]rex.TargetSpec{
		"targets[0].server": {{Kind: "native", Name: "x", Server: "-x"}},
		"targets[1].server": {{Kind: "native", Name: "x", Server: "x"}, {Kind: "native", Name: "y", Server: "a\x1b[2J"}},
		"targets[0].name":   {{Kind: "native", Name: " x", Server: "x"}},
	} {
		if _, err := rex.ConfigTargets(specs); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("ConfigTargets(%+v) = %v, want an error naming %s", specs, err, name)
		}
	}
}

func parseHostsFixture(t *testing.T, name string) rex.HostsView {
	t.Helper()
	v, err := rex.ParseHosts(rextest.Fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestParseHosts (3a-3c). The hosts fixture is derived from the rex hosts
// help text: its shape beyond home, hosts and warnings is a guess.
func TestParseHosts(t *testing.T) {
	v := parseHostsFixture(t, "hosts_list_hosts.json")
	if v.Skipped != 2 || len(v.Hosts) != 9 {
		t.Fatalf("hosts = %d, skipped = %d: %+v", len(v.Hosts), v.Skipped, v.Hosts)
	}
	studio := v.Hosts[0]
	if studio.Label != "studio" || len(studio.Endpoints) != 2 || studio.AddedFrom != "laptop" || studio.Source != "stored" ||
		!strings.Contains(string(studio.Endpoints[1]), "tailscale") {
		t.Fatalf("studio = %+v", studio)
	}
	if v.Hosts[1].AddedFrom != "init.lua:4" {
		t.Fatalf("attic = %+v", v.Hosts[1])
	}
	nulls := v.Hosts[8]
	if nulls.Label != "nulls" || len(nulls.Endpoints) != 0 || nulls.Source != "" {
		t.Fatalf("nulls = %+v", nulls)
	}
	if len(v.Warnings) != 1 || !strings.Contains(v.Warnings[0], "stale") {
		t.Fatalf("warnings = %q", v.Warnings)
	}
	if v.Home.Label != "" || !v.Home.Reached || !v.Home.ThisMachine {
		t.Fatalf("home = %+v", v.Home)
	}

	empty := parseHostsFixture(t, "hosts_list.json")
	if len(empty.Hosts) != 0 || empty.Skipped != 0 || empty.Home.Label != "home" {
		t.Fatalf("empty = %+v", empty)
	}
	live, err := rex.ParseHosts([]byte(`{"home":{"label":"","endpoint":"unix:///run/user/1000/rex/server.sock","this_machine":true,"reached":true},"hosts":[],"warnings":[]}`))
	if err != nil || live.Home.Label != "" || len(live.Hosts) != 0 {
		t.Fatalf("live shape = %+v, %v", live, err)
	}
	if v, err := rex.ParseHosts([]byte(`{"hosts":null}`)); err != nil || len(v.Hosts) != 0 {
		t.Fatalf("null hosts = %+v, %v", v, err)
	}
	for _, bad := range []string{`{"hosts":"x"}`, `not json`} {
		if _, err := rex.ParseHosts([]byte(bad)); err == nil {
			t.Errorf("ParseHosts(%s) accepted", bad)
		}
	}
}

func rowNames(rows []rex.Target) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// TestMergeTargets (3d-3f, A1).
func TestMergeTargets(t *testing.T) {
	v := parseHostsFixture(t, "hosts_list_hosts.json")
	base, _ := rex.ConfigTargets(nil)
	rows, skipped := rex.MergeTargets(base, v.Hosts, base[0])
	if want := []string{"local", "studio", "attic", "Studio Mac", "nulls"}; !slices.Equal(rowNames(rows), want) {
		t.Fatalf("rows = %q, want %q", rowNames(rows), want)
	}
	if rows[1].Source != rex.SourceHosts || rows[1].Kind != rex.TargetNative || rows[1].Server != "studio" || rows[1].Host == nil {
		t.Fatalf("studio row = %+v", rows[1])
	}
	if len(skipped) != 5 {
		t.Fatalf("skipped = %q", skipped)
	}
	joined := strings.Join(skipped, "\n")
	for _, part := range []string{"-oProxyCommand=x", "lead", "Local", "STUDIO", "bad"} {
		if !strings.Contains(joined, part) {
			t.Errorf("skipped %q does not mention %q", skipped, part)
		}
	}
	if strings.ContainsAny(joined, "\x1b\x07") {
		t.Fatalf("skipped reasons not sanitized: %q", skipped)
	}

	cfg, _ := rex.ConfigTargets([]rex.TargetSpec{{Kind: "native", Name: "studio", Server: "studio"}, {Kind: "native", Name: "attic", Server: "attic.lan"}})
	rows, skipped = rex.MergeTargets(cfg, v.Hosts, cfg[0])
	if want := []string{"local", "studio", "attic", "Studio Mac", "nulls"}; !slices.Equal(rowNames(rows), want) {
		t.Fatalf("rows = %q, want %q", rowNames(rows), want)
	}
	if rows[1].Source != rex.SourceConfig || rows[1].Host == nil || len(rows[1].Host.Endpoints) != 2 {
		t.Fatalf("merged studio = %+v", rows[1])
	}
	if rows[2].Host != nil || rows[2].Server != "attic.lan" {
		t.Fatalf("attic with another server merged: %+v", rows[2])
	}
	if !strings.Contains(strings.Join(skipped, "\n"), "attic") {
		t.Fatalf("hidden attic not reported: %q", skipped)
	}

	adhoc, idx, _ := rex.WithFlag(base, "studio")
	rows, _ = rex.MergeTargets(adhoc, v.Hosts, adhoc[idx])
	if n := len(rows); n != 5 || rows[1].Source != rex.SourceFlag || rows[1].Host == nil {
		t.Fatalf("ad-hoc merge = %+v", rows)
	}

	active := rows[3]
	rows, _ = rex.MergeTargets(base, nil, active)
	if want := []string{"local", "Studio Mac"}; !slices.Equal(rowNames(rows), want) {
		t.Fatalf("active dropped: %q", rowNames(rows))
	}
	if i, ok := rex.FindTarget(rows, "studio mac"); !ok || i != 1 {
		t.Fatalf("FindTarget = %d, %v", i, ok)
	}
}

// TestParseHostsCheck (3m, 3n). The check fixture is derived from the rex
// hosts help text; its keys are guesses decoded defensively.
func TestParseHostsCheck(t *testing.T) {
	r, err := rex.ParseHostsCheck(rextest.Fixture("hosts_check.json"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Skipped != 2 || r.Unrecognized != 1 || len(r.Entries) != 6 {
		t.Fatalf("report = %+v", r)
	}
	want := map[string]rex.CheckState{"studio": rex.CheckReachable, "attic": rex.CheckNotReachable, "nulls": rex.CheckDown,
		"Studio Mac": rex.CheckNotReachable, "weird": rex.CheckUnknown, "ghost": rex.CheckReachable}
	for _, e := range r.Entries {
		if w, ok := want[e.Label]; !ok || e.State != w {
			t.Errorf("entry %+v, want state %v", e, w)
		}
		if strings.Contains(string(e.Detail)+string(e.Status)+string(e.Endpoint), "SECRETMSG") {
			t.Errorf("message read: %+v", e)
		}
	}
	if e := r.Entries[2]; !strings.Contains(string(e.Detail), "connection refused") {
		t.Fatalf("nulls detail = %+v", e)
	}
	if e := r.Entries[3]; e.Endpoint != "tcp://192.168.1.21:7297" || e.Detail != "timeout" {
		t.Fatalf("Studio Mac = %+v", e)
	}
	if e := r.Entries[4]; !strings.Contains(string(e.Status), "quantum") {
		t.Fatalf("weird raw status lost: %+v", e)
	}

	for name, doc := range map[string]string{
		"results":    `{"results":[{"host":"studio","status":"Not_Reachable-From here"}]}`,
		"checks":     `{"checks":[{"label":"studio","status":" REACHABLE "}]}`,
		"bare array": `[{"label":"studio","reachable":true}]`,
	} {
		r, err := rex.ParseHostsCheck([]byte(doc))
		if err != nil || len(r.Entries) != 1 || r.Entries[0].Label != "studio" || r.Entries[0].State == rex.CheckUnknown {
			t.Errorf("%s: %+v, %v", name, r, err)
		}
	}
	if r, err := rex.ParseHostsCheck([]byte(`{"hosts":[]}`)); err != nil || len(r.Entries) != 0 || r.Skipped != 0 || r.Unrecognized != 0 {
		t.Fatalf("empty report = %+v, %v", r, err)
	}
	for _, bad := range []string{`{"data":[]}`, `{}`, `"x"`, `not json`, `{"hosts":"x"}`} {
		if _, err := rex.ParseHostsCheck([]byte(bad)); err == nil {
			t.Errorf("ParseHostsCheck(%s) accepted", bad)
		}
	}
}

// TestDeriveStatusNativeRevision (10a-10c).
func TestDeriveStatusNativeRevision(t *testing.T) {
	ok := func(s string) rex.Outcome {
		return rex.Outcome{Kind: rex.ExitOK, Result: rex.Result{Stdout: []byte(s)}}
	}
	whoami := ok(rextest.FixtureString("whoami.json"))
	native := rex.Native{Server: "studio"}
	srv10, v9, v8 := ok(`{"api_revision":10}`), ok(`{"api_revision":9}`), ok(`{"api_revision":8}`)
	if st := rex.DeriveStatus(native, whoami, &srv10, &v9); st.APIRevision != 10 || !st.APIMismatch {
		t.Fatalf("10a: %+v", st)
	}
	failed := rex.Outcome{Kind: rex.ExitFailed, Result: rex.Result{Code: 1}}
	if st := rex.DeriveStatus(native, whoami, &failed, &v9); st.APIRevision != 0 || st.APIMismatch {
		t.Fatalf("10b: %+v", st)
	}
	srv9 := ok(`{"api_revision":9}`)
	if st := rex.DeriveStatus(native, whoami, &srv9, &v8); !st.APIMismatch {
		t.Fatalf("10c: %+v", st)
	}
	if st := rex.DeriveStatus(rex.Local{}, whoami, &failed, &v9); st.APIRevision != 9 {
		t.Fatalf("local falls back to the client revision: %+v", st)
	}
}

// TestHostsPolicy (S1, A15).
func TestHostsPolicy(t *testing.T) {
	policy, _ := rex.NewPolicy("", nil)
	if d := policy.Check(cmds.HostsCheck("studio")); d.Verdict != rex.Allow {
		t.Fatalf("hosts check = %v", d)
	}
	for _, args := range [][]string{{"-S", "x", "hosts", "add", "h"}, {"hosts", "edit", "h"}, {"--server", "x", "hosts", "remove", "h"}} {
		if d := policy.Check(cmds.Raw(args...)); d.Verdict != rex.Deny {
			t.Errorf("%q = %v", args, d)
		}
	}
	if d := policy.Check(cmds.APICall("", "hosts.check", "")); d.Verdict != rex.Deny {
		t.Fatalf("api call hosts.check = %v", d)
	}
	if d := policy.Check(cmds.APICall("", "hosts.list", "")); d.Verdict != rex.Allow {
		t.Fatalf("api call hosts.list = %v", d)
	}
}
