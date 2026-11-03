package gui

import (
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

const (
	shellProbeArg = `echo "$SHELL"`
	pathProbeArg  = `command -v rex || env QUOTING_STYLE=literal ls /usr/local/bin/rex /opt/homebrew/bin/rex /Applications/Rex.app/Contents/Resources/bin/rex '/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex'`
	sshRunDir     = "/run/user/1000/lazyrex"
	sshConfig     = "targets:\n  - name: remote-host\n    kind: ssh\n    ssh_host: remote-host\n"
	sshExecConfig = sshConfig + "    mode: exec\n"
	// sshStableSock is the stable socket of target remote-host, ssh_host
	// remote-host.
	sshStableSock = sshRunDir + "/gui-remote-host_996d7034ffff31a3.sock"
)

var sshExecPrefix = []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ControlMaster=auto",
	"-o", "ControlPath=" + sshRunDir + "/ssh-%C", "-o", "ControlPersist=60s", "--", "remote-host"}

func quoted(words ...string) []string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = rex.ShellQuote(w)
	}
	return out
}

// pumpUI runs Go inline and queues Update, which the test applies with pump:
// tunnel goroutines then never touch the state.
type pumpUI struct {
	mu    sync.Mutex
	queue []func()
}

func (u *pumpUI) Update(f func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.queue = append(u.queue, f)
}

func (u *pumpUI) Go(f func()) { f() }

func (u *pumpUI) pump() {
	for {
		u.mu.Lock()
		q := u.queue
		u.queue = nil
		u.mu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, f := range q {
			f()
		}
	}
}

// sshRunner is fixtureRunner plus an SSH target whose login shell is shell:
// probes, and the reads and mutations over exec.
func sshRunner(shell string) *rextest.FakeRunner {
	fr := fixtureRunner()
	fr.On(shellProbeArg).Return(shell+"\n", "", 0)
	fr.On(pathProbeArg).Return(rextest.FixtureString("rex_path_probe.txt"), "", 2)
	remote := rextest.FixtureString("whoami_remote.json")
	fr.On("whoami", "--json").Return(remote, "", 0)
	for _, r := range []struct {
		args    []string
		fixture string
	}{
		{[]string{"whoami", "--json"}, "whoami_remote.json"},
		{[]string{"server", "status", "--json"}, "server_status.json"},
		{[]string{"ls", "--json"}, "ls.json"},
		{[]string{"client", "ls", "--all", "--json"}, "client_ls_all.json"},
		{[]string{"session", "inspect", "session:01aaaa"}, "session_inspect.json"},
		{[]string{"session", "inspect", "session:02bbbb"}, "session_inspect_logs.json"},
		{[]string{"block", "ls", "-s", "session:01aaaa"}, "block_ls.json"},
		{[]string{"block", "ls", "-s", "session:02bbbb"}, "block_ls_logs.json"},
	} {
		fr.On(quoted(r.args...)...).Return(rextest.FixtureString(r.fixture), "", 0)
	}
	for _, mut := range [][]string{{"server", "start", "--json"}, {"session", "rename"}, {"block", "close"}} {
		fr.On(quoted(mut...)...).Return("{}", "", 0)
	}
	return fr
}

type sshHarness struct {
	*harness
	ui      *pumpUI
	sockets *rextest.FakeSockets
	env     *rex.SSHEnv
}

func newSSHHarness(t *testing.T, fr *rextest.FakeRunner, sockets *rextest.FakeSockets, opts ...harnessOpt) *sshHarness {
	t.Helper()
	if sockets == nil {
		sockets = &rextest.FakeSockets{}
	}
	ui := &pumpUI{}
	env := rextest.SSHEnv(sshRunDir, sockets)
	h := newHarness(t, append([]harnessOpt{withRunner(fr), withUI(ui), withSSH(env)}, opts...)...)
	s := &sshHarness{harness: h, ui: ui, sockets: sockets, env: env}
	ui.pump()
	return s
}

