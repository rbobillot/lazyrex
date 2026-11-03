package gui

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
	"github.com/rbobillot/lazyrex/pkg/rexhosts"
)

// rexCfg declares the two forward-mode SSH targets of the golden file.
const rexCfg = "targets:\n  - name: edge\n    kind: ssh\n    ssh_host: user@edge\n  - name: remote-host\n    kind: ssh\n    ssh_host: remote-host\n"

// host is one entry of a fake `rex hosts list`.
type host struct{ label, endpoint, source, addedFrom string }

func hostsJSON(hs ...host) string {
	type entry struct {
		ID        string   `json:"id"`
		Label     string   `json:"label"`
		Endpoints []string `json:"endpoints"`
		Source    string   `json:"source,omitempty"`
		AddedFrom string   `json:"added_from,omitempty"`
	}
	var es []entry
	for i, h := range hs {
		e := entry{ID: "host:" + string(rune('a'+i)), Label: h.label, Endpoints: []string{}, Source: h.source, AddedFrom: h.addedFrom}
		if h.endpoint != "" {
			e.Endpoints = []string{h.endpoint}
		}
		es = append(es, e)
	}
	b, _ := json.Marshal(map[string]any{
		"home":     map[string]any{"label": "", "endpoint": "unix:///run/user/1000/rex/server.sock", "this_machine": true, "reached": true},
		"hosts":    es,
		"warnings": []string{},
	})
	return string(b)
}

func checkJSON(path string) string {
	return strings.ReplaceAll(rextest.FixtureString("config_check_clean.json"), "/tmp/c1.lua", path)
}

// heldUI runs Update inline and queues Go work, which the test runs in the
// order it chooses.
type heldUI struct{ queue []func() }

func (u *heldUI) Update(f func()) { f() }
func (u *heldUI) Go(f func())     { u.queue = append(u.queue, f) }

func (u *heldUI) run(i int) {
	f := u.queue[i]
	u.queue = slices.Delete(u.queue, i, i+1)
	f()
}

func (u *heldUI) drain() {
	for len(u.queue) > 0 {
		u.run(0)
	}
}

type rexSetup struct {
	cfg string
	env map[string]string
	// run and home default to new private directories.
	run, home string
	hosts     func(run string) []host
	file      func(run string) []byte
	opts      []harnessOpt
	// off leaves experimental.rex_hosts_file to the config file, off unless
	// cfg turns it on.
	off bool
}

type rexHarness struct {
	*harness
	run, home string
	paths     rexhosts.Paths
}

