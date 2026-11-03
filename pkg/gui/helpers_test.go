package gui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/config"
	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

const G = "rex --autostart=false --timeout=10s "

type inlineUI struct{}

func (inlineUI) Update(f func()) { f() }
func (inlineUI) Go(f func())     { f() }

func fixtureRunner() *rextest.FakeRunner {
	fr := rextest.NewFakeRunner()
	fr.On("version", "--json").Return(rextest.FixtureString("version.json"), "", 0)
	fr.On("whoami", "--json").Return(rextest.FixtureString("whoami.json"), "", 0)
	fr.On("server", "status", "--json").Return(rextest.FixtureString("server_status.json"), "", 0)
	fr.On("ls", "--json").Return(rextest.FixtureString("ls.json"), "", 0)
	fr.On("session", "inspect", "session:01aaaa").Return(rextest.FixtureString("session_inspect.json"), "", 0)
	fr.On("session", "inspect", "session:02bbbb").Return(rextest.FixtureString("session_inspect_logs.json"), "", 0)
	fr.On("block", "ls", "-s", "session:01aaaa").Return(rextest.FixtureString("block_ls.json"), "", 0)
	fr.On("block", "ls", "-s", "session:02bbbb").Return(rextest.FixtureString("block_ls_logs.json"), "", 0)
	fr.On("client", "ls", "--all", "--json").Return(rextest.FixtureString("client_ls_all.json"), "", 0)
	fr.On("client", "inspect").Return(rextest.FixtureString("client_inspect.json"), "", 0)
	fr.On("block", "inspect").Return(rextest.FixtureString("block_inspect.json"), "", 0)
	fr.On("block", "call").Return("{}", "", 0)
	fr.On("process").Return(rextest.FixtureString("block_process.json"), "", 0)
	fr.On("modes").Return(rextest.FixtureString("block_modes_normal.json"), "", 0)
	fr.On("capture").Return("\x1b[32mhello\x1b[0m\x1b]0;title\x07\nworld\n", "", 0)
	for _, mut := range [][]string{{"new"}, {"session", "rename"}, {"kill"}, {"window", "new"}, {"window", "rename"},
		{"window", "close"}, {"window", "focus"}, {"block", "focus"}, {"block", "rename"}, {"block", "close"},
		{"zoom"}, {"split"}, {"send"}, {"send-key"}, {"server", "start"}, {"server", "stop"}} {
		fr.On(mut...).Return("{}", "", 0)
	}
	fr.On("new").Return(rextest.FixtureString("new.json"), "", 0)
	fr.On("keymap", "--json").Return(rextest.FixtureString("keymap.json"), "", 0)
	fr.On("actions", "--json").Return(rextest.FixtureString("server_actions.json"), "", 0)
	fr.On("-C", "client:02app", "actions", "--json").Return(rextest.FixtureString("client_actions.json"), "", 0)
	fr.On("api", "list", "--quiet").Return(rextest.FixtureString("api_list.txt"), "", 0)
	fr.On("api", "call").Return("{}", "", 0)
	fr.On("target.complete").Return(rextest.FixtureString("target_complete.json"), "", 0)
	fr.On("api", "describe", "session.resize_pane", "--json").Return(rextest.FixtureString("api_describe_resize_pane.json"), "", 0)
	fr.On("api", "describe", "target.complete", "--json").Return(rextest.FixtureString("api_describe_target_complete.json"), "", 0)
	fr.On("do").Return("{}", "", 0)
	fr.On("hosts", "list", "--json").Return(rextest.FixtureString("hosts_list.json"), "", 0)
	return fr
}

// queuedUI runs Update inline and, while hold is set, queues Go work until
// drain, so a test can change the state while a worker is pending.
type queuedUI struct {
	hold  bool
	queue []func()
}

func (u *queuedUI) Update(f func()) { f() }

func (u *queuedUI) Go(f func()) {
	if u.hold {
		u.queue = append(u.queue, f)
		return
	}
	f()
}