func (s *sshHarness) pumpUntil(what string, cond func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(rextest.Timeout)
	for {
		s.ui.pump()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %s; status %v %q; calls:\n%s", what, s.st.Status.State, s.st.Status.Detail, s.dumpCalls())
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *sshHarness) waitState(want rex.TargetState) {
	s.t.Helper()
	s.pumpUntil("status "+want.String(), func() bool { return s.st.Status.State == want })
}

func (s *sshHarness) do(p Panel, action string) {
	s.t.Helper()
	s.dispatch(p, action)
	s.ui.pump()
}

func (s *sshHarness) tunnels() [][]string { return s.fr.CallsContaining("-N") }

// TestSSHArgvInvariants (1n): over a whole forward session every ssh argv
// has BatchMode=yes and -- right before the host, and every rex argv the
// global flags, except start.
func TestSSHArgvInvariants(t *testing.T) {
	fr := sshRunner("/bin/bash")
	s := newSSHHarness(t, fr, nil, withConfig(sshConfig), withInitial("remote-host"))
	s.waitState(rex.StateOnline)
	s.selectTarget("remote-host")
	s.do(PanelTargets, "startServer")
	if p := s.top(); p.Kind != PopupConfirm {
		t.Fatalf("popup = %+v", p)
	}
	s.ctrl.Popups.Accept("")
	s.ui.pump()

	calls := fr.Calls()
	seen := map[string]bool{}
	for _, c := range calls {
		switch {
		case c[0] == "ssh" && slices.Contains(c, shellProbeArg):
			seen["shell probe"] = true
		case c[0] == "ssh" && slices.Contains(c, pathProbeArg):
			seen["path probe"] = true
		case c[0] == "ssh" && slices.Contains(c, "-N"):
			seen["tunnel"] = true
		case c[0] == "ssh" && slices.Contains(c, "'whoami'"):
			seen["exec whoami"] = true
		case c[0] == "ssh" && slices.Contains(c, "'start'"):
			seen["start"] = true
		case c[0] == "rex" && slices.Contains(c, "unix://"+sshStableSock):
			seen["forward rex"] = true
		}
		if c[0] == "ssh" {
			i := slices.Index(c, "--")
			if !rextest.Contains(c, "-o", "BatchMode=yes") || i < 0 || i+1 >= len(c) || c[i+1] != "remote-host" {
				t.Errorf("ssh argv %q", c)
			}
		}
		if c[0] == "rex" && slices.Contains(c, "-S") {
			if !slices.Contains(c, "--autostart=false") || !slices.ContainsFunc(c, func(w string) bool { return strings.HasPrefix(w, "--timeout=") }) {
				t.Errorf("forward argv %q", c)
			}
		}
		if c[0] == "ssh" && slices.Contains(c, "'start'") && slices.Contains(c, "'--autostart=false'") {
			t.Errorf("start with --autostart=false: %q", c)
		}
	}
	for _, k := range []string{"shell probe", "path probe", "exec whoami", "tunnel", "forward rex", "start"} {
		if !seen[k] {
			t.Errorf("no %s; calls:\n%s", k, s.dumpCalls())
		}
	}
}

// TestSSHStatus255GUI (4a): unreachable (ssh) in the row and the Status tab,
// with the ssh hint; the log has the full stderr.
func TestSSHStatus255GUI(t *testing.T) {
	fr := sshRunner("/bin/bash")
	fr.On(quoted("whoami", "--json")...).Return("", rextest.FixtureString("stderr_ssh_255.txt"), 255)
	s := newSSHHarness(t, fr, nil, withConfig(sshConfig), withInitial("remote-host"))
	s.waitState(rex.StateSSHUnreachable)
	s.selectTarget("remote-host")
	if rows := s.rows(PanelTargets); !slices.ContainsFunc(rows, func(r string) bool { return strings.Contains(r, "unreachable (ssh)") }) {
		t.Fatalf("rows = %q", rows)
	}
	st := RenderStatus(s.st)
	if !containsAll(st, "ssh: connect to host remote-host port 22: Connection refused", `Run "ssh remote-host" in a terminal`, "BatchMode=yes never prompts") {
		t.Fatalf("status tab:\n%s", st)
	}
	if !strings.Contains(s.logText(), "debug1: no more authentication methods") {
		t.Fatalf("log:\n%s", s.logText())
	}
}

// TestSSHMutation255 (4d): a mutation failing in ssh toasts its first line
// and re-checks the target.
func TestSSHMutation255(t *testing.T) {
	fr := sshRunner("/bin/bash")
	s := newSSHHarness(t, fr, nil, withConfig(sshExecConfig), withInitial("remote-host"))
	s.waitState(rex.StateOnline)
	fr.On(quoted("session", "rename")...).Return("", rextest.FixtureString("stderr_ssh_255.txt"), 255)
	before := len(fr.CallsContaining(quoted("whoami", "--json")...))
	s.ctrl.run("", cmds.SessionRename("session:01aaaa", "x"), nil)
	s.ui.pump()
	if toast := s.ctrl.Popups.ToastText(); toast != "ssh: connect to host remote-host port 22: Connection refused" {
		t.Fatalf("toast %q", toast)
	}
	if n := len(fr.CallsContaining(quoted("whoami", "--json")...)); n != before+1 {
		t.Fatalf("whoami %d -> %d", before, n)
	}
}

// TestSSHStartConfirm (4b, 8e): a stopped SSH server offers s, which shows
// the exact ssh argv and runs it once accepted, then re-reads the target.
func TestSSHStartConfirm(t *testing.T) {
	fr := sshRunner("/bin/bash")
	fr.On(quoted("whoami", "--json")...).Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4).Once()
	s := newSSHHarness(t, fr, nil, withConfig(sshConfig), withInitial("remote-host"))
	s.waitState(rex.StateStopped)
	s.selectTarget("remote-host")
	if !s.ctrl.Available(PanelTargets, "startServer") {
		t.Fatal("s not offered")
	}
	if st := RenderStatus(s.st); !strings.Contains(st, "s starts it over ssh") {
		t.Fatalf("status tab:\n%s", st)
	}
	s.do(PanelTargets, "startServer")
	want := append(slices.Clone(sshExecPrefix), quoted("/usr/local/bin/rex", "--timeout=10s", "server", "start", "--json")...)
	p := s.top()
	if p.Kind != PopupConfirm || p.Body != strings.Join(want, " ") {
		t.Fatalf("popup %v %q\nwant %q", p.Kind, p.Body, strings.Join(want, " "))
	}
	if n := s.countArgv(want); n != 0 {
		t.Fatalf("start ran before confirmation")
	}
	s.ctrl.Popups.Accept("")
	s.ui.pump()
	if n := s.countArgv(want); n != 1 {
		t.Fatalf("start ran %d times; calls:\n%s", n, s.dumpCalls())
	}
	s.waitState(rex.StateOnline)
}