func newRexHarness(t *testing.T, s rexSetup) *rexHarness {
	t.Helper()
	r := &rexHarness{run: s.run, home: s.home}
	if r.run == "" {
		r.run = rextest.PrivateDir(t)
	}
	if r.home == "" {
		r.home = rextest.PrivateDir(t)
	}
	env := s.env
	p, err := rexhosts.Locate(func(k string) string { return env[k] }, func() (string, error) { return r.home, nil })
	if err != nil {
		t.Fatal(err)
	}
	r.paths = p
	if s.file != nil {
		if err := os.MkdirAll(p.Dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.Hosts, s.file(r.run), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fr := fixtureRunner()
	var hs []host
	if s.hosts != nil {
		hs = s.hosts(r.run)
	}
	fr.On("hosts", "list", "--json").Return(hostsJSON(hs...), "", 0)
	fr.On("hosts", "check", "--json").Return(`{"hosts": []}`, "", 0)
	fr.On("config", "check", "--json").Return(checkJSON(r.checkPath()), "", 0)
	fr.On("config", "reload", "--json").Return(`{"generation": 2, "diagnostics": []}`, "", 0)
	cfg := s.cfg
	if cfg == "" {
		cfg = rexCfg
	}
	opts := []harnessOpt{withRunner(fr), withConfig(cfg), withSSH(rextest.SSHEnv(r.run, &rextest.FakeSockets{})), withRexEnv(r.home, env)}
	if !s.off {
		opts = append(opts, withRexHostsFile())
	}
	opts = append(opts, s.opts...)
	r.harness = newHarness(t, opts...)
	return r
}

func (r *rexHarness) checkPath() string { return r.run + "/hosts-check-42.lua" }

func (r *rexHarness) uri(name, sshHost string) string {
	return rex.SocketURI(rex.StableSocketPath(r.run, name, rex.SSHSpec{Host: sshHost}))
}

func (r *rexHarness) edgeURI() string   { return r.uri("edge", "user@edge") }
func (r *rexHarness) remoteURI() string { return r.uri("remote-host", "remote-host") }

// golden is testdata/lazyrex_hosts.lua for runtime directory run.
func golden(run string) []byte {
	return []byte(strings.ReplaceAll(rextest.FixtureString("lazyrex_hosts.lua"), "/run/user/1000/lazyrex", run))
}

// loaded is the hosts list of a server that loaded the golden file.
func loaded(run string) []host {
	return []host{
		{label: "edge-lazyrex", endpoint: rex.SocketURI(rex.StableSocketPath(run, "edge", rex.SSHSpec{Host: "user@edge"})), source: "lazyrex-hosts.lua:4"},
		{label: "remote-host-lazyrex", endpoint: rex.SocketURI(rex.StableSocketPath(run, "remote-host", rex.SSHSpec{Host: "remote-host"})), source: "lazyrex-hosts.lua:5"},
	}
}

func (r *rexHarness) setHosts(hs ...host) {
	r.fr.On("hosts", "list", "--json").Return(hostsJSON(hs...), "", 0)
}

func (r *rexHarness) include() string {
	return `do local f = loadfile("` + r.paths.Hosts + `"); if f then f() end end`
}

func (r *rexHarness) head(label, state string) string {
	return "Rex app host " + label + " in " + r.paths.Hosts + ": " + state
}

func works(name string) string {
	return "It works while lazyrex runs with " + name + " active. Rex has no transport auth: only your user can open this socket."
}

func block(lines ...string) string { return strings.Join(lines, "\n") + "\n\n" }

// status is the Rex app host block of the Status tab with name active and
// forwarded.
func (r *rexHarness) status(name string) string {
	r.t.Helper()
	st := NewState()
	st.TargetName = name
	st.Transport = fakeGUILink{ep: rex.GUIEndpoint{State: rex.GUIForwarded, URI: "unix:///gui.sock"}}
	st.RexHosts, st.Hosts, st.RexHostsFile = r.st.RexHosts, r.st.Hosts, r.st.RexHostsFile
	out := RenderStatus(st)
	_, after, ok := strings.Cut(out, "gui endpoint  unix:///gui.sock"+sgrReset+"\n\n")
	b, _, found := strings.Cut(after, "event stream")
	if !ok || !found {
		r.t.Fatalf("status tab:\n%s", out)
	}
	return b
}

func (r *rexHarness) expectStatus(name, want string) {
	r.t.Helper()
	if got := r.status(name); got != want {
		r.t.Fatalf("Rex app host block of %s\n got %q\nwant %q", name, got, want)
	}
}

func (r *rexHarness) w() {
	r.t.Helper()
	r.focus(PanelTargets)
	r.dispatch(PanelTargets, "writeRexHosts")
}

func (r *rexHarness) expectConfirm(title string) *Popup {
	r.t.Helper()
	p := r.ctrl.Popups.Top()
	if p == nil || p.Kind != PopupConfirm || p.Title != title {
		r.t.Fatalf("popup %+v, want the confirmation %q", p, title)
	}
	return p
}

func (r *rexHarness) expectError(want string) {
	r.t.Helper()
	p := r.ctrl.Popups.Top()
	if p == nil || p.Kind != PopupError || p.Body != want {
		r.t.Fatalf("popup %+v, want the error %q", p, want)
	}
	r.ctrl.Popups.Cancel()
}

func (r *rexHarness) expectToast(want string) {
	r.t.Helper()
	if got := r.ctrl.Popups.ToastText(); got != want {
		r.t.Fatalf("toast %q, want %q", got, want)
	}
	if p := r.ctrl.Popups.Top(); p != nil {
		r.t.Fatalf("popup %+v open", p)
	}
}

func (r *rexHarness) accept() { r.t.Helper(); r.ctrl.Popups.Accept("") }

func (r *rexHarness) fileText() (string, bool) {
	b, err := os.ReadFile(r.paths.Hosts)
	return string(b), err == nil
}

func (r *rexHarness) writeConfirmBody(lines ...string) string {
	if len(lines) == 0 {
		lines = []string{"(no host: only the header)"}
	}
	return "Write " + r.paths.Hosts + " (rex config check passed). Rex runs it once " + r.paths.Init + " loads it:\n" + strings.Join(lines, "\n")
}

func (r *rexHarness) edgeLine() string {
	return `rex.host({ label = "edge-lazyrex", endpoint = "` + r.edgeURI() + `" })`
}

func (r *rexHarness) remoteLine() string {
	return `rex.host({ label = "remote-host-lazyrex", endpoint = "` + r.remoteURI() + `" })`
}

const reloadDisplay = "rex --autostart=false --timeout=10s config reload --json"

// TestRexHostsStatus (H20 to H30, H41, H42, H55, H56, H58, H66): the Rex app
// host block of every state, exact.
func TestRexHostsStatus(t *testing.T) {
	stale := func(string) []byte { return golden("/tmp/elsewhere") }
	for _, tc := range []struct {
		name string
		s    rexSetup
		want func(r *rexHarness) string
	}{
		{"S1 REX_SERVER (A1)", rexSetup{env: map[string]string{"REX_SERVER": "unix:///tmp/other.sock"}}, func(r *rexHarness) string {
			return block("Rex app host none: REX_SERVER is set, so lazyrex cannot tell which server reads the file.")
		}},
		{"S1 runtime directory", rexSetup{opts: []harnessOpt{withSSH(&rex.SSHEnv{Dir: func() (string, error) { return "", errors.New("no runtime dir") },
			Sockets: &rextest.FakeSockets{}, PID: 42, Probes: &rex.SSHProbes{}})}}, func(r *rexHarness) string {
			return block("Rex app host none: runtime directory: no runtime dir")
		}},
		{"S3 shared label", rexSetup{cfg: "targets:\n  - name: a.b\n    kind: ssh\n    ssh_host: a\n  - name: a_b\n    kind: ssh\n    ssh_host: b\n"},
			func(r *rexHarness) string {
				return block(`Rex app host none: targets "a.b" and "a_b" would share the label a-b-lazyrex; rename one.`)
			}},
		{"S4 foreign", rexSetup{file: func(string) []byte { return []byte("mine\n") }}, func(r *rexHarness) string {
			return block("Rex app host none: " + r.paths.Hosts + " is not a file lazyrex wrote (its first line is not lazyrex's header), " +
				"so lazyrex leaves it alone. Move it away, then w writes it.")
		}},
		{"S5 stored", rexSetup{hosts: func(run string) []host {
			return []host{{label: "edge-lazyrex", endpoint: loaded(run)[0].endpoint}}
		}}, func(r *rexHarness) string {
			return block("Rex app host edge-lazyrex: the label is taken by another host (stored through the API), so Rex leaves lazyrex's declaration out. " +
				"Remove or rename that host in the Rex app; lazyrex never changes hosts.")
		}},
		{"S5 declared elsewhere", rexSetup{file: golden, hosts: func(string) []host {
			return []host{{label: "edge-lazyrex", endpoint: "unix:///tmp/other.sock", source: "init.lua:3"}}
		}}, func(r *rexHarness) string {
			return block("Rex app host edge-lazyrex: the label is taken by another host (declared at init.lua:3), so Rex leaves lazyrex's declaration out. " +
				"Remove or rename that host in the Rex app; lazyrex never changes hosts.")
		}},
		{"S5 source config (A3)", rexSetup{file: golden, hosts: func(run string) []host {
			return []host{{label: "edge-lazyrex", endpoint: loaded(run)[0].endpoint, source: "config", addedFrom: "lazyrex-hosts.lua:4"}}
		}}, func(r *rexHarness) string {
			return block("Rex app host edge-lazyrex: the label is taken by another host (stored through the API), so Rex leaves lazyrex's declaration out. " +
				"Remove or rename that host in the Rex app; lazyrex never changes hosts.")
		}},
		{"S6 not written", rexSetup{}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "not written yet"),
				"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.", works("edge"))
		}},
		{"S7 out of date", rexSetup{file: stale}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "out of date"),
				"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.", works("edge"))
		}},
		{"S8 loaded", rexSetup{file: golden, hosts: loaded}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date, loaded by the local Rex server"), works("edge"))
		}},
		{"S8 spaced absolute source (A2)", rexSetup{file: golden, hosts: func(run string) []host {
			hs := loaded(run)
			hs[0].source = "/Users/Jane Doe/.config/rex/lazyrex-hosts.lua:4"
			return hs
		}}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date, loaded by the local Rex server"), works("edge"))
		}},
		{"S8 encoded endpoint (A12)", rexSetup{file: golden, hosts: func(run string) []host {
			hs := loaded(run)
			hs[0].endpoint = strings.Replace(hs[0].endpoint, "gui-edge", "gui%2Dedge", 1)
			return hs
		}}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date, loaded by the local Rex server"), works("edge"))
		}},
		{"S8 declared in init.lua (A4)", rexSetup{file: golden, hosts: func(run string) []host {
			hs := loaded(run)
			hs[0].source = "init.lua:9"
			return hs
		}}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date, loaded by the local Rex server"),
				"declared in init.lua:9, not in lazyrex-hosts.lua", works("edge"))
		}},
		{"S8 stored host on our socket", rexSetup{file: golden, hosts: func(run string) []host {
			return append(loaded(run), host{label: "edge", endpoint: loaded(run)[0].endpoint, addedFrom: "laptop"})
		}}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date, loaded by the local Rex server"),
				"The host edge also reaches this socket and is stored through the API (rex hosts add), so it syncs to your other devices, "+
					"where it reaches nothing. Remove it in the Rex app; lazyrex never removes hosts.", works("edge"))
		}},
		{"S8 own host by hand (A4)", rexSetup{file: golden, hosts: func(run string) []host {
			return append(loaded(run), host{label: "old-lazyrex", endpoint: "unix:///run/user/1000/lazyrex/gui-old_0123456789abcdef.sock", source: "init.lua:2"})
		}}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date, loaded by the local Rex server"), works("edge"),
				"The host old-lazyrex is declared in init.lua:2, which lazyrex does not manage: remove that line by hand.")
		}},
		{"S10 not loaded", rexSetup{file: golden}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date, not loaded yet"),
				"If "+r.paths.Init+" does not load it yet, add this line there (lazyrex never creates or edits "+r.paths.Init+"):",
				r.include(), "Then w (Targets) reloads the local Rex server, after a confirmation.", works("edge"))
		}},
		{"S10 REX_CONFIG", rexSetup{file: golden, env: map[string]string{"REX_CONFIG": "/etc/rex.lua"}}, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date, not loaded yet"),
				"If $REX_CONFIG (/etc/rex.lua) does not load it yet, add this line there (lazyrex never creates or edits $REX_CONFIG (/etc/rex.lua)):",
				r.include(), "Then w (Targets) reloads the local Rex server, after a confirmation.", works("edge"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRexHarness(t, tc.s)
			name := "edge"
			if strings.HasPrefix(tc.name, "S3") {
				name = "a.b"
			}
			r.expectStatus(name, tc.want(r))
		})
	}
}