func (u *queuedUI) drain() {
	for len(u.queue) > 0 {
		f := u.queue[0]
		u.queue = u.queue[1:]
		f()
	}
}

type harness struct {
	t       *testing.T
	fr      *rextest.FakeRunner
	clk     *rextest.FakeClock
	st      *State
	ctrl    *Controller
	client  *rex.Client
	cfgPath string
	streams []streamStart
}

// streamStart is one event stream the controller started.
type streamStart struct {
	ctx context.Context
	sup *rex.Supervisor
}

type harnessOpt func(*harnessCfg)

type harnessCfg struct {
	self      string
	transport rex.Transport
	fr        *rextest.FakeRunner
	allow     []string
	ui        UI
	config    *string
	initial   *string
	hosts     string
	streams   bool
	ssh       *rex.SSHEnv
	sshHosts  []string
	sshFlag   *string
	rexHome   *string
	rexEnv    map[string]string
	plain     bool
}

// withRexEnv turns the Rex app hosts file on: lazyrex's environment is env
// and its home directory home.
func withRexEnv(home string, env map[string]string) harnessOpt {
	return func(c *harnessCfg) { c.rexHome, c.rexEnv = &home, env }
}

// withoutEnvRunner hides the runner's RunEnv from the client.
func withoutEnvRunner() harnessOpt { return func(c *harnessCfg) { c.plain = true } }

// plainRunner is a runner without RunEnv.
type plainRunner struct{ rex.Runner }

// withSSH gives SSH targets the environment env.
func withSSH(env *rex.SSHEnv) harnessOpt { return func(c *harnessCfg) { c.ssh = env } }

// withSSHHosts offers hosts as the ~/.ssh/config aliases.
func withSSHHosts(hosts ...string) harnessOpt { return func(c *harnessCfg) { c.sshHosts = hosts } }

// withSSHFlag starts on the target --ssh host would select.
func withSSHFlag(host string) harnessOpt { return func(c *harnessCfg) { c.sshFlag = &host } }

func withSelf(id string) harnessOpt            { return func(c *harnessCfg) { c.self = id } }
func withAllow(names ...string) harnessOpt     { return func(c *harnessCfg) { c.allow = names } }
func withUI(ui UI) harnessOpt                  { return func(c *harnessCfg) { c.ui = ui } }
func withTransport(t rex.Transport) harnessOpt { return func(c *harnessCfg) { c.transport = t } }
func withRunner(fr *rextest.FakeRunner) harnessOpt {
	return func(c *harnessCfg) { c.fr = fr }
}

// withConfig loads targets from a config file with body, in a temp dir.
func withConfig(body string) harnessOpt { return func(c *harnessCfg) { c.config = &body } }

// withInitial starts on the target --server NAME would select.
func withInitial(name string) harnessOpt { return func(c *harnessCfg) { c.initial = &name } }

// withHosts answers `hosts list --json` with a fixture.
func withHosts(fixture string) harnessOpt { return func(c *harnessCfg) { c.hosts = fixture } }

// withStreams records the event streams instead of running them, and starts
// the controller with Start.
func withStreams() harnessOpt { return func(c *harnessCfg) { c.streams = true } }

func newHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()
	cfg := harnessCfg{transport: rex.Local{}, ui: inlineUI{}}
	for _, o := range opts {
		o(&cfg)
	}
	fr := cfg.fr
	if fr == nil {
		fr = fixtureRunner()
	}
	if cfg.hosts != "" {
		fr.On("hosts", "list", "--json").Return(rextest.FixtureString(cfg.hosts), "", 0)
	}
	policy, warnings := rex.NewPolicy(cfg.self, cfg.allow)
	var runner rex.Runner = fr
	if cfg.plain {
		runner = plainRunner{fr}
	}
	client := &rex.Client{Runner: runner, Transport: cfg.transport, Binary: "rex", Timeout: 10 * time.Second, Policy: policy}
	clk := rextest.NewFakeClock(time.Unix(1_000_000, 0))
	st := NewState()
	h := &harness{t: t, fr: fr, clk: clk, st: st, client: client}
	o := Options{Clock: clk, Config: config.Default(), Bindings: DefaultBindings(), Warnings: warnings,
		SSH: cfg.ssh, SSHConfigHosts: cfg.sshHosts}
	if cfg.rexHome != nil {
		env, home := cfg.rexEnv, *cfg.rexHome
		o.Env = func(k string) string { return env[k] }
		o.HomeDir = func() (string, error) { return home, nil }
	}
	if cfg.config != nil || cfg.initial != nil || cfg.sshFlag != nil {
		if cfg.config != nil {
			h.cfgPath = filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(h.cfgPath, []byte(*cfg.config), 0o600); err != nil {
				t.Fatal(err)
			}
			c, f, err := config.LoadFile(h.cfgPath, true)
			if err != nil {
				t.Fatal(err)
			}
			o.Config, o.File = c, f
		}
		targets, err := ConfigTargets(o.Config)
		if err != nil {
			t.Fatal(err)
		}
		initial := ""
		if cfg.initial != nil {
			initial = *cfg.initial
		}
		all, idx, err := rex.WithFlag(targets, initial)
		if cfg.sshFlag != nil {
			all, idx, err = rex.WithSSHFlag(targets, *cfg.sshFlag)
		}
		if err != nil {
			t.Fatal(err)
		}
		if idx >= len(targets) {
			flag := all[idx]
			o.Flag = &flag
		}
		o.Targets, o.Initial = targets, all[idx].Name
	}
	if cfg.streams {
		o.StartStream = func(ctx context.Context, sup *rex.Supervisor) { h.streams = append(h.streams, streamStart{ctx, sup}) }
	}
	h.ctrl = NewController(st, client, cfg.ui, o)
	t.Cleanup(h.ctrl.Close)
	if cfg.streams {
		h.ctrl.Start(context.Background())
	} else {
		h.ctrl.LoadAll()
	}
	if u := fr.Unexpected(); len(u) != 0 {
		t.Fatalf("unexpected calls during LoadAll: %q", u)
	}
	return h
}

// selectTarget focuses the Targets panel and selects the row named name.
func (h *harness) selectTarget(name string) {
	h.t.Helper()
	h.focus(PanelTargets)
	h.selectID(PanelTargets, name)
}

// activateTarget selects the row named name and activates it.
func (h *harness) activateTarget(name string) {
	h.t.Helper()
	h.selectTarget(name)
	h.dispatch(PanelTargets, "activate")
}

// addConfigTarget adds a native config target without writing any file.
func (h *harness) addConfigTarget(name, server string) {
	h.t.Helper()
	h.ctrl.setConfigTargets(append(slices.Clone(h.ctrl.cfgTargets),
		rex.Target{Name: name, Kind: rex.TargetNative, Server: server, Source: rex.SourceConfig}))
}

func (h *harness) dispatch(p Panel, action string) {
	h.t.Helper()
	if err := h.ctrl.Dispatch(p, action); err != nil {
		h.t.Fatalf("Dispatch(%v, %s): %v", p, action, err)
	}
}

func (h *harness) focus(p Panel) {
	h.t.Helper()
	h.ctrl.Focus(p)
}

func (h *harness) selectID(p Panel, id string) {
	h.t.Helper()
	if !h.ctrl.SelectByID(p, id) {
		h.t.Fatalf("SelectByID(%v, %s) failed", p, id)
	}
}

// count returns how many recorded argv equal G + want.
func (h *harness) count(want string) int {
	n := 0
	for _, c := range h.fr.Calls() {
		if slices.Equal(c, strings.Fields(G+want)) {
			n++
		}
	}
	return n
}