// TestSSHExecStreamExit4 (4e): exit 4 of the exec event stream is a stopped
// server; through the tunnel it re-checks.
func TestSSHExecStreamExit4(t *testing.T) {
	fr := sshRunner("/bin/bash")
	s := newSSHHarness(t, fr, nil, withConfig(sshExecConfig), withInitial("remote-host"), withStreams())
	s.waitState(rex.StateOnline)
	sup := s.streams[len(s.streams)-1].sup
	sup.OnState(rex.StreamState{Code: 4, Kind: rex.ExitUnreachable, Remote: false})
	s.waitState(rex.StateStopped)

	fr = sshRunner("/bin/bash")
	s = newSSHHarness(t, fr, nil, withConfig(sshConfig), withInitial("remote-host"), withStreams())
	s.waitState(rex.StateOnline)
	before := len(fr.CallsContaining("whoami", "--json"))
	s.streams[len(s.streams)-1].sup.OnState(rex.StreamState{Code: 4, Kind: rex.ExitUnreachable, Remote: true})
	s.ui.pump()
	if s.st.Status.State != rex.StateOnline || len(fr.CallsContaining("whoami", "--json")) != before+1 {
		t.Fatalf("forward exit 4: %v, whoami %d -> %d", s.st.Status.State, before, len(fr.CallsContaining("whoami", "--json")))
	}
}