// TestRexHostsStatusS2 (H21): a --ssh target is not in the file.
func TestRexHostsStatusS2(t *testing.T) {
	r := newRexHarness(t, rexSetup{cfg: "targets:\n  - name: other\n    kind: ssh\n    ssh_host: other\n    mode: exec\n",
		opts: []harnessOpt{withSSHFlag("u@lab"), withSSH(nil)}})
	r.expectStatus("u@lab", block("Rex app host none: u@lab comes from --ssh, not the config file. Add it with a (Targets), then w writes the Rex app hosts file."))
}

// TestRexHostsStatusS3Invalid (H22): a name without letters or digits.
func TestRexHostsStatusS3Invalid(t *testing.T) {
	r := newRexHarness(t, rexSetup{cfg: "targets:\n  - name: 日本\n    kind: ssh\n    ssh_host: jp\n"})
	r.expectStatus("日本", block(`Rex app host none: "日本" gives no Rex host label (it needs letters or digits); rename the target.`))
	r.w()
	r.expectError(`"日本" gives no Rex host label (it needs letters or digits); rename the target.`)
	if _, ok := r.fileText(); ok || r.countContaining("config") != 0 {
		t.Fatalf("wrote or checked:\n%s", r.dumpCalls())
	}
}

// TestRexHostsStatusS9 (H28, H39): the home server stopped.
func TestRexHostsStatusS9(t *testing.T) {
	r := newRexHarness(t, rexSetup{file: golden, opts: []harnessOpt{}})
	r.fr.On("hosts", "list", "--json").Return("", "Error: the server is not running\n", 4)
	r.ctrl.refreshHosts()
	r.expectStatus("edge", block(r.head("edge-lazyrex", "up to date; the local Rex server is not running"),
		"It reads the file when it starts, once "+r.paths.Init+" loads it:", r.include(), works("edge")))
	r.fr.Reset()
	r.w()
	r.expectToast("the local Rex server is not running: it reads " + r.paths.Init + " when it starts")
	if len(r.fr.Calls()) != 0 {
		t.Fatalf("calls:\n%s", r.dumpCalls())
	}
}

