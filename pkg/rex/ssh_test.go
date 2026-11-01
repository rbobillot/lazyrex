package rex_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

const (
	shellProbeCmd = `echo "$SHELL"`
	pathProbeCmd  = `command -v rex || env QUOTING_STYLE=literal ls /usr/local/bin/rex /opt/homebrew/bin/rex /Applications/Rex.app/Contents/Resources/bin/rex '/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex'`
	betaRex       = "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"
	runDir        = "/run/user/1000/lazyrex"
	remoteSock    = "/run/user/1001/rex/server.sock"
	sshSession    = rex.SessionID("session:01aaaa")
	sshBlock      = rex.BlockID("block:01b9")
)

func fwdSock(n int) string { return fmt.Sprintf("%s/fwd-42-%d.sock", runDir, n) }

var (
	probePrefix = []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", "remote-host"}
	execPrefix  = []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ControlMaster=auto",
		"-o", "ControlPath=" + runDir + "/ssh-%C", "-o", "ControlPersist=60s", "--", "remote-host"}
)

func joined(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// shellSplit splits a command line the way a POSIX shell would, for the
// quoting lazyrex emits: single quotes and backslashes only. Any other shell
// syntax outside quotes is an error, so a test catches injection.
func shellSplit(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == ' ':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case ch == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("unterminated quote in %q", s)
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case ch == '\\' && i+1 < len(s):
			cur.WriteByte(s[i+1])
			i++
			inWord = true
		case strings.IndexByte("$`;|&<>()\"\n\t*?[#~!{}", ch) >= 0:
			return nil, fmt.Errorf("unquoted shell syntax %q in %q", ch, s)
		default:
			cur.WriteByte(ch)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// remoteWords re-splits the words ssh joins after the host, as the remote
// login shell does.
func remoteWords(t *testing.T, argv []string) []string {
	t.Helper()
	i := slices.Index(argv, "--")
	if i < 0 || i+1 >= len(argv) || argv[i+1] != "remote-host" {
		t.Fatalf("no -- before the host in %q", argv)
	}
	words, err := shellSplit(strings.Join(argv[i+2:], " "))
	if err != nil {
		t.Fatal(err)
	}
	return words
}

func sshClient(fr *rextest.FakeRunner, tr rex.Transport, self string) (*rex.Client, *logRec) {
	policy, _ := rex.NewPolicy(self, nil)
	log := &logRec{}
	return &rex.Client{Runner: fr, Transport: tr, Binary: "rex", Timeout: 10 * time.Second, Policy: policy, Log: log.add}, log
}

type logRec struct {
	mu      sync.Mutex
	entries []rex.LogEntry
}

func (l *logRec) add(e rex.LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
}

func (l *logRec) all() []rex.LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

func (l *logRec) text() string {
	var b strings.Builder
	for _, e := range l.all() {
		fmt.Fprintf(&b, "note=%q display=%q refused=%q code=%d stderr=%q\n", e.Note, e.Display, e.Refused, e.Code, e.Stderr)
	}
	return b.String()
}

// remoteRunner answers the probes, the exec and the forwarded reads of an
// SSH target whose login shell is shell.
func remoteRunner(shell string) *rextest.FakeRunner {
	fr := rextest.NewFakeRunner()
	fr.On(shellProbeCmd).Return(shell+"\n", "", 0)
	fr.On(pathProbeCmd).Return(rextest.FixtureString("rex_path_probe.txt"), "", 2)
	for _, args := range [][]string{{"whoami", "--json"}, {"server", "status", "--json"}} {
		fixture := map[string]string{"whoami": "whoami_remote.json", "server": "server_status.json"}[args[0]]
		quoted := make([]string, len(args))
		for i, a := range args {
			quoted[i] = rex.ShellQuote(a)
		}
		fr.On(quoted...).Return(rextest.FixtureString(fixture), "", 0)
		fr.On(args...).Return(rextest.FixtureString(fixture), "", 0)
	}
	fr.On("ls", "--json").Return(rextest.FixtureString("ls.json"), "", 0)
	for _, mut := range [][]string{{"server", "start", "--json"}, {"send"}, {"kill"}, {"do"}} {
		fr.On(mut...).Return("{}", "", 0)
		q := make([]string, len(mut))
		for i, a := range mut {
			q[i] = rex.ShellQuote(a)
		}
		fr.On(q...).Return("{}", "", 0)
	}
	return fr
}

type linkRig struct {
	t       *testing.T
	fr      *rextest.FakeRunner
	sockets *rextest.FakeSockets
	env     *rex.SSHEnv
	clk     *rextest.FakeClock
	group   *rex.TunnelGroup
	base    *rex.Client
	link    *rex.SSHLink
	ctx     context.Context
	cancel  context.CancelFunc
	log     *logRec
	ups     chan bool
}

type rigOpt func(*linkRig)

func withSockets(s *rextest.FakeSockets) rigOpt { return func(r *linkRig) { r.sockets = s } }
func withSelfBlock(id string) rigOpt {
	return func(r *linkRig) { r.base, r.log = sshClient(r.fr, rex.Local{}, id) }
}

func newLinkRig(t *testing.T, fr *rextest.FakeRunner, spec rex.SSHSpec, opts ...rigOpt) *linkRig {
	t.Helper()
	r := &linkRig{t: t, fr: fr, sockets: &rextest.FakeSockets{}, clk: rextest.NewFakeClock(time.Unix(1_000_000, 0)),
		group: &rex.TunnelGroup{}, ups: make(chan bool, 64)}
	r.base, r.log = sshClient(fr, rex.Local{}, "")
	for _, o := range opts {
		o(r)
	}
	r.env = rextest.SSHEnv(runDir, r.sockets)
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.link = rex.NewSSHLink(r.ctx, rex.LinkOptions{Base: r.base, Name: "remote-host", Spec: spec, Env: r.env, Clock: r.clk,
		Group: r.group, OnTunnel: func(up bool) { r.ups <- up }})
	t.Cleanup(r.close)
	return r
}

func (r *linkRig) close() {
	r.cancel()
	r.group.Stop()
	if !r.group.Wait(rextest.Timeout) {
		r.t.Error("tunnel goroutines still running after cancel")
	}
}

func (r *linkRig) status() rex.TargetStatus { return r.link.Status(r.ctx, nil) }

func (r *linkRig) client() *rex.Client { return r.base.With(r.link) }

func (r *linkRig) waitTunnel(want bool) {
	r.t.Helper()
	select {
	case up := <-r.ups:
		if up != want {
			r.t.Fatalf("tunnel up = %v, want %v", up, want)
		}
	case <-time.After(rextest.Timeout):
		r.t.Fatalf("timed out waiting for the tunnel to be up = %v", want)
	}
}

func (r *linkRig) noTunnelChange() {
	r.t.Helper()
	select {
	case up := <-r.ups:
		r.t.Fatalf("unexpected tunnel change up = %v", up)
	default:
	}
}

// online brings a forward-mode link online through its tunnel.
func (r *linkRig) online() rex.TargetStatus {
	r.t.Helper()
	if st := r.status(); st.State != rex.StateUnknown {
		r.t.Fatalf("first status = %v (%s), want unknown while the tunnel starts", st.State, st.Detail)
	}
	r.waitTunnel(true)
	st := r.status()
	if st.State != rex.StateOnline {
		r.t.Fatalf("status = %v (%s), want online; calls:\n%s", st.State, st.Detail, dumpCalls(r.fr))
	}
	return st
}

func dumpCalls(fr *rextest.FakeRunner) string {
	var b strings.Builder
	for _, c := range fr.Calls() {
		b.WriteString(strings.Join(c, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func countArgv(fr *rextest.FakeRunner, argv []string) int {
	n := 0
	for _, c := range fr.Calls() {
		if slices.Equal(c, argv) {
			n++
		}
	}
	return n
}

var forward = rex.SSHSpec{Host: "remote-host"}
var execOnly = rex.SSHSpec{Host: "remote-host", Mode: rex.SSHModeExec}

// TestSSHExecArgvGolden (1a, decision 3): the exact exec argv, quoted for a
// POSIX shell and plain for any other.
func TestSSHExecArgvGolden(t *testing.T) {
	tr := rex.SSHExec{Host: "remote-host", RexPath: "/usr/local/bin/rex", ControlDir: runDir}
	c, _ := sshClient(rextest.NewFakeRunner(), tr, "")
	want := joined(execPrefix, []string{"'/usr/local/bin/rex'", "'--autostart=false'", "'--timeout=10s'", "'ls'", "'--json'"})
	if got := c.Argv(cmds.Ls()); !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	send := cmds.Send(sshSession, sshBlock, "it's $(id) `x` a b")
	got := c.Argv(send)
	if i := slices.Index(got, "--"); i < 0 || got[i+1] != "remote-host" || !slices.Equal(got[:i+2], execPrefix) {
		t.Fatalf("options or host: %q", got)
	}
	rexArgv := rex.Local{}.Argv("/usr/local/bin/rex", []string{"--autostart=false", "--timeout=10s"}, send.Args)
	if words := remoteWords(t, got); !slices.Equal(words, rexArgv) {
		t.Fatalf("remote words\n got %q\nwant %q", words, rexArgv)
	}

	plain := tr
	plain.NonPOSIX = true
	pc, _ := sshClient(rextest.NewFakeRunner(), plain, "")
	want = joined(execPrefix, []string{"/usr/local/bin/rex", "--autostart=false", "--timeout=10s", "ls", "--json"})
	if got := pc.Argv(cmds.Ls()); !slices.Equal(got, want) {
		t.Fatalf("non-POSIX argv\n got %q\nwant %q", got, want)
	}
}

// TestSSHNonPOSIXRefusesWords (decision 3): under a shell not known to be
// POSIX, a word that is not plain is refused before the runner.
func TestSSHNonPOSIXRefusesWords(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On().Return("{}", "", 0)
	for name, tc := range map[string]struct {
		tr  rex.SSHExec
		cmd rex.Cmd
	}{
		"text":     {rex.SSHExec{Host: "remote-host", NonPOSIX: true, RexPath: "/usr/local/bin/rex"}, cmds.Send(sshSession, sshBlock, "a b")},
		"quote":    {rex.SSHExec{Host: "remote-host", NonPOSIX: true, RexPath: "/usr/local/bin/rex"}, cmds.Send(sshSession, sshBlock, "it's")},
		"rex path": {rex.SSHExec{Host: "remote-host", NonPOSIX: true, RexPath: "/opt/my rex/rex"}, cmds.Ls()},
	} {
		c, log := sshClient(fr, tc.tr, "")
		_, err := c.Exec(context.Background(), tc.cmd, true)
		var denied *rex.DeniedError
		if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "not POSIX") {
			t.Errorf("%s: err = %v, want a refusal naming the shell", name, err)
		}
		if len(log.all()) == 0 || log.all()[0].Refused == "" {
			t.Errorf("%s: refusal not logged: %s", name, log.text())
		}
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("runner called:\n%s", dumpCalls(fr))
	}
}

// TestSSHExecHostileArgs (1b): each hostile word stays one quoted word.
func TestSSHExecHostileArgs(t *testing.T) {
	hostile := []string{"'; rm -rf ~'", "$(id)", "a`id`", "a\nb", ""}
	tr := rex.SSHExec{Host: "remote-host", RexPath: "/opt/my rex/rex", ControlDir: runDir}
	c, _ := sshClient(rextest.NewFakeRunner(), tr, "")
	cmd := cmds.Split(sshSession, sshBlock, rex.SplitOpts{Dir: "right", Command: hostile})
	if cmd.Err != nil {
		t.Fatal(cmd.Err)
	}
	argv := c.Argv(cmd)
	words := remoteWords(t, argv)
	if words[0] != "/opt/my rex/rex" {
		t.Fatalf("rex path = %q", words[0])
	}
	if got := words[len(words)-len(hostile):]; !slices.Equal(got, hostile) {
		t.Fatalf("hostile words\n got %q\nwant %q", got, hostile)
	}
	for _, w := range argv[slices.Index(argv, "remote-host")+1:] {
		if !strings.HasPrefix(w, "'") || !strings.HasSuffix(w, "'") {
			t.Errorf("word %q is not quoted", w)
		}
	}
}

// TestSSHForwardArgv (1c).
func TestSSHForwardArgv(t *testing.T) {
	c, _ := sshClient(rextest.NewFakeRunner(), rex.SSHForward{Host: "remote-host", Socket: fwdSock(1)}, "")
	c.Timeout = 5 * time.Second
	want := split("rex --autostart=false --timeout=5s -S unix:///run/user/1000/lazyrex/fwd-42-1.sock ls --json")
	if got := c.Argv(cmds.Ls()); !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
	want = append(split("rex --autostart=false --timeout=5s -S unix:///run/user/1000/lazyrex/fwd-42-1.sock"), cmds.Events().Args...)
	if got := c.Argv(cmds.Events()); !slices.Equal(got, want) {
		t.Fatalf("events argv\n got %q\nwant %q", got, want)
	}
}

// TestTunnelArgv (1d): the exact tunnel argv, the host last after --.
func TestTunnelArgv(t *testing.T) {
	r := newLinkRig(t, remoteRunner("/bin/bash"), forward)
	r.online()
	tunnels := r.fr.CallsContaining("-N")
	want := []string{"ssh", "-N", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3", "-L", fwdSock(1) + ":" + remoteSock, "--", "remote-host"}
	if len(tunnels) != 1 || !slices.Equal(tunnels[0], want) {
		t.Fatalf("tunnels\n got %q\nwant %q", tunnels, want)
	}
}

// TestSSHProbeArgv (1e, decision 1): the probes use the probe options only,
// each remote command one argv element; whoami uses the exec options.
func TestSSHProbeArgv(t *testing.T) {
	r := newLinkRig(t, remoteRunner("/bin/bash"), forward)
	r.status()
	calls := r.fr.Calls()
	want := [][]string{
		joined(probePrefix, []string{`echo "$SHELL"`}),
		joined(probePrefix, []string{pathProbeCmd}),
		joined(execPrefix, []string{"'/usr/local/bin/rex'", "'--autostart=false'", "'--timeout=10s'", "'whoami'", "'--json'"}),
	}
	if len(calls) < 3 {
		t.Fatalf("calls:\n%s", dumpCalls(r.fr))
	}
	for i, w := range want {
		if !slices.Equal(calls[i], w) {
			t.Errorf("call %d\n got %q\nwant %q", i, calls[i], w)
		}
	}
}

// TestSSHServerStartRoute (1f): start always goes over exec, without
// --autostart=false; reads go through the tunnel.
func TestSSHServerStartRoute(t *testing.T) {
	r := newLinkRig(t, remoteRunner("/bin/bash"), forward)
	r.online()
	c := r.client()
	if _, err := c.Exec(r.ctx, cmds.ServerStart(), true); err != nil {
		t.Fatal(err)
	}
	want := joined(execPrefix, []string{"'/usr/local/bin/rex'", "'--timeout=10s'", "'server'", "'start'", "'--json'"})
	if n := countArgv(r.fr, want); n != 1 {
		t.Fatalf("start argv %q ran %d times; calls:\n%s", want, n, dumpCalls(r.fr))
	}
	if _, err := c.Exec(r.ctx, cmds.Ls(), false); err != nil {
		t.Fatal(err)
	}
	want = split("rex --autostart=false --timeout=10s -S unix://" + fwdSock(1) + " ls --json")
	if n := countArgv(r.fr, want); n != 1 {
		t.Fatalf("ls argv %q ran %d times; calls:\n%s", want, n, dumpCalls(r.fr))
	}
}

// TestCheckSSHHost (1g).
func TestCheckSSHHost(t *testing.T) {
	for _, bad := range []string{"-oProxyCommand=x", "-x", "a b", "a\tb", "a\n", "", "a\x1b[2J", "\xff", " remote-host", "remote-host ", strings.Repeat("h", 256)} {
		if rex.CheckSSHHost(bad) == nil {
			t.Errorf("CheckSSHHost(%q) accepted", bad)
		}
	}
	for _, good := range []string{"remote-host", "u@remote-host", "ssh://u@h:2222"} {
		if err := rex.CheckSSHHost(good); err != nil {
			t.Errorf("CheckSSHHost(%q) = %v", good, err)
		}
	}
}

const macosEndpoint = "unix:///Users/user/Library/Application%20Support/rex/server.sock"

// TestNormalizeEndpoint (1h): the path is percent-decoded, then checked.
func TestNormalizeEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"unix:///run/user/1000/rex/server.sock":          "/run/user/1000/rex/server.sock",
		"unix:///run/user/1000/rex%2Bx/server.sock":      "/run/user/1000/rex+x/server.sock",
		"unix:///run/user/1000/rex+x/server.sock":        "/run/user/1000/rex+x/server.sock",
		"unix:///ru%6e/x":                                "/run/x",
		"unix:///run%2Fuser/x":                           "/run/user/x",
		"unix:///a%3Fb%23c":                              "/a?b#c",
		"unix:///a$b":                                    "/a$b",
		"unix:///" + strings.Repeat("a", 103):            "/" + strings.Repeat("a", 103),
		"unix:///" + strings.Repeat("a", 101) + "%41%41": "/" + strings.Repeat("a", 101) + "AA",
		"unix:///" + strings.Repeat("%61", 103):          "/" + strings.Repeat("a", 103),
	} {
		if got, err := rex.NormalizeEndpoint(in); err != nil || got != want {
			t.Errorf("NormalizeEndpoint(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for _, tc := range []struct{ in, errPart string }{
		{macosEndpoint, "whitespace"},
		{"unix:///a%20b", "whitespace"},
		{"unix:///a%C", "malformed"},
		{"unix:///a%2", "malformed"},
		{"unix:///a%zz", "malformed"},
		{"unix:///a%", "malformed"},
		{"unix:///a%25C", "'%'"},
		{"unix:///a%2525", "'%'"},
		{"unix:///a%24%7BHOME%7D", "'${'"},
		{"unix:///a${HOME}", "'${'"},
		{"unix:///a%3Ab", "':'"},
		{"unix:///a%1Bb", "control"},
		{"unix:///a%00", "control"},
		{"unix:///a%FF", "UTF-8"},
		{"unix:///a?x", "query or fragment"},
		{"unix:///a#x", "query or fragment"},
		{"unix:///a?", "query or fragment"},
		{"unix://%2Fa", "not absolute"},
		{"unix:///" + strings.Repeat("a", 101) + "%41%41%41", "longer than 104"},
	} {
		if got, err := rex.NormalizeEndpoint(tc.in); err == nil || !strings.Contains(err.Error(), tc.errPart) {
			t.Errorf("NormalizeEndpoint(%q) = %q, %v, want an error with %q", tc.in, got, err, tc.errPart)
		}
	}
	for _, bad := range []string{"http://h:7297", "unix://rel/x", "unix:rel", "/abs", "unix:///a:b", "unix:///a b", "unix:///a\nb",
		"unix:///a\x1b", "UNIX:///x", "unix://", "unix:///", "unix:///\xff", "unix:///" + strings.Repeat("a", 104)} {
		if got, err := rex.NormalizeEndpoint(bad); err == nil {
			t.Errorf("NormalizeEndpoint(%q) = %q, want an error", bad, got)
		}
	}
	for _, huge := range []string{"unix:///" + strings.Repeat("%zz", 400), "unix:///a?" + strings.Repeat("\x1b[2J", 400)} {
		if _, err := rex.NormalizeEndpoint(huge); err == nil || len(err.Error()) >= 200 {
			t.Errorf("NormalizeEndpoint(%d bytes) = %v, want a short error", len(huge), err)
		}
	}
}

// TestCheckRexPath (1i).
func TestCheckRexPath(t *testing.T) {
	for _, good := range []string{"/usr/local/bin/rex", "/opt/homebrew/bin/rex", "/Applications/Rex.app/Contents/Resources/bin/rex", "/opt/my rex/rex", betaRex} {
		if err := rex.CheckRexPath(good); err != nil {
			t.Errorf("CheckRexPath(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{"rex", "./rex", "/a\nb", "/a\x1b", "", "/" + strings.Repeat("a", 1024)} {
		if rex.CheckRexPath(bad) == nil {
			t.Errorf("CheckRexPath(%q) accepted", bad)
		}
	}
}

// TestPathProbeWords: after `command -v rex ||`, the probe is ls of the four
// known paths, each one word, the Rex Beta one with its spaces.
func TestPathProbeWords(t *testing.T) {
	rest, ok := strings.CutPrefix(pathProbeCmd, "command -v rex || ")
	if !ok {
		t.Fatalf("probe = %q", pathProbeCmd)
	}
	words, err := shellSplit(rest)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"env", "QUOTING_STYLE=literal", "ls", "/usr/local/bin/rex", "/opt/homebrew/bin/rex",
		"/Applications/Rex.app/Contents/Resources/bin/rex", betaRex}
	if !slices.Equal(words, want) {
		t.Fatalf("words\n got %q\nwant %q", words, want)
	}
}

// TestConfigSSHTargets (1j): SSH targets keep file order; unsafe or
// malformed entries refuse the whole config.
func TestConfigSSHTargets(t *testing.T) {
	ts, err := rex.ConfigTargets([]rex.TargetSpec{
		{Name: "studio", Kind: "native", Server: "studio"},
		{Name: "edge", Kind: "ssh", SSHHost: "remote-host", Mode: "exec", RexPath: "/opt/rex"},
		{Name: "lab", Kind: "ssh", SSHHost: "u@lab"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowNames(ts); !slices.Equal(got, []string{"local", "studio", "edge", "lab"}) {
		t.Fatalf("names = %q", got)
	}
	if edge := ts[2]; edge.Kind != rex.TargetSSH || edge.SSH != (rex.SSHSpec{Host: "remote-host", Mode: rex.SSHModeExec, RexPath: "/opt/rex"}) || edge.Source != rex.SourceConfig {
		t.Fatalf("edge = %+v", edge)
	}
	if lab := ts[3]; lab.SSH.Mode != "" || lab.Transport().Name() != "ssh" {
		t.Fatalf("lab = %+v", lab)
	}
	for name, specs := range map[string][]rex.TargetSpec{
		"ssh_host":         {{Name: "edge", Kind: "ssh", SSHHost: "-oProxyCommand=x"}},
		"ssh_host space":   {{Name: "edge", Kind: "ssh", SSHHost: "edge server"}},
		"ssh_host missing": {{Name: "edge", Kind: "ssh"}},
		"server":           {{Name: "edge", Kind: "ssh", SSHHost: "remote-host", Server: "x"}},
		"mode":             {{Name: "edge", Kind: "ssh", SSHHost: "remote-host", Mode: "tunnel"}},
		"rex_path":         {{Name: "edge", Kind: "ssh", SSHHost: "remote-host", RexPath: "rex"}},
		"ssh_host native":  {{Name: "edge", Kind: "native", Server: "edge", SSHHost: "remote-host"}},
		"duplicate":        {{Name: "edge", Kind: "native", Server: "edge"}, {Name: "EDGE", Kind: "ssh", SSHHost: "remote-host"}},
		"local":            {{Name: "Local", Kind: "ssh", SSHHost: "remote-host"}},
	} {
		if _, err := rex.ConfigTargets(specs); err == nil {
			t.Errorf("%s: accepted %+v", name, specs)
		}
	}
}

// TestWithSSHFlag (1l).
func TestWithSSHFlag(t *testing.T) {
	base, err := rex.ConfigTargets([]rex.TargetSpec{
		{Name: "edge", Kind: "ssh", SSHHost: "remote-host"},
		{Name: "studio", Kind: "native", Server: "studio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	all, i, err := rex.WithSSHFlag(base, "remote-host")
	if err != nil || all[i].Name != "edge" || len(all) != len(base) {
		t.Fatalf("exact match: %+v %d %v", all, i, err)
	}
	all, i, err = rex.WithSSHFlag(base, "REMOTE-HOST")
	if err != nil || len(all) != len(base)+1 || all[i].Name != "REMOTE-HOST" || all[i].SSH.Host != "REMOTE-HOST" || all[i].Source != rex.SourceFlag {
		t.Fatalf("case differs: %+v %d %v", all, i, err)
	}
	if _, _, err := rex.WithSSHFlag(base, "studio"); err == nil {
		t.Fatal("a taken name was accepted")
	}
	for _, bad := range []string{"-oProxyCommand=x", "a b", "local"} {
		if _, _, err := rex.WithSSHFlag(base, bad); err == nil {
			t.Errorf("--ssh %q accepted", bad)
		}
	}
}

// TestSSHStatus255 (4a): exit 255 of ssh is unreachable (ssh) with its first
// line; the log keeps the full stderr.
func TestSSHStatus255(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	stderr := rextest.FixtureString("stderr_ssh_255.txt")
	fr.On("'whoami'", "'--json'").Return("", stderr, 255)
	r := newLinkRig(t, fr, forward)
	st := r.status()
	if st.State != rex.StateSSHUnreachable || st.Detail != "ssh: connect to host remote-host port 22: Connection refused" {
		t.Fatalf("status = %v %q", st.State, st.Detail)
	}
	if !strings.Contains(r.log.text(), "no more authentication methods") {
		t.Fatalf("full stderr not logged:\n%s", r.log.text())
	}
	if n := len(fr.CallsContaining("-N")); n != 0 {
		t.Fatalf("tunnel started for an unreachable host")
	}
}

// TestSSHStatusStopped (4b): exit 4 over exec is a stopped server.
func TestSSHStatusStopped(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	fr.On("'whoami'", "'--json'").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	r := newLinkRig(t, fr, forward)
	if st := r.status(); st.State != rex.StateStopped {
		t.Fatalf("status = %v %q", st.State, st.Detail)
	}
	if n := len(fr.CallsContaining("-N")); n != 0 {
		t.Fatalf("tunnel started for a stopped server")
	}
}

// TestSSHProbeFailures (4c).
func TestSSHProbeFailures(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	fr.On(shellProbeCmd).Return("", "ssh: Could not resolve hostname remote-host\n", 255)
	st := newLinkRig(t, fr, forward).status()
	if st.State != rex.StateSSHUnreachable || !strings.Contains(st.Detail, "Could not resolve") {
		t.Fatalf("shell probe 255: %v %q", st.State, st.Detail)
	}

	fr = remoteRunner("/bin/bash")
	fr.On(pathProbeCmd).Return("", "ls: cannot access '/usr/local/bin/rex'\n", 2)
	st = newLinkRig(t, fr, forward).status()
	if st.State != rex.StateError || !strings.Contains(st.Detail, "rex_path") {
		t.Fatalf("no rex: %v %q", st.State, st.Detail)
	}
	if n := len(fr.CallsContaining("'whoami'", "'--json'")); n != 0 {
		t.Fatalf("whoami ran without a rex path")
	}

	fr = remoteRunner("/bin/bash")
	newLinkRig(t, fr, forward).status()
	if n := len(fr.CallsContaining("'/usr/local/bin/rex'", "'--autostart=false'")); n == 0 {
		t.Fatalf("parsed path unused; calls:\n%s", dumpCalls(fr))
	}
}

// TestMapExitSSHRoutes (4d): 255 is ssh's over exec only; a mutation's
// toast is the first stderr line.
func TestMapExitSSHRoutes(t *testing.T) {
	if k := rex.MapExit(255, rex.SSHExec{Host: "remote-host"}, nil); k != rex.ExitSSH {
		t.Errorf("exec 255 = %v", k)
	}
	if k := rex.MapExit(255, rex.SSHForward{Host: "remote-host"}, nil); k != rex.ExitFailed {
		t.Errorf("forward 255 = %v", k)
	}
	fr := rextest.NewFakeRunner()
	fr.On("'kill'").Return("", rextest.FixtureString("stderr_ssh_255.txt"), 255)
	c, _ := sshClient(fr, rex.SSHExec{Host: "remote-host", RexPath: "/usr/local/bin/rex", ControlDir: runDir}, "")
	out, err := c.Exec(context.Background(), cmds.Kill(sshSession), true)
	if err != nil || out.Kind != rex.ExitSSH || out.Toast != "ssh: connect to host remote-host port 22: Connection refused" {
		t.Fatalf("kill over ssh: %+v %v", out, err)
	}
}

// TestTunnelRestartBackoff (7a).
func TestTunnelRestartBackoff(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	fr.OnStream("-N").Exit(255, "ssh: connect to host remote-host port 22: Connection refused\n")
	fr.OnStream("-N").Exit(255, "ssh: connect to host remote-host port 22: Connection refused\n")
	third := make(chan struct{})
	fr.OnStream("-N").Until(third).Exit(255, "client_loop: send disconnect: Broken pipe\n")
	sockets := &rextest.FakeSockets{Ready: func(n int) bool { return n >= 3 }}
	r := newLinkRig(t, fr, forward, withSockets(sockets))

	if st := r.status(); st.State != rex.StateUnknown {
		t.Fatalf("status = %v", st.State)
	}
	r.waitTunnel(false)
	r.clk.BlockUntilWaiters(1)
	st := r.status()
	if st.State != rex.StateSSHUnreachable || st.Detail != "ssh tunnel: ssh: connect to host remote-host port 22: Connection refused (retry in 1s)" {
		t.Fatalf("after exit 1: %v %q", st.State, st.Detail)
	}
	r.clk.Advance(time.Second)
	r.waitTunnel(false)
	r.clk.BlockUntilWaiters(2)
	r.clk.Advance(2 * time.Second)
	r.waitTunnel(true)
	if st := r.status(); st.State != rex.StateOnline {
		t.Fatalf("after start 3: %v %q", st.State, st.Detail)
	}
	if got := sockets.Prepared(); !slices.Equal(got, []string{fwdSock(1), fwdSock(1), fwdSock(1)}) {
		t.Fatalf("prepared = %q", got)
	}
	if got := sockets.Cleaned(); len(got) != 2 {
		t.Fatalf("cleaned = %q", got)
	}
	close(third)
	r.waitTunnel(false)
	r.clk.BlockUntilWaiters(3)
	if got := r.clk.Waits(); !slices.Equal(got, []time.Duration{time.Second, 2 * time.Second, time.Second}) {
		t.Fatalf("waits = %v", got)
	}
	waitFor(t, "third cleanup", func() bool { return len(sockets.Cleaned()) == 3 })
}

// TestTunnelKilledOnCancel (7b).
func TestTunnelKilledOnCancel(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	tunnel := fr.OnStream("-N").Hold()
	sockets := &rextest.FakeSockets{}
	r := newLinkRig(t, fr, forward, withSockets(sockets))
	r.online()
	r.cancel()
	select {
	case <-tunnel.Ended():
	case <-time.After(rextest.Timeout):
		t.Fatal("tunnel stream still running")
	}
	r.group.Stop()
	if !r.group.Wait(rextest.Timeout) {
		t.Fatal("tunnel goroutine still running")
	}
	if got := sockets.Cleaned(); !slices.Equal(got, []string{fwdSock(1)}) {
		t.Fatalf("cleaned = %q", got)
	}
	if n := len(fr.CallsContaining("-N")); n != 1 {
		t.Fatalf("%d tunnels started", n)
	}
	r.noTunnelChange()
}

func remoteWhoami(endpoint string) string {
	return strings.Replace(rextest.FixtureString("whoami_remote.json"), "unix://"+remoteSock, endpoint, 1)
}

// TestStaleLoopIgnored (7d): an old loop finishing late changes nothing.
func TestStaleLoopIgnored(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	gate := make(chan struct{})
	sockets := &rextest.FakeSockets{}
	sockets.GateCleanup(t, fwdSock(1), gate)
	r := newLinkRig(t, fr, forward, withSockets(sockets))
	r.online()

	fr.On("whoami", "--json").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	fr.On("'whoami'", "'--json'").Return(remoteWhoami("unix:///run/user/1001/rex2/server.sock"), "", 0)
	if st := r.status(); st.State != rex.StateUnreachable {
		t.Fatalf("status = %v %q", st.State, st.Detail)
	}
	r.waitTunnel(true)
	gen := r.link.RouteGen()
	close(gate)
	waitFor(t, "loop 1 cleanup", func() bool { return slices.Contains(sockets.Cleaned(), fwdSock(1)) })
	time.Sleep(20 * time.Millisecond)
	if g := r.link.RouteGen(); g != gen {
		t.Fatalf("route generation %d -> %d after the old loop ended", gen, g)
	}
	r.noTunnelChange()
	if d := r.link.Describe(); !strings.Contains(d, "tunnel up") {
		t.Fatalf("describe = %q", d)
	}
	route, _, err := r.link.RouteFor(cmds.Ls())
	if fwd, ok := route.(rex.SSHForward); err != nil || !ok || fwd.Socket != fwdSock(2) {
		t.Fatalf("route = %#v, %v", route, err)
	}
	tunnels := fr.CallsContaining("-N")
	if len(tunnels) != 2 || !slices.Contains(tunnels[1], fwdSock(2)+":/run/user/1001/rex2/server.sock") {
		t.Fatalf("tunnels = %q", tunnels)
	}
}

// TestSwitchAndCloseRace (7e): a status racing quit starts no tunnel.
func TestSwitchAndCloseRace(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	entered, release := make(chan struct{}), make(chan struct{})
	holds := rextest.NewHolds(t)
	var once sync.Once
	fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, "'whoami'") {
			once.Do(func() {
				close(entered)
				holds.Wait(release, "the release of the held whoami")
			})
		}
	}
	r := newLinkRig(t, fr, forward)
	done := make(chan rex.TargetStatus, 1)
	go func() { done <- r.status() }()
	rextest.Recv(t, entered, "the status read to reach whoami")
	r.group.Stop()
	close(release)
	if st := rextest.Recv(t, done, "the status racing Stop to return"); st.State != rex.StateUnknown {
		t.Fatalf("status = %v %q", st.State, st.Detail)
	}
	r.status()
	if !r.group.Wait(time.Second) {
		t.Fatal("Wait did not return")
	}
	if n := len(fr.CallsContaining("-N")); n != 0 {
		t.Fatalf("a tunnel started after Stop:\n%s", dumpCalls(fr))
	}
}

// TestTunnelGroup (7f).
func TestTunnelGroup(t *testing.T) {
	var g rex.TunnelGroup
	hold := make(chan struct{})
	holds := rextest.NewHolds(t)
	if !g.Go(func() { holds.Wait(hold, "the close of hold") }) {
		t.Fatal("Go refused before Stop")
	}
	g.Stop()
	if g.Go(func() {}) {
		t.Fatal("Go ran after Stop")
	}
	if g.Wait(10 * time.Millisecond) {
		t.Fatal("Wait returned with a goroutine running")
	}
	close(hold)
	if !g.Wait(rextest.Timeout) {
		t.Fatal("Wait did not return")
	}
}

// TestPreparedRouteChanged (P2): a tunnel flap between confirmation and run
// refuses the confirmed command.
func TestPreparedRouteChanged(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	drop := make(chan struct{})
	fr.OnStream("-N").Until(drop).Exit(255, "broken pipe\n")
	r := newLinkRig(t, fr, forward)
	r.online()
	c := r.client()
	p, err := c.Prepare(cmds.Kill(sshSession))
	if err != nil || p.Decision.Verdict != rex.Confirm {
		t.Fatalf("prepare: %+v %v", p, err)
	}
	close(drop)
	r.waitTunnel(false)
	r.clk.BlockUntilWaiters(1)
	r.clk.Advance(time.Second)
	r.waitTunnel(true)
	_, err = c.RunPrepared(r.ctx, p, true, nil)
	var denied *rex.DeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "route to remote-host changed") {
		t.Fatalf("err = %v", err)
	}
	if n := len(fr.CallsContaining("kill")); n != 0 {
		t.Fatalf("kill ran:\n%s", dumpCalls(fr))
	}
	if !strings.Contains(r.log.text(), "route to remote-host changed") {
		t.Fatalf("refusal not logged:\n%s", r.log.text())
	}
}

// TestPreparedOwnerAndPolicyRecheck (P3).
func TestPreparedOwnerAndPolicyRecheck(t *testing.T) {
	fr := rextest.NewFakeRunner()
	fr.On().Return("{}", "", 0)
	c, log := sshClient(fr, rex.Local{}, string(self))
	other := c.With(rex.Native{Server: "studio"})

	p, err := c.Prepare(cmds.Kill(sshSession))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.RunPrepared(context.Background(), p, true, nil); err == nil || !strings.Contains(err.Error(), "target changed") {
		t.Errorf("other client: %v", err)
	}

	p, err = c.Prepare(cmds.Do("mystery", "b=shell"))
	if err != nil {
		t.Fatal(err)
	}
	c.Policy.Labels.Set("shell")
	if _, err := c.RunPrepared(context.Background(), p, true, nil); err == nil || !strings.Contains(err.Error(), string(self)) {
		t.Errorf("label set after prepare: %v", err)
	}
	c.Policy.Labels.Set("")

	p, err = c.Prepare(cmds.Kill(sshSession))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunPrepared(context.Background(), p.Touching(self), true, nil); err == nil {
		t.Error("fresh blocks holding the own block: ran")
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("runner called:\n%s", dumpCalls(fr))
	}
	if n := strings.Count(log.text(), "refused=\"\""); n != 0 {
		t.Fatalf("a refusal was not logged:\n%s", log.text())
	}
}

// TestSSHSelfBlockRefused (8a): input to the own block is refused on both
// routes of a link, before any runner call.
func TestSSHSelfBlockRefused(t *testing.T) {
	for name, spec := range map[string]rex.SSHSpec{"exec": execOnly, "forward": forward} {
		t.Run(name, func(t *testing.T) {
			fr := remoteRunner("/bin/bash")
			r := newLinkRig(t, fr, spec, withSelfBlock(string(self)))
			if name == "forward" {
				r.online()
			} else if st := r.status(); st.State != rex.StateOnline {
				t.Fatalf("status = %v %q", st.State, st.Detail)
			}
			fr.Reset()
			c := r.client()
			for _, cmd := range []rex.Cmd{cmds.Send(sshSession, self, "ls"), cmds.SendKey(sshSession, self, "enter"),
				cmds.SendKey(sshSession, self, "ctrl+c"), cmds.BlockClose(sshSession, self)} {
				if _, err := c.Exec(r.ctx, cmd, true); err == nil {
					t.Errorf("%q ran on the own block", cmd.Args)
				}
			}
			if n := len(fr.Calls()); n != 0 {
				t.Fatalf("runner called:\n%s", dumpCalls(fr))
			}
		})
	}
}

// TestSSHRedaction (8b): payloads are redacted in the display and the log
// of both routes; the argv keeps them.
func TestSSHRedaction(t *testing.T) {
	const secret = "hunter2-secret"
	for name, spec := range map[string]rex.SSHSpec{"exec": execOnly, "forward": forward} {
		t.Run(name, func(t *testing.T) {
			fr := remoteRunner("/bin/bash")
			r := newLinkRig(t, fr, spec)
			if name == "forward" {
				r.online()
			} else {
				r.status()
			}
			c := r.client()
			cmd := cmds.Send(sshSession, sshBlock, secret)
			if d := c.Display(cmd); strings.Contains(d, secret) || !strings.Contains(d, "<redacted") {
				t.Fatalf("display %q", d)
			}
			if _, err := c.Exec(r.ctx, cmd, true); err != nil {
				t.Fatal(err)
			}
			last := fr.Calls()[len(fr.Calls())-1]
			want := secret
			if name == "exec" {
				want = rex.ShellQuote(secret)
			}
			if !slices.Contains(last, want) {
				t.Fatalf("argv %q lost the payload", last)
			}
			if lt := r.log.text(); strings.Contains(lt, secret) || !strings.Contains(lt, "<redacted") {
				t.Fatalf("log:\n%s", lt)
			}
		})
	}
	c, log := sshClient(remoteRunner("/bin/bash"), rex.SSHForward{Host: "remote-host", Socket: fwdSock(1)}, "")
	if _, err := c.Exec(context.Background(), cmds.Send(sshSession, sshBlock, secret), true); err != nil {
		t.Fatal(err)
	}
	if lt := log.text(); strings.Contains(lt, secret) {
		t.Fatalf("SSHForward log:\n%s", lt)
	}
}

// TestSSHNonPOSIXShell (8c, decisions 2 and 3).
func TestSSHNonPOSIXShell(t *testing.T) {
	for _, shell := range []string{"/usr/bin/fish", "/bin/csh", "/bin/tcsh", "", "nu"} {
		t.Run("exec "+shell, func(t *testing.T) {
			fr := remoteRunner(shell)
			if shell == "nu" {
				fr.On(shellProbeCmd).Return("", "Error: nu::parser::unsupported_operation\n", 1)
			}
			r := newLinkRig(t, fr, execOnly)
			st := r.status()
			name := shell
			if shell == "" || shell == "nu" {
				name = "unknown"
			}
			if st.State != rex.StateError || !strings.Contains(st.Detail, "POSIX") || !strings.Contains(st.Detail, name) {
				t.Fatalf("status = %v %q", st.State, st.Detail)
			}
			if _, _, err := r.link.RouteFor(cmds.Ls()); err == nil {
				t.Fatal("a read was routed over exec under a non-POSIX shell")
			}
		})
	}
	for _, shell := range []string{"/usr/bin/fish", "nu"} {
		t.Run("forward "+shell, func(t *testing.T) {
			fr := remoteRunner(shell)
			if shell == "nu" {
				fr.On(shellProbeCmd).Return("", "Error: nu::parser::unsupported_operation\n", 1)
			}
			r := newLinkRig(t, fr, forward)
			r.online()
			c := r.client()
			if _, err := c.Exec(r.ctx, cmds.Send(sshSession, sshBlock, "echo it's"), true); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Exec(r.ctx, cmds.ServerStart(), true); err != nil {
				t.Fatal(err)
			}
			want := joined(execPrefix, []string{"/usr/local/bin/rex", "--timeout=10s", "server", "start", "--json"})
			if n := countArgv(fr, want); n != 1 {
				t.Fatalf("start %q ran %d times; calls:\n%s", want, n, dumpCalls(fr))
			}
			for _, call := range fr.Calls() {
				if slices.Contains(call, "'send'") {
					t.Fatalf("send went over exec: %q", call)
				}
			}
		})
	}
	t.Run("fish rex_path with a space", func(t *testing.T) {
		fr := remoteRunner("/usr/bin/fish")
		r := newLinkRig(t, fr, rex.SSHSpec{Host: "remote-host", RexPath: "/opt/my rex/rex"})
		st := r.status()
		if st.State != rex.StateError || !strings.Contains(st.Detail, "not POSIX") {
			t.Fatalf("status = %v %q", st.State, st.Detail)
		}
		for _, call := range fr.Calls() {
			if slices.ContainsFunc(call, func(w string) bool { return strings.Contains(w, "my rex") }) {
				t.Fatalf("ran %q", call)
			}
		}
	})
}

// TestSSHTunnelDownRefusesInput (8d).
func TestSSHTunnelDownRefusesInput(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	r := newLinkRig(t, fr, forward, withSockets(&rextest.FakeSockets{Ready: func(int) bool { return false }}))
	if st := r.status(); st.State != rex.StateUnknown {
		t.Fatalf("status = %v", st.State)
	}
	waitFor(t, "the tunnel to start", func() bool { return len(fr.CallsContaining("-N")) == 1 })
	fr.Reset()
	_, err := r.client().Exec(r.ctx, cmds.Send(sshSession, sshBlock, "ls"), true)
	var denied *rex.DeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Reason, "tunnel to remote-host is down") {
		t.Fatalf("err = %v", err)
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("runner called:\n%s", dumpCalls(fr))
	}
}

// TestSSHWithoutEnv (8f): no SSH environment, no ssh call.
func TestSSHWithoutEnv(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	base, _ := sshClient(fr, rex.Local{}, "")
	link := rex.NewSSHLink(context.Background(), rex.LinkOptions{Base: base, Name: "remote-host", Spec: forward})
	if st := link.Status(context.Background(), nil); st.State != rex.StateError || !strings.Contains(st.Detail, "SSH targets are not available") {
		t.Fatalf("status = %v %q", st.State, st.Detail)
	}
	if _, err := base.With(link).Exec(context.Background(), cmds.ServerStart(), true); err == nil {
		t.Fatal("start ran")
	}
	if st := rex.ProbeSSH(context.Background(), base, "remote-host", forward, nil); st.State != rex.StateError {
		t.Fatalf("probe = %v", st.State)
	}
	dirless := &rex.SSHEnv{Dir: func() (string, error) { return "", errors.New("$XDG_RUNTIME_DIR is not set") }}
	link = rex.NewSSHLink(context.Background(), rex.LinkOptions{Base: base, Name: "remote-host", Spec: forward, Env: dirless})
	if st := link.Status(context.Background(), nil); st.State != rex.StateError || !strings.Contains(st.Detail, "private runtime directory") {
		t.Fatalf("status = %v %q", st.State, st.Detail)
	}
	if n := len(fr.Calls()); n != 0 {
		t.Fatalf("runner called:\n%s", dumpCalls(fr))
	}
}

// TestSSHProbeOnceAndCoalesced (8g): concurrent probes of one target run the
// probe sequence once; another target name probes on its own.
func TestSSHProbeOnceAndCoalesced(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	entered, release := make(chan struct{}), make(chan struct{})
	holds := rextest.NewHolds(t)
	var once sync.Once
	fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, shellProbeCmd) {
			once.Do(func() {
				close(entered)
				holds.Wait(release, "the release of the held shell probe")
			})
		}
	}
	base, _ := sshClient(fr, rex.Local{}, "")
	env := rextest.SSHEnv(runDir, &rextest.FakeSockets{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st := rex.ProbeSSH(context.Background(), base, "remote-host", forward, env); st.State != rex.StateOnline {
				t.Errorf("probe = %v %q", st.State, st.Detail)
			}
		}()
	}
	rextest.Recv(t, entered, "the first shell probe")
	time.Sleep(10 * time.Millisecond)
	close(release)
	rextest.WaitGroup(t, &wg, "the two concurrent probes")
	count := func() (int, int) {
		return len(fr.CallsContaining(shellProbeCmd)), len(fr.CallsContaining(pathProbeCmd))
	}
	if s, p := count(); s != 1 || p != 1 {
		t.Fatalf("probes: shell %d, path %d", s, p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var group rex.TunnelGroup
	defer func() { cancel(); group.Stop(); group.Wait(rextest.Timeout) }()
	link := rex.NewSSHLink(ctx, rex.LinkOptions{Base: base, Name: "REMOTE-HOST", Spec: forward, Env: env, Clock: rextest.NewFakeClock(time.Unix(0, 0)), Group: &group})
	link.Status(ctx, nil)
	if s, p := count(); s != 1 || p != 1 {
		t.Fatalf("activation re-probed: shell %d, path %d", s, p)
	}
	rex.ProbeSSH(context.Background(), base, "other", forward, env)
	if s, p := count(); s != 2 || p != 2 {
		t.Fatalf("another name: shell %d, path %d", s, p)
	}
}

// TestSSHProbeFailureCached (decision 4): polls after a failed probe do not
// probe again; forgetting the probe does.
func TestSSHProbeFailureCached(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	fr.On(pathProbeCmd).Return("", "", 1)
	r := newLinkRig(t, fr, forward)
	for range 3 {
		if st := r.status(); st.State != rex.StateError || !strings.Contains(st.Detail, "rex_path") {
			t.Fatalf("status = %v %q", st.State, st.Detail)
		}
	}
	if s, p := len(fr.CallsContaining(shellProbeCmd)), len(fr.CallsContaining(pathProbeCmd)); s != 1 || p != 1 {
		t.Fatalf("probes: shell %d, path %d", s, p)
	}
	r.env.Probes.Forget(rex.ProbeKey("remote-host", forward))
	r.status()
	if s, p := len(fr.CallsContaining(shellProbeCmd)), len(fr.CallsContaining(pathProbeCmd)); s != 2 || p != 2 {
		t.Fatalf("after Forget: shell %d, path %d", s, p)
	}
}

// TestSSHStatusGeneration (decision 5): only a newer status read applies.
func TestSSHStatusGeneration(t *testing.T) {
	r := newLinkRig(t, remoteRunner("/bin/bash"), forward)
	g1, g2 := r.link.BeginStatus(), r.link.BeginStatus()
	if !r.link.ApplyStatus(g2) {
		t.Fatal("newest refused")
	}
	if r.link.ApplyStatus(g1) {
		t.Fatal("older applied after newer")
	}
	if g3 := r.link.BeginStatus(); !r.link.ApplyStatus(g3) {
		t.Fatal("next refused")
	}
}

// TestSSHFallbackToExec (8h): an endpoint that cannot be forwarded uses
// exec mode, logged once, with no tunnel.
func TestSSHFallbackToExec(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	fr.On("'whoami'", "'--json'").Return(rextest.FixtureString("whoami_remote_space.json"), "", 0)
	r := newLinkRig(t, fr, forward)
	for range 2 {
		if st := r.status(); st.State != rex.StateOnline {
			t.Fatalf("status = %v %q", st.State, st.Detail)
		}
	}
	if d := r.link.Describe(); d != "ssh remote-host, exec mode (endpoint cannot be forwarded)" {
		t.Fatalf("describe = %q", d)
	}
	notes := 0
	for _, e := range r.log.all() {
		if strings.Contains(e.Note, "cannot be forwarded: using exec mode") && strings.HasPrefix(e.Note, "remote-host: ") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("%d fallback notes:\n%s", notes, r.log.text())
	}
	if n := len(fr.CallsContaining("-N")); n != 0 {
		t.Fatal("tunnel started")
	}
	if route, _, err := r.link.RouteFor(cmds.Ls()); err != nil || route.Name() != "ssh" || route.Remote() {
		t.Fatalf("route = %#v %v", route, err)
	}
}

// TestSSHMacOSRexBeta: on a macOS server with only Rex Beta, the probe finds
// its CLI, and the percent-encoded endpoint decodes to a path with a space,
// so the target uses exec mode with that CLI, no tunnel.
func TestSSHMacOSRexBeta(t *testing.T) {
	fr := remoteRunner("/bin/zsh")
	fr.On(pathProbeCmd).Return(rextest.FixtureString("rex_path_probe_beta.txt"), "ls: /usr/local/bin/rex: No such file or directory\n", 1)
	fr.On("'whoami'", "'--json'").Return(rextest.FixtureString("whoami_remote_macos.json"), "", 0)
	r := newLinkRig(t, fr, forward)
	for range 2 {
		if st := r.status(); st.State != rex.StateOnline {
			t.Fatalf("status = %v %q; calls:\n%s", st.State, st.Detail, dumpCalls(fr))
		}
	}
	if d := r.link.Describe(); d != "ssh remote-host, exec mode (endpoint cannot be forwarded)" {
		t.Fatalf("describe = %q", d)
	}
	notes := 0
	for _, e := range r.log.all() {
		if strings.HasPrefix(e.Note, "remote-host: ") && strings.Contains(e.Note, "Application Support") && strings.Contains(e.Note, "whitespace") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("%d fallback notes:\n%s", notes, r.log.text())
	}
	if n := len(fr.CallsContaining("-N")); n != 0 {
		t.Fatalf("tunnel started:\n%s", dumpCalls(fr))
	}
	execCalls := fr.CallsContaining("'--autostart=false'")
	if len(execCalls) == 0 {
		t.Fatalf("no exec call:\n%s", dumpCalls(fr))
	}
	for _, c := range execCalls {
		if w := remoteWords(t, c); w[0] != betaRex {
			t.Fatalf("rex path %q in %q", w[0], c)
		}
	}
	status := fr.CallsContaining("'server'", "'status'")
	want := rex.Local{}.Argv(betaRex, []string{"--autostart=false", "--timeout=10s"}, []string{"server", "status", "--json"})
	if len(status) == 0 || !slices.Equal(remoteWords(t, status[0]), want) {
		t.Fatalf("server status calls %q, want remote words %q", status, want)
	}
}

// TestSSHForwardFailureStaysForward (8i, decision 9).
func TestSSHForwardFailureStaysForward(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	r := newLinkRig(t, fr, forward)
	r.online()
	fr.On("whoami", "--json").Return("", "Error: dial unix "+fwdSock(1)+": connection refused\n", 4)
	st := r.status()
	if st.State != rex.StateUnreachable || st.Detail != "Error: dial unix "+fwdSock(1)+": connection refused" {
		t.Fatalf("status = %v %q", st.State, st.Detail)
	}
	if d := r.link.Describe(); d != "ssh remote-host, forward mode, tunnel up" {
		t.Fatalf("describe = %q", d)
	}
	if strings.Contains(r.log.text(), "cannot be forwarded") {
		t.Fatalf("fell back:\n%s", r.log.text())
	}
	if n := len(fr.CallsContaining("-N")); n != 1 {
		t.Fatalf("%d tunnels", n)
	}
}

// TestSSHInactiveProbe (8j): probing an inactive row runs the probes, whoami
// and server status over exec, and no tunnel.
func TestSSHInactiveProbe(t *testing.T) {
	fr := remoteRunner("/bin/bash")
	base, _ := sshClient(fr, rex.Local{}, "")
	st := rex.ProbeSSH(context.Background(), base, "remote-host", forward, rextest.SSHEnv(runDir, &rextest.FakeSockets{}))
	if st.State != rex.StateOnline || st.Whoami == nil || st.Server == nil {
		t.Fatalf("probe = %+v", st)
	}
	var got []string
	for _, c := range fr.Calls() {
		got = append(got, c[len(c)-1])
	}
	want := []string{shellProbeCmd, pathProbeCmd, "'--json'", "'--json'"}
	if !slices.Equal(got, want) || len(fr.CallsContaining("'server'", "'status'")) != 1 {
		t.Fatalf("calls:\n%s", dumpCalls(fr))
	}
}

// TestSSHStreamRemote (4e): an event stream ending with exit 4 is a stopped
// server over exec, and ambiguous through the tunnel.
func TestSSHStreamRemote(t *testing.T) {
	for name, spec := range map[string]rex.SSHSpec{"exec": execOnly, "forward": forward} {
		t.Run(name, func(t *testing.T) {
			fr := remoteRunner("/bin/bash")
			r := newLinkRig(t, fr, spec)
			if name == "forward" {
				r.online()
				fr.OnStream("events", "--all", "--json").Exit(4, "Error: no Rex server is running\n")
			} else {
				r.status()
				fr.OnStream("'events'", "'--all'", "'--json'").Exit(4, "Error: no Rex server is running\n")
			}
			states := make(chan rex.StreamState, 8)
			sup := &rex.Supervisor{Client: r.client(), Clock: r.clk, Min: time.Second, Max: time.Second,
				OnEvent: func(rex.Event) {}, OnBadLine: func([]byte, error) {}, OnState: func(s rex.StreamState) { states <- s }}
			ctx, cancel := context.WithCancel(r.ctx)
			defer cancel()
			go sup.Run(ctx)
			s := rextest.Recv(t, states, "the first stream state")
			if s.Up || s.Kind != rex.ExitUnreachable || s.Remote != (name == "forward") {
				t.Fatalf("state = %+v", s)
			}
		})
	}
}

// TestSSHShellProbeExit1Forward (decision 2): a shell probe failing with an
// exit other than 255 still lets forward mode come online.
func TestSSHShellProbeExit1Forward(t *testing.T) {
	fr := remoteRunner("")
	fr.On(shellProbeCmd).Return("", "Error: nu::parser::unsupported_operation\n", 1)
	r := newLinkRig(t, fr, forward)
	r.online()
	if d := r.link.Describe(); d != "ssh remote-host, forward mode, tunnel up" {
		t.Fatalf("describe = %q", d)
	}
}