// TestSSHTunnelRestartGUI (7c).
func TestSSHTunnelRestartGUI(t *testing.T) {
	fr := sshRunner("/bin/bash")
	drop := make(chan struct{})
	fr.OnStream("-N").Until(drop).Exit(255, "client_loop: send disconnect: Broken pipe\n")
	s := newSSHHarness(t, fr, nil, withConfig(sshConfig), withInitial("remote-host"))
	s.waitState(rex.StateOnline)
	if s.count("-S unix://"+sshStableSock+" ls --json") == 0 {
		t.Fatalf("ls not read through the tunnel:\n%s", s.dumpCalls())
	}
	fr.On(quoted("whoami", "--json")...).Return("", rextest.FixtureString("stderr_ssh_255.txt"), 255)
	close(drop)
	s.waitState(rex.StateSSHUnreachable)
	s.clk.BlockUntilWaiters(1)
	lsBefore := len(fr.CallsContaining("ls", "--json"))
	s.clk.Advance(time.Second)
	s.waitState(rex.StateOnline)
	s.pumpUntil("ls re-read", func() bool { return len(fr.CallsContaining("ls", "--json")) > lsBefore })
	if n := len(s.tunnels()); n != 2 {
		t.Fatalf("%d tunnels", n)
	}
}

// TestSSHSwitchKillsTunnel (7g, C12): switching away kills the tunnel; back
// on the target, the new tunnel takes the same stable socket once the old
// one is cleaned up.
func TestSSHSwitchKillsTunnel(t *testing.T) {
	fr := sshRunner("/bin/bash")
	first := fr.OnStream("-N").Hold()
	gate := make(chan struct{})
	sockets := &rextest.FakeSockets{}
	sockets.GateCleanup(t, sshStableSock, gate)
	s := newSSHHarness(t, fr, sockets, withConfig(sshConfig))
	s.activateTarget("remote-host")
	s.waitState(rex.StateOnline)
	s.activateTarget("local")
	select {
	case <-first.Ended():
	case <-time.After(rextest.Timeout):
		t.Fatal("tunnel still running after the switch")
	}
	s.activateTarget("remote-host")
	time.Sleep(20 * time.Millisecond)
	s.ui.pump()
	if n := len(s.tunnels()); n != 1 {
		t.Fatalf("%d tunnels while the old cleanup is held:\n%s", n, s.dumpCalls())
	}
	close(gate)
	s.waitState(rex.StateOnline)
	tunnels := s.tunnels()
	if len(tunnels) != 2 {
		t.Fatalf("tunnels = %q", tunnels)
	}
	for _, tn := range tunnels {
		if !slices.ContainsFunc(tn, func(w string) bool { return strings.HasPrefix(w, sshStableSock+":") }) {
			t.Fatalf("tunnel %q does not use the stable socket", tn)
		}
	}
}

// TestPreparedConfirmEqualsExecuted (P1): the confirmation shows the argv
// that runs.
func TestPreparedConfirmEqualsExecuted(t *testing.T) {
	fr := sshRunner("/bin/bash")
	fr.On("block", "close").Return("{}", "", 0)
	s := newSSHHarness(t, fr, nil, withConfig(sshConfig), withInitial("remote-host"))
	s.waitState(rex.StateOnline)
	s.focus(PanelBlocks)
	s.do(PanelBlocks, "closeBlock")
	p := s.top()
	if p.Kind != PopupConfirm {
		t.Fatalf("popup = %+v", p)
	}
	body := p.Body
	s.ctrl.Popups.Accept("")
	s.ui.pump()
	closes := fr.CallsContaining("block", "close")
	if len(closes) != 1 || strings.Join(closes[0], " ") != body {
		t.Fatalf("confirmed %q, ran %q", body, closes)
	}
	if !strings.Contains(body, "-S unix://"+sshStableSock) {
		t.Fatalf("body %q", body)
	}
}