// TestRexHostsWrite (H33, H51): w checks, confirms, writes, confirms and runs
// the reload on this machine without REX_SERVER, then lists hosts again.
func TestRexHostsWrite(t *testing.T) {
	r := newRexHarness(t, rexSetup{})
	var copied []byte
	var mode fs.FileMode
	r.fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, "config", "check") {
			copied, _ = os.ReadFile(argv[len(argv)-1])
			if fi, err := os.Lstat(argv[len(argv)-1]); err == nil {
				mode = fi.Mode()
			}
		}
	}
	r.fr.Reset()
	r.w()
	p := r.expectConfirm("Write the Rex app hosts file")
	if want := r.writeConfirmBody(r.edgeLine(), r.remoteLine()); p.Body != want {
		t.Fatalf("body\n got %q\nwant %q", p.Body, want)
	}
	if want := golden(r.run); string(copied) != string(want) || mode != 0o600 {
		t.Fatalf("checked copy (%v):\n%s", mode, copied)
	}
	if _, err := os.Lstat(r.checkPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("check copy left: %v", err)
	}
	r.expectCall("config check --json " + r.checkPath())
	if d := r.fr.Drops("config", "check"); len(d) != 1 || !slices.Equal(d[0], []string{"REX_SERVER"}) {
		t.Fatalf("drops %q", d)
	}
	if _, ok := r.fileText(); ok {
		t.Fatal("written before the confirmation")
	}
	r.accept()
	if got, _ := r.fileText(); got != string(golden(r.run)) {
		t.Fatalf("file:\n%s", got)
	}
	if fi, _ := os.Lstat(r.paths.Hosts); fi.Mode() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(r.paths.Dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode())
	}
	p = r.expectConfirm("Reload the local Rex server")
	if p.Body != reloadDisplay {
		t.Fatalf("reload body %q", p.Body)
	}
	if r.countContaining("config", "reload") != 0 {
		t.Fatal("reloaded before the confirmation")
	}
	r.setHosts(loaded(r.run)...)
	lists := r.countContaining("hosts", "list")
	r.accept()
	if r.count("config reload --json") != 1 {
		t.Fatalf("calls:\n%s", r.dumpCalls())
	}
	if d := r.fr.Drops("config", "reload"); len(d) != 1 || !slices.Equal(d[0], []string{"REX_SERVER"}) {
		t.Fatalf("drops %q", d)
	}
	if r.countContaining("hosts", "list") != lists+1 {
		t.Fatalf("hosts not listed again:\n%s", r.dumpCalls())
	}
	r.expectToast("the local Rex server read its config again")
	r.expectStatus("edge", block(r.head("edge-lazyrex", "up to date, loaded by the local Rex server"), works("edge")))
	for _, c := range r.fr.Calls() {
		if c[0] == "rex" && !slices.Contains(c, "--autostart=false") {
			t.Errorf("argv without --autostart=false: %q", c)
		}
		if rextest.Contains(c, "hosts", "add") || rextest.Contains(c, "hosts", "edit") || rextest.Contains(c, "hosts", "remove") {
			t.Errorf("changed hosts: %q", c)
		}
	}
	if slices.ContainsFunc(r.rowNames(), func(n string) bool { return strings.HasSuffix(n, "-lazyrex") }) {
		t.Fatalf("own hosts listed: %q", r.rowNames())
	}
	if !strings.Contains(r.logText(), `hosts: hid "edge-lazyrex": lazyrex's own host`) {
		t.Fatalf("log:\n%s", r.logText())
	}
}