func (h *harness) countContaining(tokens ...string) int {
	return len(h.fr.CallsContaining(tokens...))
}

func (h *harness) expectCall(want string) {
	h.t.Helper()
	if h.count(want) == 0 {
		h.t.Fatalf("no call %q; calls:\n%s", G+want, h.dumpCalls())
	}
}

func (h *harness) dumpCalls() string {
	var b strings.Builder
	for _, c := range h.fr.Calls() {
		b.WriteString(strings.Join(c, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func (h *harness) top() *Popup {
	h.t.Helper()
	p := h.ctrl.Popups.Top()
	if p == nil {
		h.t.Fatalf("no popup open")
	}
	return p
}

func (h *harness) answer(input string) {
	h.t.Helper()
	if h.top().Kind != PopupPrompt {
		h.t.Fatalf("top popup is %v, want a prompt", h.top().Kind)
	}
	h.ctrl.Popups.Accept(input)
}

func (h *harness) choose(label string) {
	h.t.Helper()
	p := h.top()
	if p.Kind != PopupMenu {
		h.t.Fatalf("top popup is %v, want a menu", p.Kind)
	}
	for i, it := range p.Items {
		if it.Label == label {
			p.Selected = i
			h.ctrl.Popups.Accept("")
			return
		}
	}
	h.t.Fatalf("menu %q has no item %q", p.Title, label)
}

// chooseWhere selects the first menu item whose label satisfies match.
func (h *harness) chooseWhere(what string, match func(string) bool) {
	h.t.Helper()
	p := h.top()
	if p.Kind != PopupMenu {
		h.t.Fatalf("top popup is %v (%q: %q), want a menu", p.Kind, p.Title, p.Body)
	}
	for i, it := range p.Items {
		if match(it.Label) {
			p.Selected = i
			h.ctrl.Popups.Accept("")
			return
		}
	}
	h.t.Fatalf("menu %q has no item %s; items: %q", p.Title, what, menuLabels(p))
}

func (h *harness) choosePrefix(prefix string) {
	h.t.Helper()
	h.chooseWhere("starting with "+prefix, func(l string) bool { return strings.HasPrefix(l, prefix) })
}

func (h *harness) chooseContaining(part string) {
	h.t.Helper()
	h.chooseWhere("containing "+part, func(l string) bool { return strings.Contains(l, part) })
}

// chooseAction picks an action from a menu of actions ("name [tag]  description").
func (h *harness) chooseAction(name string) {
	h.t.Helper()
	h.choosePrefix(name + " [")
}

func menuLabels(p *Popup) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, it.Label)
	}
	return out
}

// acceptPrefilled accepts the top prompt with its prefilled text.
func (h *harness) acceptPrefilled() string {
	h.t.Helper()
	initial := h.top().Initial
	h.answer(initial)
	return initial
}

// countArgv returns how many recorded argv equal argv.
func (h *harness) countArgv(argv []string) int {
	n := 0
	for _, c := range h.fr.Calls() {
		if slices.Equal(c, argv) {
			n++
		}
	}
	return n
}

func (h *harness) logText() string { return strings.Join(h.st.Log, "\n") }

// expectRefusal checks that the top popup is a refusal mentioning part, that
// it was logged, and that it is not a confirmation.
func (h *harness) expectRefusal(part string) {
	h.t.Helper()
	p := h.ctrl.Popups.Top()
	if p == nil || p.Kind != PopupError || !strings.HasPrefix(p.Body, "refused:") || !strings.Contains(p.Body, part) {
		h.t.Fatalf("popup = %+v, want a refusal containing %q", p, part)
	}
	if !strings.Contains(h.logText(), "refused:") {
		h.t.Fatalf("refusal not logged: %q", h.st.Log)
	}
}

func (h *harness) rows(p Panel) []string {
	var out []string
	all := h.st.Rows(p)
	for _, i := range h.st.Visible(p) {
		out = append(out, all[i])
	}
	return out
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