// macOSBeta brings a macOS server with Rex Beta and its percent-encoded
// endpoint online through the tunnel, with lazyrex's home directory home.
func macOSBeta(t *testing.T, home string, opts ...harnessOpt) *sshHarness {
	t.Helper()
	const betaRex = "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"
	fr := sshRunner("/bin/zsh")
	fr.On(pathProbeArg).Return(rextest.FixtureString("rex_path_probe_beta.txt"), "ls: /usr/local/bin/rex: No such file or directory\n", 1)
	fr.On(quoted("whoami", "--json")...).Return(rextest.FixtureString("whoami_remote_macos.json"), "", 0)
	s := newSSHHarness(t, fr, nil, append([]harnessOpt{withConfig(sshConfig), withInitial("remote-host"), withRexEnv(home, nil)}, opts...)...)
	s.waitState(rex.StateOnline)
	tunnels := s.tunnels()
	if len(tunnels) != 1 || !slices.Contains(tunnels[0], sshStableSock+":/Users/user/Library/Application Support/rex/server.sock") {
		t.Fatalf("tunnels = %q", tunnels)
	}
	if s.count("-S unix://"+sshStableSock+" ls --json") == 0 {
		t.Fatalf("ls not read through the tunnel:\n%s", s.dumpCalls())
	}
	execCalls := fr.CallsContaining("'--autostart=false'")
	if len(execCalls) == 0 {
		t.Fatalf("no exec call:\n%s", s.dumpCalls())
	}
	for _, c := range execCalls {
		if !rextest.Contains(c, "remote-host", rex.ShellQuote(betaRex)) {
			t.Fatalf("exec call %q does not run the Rex Beta CLI", c)
		}
	}
	return s
}

// TestSSHMacOSFallbackGUI (C24, amended by H32 and K1): with the Rex app
// hosts file on, the Status tab gives the endpoint and the state of its Rex
// app host.
func TestSSHMacOSFallbackGUI(t *testing.T) {
	home := rextest.PrivateDir(t)
	s := macOSBeta(t, home, withRexHostsFile())
	st := RenderStatus(s.st)
	if !containsAll(st, "forward mode, tunnel up", sgrGreen+"gui endpoint  unix://"+sshStableSock+sgrReset+"\n",
		"\nRex app host remote-host-lazyrex in "+home+"/.config/rex/lazyrex-hosts.lua: not written yet\n") ||
		strings.Contains(st, "rex hosts add") {
		t.Fatalf("status tab:\n%s", st)
	}
}

// TestSSHMacOSFallbackGUIHint (C24 before H32, F17, F25): with the Rex app
// hosts file off, the Status tab gives the endpoint, the Rex app path and
// the command for the Rex app, and lazyrex never looks for Rex's config.
func TestSSHMacOSFallbackGUIHint(t *testing.T) {
	s := macOSBeta(t, rextest.PrivateDir(t))
	st := RenderStatus(s.st)
	if !containsAll(st, "forward mode, tunnel up", sgrGreen+"gui endpoint  unix://"+sshStableSock+sgrReset+"\n",
		"\nRex app: Settings > Remote Hosts > Add Remote Host, with the label remote-host and the endpoint unix://"+sshStableSock+".\n",
		"\nOr run once (lazyrex never runs it):\n",
		"\nrex hosts add remote-host unix://"+sshStableSock+"\n") ||
		strings.Contains(st, "Rex app host") {
		t.Fatalf("status tab:\n%s", st)
	}
	if n := s.rexReads.Load(); n != 0 {
		t.Fatalf("read lazyrex's environment %d times", n)
	}
}