// TestRexHostsReloadFromRemote (H34): the reload goes to this machine's
// server whatever target is active.
func TestRexHostsReloadFromRemote(t *testing.T) {
	t.Run("native", func(t *testing.T) {
		r := newRexHarness(t, rexSetup{cfg: rexCfg + "  - name: srv\n    kind: native\n    server: srv\n", file: golden,
			opts: []harnessOpt{withInitial("srv")}})
		r.w()
		r.expectConfirm("Reload the local Rex server")
		r.accept()
		calls := r.fr.CallsContaining("config", "reload")
		if len(calls) != 1 || !slices.Equal(calls[0], strings.Fields(G+"config reload --json")) {
			t.Fatalf("reload calls %q", calls)
		}
	})
	for _, mode := range []string{"forward", "exec"} {
		t.Run("ssh "+mode, func(t *testing.T) {
			run, home := rextest.PrivateDir(t), rextest.PrivateDir(t)
			fr := sshRunner("/bin/bash")
			fr.On("config", "reload", "--json").Return(`{"generation": 2, "diagnostics": []}`, "", 0)
			p := rexhosts.Paths{Dir: home + "/.config/rex", Hosts: home + "/.config/rex/lazyrex-hosts.lua"}
			if err := os.MkdirAll(p.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := sshConfig + "    mode: " + mode + "\n" + "  - name: edge\n    kind: ssh\n    ssh_host: user@edge\n"
			want := strings.Split(string(golden(run)), "\n")
			want = slices.DeleteFunc(want, func(l string) bool { return strings.Contains(l, "remote-host") && mode == "exec" })
			if err := os.WriteFile(p.Hosts, []byte(strings.Join(want, "\n")), 0o600); err != nil {
				t.Fatal(err)
			}
			ui := &pumpUI{}
			h := newHarness(t, withRunner(fr), withUI(ui), withSSH(rextest.SSHEnv(run, &rextest.FakeSockets{})),
				withConfig(cfg), withInitial("remote-host"), withRexEnv(home, nil), withRexHostsFile())
			s := &sshHarness{harness: h, ui: ui}
			s.waitState(rex.StateOnline)
			s.do(PanelTargets, "writeRexHosts")
			if p := s.top(); p.Kind != PopupConfirm || p.Body != reloadDisplay {
				t.Fatalf("popup %+v", p)
			}
			s.ctrl.Popups.Accept("")
			s.ui.pump()
			calls := fr.CallsContaining("config", "reload")
			if len(calls) != 1 || !slices.Equal(calls[0], strings.Fields(G+"config reload --json")) {
				t.Fatalf("reload calls %q:\n%s", calls, s.dumpCalls())
			}
			if len(fr.CallsContaining("'config'")) != 0 {
				t.Fatalf("config command over ssh:\n%s", s.dumpCalls())
			}
		})
	}
}

// TestRexHostsPaletteReload (H35, H64, A10): a reload typed in the palette
// is refused on a remote target and without RunEnv, and confirmed then run
// without REX_SERVER on this machine.
func TestRexHostsPaletteReload(t *testing.T) {
	for _, line := range []string{"api call server.config_reload", "do config.reload"} {
		t.Run(line, func(t *testing.T) {
			name := line[strings.LastIndex(line, " ")+1:]
			s := newSSHHarness(t, sshRunner("/bin/bash"), nil, withConfig(sshConfig), withInitial("remote-host"))
			s.waitState(rex.StateOnline)
			s.fr.Reset()
			s.palette(line)
			s.ui.pump()
			s.expectRefusal("runs on this machine's server only")
			if n := len(s.fr.Calls()); n != 0 {
				t.Fatalf("calls:\n%s", s.dumpCalls())
			}

			h := newHarness(t, withoutEnvRunner())
			h.fr.Reset()
			h.palette(line)
			h.expectRefusal("")
			if n := len(h.fr.Calls()); n != 0 {
				t.Fatalf("calls:\n%s", h.dumpCalls())
			}

			h = newHarness(t)
			h.fr.On("actions", "--json").Return(`{"actions": [{"name": "config.reload"}]}`, "", 0)
			h.fr.On("-C", "client:02app", "actions", "--json").Return(`{"actions": []}`, "", 0)
			h.fr.Reset()
			h.palette(line)
			if p := h.top(); p.Kind != PopupConfirm {
				t.Fatalf("popup %+v", p)
			}
			h.ctrl.Popups.Accept("")
			if d := h.fr.Drops(name); len(d) != 1 || !slices.Equal(d[0], []string{"REX_SERVER"}) {
				t.Fatalf("drops %q:\n%s", d, h.dumpCalls())
			}
		})
	}
}

// TestRexHostsDecline (H36, H59, A5): declining a confirmation leaves w
// usable.
func TestRexHostsDecline(t *testing.T) {
	r := newRexHarness(t, rexSetup{})
	for range 2 {
		r.w()
		r.expectConfirm("Write the Rex app hosts file")
		r.ctrl.Popups.Cancel()
	}
	if _, ok := r.fileText(); ok {
		t.Fatal("written after a decline")
	}
	r.w()
	r.accept()
	r.expectConfirm("Reload the local Rex server")
	r.ctrl.Popups.Cancel()
	r.expectStatus("edge", block(r.head("edge-lazyrex", "up to date, not loaded yet"),
		"If "+r.paths.Init+" does not load it yet, add this line there (lazyrex never creates or edits "+r.paths.Init+"):",
		r.include(), "Then w (Targets) reloads the local Rex server, after a confirmation.", works("edge")))
	r.fr.Reset()
	r.w()
	r.expectConfirm("Reload the local Rex server")
	if r.countContaining("config", "check") != 0 {
		t.Fatalf("checked an up to date file:\n%s", r.dumpCalls())
	}
	r.ctrl.Popups.Cancel()
	r.w()
	r.expectConfirm("Reload the local Rex server")
}

// TestRexHostsCheckFails (H37): a failed check writes nothing.
func TestRexHostsCheckFails(t *testing.T) {
	r := newRexHarness(t, rexSetup{file: func(string) []byte { return golden("/tmp/elsewhere") }})
	r.fr.On("config", "check", "--json").Return(rextest.FixtureString("config_check_errors.json"), "Error: the configuration has errors\n", 1)
	r.w()
	p := r.ctrl.Popups.Top()
	if p == nil || p.Kind != PopupError || !strings.HasPrefix(p.Body, r.paths.Hosts+" not written: rex config check: ") || !strings.Contains(p.Body, "boom") {
		t.Fatalf("popup %+v", p)
	}
	if got, _ := r.fileText(); got != string(golden("/tmp/elsewhere")) {
		t.Fatalf("file changed:\n%s", got)
	}
	if _, err := os.Lstat(r.checkPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("check copy left: %v", err)
	}
}

// TestRexHostsReloadOutcomes (H38, H39).
func TestRexHostsReloadOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr string
		code                 int
		check                func(r *rexHarness)
	}{
		{"warning", rextest.FixtureString("config_reload_warning.json"), "", 0, func(r *rexHarness) {
			r.expectToast("the local Rex server read its config again, with 1 complaint: init.lua:3: x")
			if !strings.Contains(r.logText(), "config reload: notice init.lua:3: x") {
				t.Fatalf("log:\n%s", r.logText())
			}
		}},
		{"garbage", "not json\x1b[31m", "", 0, func(r *rexHarness) {
			r.expectToast("the local Rex server read its config again (output unreadable)")
		}},
		{"errors", rextest.FixtureString("config_check_errors.json"), "Error: the configuration has errors\n", 1, func(r *rexHarness) {
			p := r.ctrl.Popups.Top()
			if p == nil || p.Kind != PopupError || !strings.HasPrefix(p.Body, "rex config reload: ") || !strings.Contains(p.Body, "boom") {
				t.Fatalf("popup %+v", p)
			}
			if !strings.Contains(r.logText(), "boom") || !strings.Contains(r.logText(), "so this host is ignored") {
				t.Fatalf("log:\n%s", r.logText())
			}
		}},
		{"stopped", "", "Error: the server is not running\n", 4, func(r *rexHarness) {
			r.expectToast("the local Rex server is not running: it reads " + r.paths.Init + " when it starts")
			if !r.st.Hosts.HomeStopped || slices.Contains(r.rowNames(), "studio") {
				t.Fatalf("home stopped %v, rows %q", r.st.Hosts.HomeStopped, r.rowNames())
			}
			r.expectStatus("edge", block(r.head("edge-lazyrex", "up to date; the local Rex server is not running"),
				"It reads the file when it starts, once "+r.paths.Init+" loads it:", r.include(), works("edge")))
			if r.countContaining("server", "start") != 0 || r.countContaining("whoami") != 0 {
				t.Fatalf("calls:\n%s", r.dumpCalls())
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRexHarness(t, rexSetup{file: golden, hosts: func(string) []host {
				return []host{{label: "studio", endpoint: "tcp://192.168.1.20:7297"}}
			}})
			if !slices.Contains(r.rowNames(), "studio") {
				t.Fatalf("rows %q", r.rowNames())
			}
			r.fr.On("config", "reload", "--json").Return(tc.stdout, tc.stderr, tc.code)
			r.w()
			r.expectConfirm("Reload the local Rex server")
			r.fr.Reset()
			r.accept()
			tc.check(r)
		})
	}
}