// TestSSHStatusColorConfined (F26, F27): the color of the gui endpoint line
// stays on that line of the Status tab, and a search in the main view still
// finds it.
func TestSSHStatusColorConfined(t *testing.T) {
	for _, on := range []bool{false, true} {
		opts := []harnessOpt{withConfig(sshConfig), withInitial("remote-host"), withRexEnv(rextest.PrivateDir(t), nil)}
		if on {
			opts = append(opts, withRexHostsFile())
		}
		s := newSSHHarness(t, sshRunner("/bin/bash"), nil, opts...)
		s.waitState(rex.StateOnline)
		s.selectTarget("remote-host")
		for _, r := range s.rows(PanelTargets) {
			expectNoControls(t, "row", r)
		}
		expectNoControls(t, "log", s.logText())
		expectNoControls(t, "footer", Footer(s.st, s.ctrl.Bindings(PanelTargets)))
		expectNoControls(t, "subtitle", TargetsSubtitle(s.st))
		if got := statusColor(t, "status tab", s.st.Main.Content); got != sgrGreen {
			t.Fatalf("on %v: color %q:\n%q", on, got, s.st.Main.Content)
		}
		line := slices.IndexFunc(strings.Split(s.st.Main.Content, "\n"), func(l string) bool {
			return strings.HasPrefix(l, sgrGreen+"gui endpoint  unix://"+sshStableSock)
		})
		s.dispatch(PanelMain, "filter")
		s.answer("gui endpoint")
		if line < 0 || !slices.Equal(s.st.Main.Matches, []int{line}) {
			t.Fatalf("on %v: matches %v, want [%d]:\n%s", on, s.st.Main.Matches, line, s.st.Main.Content)
		}
	}
}

// TestSSHWithoutEnvGUI (8f).
func TestSSHWithoutEnvGUI(t *testing.T) {
	fr := sshRunner("/bin/bash")
	h := newHarness(t, withRunner(fr), withConfig(sshConfig), withInitial("remote-host"))
	if h.st.Status.State != rex.StateError || !strings.Contains(RenderStatus(h.st), "SSH targets are not available") {
		t.Fatalf("status %v:\n%s", h.st.Status.State, RenderStatus(h.st))
	}
	h.selectTarget("remote-host")
	h.dispatch(PanelTargets, "startServer")
	for _, c := range fr.Calls() {
		if c[0] == "ssh" {
			t.Fatalf("ran %q", c)
		}
	}
}

// TestSSHInactiveProbeGUI (8j): c on an inactive SSH row probes it over
// exec, without a tunnel.
func TestSSHInactiveProbeGUI(t *testing.T) {
	fr := sshRunner("/bin/bash")
	s := newSSHHarness(t, fr, nil, withConfig(sshConfig))
	fr.Reset()
	s.selectTarget("remote-host")
	s.do(PanelTargets, "checkTarget")
	r, _ := s.st.SelectedTarget()
	if r.Status.State != rex.StateOnline {
		t.Fatalf("row = %+v", r.Status)
	}
	var tails []string
	for _, c := range fr.Calls() {
		tails = append(tails, c[len(c)-1])
	}
	if want := []string{shellProbeArg, pathProbeArg, "'--json'", "'--json'"}; !slices.Equal(tails, want) || len(s.tunnels()) != 0 {
		t.Fatalf("calls:\n%s", s.dumpCalls())
	}
}

// TestSSHCheckReprobes (8g): c on the active SSH row probes again.
func TestSSHCheckReprobes(t *testing.T) {
	fr := sshRunner("/bin/bash")
	s := newSSHHarness(t, fr, nil, withConfig(sshConfig), withInitial("remote-host"))
	s.waitState(rex.StateOnline)
	s.ctrl.checkTarget()
	s.ui.pump()
	if n := len(fr.CallsContaining(shellProbeArg)); n != 1 {
		t.Fatalf("polls re-probed: %d", n)
	}
	s.selectTarget("remote-host")
	s.do(PanelTargets, "checkTarget")
	if n := len(fr.CallsContaining(shellProbeArg)); n != 2 {
		t.Fatalf("c did not re-probe: %d", n)
	}
}