// TestRexHostsReloadSequence (H40, H62, A8): only a hosts list started after
// the reload, and read successfully, says the reload did not load the file.
func TestRexHostsReloadSequence(t *testing.T) {
	s10 := func(r *rexHarness) string {
		return block(r.head("edge-lazyrex", "up to date, not loaded yet"),
			"If "+r.paths.Init+" does not load it yet, add this line there (lazyrex never creates or edits "+r.paths.Init+"):",
			r.include(), "Then w (Targets) reloads the local Rex server, after a confirmation.", works("edge"))
	}
	s11 := func(r *rexHarness) string {
		return block(r.head("edge-lazyrex", "up to date, still not loaded after the reload"),
			"Check that "+r.paths.Init+" has this line, and the reload output in the command log:", r.include(),
			"If your Rex ignores hosts declared in a loaded file, copy this line into "+r.paths.Init+" instead:", r.edgeLine(), works("edge"))
	}
	for _, tc := range []struct {
		name string
		// after answers the first hosts list started after the reload.
		after func(r *rexHarness)
		mid   func(r *rexHarness) string
	}{
		{"good", nil, nil},
		{"failed", func(r *rexHarness) { r.fr.On("hosts", "list", "--json").Return("", "Error: boom\n", 1) }, s10},
		{"garbage", func(r *rexHarness) { r.fr.On("hosts", "list", "--json").Return("not json", "", 0) }, s10},
		{"stopped", func(r *rexHarness) { r.fr.On("hosts", "list", "--json").Return("", "Error: not running\n", 4) }, func(r *rexHarness) string {
			return block(r.head("edge-lazyrex", "up to date; the local Rex server is not running"),
				"It reads the file when it starts, once "+r.paths.Init+" loads it:", r.include(), works("edge"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ui := &heldUI{}
			r := newRexHarness(t, rexSetup{file: golden, opts: []harnessOpt{withUI(ui)}})
			ui.drain()
			r.expectStatus("edge", s10(r))
			r.ctrl.refreshHosts()
			before := len(ui.queue)
			r.w()
			if len(ui.queue) != before+1 {
				t.Fatalf("w queued %d workers", len(ui.queue)-before)
			}
			ui.run(before)
			r.expectConfirm("Reload the local Rex server")
			r.accept()
			ui.run(len(ui.queue) - 1)
			r.expectToast("the local Rex server read its config again")
			ui.run(0)
			r.expectStatus("edge", s10(r))
			if tc.after != nil {
				tc.after(r)
				r.ctrl.Tick()
				ui.drain()
				r.expectStatus("edge", tc.mid(r))
				r.setHosts()
				r.ctrl.refreshHosts()
				ui.drain()
			} else {
				r.ctrl.Tick()
				ui.drain()
			}
			r.expectStatus("edge", s11(r))
			r.setHosts(loaded(r.run)...)
			r.ctrl.refreshHosts()
			ui.drain()
			r.expectStatus("edge", block(r.head("edge-lazyrex", "up to date, loaded by the local Rex server"), works("edge")))
		})
	}
}

// TestRexHostsConflict (H41, H57, H60, A3, A6): a stored host with lazyrex's
// label stays listed, and w names it instead of reloading.
func TestRexHostsConflict(t *testing.T) {
	stored := func(run string) []host {
		return []host{
			{label: "edge-lazyrex", endpoint: loaded(run)[0].endpoint, source: "config", addedFrom: "lazyrex-hosts.lua:4"},
			loaded(run)[1],
		}
	}
	r := newRexHarness(t, rexSetup{file: golden, hosts: stored})
	if !slices.Contains(r.rowNames(), "edge-lazyrex") {
		t.Fatalf("stored host hidden: %q", r.rowNames())
	}
	r.fr.Reset()
	r.w()
	r.expectError("edge-lazyrex is taken by another host (stored through the API): remove or rename it in the Rex app")
	if len(r.fr.Calls()) != 0 {
		t.Fatalf("calls:\n%s", r.dumpCalls())
	}

	both := func(run string) []host {
		return []host{{label: "edge-lazyrex", endpoint: loaded(run)[0].endpoint}, {label: "remote-host-lazyrex", endpoint: loaded(run)[1].endpoint}}
	}
	r = newRexHarness(t, rexSetup{hosts: both})
	r.w()
	r.expectConfirm("Write the Rex app hosts file")
	r.accept()
	if got, _ := r.fileText(); got != string(golden(r.run)) {
		t.Fatalf("file:\n%s", got)
	}
	if p := r.ctrl.Popups.Top(); p != nil {
		t.Fatalf("popup %+v after writing only conflicting hosts", p)
	}
}

// TestRexHostsStoredSameName (H42): a stored host named like a config
// target stays hidden by name, as before.
func TestRexHostsStoredSameName(t *testing.T) {
	r := newRexHarness(t, rexSetup{file: golden, hosts: func(run string) []host {
		return append(loaded(run), host{label: "edge", endpoint: loaded(run)[0].endpoint, addedFrom: "laptop"})
	}})
	n := 0
	for _, name := range r.rowNames() {
		if strings.EqualFold(name, "edge") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("rows %q", r.rowNames())
	}
}

// TestRexHostsStaleOwn (H43, A4): a host lazyrex-hosts.lua declared for a
// removed target is hidden and asks for a reload; one declared elsewhere is
// shown and never does.
func TestRexHostsStaleOwn(t *testing.T) {
	gone := host{label: "gone-lazyrex", endpoint: "unix:///run/user/1000/lazyrex/gui-gone_0123456789abcdef.sock",
		source: "/Users/Jane Doe/.config/rex/lazyrex-hosts.lua:5"}
	r := newRexHarness(t, rexSetup{file: golden, hosts: func(run string) []host { return append(loaded(run), gone) }})
	if slices.Contains(r.rowNames(), "gone-lazyrex") || !strings.Contains(r.logText(), `hosts: hid "gone-lazyrex": lazyrex's own host`) {
		t.Fatalf("rows %q, log:\n%s", r.rowNames(), r.logText())
	}
	r.fr.Reset()
	r.w()
	r.expectConfirm("Reload the local Rex server")
	if r.countContaining("config", "check") != 0 {
		t.Fatalf("calls:\n%s", r.dumpCalls())
	}

	byHand := host{label: "old-lazyrex", endpoint: "unix:///run/user/1000/lazyrex/gui-old_0123456789abcdef.sock", source: "init.lua:2"}
	r = newRexHarness(t, rexSetup{file: golden, hosts: func(run string) []host { return append(loaded(run), byHand) }})
	if !slices.Contains(r.rowNames(), "old-lazyrex") {
		t.Fatalf("rows %q", r.rowNames())
	}
	r.fr.Reset()
	r.w()
	r.expectToast(r.paths.Hosts + " is up to date and loaded")
	if len(r.fr.Calls()) != 0 {
		t.Fatalf("calls:\n%s", r.dumpCalls())
	}

	initEdge := func(run string) []host {
		hs := loaded(run)
		hs[0].source = "init.lua:9"
		return hs
	}
	r = newRexHarness(t, rexSetup{file: golden, hosts: initEdge})
	if !slices.Contains(r.rowNames(), "edge-lazyrex") {
		t.Fatalf("rows %q", r.rowNames())
	}
	r.w()
	r.expectToast(r.paths.Hosts + " is up to date and loaded")
}

// TestRexHostsEditTarget (H44): a config edit makes the file out of date,
// the old declaration stays hidden and is no conflict.
func TestRexHostsEditTarget(t *testing.T) {
	r := newRexHarness(t, rexSetup{file: golden, hosts: loaded})
	r.selectTarget("edge")
	r.dispatch(PanelTargets, "editTarget")
	r.acceptPrefilled()
	r.answer("user@edge2")
	r.choosePrefix("forward:")
	r.expectStatus("edge", block(r.head("edge-lazyrex", "out of date"),
		"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.", works("edge")))
	if slices.ContainsFunc(r.rowNames(), func(n string) bool { return strings.HasSuffix(n, "-lazyrex") }) {
		t.Fatalf("rows %q", r.rowNames())
	}
	r.w()
	p := r.expectConfirm("Write the Rex app hosts file")
	if !strings.Contains(p.Body, r.uri("edge", "user@edge2")) {
		t.Fatalf("body %q", p.Body)
	}
}

// TestRexHostsOwnHostsSkipChecks (H45): lazyrex's own hosts start no hosts
// check and count as neither unmatched nor unlisted.
func TestRexHostsOwnHostsSkipChecks(t *testing.T) {
	r := newRexHarness(t, rexSetup{file: golden, hosts: loaded})
	if r.countContaining("hosts", "check") != 0 {
		t.Fatalf("checked own hosts:\n%s", r.dumpCalls())
	}
	r.fr.On("hosts", "check").Return(`{"hosts": [{"label": "edge-lazyrex", "status": "quantum"}, {"label": "studio", "status": "reachable", "endpoint": "tcp://192.168.1.20:7297"}]}`, "", 0)
	r.setHosts(append(loaded(r.run), host{label: "studio", endpoint: "tcp://192.168.1.20:7297"})...)
	r.ctrl.refreshHosts()
	if r.countContaining("hosts", "check") != 1 {
		t.Fatalf("calls:\n%s", r.dumpCalls())
	}
	if h := r.st.Hosts; h.Unmatched != 0 || len(h.Unlisted) != 0 {
		t.Fatalf("hosts %+v", h)
	}
}

// TestRexHostsUpToDate (H46): nothing to do is a toast.
func TestRexHostsUpToDate(t *testing.T) {
	r := newRexHarness(t, rexSetup{file: golden, hosts: loaded})
	r.fr.Reset()
	r.w()
	r.expectToast(r.paths.Hosts + " is up to date and loaded")
	if len(r.fr.Calls()) != 0 {
		t.Fatalf("calls:\n%s", r.dumpCalls())
	}
}

// TestRexHostsBusy (H47): one w at a time, its check included.
func TestRexHostsBusy(t *testing.T) {
	ui := &heldUI{}
	r := newRexHarness(t, rexSetup{opts: []harnessOpt{withUI(ui)}})
	ui.drain()
	r.w()
	r.w()
	r.expectError("the Rex app hosts file is still being written")
	ui.drain()
	r.expectConfirm("Write the Rex app hosts file")
	r.accept()
	r.w()
	r.expectError("the Rex app hosts file is still being written")
	ui.drain()
	r.expectConfirm("Reload the local Rex server")
	r.ctrl.Popups.Cancel()
	r.ctrl.configBusy = true
	r.w()
	r.expectError("another config edit is still running")
}

// TestRexHostsChangedSinceCheck (H48): the file or the targets changing
// after the check write nothing.
func TestRexHostsChangedSinceCheck(t *testing.T) {
	old := func(string) []byte { return golden("/tmp/elsewhere") }
	r := newRexHarness(t, rexSetup{file: old})
	r.w()
	r.expectConfirm("Write the Rex app hosts file")
	edited := string(golden("/tmp/elsewhere")) + "-- mine\n"
	if err := os.WriteFile(r.paths.Hosts, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	r.accept()
	r.expectError(r.paths.Hosts + " changed on disk since lazyrex read it; press w again")
	if got, _ := r.fileText(); got != edited {
		t.Fatalf("file:\n%s", got)
	}

	r = newRexHarness(t, rexSetup{file: old})
	r.w()
	r.expectConfirm("Write the Rex app hosts file")
	r.ctrl.setConfigTargets(append(slices.Clone(r.ctrl.cfgTargets),
		rex.Target{Name: "new", Kind: rex.TargetSSH, SSH: rex.SSHSpec{Host: "new"}, Source: rex.SourceConfig}))
	r.accept()
	r.expectError("the SSH targets changed since the check; press w again")
	if got, _ := r.fileText(); got != string(golden("/tmp/elsewhere")) {
		t.Fatalf("file:\n%s", got)
	}
}

// TestRexHostsNoTarget (H49): zero forward-mode targets read no runtime
// directory until a header-only file has to replace lazyrex's.
func TestRexHostsNoTarget(t *testing.T) {
	const native = "targets:\n  - name: srv\n    kind: native\n    server: srv\n"
	dirCalls := 0
	env := func(run string) *rex.SSHEnv {
		e := rextest.SSHEnv(run, &rextest.FakeSockets{})
		e.Dir = func() (string, error) { dirCalls++; return run, nil }
		return e
	}
	run := rextest.PrivateDir(t)
	r := newRexHarness(t, rexSetup{cfg: native, run: run, opts: []harnessOpt{withSSH(env(run))}})
	r.w()
	r.expectToast("no forward-mode SSH target to declare")
	if dirCalls != 0 {
		t.Fatalf("runtime directory read %d times", dirCalls)
	}
	if _, err := os.Lstat(r.paths.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("config directory created: %v", err)
	}

	run = rextest.PrivateDir(t)
	r = newRexHarness(t, rexSetup{cfg: native, run: run, file: golden, hosts: loaded, opts: []harnessOpt{withSSH(env(run))}})
	r.w()
	p := r.expectConfirm("Write the Rex app hosts file")
	if p.Body != r.writeConfirmBody() {
		t.Fatalf("body %q", p.Body)
	}
	r.accept()
	header := strings.Join(strings.SplitN(string(golden(run)), "\n", 3)[:2], "\n") + "\n\n"
	if got, _ := r.fileText(); got != header {
		t.Fatalf("file %q, want %q", got, header)
	}
	r.expectConfirm("Reload the local Rex server")
}

// TestRexHostsUnavailable (H50, H55, A1): no environment, a runtime
// directory error or REX_SERVER turn the feature off without touching any
// file.
func TestRexHostsUnavailable(t *testing.T) {
	h := newHarness(t, withConfig(rexCfg), withSSH(rextest.SSHEnv(rextest.PrivateDir(t), &rextest.FakeSockets{})), withRexHostsFile())
	h.focus(PanelTargets)
	h.dispatch(PanelTargets, "writeRexHosts")
	if p := h.ctrl.Popups.Top(); p == nil || p.Kind != PopupError || p.Body != "the Rex app hosts file is not available: it needs lazyrex's environment" {
		t.Fatalf("popup %+v", p)
	}
	st := NewState()
	st.TargetName, st.Transport = "edge", fakeGUILink{ep: rex.GUIEndpoint{State: rex.GUIForwarded, URI: "unix:///gui.sock"}}
	st.RexHosts, st.RexHostsFile = h.st.RexHosts, h.st.RexHostsFile
	if out := RenderStatus(st); strings.Contains(out, "Rex app host") || strings.Contains(out, "rex hosts add") {
		t.Fatalf("status tab:\n%s", out)
	}

	for _, tc := range []struct {
		name string
		s    rexSetup
		want string
	}{
		{"runtime directory", rexSetup{opts: []harnessOpt{withSSH(&rex.SSHEnv{Dir: func() (string, error) { return "", errors.New("no runtime dir") },
			Sockets: &rextest.FakeSockets{}, PID: 42, Probes: &rex.SSHProbes{}})}}, "the Rex app hosts file is not available: runtime directory: no runtime dir"},
		{"REX_SERVER", rexSetup{env: map[string]string{"REX_SERVER": "unix:///tmp/other.sock"}}, "the Rex app hosts file is not available: REX_SERVER is set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRexHarness(t, tc.s)
			r.fr.Reset()
			r.w()
			r.expectError(tc.want)
			if len(r.fr.Calls()) != 0 {
				t.Fatalf("calls:\n%s", r.dumpCalls())
			}
			if _, err := os.Lstat(filepath.Join(r.home, ".config")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("touched the config directory: %v", err)
			}
		})
	}
}

// TestRexHostsAddTarget (H52): adding an SSH target makes the file out of
// date, with no prompt.
func TestRexHostsAddTarget(t *testing.T) {
	r := newRexHarness(t, rexSetup{file: golden, hosts: loaded})
	r.selectTarget("local")
	r.dispatch(PanelTargets, "addTarget")
	r.choose("ssh: another destination…")
	r.answer("a")
	r.acceptPrefilled()
	r.choosePrefix("forward:")
	if p := r.ctrl.Popups.Top(); p != nil {
		t.Fatalf("popup %+v", p)
	}
	r.expectStatus("edge", block(r.head("edge-lazyrex", "out of date"),
		"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.", works("edge")))
}

// TestRexHostsTwoLazyrex (H53): two lazyrex with the same config and runtime
// directory agree on the file; another config sees it out of date, never
// foreign.
func TestRexHostsTwoLazyrex(t *testing.T) {
	a := newRexHarness(t, rexSetup{})
	a.w()
	a.accept()
	a.ctrl.Popups.Cancel()
	b := newRexHarness(t, rexSetup{run: a.run, home: a.home})
	b.expectStatus("edge", a.status("edge"))
	b.fr.Reset()
	b.w()
	b.expectConfirm("Reload the local Rex server")

	c := newRexHarness(t, rexSetup{run: a.run, home: a.home, cfg: "targets:\n  - name: edge\n    kind: ssh\n    ssh_host: user@edge\n"})
	c.expectStatus("edge", block(c.head("edge-lazyrex", "out of date"),
		"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.", works("edge")))
	c.w()
	if p := c.expectConfirm("Write the Rex app hosts file"); p.Body != c.writeConfirmBody(c.edgeLine()) {
		t.Fatalf("body %q", p.Body)
	}
}

// TestRexHostsSanitized (H54): paths reach the confirmation without control
// characters.
func TestRexHostsSanitized(t *testing.T) {
	home := rextest.PrivateDir(t)
	xdg := filepath.Join(home, "x\x1b[31mcfg")
	if err := os.MkdirAll(xdg, 0o700); err != nil {
		t.Fatal(err)
	}
	r := newRexHarness(t, rexSetup{home: home, env: map[string]string{"XDG_CONFIG_HOME": xdg}})
	r.w()
	p := r.expectConfirm("Write the Rex app hosts file")
	if strings.Contains(p.Body, "\x1b") || !strings.Contains(p.Body, r.edgeLine()) {
		t.Fatalf("body %q", p.Body)
	}
	if strings.Contains(r.status("edge"), "\x1b") {
		t.Fatalf("status %q", r.status("edge"))
	}
}

// TestRexHostsMissingDirectory (H65, A11): Status reads a missing config
// directory as an absent file and creates nothing.
func TestRexHostsMissingDirectory(t *testing.T) {
	r := newRexHarness(t, rexSetup{})
	r.expectStatus("edge", block(r.head("edge-lazyrex", "not written yet"),
		"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.", works("edge")))
	if _, err := os.Lstat(filepath.Join(r.home, ".config")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("created: %v", err)
	}
	if err := os.MkdirAll(r.paths.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(r.paths.Dir, 0o777); err != nil {
		t.Fatal(err)
	}
	r.dispatch(PanelTargets, "refresh")
	if got := r.status("edge"); !strings.HasPrefix(got, "Rex app host none: ") {
		t.Fatalf("status %q", got)
	}
	r.fr.Reset()
	r.w()
	if p := r.ctrl.Popups.Top(); p == nil || p.Kind != PopupError || !strings.HasPrefix(p.Body, "the Rex app hosts file is not available: ") {
		t.Fatalf("popup %+v", p)
	}
	if r.countContaining("config", "check") != 0 {
		t.Fatalf("calls:\n%s", r.dumpCalls())
	}
}