// TestSSHStatusOrderGUI (decision 5): of two status checks, the second runs
// once the first lands, so the newer result is kept.
func TestSSHStatusOrderGUI(t *testing.T) {
	fr := sshRunner("/bin/bash")
	ui := &goUI{}
	env := rextest.SSHEnv(sshRunDir, &rextest.FakeSockets{})
	h := newHarness(t, withRunner(fr), withUI(ui), withSSH(env), withConfig(sshExecConfig), withInitial("remote-host"))
	ui.settle(t)
	if h.st.Status.State != rex.StateOnline {
		t.Fatalf("status %v %q", h.st.Status.State, h.st.Status.Detail)
	}
	fr.On(quoted("whoami", "--json")...).Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	fr.On(quoted("whoami", "--json")...).Return(rextest.FixtureString("whoami_remote.json"), "", 0).Once()
	var armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	holds := rextest.NewHolds(t)
	fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, "'whoami'") && armed.CompareAndSwap(true, false) {
			close(entered)
			holds.Wait(release, "the release of the first check's whoami")
		}
	}
	before := execWhoami(fr)
	armed.Store(true)
	h.ctrl.checkTarget()
	rextest.Recv(t, entered, "the first check's whoami")
	h.ctrl.checkTarget()
	if n := execWhoami(fr); n != before+1 {
		t.Fatalf("exec whoami = %d during the first check, want %d", n, before+1)
	}
	if d := h.ctrl.conn.Load().statusDirty; d != statusCheck {
		t.Fatalf("dirty = %v during the first check, want a check", d)
	}
	close(release)
	ui.settle(t)
	if h.st.Status.State != rex.StateStopped {
		t.Fatalf("status = %v, want the newer stopped", h.st.Status.State)
	}
	if n := execWhoami(fr); n != before+2 {
		t.Fatalf("exec whoami = %d, want %d", n, before+2)
	}
}

const sshEditConfig = `# hand written
targets:
  - name: studio # the mac
    kind: native
    server: studio
  - name: edge # keep me
    kind: ssh
    ssh_host: remote-host
    rex_path: /opt/rex/bin/rex
`

// TestAddSSHTarget (G1): ~/.ssh/config aliases are offered in file order;
// the entry is written with comments kept and nothing run.
func TestAddSSHTarget(t *testing.T) {
	h := newHarness(t, withConfig(editConfig), withSSHHosts("lab", "remote-host"))
	h.selectTarget("local")
	h.fr.Reset()
	h.dispatch(PanelTargets, "addTarget")
	want := []string{"native: rex -S SERVER…", "ssh: lab (~/.ssh/config)", "ssh: remote-host (~/.ssh/config)", "ssh: another destination…"}
	if got := menuLabels(h.top()); !slices.Equal(got, want) {
		t.Fatalf("menu = %q", got)
	}
	h.choose("ssh: remote-host (~/.ssh/config)")
	if got := h.acceptPrefilled(); got != "remote-host" {
		t.Fatalf("name prefilled %q", got)
	}
	h.choosePrefix("forward:")
	raw, _ := os.ReadFile(h.cfgPath)
	if !containsAll(string(raw), "name: remote-host", "kind: ssh", "ssh_host: remote-host", "mode: forward", "# hand written", "# the mac", "note: keep me") {
		t.Fatalf("config:\n%s", raw)
	}
	if !slices.Contains(h.rowNames(), "remote-host") {
		t.Fatalf("rows = %q", h.rowNames())
	}
	h.dispatch(PanelTargets, "addTarget")
	if got := menuLabels(h.top()); slices.Contains(got, "ssh: remote-host (~/.ssh/config)") {
		t.Fatalf("remote-host still offered: %q", got)
	}
	if n := len(h.fr.Calls()); n != 0 {
		t.Fatalf("config editing ran:\n%s", h.dumpCalls())
	}
}

// TestAddSSHTargetFileOrder: the ~/.ssh/config aliases are offered in file
// order whatever their names; one an SSH target reaches is left out.
func TestAddSSHTargetFileOrder(t *testing.T) {
	for _, tc := range []struct {
		config      string
		hosts, want []string
	}{
		{editConfig, []string{"zeta", "remote-host", "alpha"}, []string{"zeta", "remote-host", "alpha"}},
		{editConfig, []string{"remote-host", "alpha", "zeta"}, []string{"remote-host", "alpha", "zeta"}},
		{editConfig, []string{"alpha", "zeta", "remote-host"}, []string{"alpha", "zeta", "remote-host"}},
		{sshEditConfig, []string{"zeta", "remote-host", "alpha"}, []string{"zeta", "alpha"}},
	} {
		h := newHarness(t, withConfig(tc.config), withSSHHosts(tc.hosts...))
		h.selectTarget("local")
		h.dispatch(PanelTargets, "addTarget")
		want := []string{"native: rex -S SERVER…"}
		for _, x := range tc.want {
			want = append(want, "ssh: "+x+" (~/.ssh/config)")
		}
		want = append(want, "ssh: another destination…")
		if got := menuLabels(h.top()); !slices.Equal(got, want) {
			t.Errorf("hosts %q: menu = %q, want %q", tc.hosts, got, want)
		}
	}
}

// TestAddSSHFlagTarget: a --ssh row gets the prefilled SSH flow.
func TestAddSSHFlagTarget(t *testing.T) {
	h := newHarness(t, withConfig(editConfig), withSSHFlag("u@lab"))
	h.selectTarget("u@lab")
	h.dispatch(PanelTargets, "addTarget")
	if got := h.acceptPrefilled(); got != "u@lab" {
		t.Fatalf("destination prefilled %q", got)
	}
	if got := h.acceptPrefilled(); got != "u@lab" {
		t.Fatalf("name prefilled %q", got)
	}
	h.choosePrefix("exec:")
	raw, _ := os.ReadFile(h.cfgPath)
	if !containsAll(string(raw), "name: u@lab", "kind: ssh", "ssh_host: u@lab", "mode: exec") {
		t.Fatalf("config:\n%s", raw)
	}
}

// TestAddSSHTargetRefused (G2).
func TestAddSSHTargetRefused(t *testing.T) {
	for name, in := range map[string][2]string{
		"option": {"-oProxyCommand=x", ""},
		"space":  {"a b", ""},
		"ctrl":   {"a\x1b[2J", ""},
		"taken":  {"lab", "STUDIO"},
		"local":  {"lab", "local"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, withConfig(editConfig))
			h.fr.Reset()
			h.focus(PanelTargets)
			h.dispatch(PanelTargets, "addTarget")
			h.choose("ssh: another destination…")
			h.answer(in[0])
			if top := h.ctrl.Popups.Top(); top != nil && top.Kind == PopupPrompt {
				h.answer(in[1])
			}
			if p := h.ctrl.Popups.Top(); p == nil || p.Kind != PopupError {
				t.Fatalf("popup %+v, want an error", p)
			}
			if raw, _ := os.ReadFile(h.cfgPath); string(raw) != editConfig {
				t.Fatalf("config changed:\n%s", raw)
			}
			if n := len(h.fr.Calls()); n != 0 {
				t.Fatalf("calls:\n%s", h.dumpCalls())
			}
		})
	}
}

// TestEditRemoveSSHTarget (G3).
func TestEditRemoveSSHTarget(t *testing.T) {
	h := newHarness(t, withConfig(sshEditConfig))
	h.selectTarget("edge")
	h.dispatch(PanelTargets, "editTarget")
	h.acceptPrefilled()
	if got := h.top().Initial; got != "remote-host" {
		t.Fatalf("destination prefilled %q", got)
	}
	h.answer("u@edge2")
	h.choosePrefix("exec:")
	raw, _ := os.ReadFile(h.cfgPath)
	if !containsAll(string(raw), "# keep me", "rex_path: /opt/rex/bin/rex", "ssh_host: u@edge2", "mode: exec", "# the mac") {
		t.Fatalf("config:\n%s", raw)
	}
	h.selectTarget("edge")
	h.dispatch(PanelTargets, "removeTarget")
	p := h.top()
	if p.Kind != PopupConfirm || !containsAll(p.Body, "Remove edge (ssh u@edge2)", "~/.ssh/config is not changed.") {
		t.Fatalf("popup %+v", p)
	}
	h.ctrl.Popups.Accept("")
	if raw, _ := os.ReadFile(h.cfgPath); strings.Contains(string(raw), "name: edge ") || strings.Contains(string(raw), "u@edge2") {
		t.Fatalf("config:\n%s", raw)
	}
	if n := len(h.fr.CallsContaining("ssh")); n != 0 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}
