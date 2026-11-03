package gui

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/config"
	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

const studioConfig = "targets:\n  - name: studio\n    kind: native\n    server: studio\n"

const remoteLs = `{"sessions":[{"session_id":"session:09zzzz","label":"remote"}]}`

const N = G + "-S studio "

func event(t *testing.T, line string) rex.Event {
	t.Helper()
	e, err := rex.ParseEvent([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (h *harness) rowNames() []string {
	var out []string
	for _, r := range h.st.Targets {
		out = append(out, r.Target.Name)
	}
	return out
}

func (h *harness) row(name string) string {
	h.t.Helper()
	for i, r := range h.st.Targets {
		if strings.EqualFold(r.Target.Name, name) {
			return h.st.Rows(PanelTargets)[i]
		}
	}
	h.t.Fatalf("no row %q in %q", name, h.rowNames())
	return ""
}

func (h *harness) callsWith(token string) int {
	n := 0
	for _, c := range h.fr.Calls() {
		if slices.Contains(c, token) {
			n++
		}
	}
	return n
}

func (h *harness) noPopupOrToast() {
	h.t.Helper()
	if p := h.ctrl.Popups.Top(); p != nil {
		h.t.Fatalf("popup %+v", p)
	}
	if toast := h.ctrl.Popups.ToastText(); toast != "" {
		h.t.Fatalf("toast %q", toast)
	}
}

func (h *harness) expectError(part string) {
	h.t.Helper()
	p := h.ctrl.Popups.Top()
	if p == nil || p.Kind != PopupError || !strings.Contains(p.Body, part) {
		h.t.Fatalf("popup = %+v, want an error containing %q", p, part)
	}
}

func expectNoControls(t *testing.T, what, s string) {
	t.Helper()
	if strings.ContainsAny(s, "\x1b\x07") {
		t.Fatalf("%s holds ESC or BEL: %q", what, s)
	}
}

// statusColor returns the color of a Status tab's gui endpoint line, "" when
// there is none, and fails on any other control: the only escapes allowed are
// one fixed color at the start of that line and one reset at its end.
func statusColor(t *testing.T, what, s string) string {
	t.Helper()
	color := ""
	for _, line := range strings.Split(s, "\n") {
		if !strings.ContainsAny(line, "\x1b\x07\u009b") {
			continue
		}
		c := ""
		for _, k := range []string{sgrGreen, sgrYellow, sgrRed} {
			if strings.HasPrefix(line, k+"gui endpoint  ") {
				c = k
			}
		}
		inner, reset := strings.CutSuffix(strings.TrimPrefix(line, c), sgrReset)
		if c == "" || !reset || color != "" || strings.ContainsAny(inner, "\x1b\x07\u009b") {
			t.Fatalf("%s holds a control outside the gui endpoint color: %q", what, line)
		}
		color = c
	}
	return color
}

// TestNativeInitialLoad (1l): version and hosts list go to the home server,
// everything else through -S.
func TestNativeInitialLoad(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig), withInitial("studio"))
	h.expectCall("version --json")
	h.expectCall("hosts list --json")
	for _, w := range []string{"-S studio whoami --json", "-S studio server status --json", "-S studio ls --json", "-S studio client ls --all --json"} {
		h.expectCall(w)
	}
	for _, c := range h.fr.Calls() {
		if (slices.Contains(c, "version") || slices.Contains(c, "hosts")) && slices.Contains(c, "-S") {
			t.Fatalf("home call through -S: %q", c)
		}
	}
	if h.st.TargetName != "studio" || h.st.Status.State != rex.StateOnline {
		t.Fatalf("target %q, status %v", h.st.TargetName, h.st.Status.State)
	}
	if !strings.HasPrefix(Footer(h.st, nil), "studio: online") {
		t.Fatalf("footer = %q", Footer(h.st, nil))
	}
	if got := h.rowNames(); !slices.Equal(got, []string{"local", "studio"}) {
		t.Fatalf("rows = %q", got)
	}
	if row := h.row("studio"); !strings.HasPrefix(row, "*") {
		t.Fatalf("active row not marked: %q", row)
	}
}

// TestConfigRows (C1).
func TestConfigRows(t *testing.T) {
	h := newHarness(t, withConfig("targets:\n  - kind: local\n    name: home\n  - kind: native\n    name: lab\n    server: lab.ts.net\n"))
	if got := h.rowNames(); !slices.Equal(got, []string{"home", "lab"}) {
		t.Fatalf("rows = %q", got)
	}
	if row := h.row("home"); !containsAll(row, "*", "home", "local", "online") {
		t.Fatalf("home row = %q", row)
	}
	if row := h.row("lab"); !containsAll(row, "lab", "native", "unknown") || strings.HasPrefix(row, "*") {
		t.Fatalf("lab row = %q", row)
	}
	if !strings.HasPrefix(Footer(h.st, nil), "home: online") {
		t.Fatalf("footer = %q", Footer(h.st, nil))
	}
	if h.countContaining("-S") != 0 {
		t.Fatalf("an inactive target was read; calls:\n%s", h.dumpCalls())
	}
}

// TestActivateSwitches (A1).
func TestActivateSwitches(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig))
	h.fr.On("-S", "studio", "ls", "--json").Return(remoteLs, "", 0)
	h.focus(PanelSessions)
	h.dispatch(PanelSessions, "down")
	if h.st.Lists[PanelSessions].Selected != 1 {
		t.Fatalf("setup: selection %d", h.st.Lists[PanelSessions].Selected)
	}
	gen := h.ctrl.targetGen()
	h.activateTarget("studio")
	if h.ctrl.targetGen() <= gen {
		t.Fatalf("generation not bumped")
	}
	if n, ok := h.st.Transport.(rex.Native); !ok || n.Server != "studio" {
		t.Fatalf("transport = %#v", h.st.Transport)
	}
	if h.st.TargetName != "studio" || h.count("-S studio ls --json") != 1 {
		t.Fatalf("target %q; calls:\n%s", h.st.TargetName, h.dumpCalls())
	}
	if rows := h.rows(PanelSessions); len(rows) != 1 || !strings.Contains(rows[0], "remote") {
		t.Fatalf("sessions = %q", rows)
	}
	for _, p := range []Panel{PanelSessions, PanelWindows, PanelBlocks, PanelClients} {
		if h.st.Lists[p].Selected != 0 {
			t.Fatalf("%v selection kept: %d", p, h.st.Lists[p].Selected)
		}
	}
	if !strings.HasPrefix(h.row("studio"), "*") || strings.HasPrefix(h.row("local"), "*") {
		t.Fatalf("rows = %q", h.rows(PanelTargets))
	}
	if !strings.HasPrefix(Footer(h.st, nil), "studio: online") {
		t.Fatalf("footer = %q", Footer(h.st, nil))
	}
}

// TestActivateBeforeQueuedWorker (A3).
func TestActivateBeforeQueuedWorker(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui), withConfig(studioConfig))
	h.xMenu(PanelBlocks)
	h.chooseContaining("resize pane")
	h.acceptPrefilled()
	h.acceptPrefilled()
	h.choose("right")
	ui.hold = true
	h.answer("5")
	if len(ui.queue) != 1 {
		t.Fatalf("%d workers queued, want 1", len(ui.queue))
	}
	ui.hold = false
	h.activateTarget("studio")
	ui.drain()
	h.expectTargetRefusal()
	if n := apiCalls(h); n != 0 {
		t.Fatalf("api call ran; calls:\n%s", h.dumpCalls())
	}
}

// TestActivateDropsQueuedRefresh (A4): a refresh queued for the old target
// neither runs rex nor changes the new target's state.
func TestActivateDropsQueuedRefresh(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui), withConfig(studioConfig))
	h.fr.On("-S", "studio", "ls", "--json").Return(remoteLs, "", 0)
	ui.hold = true
	h.clk.Advance(5 * time.Second)
	h.ctrl.Tick()
	if len(ui.queue) == 0 {
		t.Fatalf("no refresh queued")
	}
	ui.hold = false
	h.activateTarget("studio")
	h.fr.Reset()
	ui.drain()
	if n := len(h.fr.Calls()); n != 0 {
		t.Fatalf("the old target's refresh ran %d calls:\n%s", n, h.dumpCalls())
	}
	if rows := h.rows(PanelSessions); len(rows) != 1 || !strings.Contains(rows[0], "remote") || h.st.TargetName != "studio" {
		t.Fatalf("sessions = %q on %q", rows, h.st.TargetName)
	}
}

// TestStreamPerConnection (A5, 4g, round 3): each target has its own stream;
// the old one is cancelled and its callbacks, even delivered late, change
// nothing and read nothing.
func TestStreamPerConnection(t *testing.T) {
	ui := &goUI{}
	h := newHarness(t, withUI(ui), withConfig(studioConfig), withStreams())
	ui.settle(t)
	if len(h.streams) != 1 {
		t.Fatalf("%d streams started, want 1", len(h.streams))
	}
	old := h.streams[0]
	if argv := old.sup.Client.Argv(cmds.Events()); slices.Contains(argv, "-S") {
		t.Fatalf("local stream argv = %q", argv)
	}
	old.sup.OnEvent(event(t, `{"type":"session_created","payload":{"session_id":"session:03cccc"}}`))
	h.activateTarget("studio")
	ui.settle(t)
	if len(h.streams) != 2 {
		t.Fatalf("%d streams started, want 2", len(h.streams))
	}
	if old.ctx.Err() == nil {
		t.Fatalf("the old stream's context is not cancelled")
	}
	if argv := h.streams[1].sup.Client.Argv(cmds.Events()); !slices.Equal(argv, strings.Fields(N+"events --all --json")) {
		t.Fatalf("new stream argv = %q", argv)
	}
	before := h.st.Status
	h.fr.Reset()
	old.sup.OnState(rex.StreamState{Up: false, Code: 4, Kind: rex.ExitUnreachable, Stderr: "Error: no Rex server is running"})
	old.sup.OnState(rex.StreamState{Up: true})
	old.sup.OnEvent(event(t, `{"type":"session_created","payload":{"session_id":"session:04dddd"}}`))
	old.sup.OnEvent(event(t, `{"type":"hosts_changed"}`))
	ui.settle(t)
	h.clk.Advance(time.Second)
	ui.settle(t)
	h.ctrl.Tick()
	ui.settle(t)
	if n := len(h.fr.Calls()); n != 0 {
		t.Fatalf("old stream callbacks ran %d calls:\n%s", n, h.dumpCalls())
	}
	if h.st.Status.State != before.State || h.st.StreamUp {
		t.Fatalf("old stream changed the new target: %+v, up %v", h.st.Status, h.st.StreamUp)
	}
	h.streams[1].sup.OnState(rex.StreamState{Up: true})
	ui.settle(t)
	if !h.st.StreamUp {
		t.Fatalf("the new stream's state is ignored")
	}
}

// TestCompletionAfterSwitch (A6).
func TestCompletionAfterSwitch(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig))
	h.openPalette()
	h.complete("api call hosts.")
	h.activateTarget("studio")
	h.openPalette()
	h.complete("api call hosts.")
	if h.count("api list --quiet") != 1 || h.count("-S studio api list --quiet") != 1 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestActivateActiveRow (A7).
func TestActivateActiveRow(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig), withStreams())
	gen := h.ctrl.targetGen()
	h.fr.Reset()
	h.activateTarget("local")
	if h.ctrl.targetGen() != gen || len(h.streams) != 1 {
		t.Fatalf("gen %d -> %d, %d streams", gen, h.ctrl.targetGen(), len(h.streams))
	}
	if h.count("whoami --json") != 1 || h.count("hosts list --json") != 1 || h.count("ls --json") != 1 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestKeymapTabInactiveRow (A8).
func TestKeymapTabInactiveRow(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig))
	h.selectTarget("studio")
	h.fr.Reset()
	h.dispatch(PanelTargets, "nextTab")
	if h.st.Tab[PanelTargets] != TabKeymap {
		t.Fatalf("tab %d", h.st.Tab[PanelTargets])
	}
	if n := len(h.fr.Calls()); n != 0 {
		t.Fatalf("keymap read for an inactive target:\n%s", h.dumpCalls())
	}
	if !strings.Contains(h.st.Main.Content, "activate studio") {
		t.Fatalf("main = %q", h.st.Main.Content)
	}
	h.selectTarget("local")
	if h.count("keymap --json") != 1 {
		t.Fatalf("active keymap not read; calls:\n%s", h.dumpCalls())
	}
}

// TestServerKeysActOnActiveOnly (A9, A11).
func TestServerKeysActOnActiveOnly(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig))
	h.selectTarget("studio")
	h.dispatch(PanelTargets, "stopServer")
	h.expectError("activate studio first")
	h.ctrl.Popups.Clear()
	h.activateTarget("studio")
	h.dispatch(PanelTargets, "stopServer")
	if p := h.top(); p.Kind != PopupConfirm || p.Body != N+"server stop" {
		t.Fatalf("popup %+v", p)
	}
	h.ctrl.Popups.Clear()
	h.selectTarget("local")
	h.dispatch(PanelTargets, "startServer")
	h.expectError("activate local first")
	if h.countContaining("server", "start") != 0 || h.countContaining("server", "stop") != 0 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestOwnBlockOnNative (A10).
func TestOwnBlockOnNative(t *testing.T) {
	h := newHarness(t, withSelf("block:01b1"), withConfig(studioConfig))
	h.activateTarget("studio")
	h.focus(PanelBlocks)
	for _, action := range []string{"closeBlock", "sendText"} {
		h.ctrl.Popups.Clear()
		h.dispatch(PanelBlocks, action)
		h.expectError("own block")
	}
	if h.countContaining("block", "close") != 0 || h.countContaining("send") != 0 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestActivateRace (A11): activations racing workers stay race-free and end
// on the last activated target.
func TestActivateRace(t *testing.T) {
	ui := &goUI{}
	h := newHarness(t, withUI(ui), withConfig(studioConfig))
	ui.settle(t)
	for i := range 10 {
		h.dispatch(PanelSessions, "refresh")
		h.activateTarget([]string{"studio", "local"}[i%2])
		h.dispatch(PanelSessions, "refresh")
	}
	ui.settle(t)
	if h.st.TargetName != "local" || h.st.Status.State != rex.StateOnline {
		t.Fatalf("target %q, status %v", h.st.TargetName, h.st.Status.State)
	}
	if u := h.fr.Unexpected(); len(u) != 0 {
		t.Fatalf("unexpected calls: %q", u)
	}
}

// TestMutationResultDroppedAfterSwitch (1a): a mutation that finished on the
// old connection lands nowhere once another target is active.
func TestMutationResultDroppedAfterSwitch(t *testing.T) {
	ui := &goUI{}
	h := newHarness(t, withUI(ui), withConfig(studioConfig))
	h.fr.On("-S", "studio", "ls", "--json").Return(remoteLs, "", 0)
	for _, f := range []struct{ fixture, cmd string }{{"session_inspect.json", "session inspect"}, {"block_ls.json", "block ls -s"}} {
		body := strings.ReplaceAll(rextest.FixtureString(f.fixture), "session:01aaaa", "session:09zzzz")
		h.fr.On(append(strings.Fields("-S studio "+f.cmd), "session:09zzzz")...).Return(body, "", 0)
	}
	ui.settle(t)
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "toggleZoom")
	ui.waitWorkers(t)
	h.activateTarget("studio")
	ui.settle(t)
	if h.st.TargetName != "studio" || len(h.st.Zoomed) != 0 {
		t.Fatalf("target %q, zoomed %v", h.st.TargetName, h.st.Zoomed)
	}
	for _, c := range h.fr.CallsContaining("-S", "studio") {
		if slices.Contains(c, "session:01aaaa") {
			t.Fatalf("the old session was read on studio: %q", c)
		}
	}
	if u := h.fr.Unexpected(); len(u) != 0 {
		t.Fatalf("unexpected calls: %q", u)
	}
}

func checkFixture(fr *rextest.FakeRunner) {
	fr.On("hosts", "check", "--json").Return(rextest.FixtureString("hosts_check.json"), "", 0)
}

func hostsHarness(t *testing.T, opts ...harnessOpt) *harness {
	t.Helper()
	fr := fixtureRunner()
	checkFixture(fr)
	return newHarness(t, append([]harnessOpt{withRunner(fr), withHosts("hosts_list_hosts.json")}, opts...)...)
}

// TestHostsDiscovery (3g, 3o).
func TestHostsDiscovery(t *testing.T) {
	h := hostsHarness(t)
	if got := h.rowNames(); !slices.Equal(got, []string{"local", "studio", "attic", "Studio Mac", "nulls"}) {
		t.Fatalf("rows = %q", got)
	}
	for _, r := range h.rows(PanelTargets) {
		expectNoControls(t, "row", r)
	}
	if n := h.count("hosts check --json"); n != 1 {
		t.Fatalf("hosts check ran %d times; calls:\n%s", n, h.dumpCalls())
	}
	if h.countContaining("-S") != 0 {
		t.Fatalf("a discovered host was read; calls:\n%s", h.dumpCalls())
	}
	if !containsAll(h.row("studio"), "studio", "native", "reachable") || !strings.Contains(h.row("attic"), "unreachable") ||
		!strings.Contains(h.row("nulls"), "down") || !strings.Contains(h.row("Studio Mac"), "unreachable") {
		t.Fatalf("rows = %q", h.rows(PanelTargets))
	}
	log := h.logText()
	for _, part := range []string{"-oProxyCommand=x", "lead", "Local", "STUDIO"} {
		if !strings.Contains(log, part) {
			t.Errorf("skipped host %q not logged:\n%s", part, log)
		}
	}
	expectNoControls(t, "log", log)
	h.selectTarget("studio")
	status := h.st.Main.Content
	if !containsAll(status, "laptop", "stored", "192.168.1.20:7297", "2 skipped", "1 unrecognized", "1 unmatched", "quantum") {
		t.Fatalf("status tab:\n%s", status)
	}
	expectNoControls(t, "status tab", status)
	if strings.Contains(status, "SECRETMSG") {
		t.Fatalf("message shown:\n%s", status)
	}
	h.selectTarget("nulls")
	if !strings.Contains(h.st.Main.Content, "connection refused") {
		t.Fatalf("nulls status:\n%s", h.st.Main.Content)
	}
	expectNoControls(t, "nulls status", h.st.Main.Content)
}

func homeStopped(fr *rextest.FakeRunner) {
	fr.On("hosts", "list", "--json").Return("", rextest.FixtureString("stderr_hosts_home_stopped.txt"), 4)
}

// TestHomeServerStopped (3h, A12).
func TestHomeServerStopped(t *testing.T) {
	fr := fixtureRunner()
	homeStopped(fr)
	checkFixture(fr)
	h := newHarness(t, withRunner(fr), withConfig(studioConfig))
	if !h.st.Hosts.HomeStopped || TargetsSubtitle(h.st) != "home server stopped" {
		t.Fatalf("hosts = %+v, subtitle %q", h.st.Hosts, TargetsSubtitle(h.st))
	}
	h.noPopupOrToast()
	if h.countContaining("hosts", "check") != 0 {
		t.Fatalf("hosts check ran; calls:\n%s", h.dumpCalls())
	}
	if got := h.rowNames(); !slices.Equal(got, []string{"local", "studio"}) {
		t.Fatalf("rows = %q", got)
	}
	if !strings.Contains(RenderStatus(h.st), "home server stopped") {
		t.Fatalf("status tab:\n%s", RenderStatus(h.st))
	}
	a, err := newApp(h.ctrl, appOptions{Headless: true, Width: 120, Height: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if err := a.redraw(); err != nil {
		t.Fatal(err)
	}
	if snap := a.g.Snapshot(); !strings.Contains(snap, "home server stopped") {
		t.Fatalf("snapshot:\n%s", snap)
	}
}

// TestHomeStoppedKeepsActive (A12): discovered rows go, the active one stays.
func TestHomeStoppedKeepsActive(t *testing.T) {
	h := hostsHarness(t)
	h.activateTarget("attic")
	homeStopped(h.fr)
	h.dispatch(PanelTargets, "refresh")
	if got := h.rowNames(); !slices.Equal(got, []string{"local", "attic"}) {
		t.Fatalf("rows = %q", got)
	}
	h.noPopupOrToast()
}

// TestHomeBackOnline (A9, A12): local coming online re-lists hosts.
func TestHomeBackOnline(t *testing.T) {
	fr := fixtureRunner()
	homeStopped(fr)
	fr.On("whoami").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	fr.On("server", "status").Return("", rextest.FixtureString("stderr_server_not_running.txt"), 1)
	h := newHarness(t, withRunner(fr))
	if !h.st.Hosts.HomeStopped || h.st.Status.State != rex.StateStopped {
		t.Fatalf("setup: %+v %v", h.st.Hosts, h.st.Status.State)
	}
	h.fr.On("whoami", "--json").Return(rextest.FixtureString("whoami.json"), "", 0)
	h.fr.On("server", "status", "--json").Return(rextest.FixtureString("server_status.json"), "", 0)
	h.fr.On("hosts", "list", "--json").Return(rextest.FixtureString("hosts_list.json"), "", 0)
	h.fr.Reset()
	h.dispatch(PanelTargets, "checkTarget")
	if h.count("hosts list --json") != 1 || h.st.Hosts.HomeStopped {
		t.Fatalf("hosts %+v; calls:\n%s", h.st.Hosts, h.dumpCalls())
	}
}

// TestHomeAway (3i).
func TestHomeAway(t *testing.T) {
	fr := fixtureRunner()
	fr.On("hosts", "list", "--json").Return(strings.Replace(rextest.FixtureString("hosts_list.json"), `"reached": true`, `"reached": false`, 1), "", 0)
	h := newHarness(t, withRunner(fr))
	if !strings.Contains(RenderStatus(h.st), "home away: hosts as last reached") {
		t.Fatalf("status tab:\n%s", RenderStatus(h.st))
	}
}

// TestHostsChangedCoalesced (3j).
func TestHostsChangedCoalesced(t *testing.T) {
	ui := &goUI{}
	h := newHarness(t, withUI(ui))
	ui.settle(t)
	h.fr.Reset()
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ctrl.HandleEvent(event(t, `{"type":"hosts_changed"}`))
		}()
	}
	ticks := 0
	tick := func() {
		ui.settle(t)
		h.ctrl.Tick()
		ticks++
		ui.settle(t)
	}
	for range 20 {
		tick()
	}
	rextest.WaitGroup(t, &wg, "the 100 hosts_changed handlers")
	for range 3 {
		tick()
	}
	list := h.count("hosts list --json")
	if list < 1 || list > ticks {
		t.Fatalf("hosts list ran %d times in %d ticks", list, ticks)
	}
	if h.countContaining("hosts", "check") != 0 || h.count("ls --json") != 0 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestHostsNotPolled (3k).
func TestHostsNotPolled(t *testing.T) {
	h := hostsHarness(t)
	for range 60 {
		h.clk.Advance(time.Second)
		h.ctrl.Tick()
	}
	if h.count("whoami --json") < 2 {
		t.Fatalf("no polling; calls:\n%s", h.dumpCalls())
	}
	if h.count("hosts list --json") != 1 || h.count("hosts check --json") != 1 {
		t.Fatalf("hosts read on the poll timer; calls:\n%s", h.dumpCalls())
	}
}

// TestRefreshRelistsHosts (3l).
func TestRefreshRelistsHosts(t *testing.T) {
	h := hostsHarness(t)
	h.dispatch(PanelSessions, "refresh")
	if h.count("hosts list --json") != 2 || h.count("hosts check --json") != 1 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestHostsListFailures (4d).
func TestHostsListFailures(t *testing.T) {
	cases := map[string]struct {
		stdout, stderr string
		code           int
		toast          string
	}{
		"exit 1":     {"", "Error: boom\nmore", 1, "boom"},
		"exit 2":     {"", "Error: unknown flag", 2, "lazyrex bug or Rex version drift"},
		"unparsable": {"not json", "", 0, "hosts"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := hostsHarness(t)
			h.fr.On("hosts", "list", "--json").Return(tc.stdout, tc.stderr, tc.code)
			h.dispatch(PanelSessions, "refresh")
			if toast := h.ctrl.Popups.ToastText(); !strings.Contains(toast, tc.toast) {
				t.Fatalf("toast %q, want %q", toast, tc.toast)
			}
			if p := h.ctrl.Popups.Top(); p != nil {
				t.Fatalf("popup %+v", p)
			}
			if got := h.rowNames(); !slices.Contains(got, "studio") || len(got) != 5 {
				t.Fatalf("previous hosts lost: %q", got)
			}
			if tc.code == 1 && TargetsSubtitle(h.st) != "hosts: error" {
				t.Fatalf("subtitle %q", TargetsSubtitle(h.st))
			}
		})
	}
}

// TestHostsListUnparsableMarksError (5): a successful but malformed hosts
// list is a hosts error, keeping the last good list.
func TestHostsListUnparsableMarksError(t *testing.T) {
	h := hostsHarness(t)
	h.fr.On("hosts", "list", "--json").Return("not json", "", 0)
	h.dispatch(PanelSessions, "refresh")
	if TargetsSubtitle(h.st) != "hosts: error" {
		t.Fatalf("subtitle %q", TargetsSubtitle(h.st))
	}
	if status := RenderStatus(h.st); !strings.Contains(status, "hosts list failed") {
		t.Fatalf("status tab:\n%s", status)
	}
	if got := h.rowNames(); len(got) != 5 {
		t.Fatalf("previous hosts lost: %q", got)
	}
	if toast := h.ctrl.Popups.ToastText(); !strings.Contains(toast, "parse hosts list") {
		t.Fatalf("toast %q", toast)
	}
	expectNoControls(t, "status tab", RenderStatus(h.st))
	expectNoControls(t, "toast", h.ctrl.Popups.ToastText())
}

// TestHostsMalformedAfterHomeStopped (5b): a malformed list that exited 0
// shows the home server answered.
func TestHostsMalformedAfterHomeStopped(t *testing.T) {
	fr := fixtureRunner()
	homeStopped(fr)
	h := newHarness(t, withRunner(fr))
	if !h.st.Hosts.HomeStopped {
		t.Fatalf("setup: %+v", h.st.Hosts)
	}
	h.fr.On("hosts", "list", "--json").Return("not json", "", 0)
	h.dispatch(PanelSessions, "refresh")
	if h.st.Hosts.HomeStopped || TargetsSubtitle(h.st) != "hosts: error" {
		t.Fatalf("hosts %+v, subtitle %q", h.st.Hosts, TargetsSubtitle(h.st))
	}
	if status := RenderStatus(h.st); !strings.Contains(status, "hosts list failed") || strings.Contains(status, "home server stopped") {
		t.Fatalf("status tab:\n%s", status)
	}
	if got := h.rowNames(); !slices.Equal(got, []string{"local"}) {
		t.Fatalf("rows = %q", got)
	}
	if toast := h.ctrl.Popups.ToastText(); !strings.Contains(toast, "parse hosts list") {
		t.Fatalf("toast %q", toast)
	}
}

// TestHostsCheckFailures (4e).
func TestHostsCheckFailures(t *testing.T) {
	cases := map[string]struct {
		stdout, stderr string
		code           int
		check          func(t *testing.T, h *harness)
	}{
		"exit 4": {"", rextest.FixtureString("stderr_hosts_home_stopped.txt"), 4, func(t *testing.T, h *harness) {
			if !h.st.Hosts.HomeStopped || TargetsSubtitle(h.st) != "home server stopped" {
				t.Fatalf("hosts %+v", h.st.Hosts)
			}
			h.noPopupOrToast()
		}},
		"exit 1": {"", "Error: boom", 1, func(t *testing.T, h *harness) {
			if toast := h.ctrl.Popups.ToastText(); !strings.Contains(toast, "boom") {
				t.Fatalf("toast %q", toast)
			}
		}},
		"empty": {`{"hosts":[]}`, "", 0, func(t *testing.T, h *harness) {
			h.noPopupOrToast()
			if !strings.Contains(h.row("studio"), "unknown") {
				t.Fatalf("studio row = %q", h.row("studio"))
			}
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fr := fixtureRunner()
			fr.On("hosts", "check", "--json").Return(tc.stdout, tc.stderr, tc.code)
			h := newHarness(t, withRunner(fr), withHosts("hosts_list_hosts.json"))
			tc.check(t, h)
		})
	}
}

func checkHomeStopped(fr *rextest.FakeRunner) {
	fr.On("hosts", "check").Return("", rextest.FixtureString("stderr_hosts_home_stopped.txt"), 4)
}

// TestHostsCheckHomeStoppedHidesHosts (3a): hosts check finding the home
// server stopped hides the discovered hosts, as hosts list does.
func TestHostsCheckHomeStoppedHidesHosts(t *testing.T) {
	fr := fixtureRunner()
	checkHomeStopped(fr)
	h := newHarness(t, withRunner(fr), withHosts("hosts_list_hosts.json"))
	if got := h.rowNames(); !slices.Equal(got, []string{"local"}) {
		t.Fatalf("rows = %q", got)
	}
	if !h.st.Hosts.HomeStopped || TargetsSubtitle(h.st) != "home server stopped" {
		t.Fatalf("hosts %+v, subtitle %q", h.st.Hosts, TargetsSubtitle(h.st))
	}
	h.noPopupOrToast()
}

// TestHostsCheckHomeStoppedKeepsActive (3b): the active discovered host
// stays listed.
func TestHostsCheckHomeStoppedKeepsActive(t *testing.T) {
	h := hostsHarness(t)
	h.activateTarget("attic")
	checkHomeStopped(h.fr)
	h.dispatch(PanelTargets, "checkTarget")
	if got := h.rowNames(); !slices.Equal(got, []string{"local", "attic"}) {
		t.Fatalf("rows = %q", got)
	}
}

// TestNativeUnreachable (4a-4c).
func TestNativeUnreachable(t *testing.T) {
	cases := map[string]struct {
		setup func(fr *rextest.FakeRunner)
		state rex.TargetState
	}{
		"exit 4": {func(fr *rextest.FakeRunner) {
			fr.On("-S", "studio", "whoami").Return("", "Error: cannot reach studio\n  caused by: timeout\n", 4)
		}, rex.StateUnreachable},
		"not running": {func(fr *rextest.FakeRunner) {
			fr.On("-S", "studio", "whoami").Return("", "Error: boom", 1)
			fr.On("-S", "studio", "server", "status").Return("", rextest.FixtureString("stderr_server_not_running.txt"), 1)
		}, rex.StateUnreachable},
		"interrupted": {func(fr *rextest.FakeRunner) {
			fr.On("-S", "studio", "whoami").Return("", "", 130)
		}, rex.StateUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fr := fixtureRunner()
			tc.setup(fr)
			h := newHarness(t, withRunner(fr), withConfig(studioConfig), withInitial("studio"))
			if h.st.Status.State != tc.state {
				t.Fatalf("state %v, want %v", h.st.Status.State, tc.state)
			}
			h.noPopupOrToast()
			if name == "exit 4" {
				if h.st.Status.Detail != "Error: cannot reach studio" || !strings.Contains(h.logText(), "caused by: timeout") {
					t.Fatalf("detail %q; log:\n%s", h.st.Status.Detail, h.logText())
				}
				if !strings.Contains(RenderStatus(h.st), "lazyrex cannot reach studio") {
					t.Fatalf("status tab:\n%s", RenderStatus(h.st))
				}
			}
		})
	}
}

// TestNativeMutationUnreachable (4f).
func TestNativeMutationUnreachable(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig), withInitial("studio"))
	h.fr.On("-S", "studio", "window", "close").Return("", "Error: studio is unreachable\nretry later", 4)
	whoami := h.count("-S studio whoami --json")
	confirmOf(PanelWindows, "closeWindow")(h)
	accept(h)
	if toast := h.ctrl.Popups.ToastText(); toast != "Error: studio is unreachable" {
		t.Fatalf("toast %q", toast)
	}
	if h.count("-S studio whoami --json") != whoami+1 {
		t.Fatalf("no recheck; calls:\n%s", h.dumpCalls())
	}
}

func mismatchRunner() *rextest.FakeRunner {
	fr := fixtureRunner()
	fr.On("-S", "studio", "server", "status", "--json").Return(`{"endpoint":"tcp://studio:7297","api_revision":10,"pid":1}`, "", 0)
	return fr
}

// TestInactiveProbe (10d, A6).
func TestInactiveProbe(t *testing.T) {
	h := newHarness(t, withRunner(mismatchRunner()), withConfig(studioConfig))
	h.selectTarget("studio")
	gen, status := h.ctrl.targetGen(), h.st.Status
	h.fr.Reset()
	h.dispatch(PanelTargets, "checkTarget")
	if h.count("-S studio whoami --json") != 1 || h.count("-S studio server status --json") != 1 || h.countContaining("ls") != 0 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
	if !strings.Contains(h.row("studio"), "api mismatch") {
		t.Fatalf("studio row = %q", h.row("studio"))
	}
	if h.ctrl.targetGen() != gen || h.st.Status.State != status.State || h.st.Status.APIMismatch || h.st.TargetName != "local" {
		t.Fatalf("active target changed: %+v", h.st.Status)
	}
	if f := Footer(h.st, nil); !strings.HasPrefix(f, "local: online") || strings.Contains(f, "mismatch") {
		t.Fatalf("footer = %q", f)
	}
	if !strings.Contains(h.st.Main.Content, "server api_revision 10") {
		t.Fatalf("status tab:\n%s", h.st.Main.Content)
	}
}

func (h *harness) targetRow(name string) TargetRow {
	h.t.Helper()
	for _, r := range h.st.Targets {
		if strings.EqualFold(r.Target.Name, name) {
			return r
		}
	}
	h.t.Fatalf("no row %q in %q", name, h.rowNames())
	return TargetRow{}
}

// TestProbeDroppedForChangedServer (2a): a probe of a server the row no
// longer reaches leaves the row alone.
func TestProbeDroppedForChangedServer(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui), withRunner(mismatchRunner()), withConfig(studioConfig))
	h.selectTarget("studio")
	ui.hold = true
	h.dispatch(PanelTargets, "checkTarget")
	if len(ui.queue) != 1 {
		t.Fatalf("%d workers queued, want 1", len(ui.queue))
	}
	h.ctrl.setConfigTargets([]rex.Target{h.ctrl.cfgTargets[0],
		{Name: "studio", Kind: rex.TargetNative, Server: "studio.lan", Source: rex.SourceConfig}})
	ui.hold = false
	ui.drain()
	r := h.targetRow("studio")
	if strings.Contains(h.row("studio"), "api mismatch") || r.Status.State != rex.StateUnknown || r.Probing {
		t.Fatalf("studio row = %q, %+v", h.row("studio"), r)
	}
}

// queueProbes probes the inactive studio row twice, leaving both workers
// queued.
func queueProbes(t *testing.T) (*harness, *queuedUI) {
	t.Helper()
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui), withConfig(studioConfig))
	h.selectTarget("studio")
	ui.hold = true
	h.dispatch(PanelTargets, "checkTarget")
	h.dispatch(PanelTargets, "checkTarget")
	if len(ui.queue) != 2 {
		t.Fatalf("%d workers queued, want 2", len(ui.queue))
	}
	return h, ui
}

// TestProbesOutOfOrder (2b): the older probe finishing last does not
// overwrite the newer one's result.
func TestProbesOutOfOrder(t *testing.T) {
	h, ui := queueProbes(t)
	ui.queue[1]()
	h.fr.On("-S", "studio", "server", "status", "--json").Return(`{"endpoint":"tcp://studio:7297","api_revision":10,"pid":1}`, "", 0)
	ui.queue[0]()
	if row := h.row("studio"); !strings.Contains(row, "online") || strings.Contains(row, "api mismatch") {
		t.Fatalf("studio row = %q", row)
	}
}

// TestStaleProbeKeepsProbing (2c): the older probe finishing first leaves
// the row probing until the newer one finishes.
func TestStaleProbeKeepsProbing(t *testing.T) {
	h, ui := queueProbes(t)
	ui.queue[0]()
	if !h.targetRow("studio").Probing {
		t.Fatalf("probing cleared while the newer probe runs")
	}
	ui.queue[1]()
	if h.targetRow("studio").Probing {
		t.Fatalf("probing left set")
	}
}

// TestActivateMismatch (10e).
func TestActivateMismatch(t *testing.T) {
	h := newHarness(t, withRunner(mismatchRunner()), withConfig(studioConfig))
	h.activateTarget("studio")
	if !strings.Contains(Footer(h.st, nil), "api mismatch") || !containsAll(RenderStatus(h.st), "server api_revision 10", "client api_revision 9") {
		t.Fatalf("footer %q; status:\n%s", Footer(h.st, nil), RenderStatus(h.st))
	}
}

// TestStartNeverOfferedOnNative (K1).
func TestStartNeverOfferedOnNative(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig), withInitial("studio"))
	h.focus(PanelTargets)
	if h.ctrl.Available(PanelTargets, "startServer") {
		t.Fatalf("start offered")
	}
	for _, b := range h.ctrl.Bindings(PanelTargets) {
		if b.Action == "startServer" {
			t.Fatalf("start in the bindings")
		}
	}
	if strings.Contains(Footer(h.st, h.ctrl.Bindings(PanelTargets)), "start server") {
		t.Fatalf("footer offers start")
	}
	h.dispatch(PanelTargets, "help")
	for _, l := range menuLabels(h.top()) {
		if strings.Contains(l, "start server") {
			t.Fatalf("help offers start: %q", l)
		}
	}
	h.ctrl.Popups.Clear()
	h.xMenu(PanelTargets)
	for _, l := range menuLabels(h.top()) {
		if strings.Contains(l, "start server") {
			t.Fatalf("x menu offers start: %q", l)
		}
	}
	h.ctrl.Popups.Clear()
	if err := h.ctrl.Dispatch(PanelTargets, "startServer"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	h.expectError("not offered")
	if h.countContaining("server", "start") != 0 {
		t.Fatalf("server start ran")
	}
}

// TestStartOfferedAfterLocal (K2, K3).
func TestStartOfferedAfterLocal(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig), withInitial("studio"))
	h.activateTarget("local")
	if !h.ctrl.Available(PanelTargets, "startServer") {
		t.Fatalf("start not offered on local")
	}
	h.dispatch(PanelTargets, "startServer")
	if p := h.top(); p.Kind != PopupConfirm || p.Body != "rex --timeout=10s server start --json" {
		t.Fatalf("popup %+v", p)
	}
	h.ctrl.Popups.Clear()
	h.selectTarget("studio")
	if h.ctrl.Available(PanelTargets, "startServer") {
		t.Fatalf("start offered with a native row selected")
	}
}

// TestKeysBoundUnfiltered (K4): every key is bound, so s works once a local
// target is active again.
func TestKeysBoundUnfiltered(t *testing.T) {
	h := newHarness(t, withConfig(studioConfig), withInitial("studio"))
	a, err := newApp(h.ctrl, appOptions{Headless: true, Width: 120, Height: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if !slices.ContainsFunc(a.boundBindings(PanelTargets), func(b Binding) bool { return b.Action == "startServer" && b.Key == "s" }) {
		t.Fatalf("s not bound: %+v", a.boundBindings(PanelTargets))
	}
}

// TestTargetKeys (K5).
func TestTargetKeys(t *testing.T) {
	bs := DefaultBindings()
	want := map[string]string{"<enter>": "activate", "<space>": "activate", "s": "startServer", "S": "stopServer", "c": "checkTarget",
		"a": "addTarget", "e": "editTarget", "d": "removeTarget"}
	got := map[string]string{}
	for _, b := range BindingsFor(bs, PanelTargets) {
		if !b.Global {
			got[b.Key] = b.Action
		}
	}
	for k, a := range want {
		if got[k] != a {
			t.Errorf("targets %s = %q, want %q", k, got[k], a)
		}
	}
	over, err := ResolveBindings(map[string]map[string]config.KeyList{"targets": {"addTarget": {"A"}}})
	if err != nil {
		t.Fatal(err)
	}
	assertNoDuplicates(t, over)
}

const editConfig = `# hand written
rex:
  timeout: 10s # keep
targets:
  - name: studio # the mac
    kind: native
    server: studio
    note: keep me
`

// TestAddTarget (G1, G7).
func TestAddTarget(t *testing.T) {
	h := newHarness(t, withConfig(editConfig))
	h.selectTarget("local")
	h.fr.Reset()
	h.dispatch(PanelTargets, "addTarget")
	h.choosePrefix("native:")
	h.answer("lab")
	h.answer("lab.ts.net")
	raw, _ := os.ReadFile(h.cfgPath)
	if !containsAll(string(raw), "name: lab", "lab.ts.net", "# hand written", "# keep", "# the mac", "note: keep me") {
		t.Fatalf("config:\n%s", raw)
	}
	if !slices.Contains(h.rowNames(), "lab") {
		t.Fatalf("rows = %q", h.rowNames())
	}
	if toast := h.ctrl.Popups.ToastText(); !strings.Contains(toast, "lab") {
		t.Fatalf("toast %q", toast)
	}
	if n := len(h.fr.Calls()); n != 0 {
		t.Fatalf("config editing ran rex:\n%s", h.dumpCalls())
	}
}

// TestAddTargetRefused (G2).
func TestAddTargetRefused(t *testing.T) {
	cases := map[string][2]string{
		"bad server":  {"lab", "-x"},
		"name local":  {"Local", "lab"},
		"duplicate":   {"STUDIO", "other"},
		"bad name":    {" lab", "lab"},
		"server ctrl": {"lab", "a\x1b[2J"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, withConfig(editConfig))
			h.fr.Reset()
			h.focus(PanelTargets)
			h.dispatch(PanelTargets, "addTarget")
			h.choosePrefix("native:")
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

// TestAddDiscoveredTarget (G3).
func TestAddDiscoveredTarget(t *testing.T) {
	h := hostsHarness(t, withConfig(editConfig))
	h.selectTarget("Studio Mac")
	h.dispatch(PanelTargets, "addTarget")
	if got := h.acceptPrefilled(); got != "Studio Mac" {
		t.Fatalf("name prefilled %q", got)
	}
	if got := h.acceptPrefilled(); got != "Studio Mac" {
		t.Fatalf("server prefilled %q", got)
	}
	c, err := config.Load(h.cfgPath, true)
	if err != nil || !slices.Contains(c.Targets, config.Target{Name: "Studio Mac", Kind: "native", Server: "Studio Mac"}) {
		t.Fatalf("config %+v, %v", c.Targets, err)
	}
	if got := h.rowNames(); !slices.Equal(got, []string{"local", "studio", "Studio Mac", "attic", "nulls"}) {
		t.Fatalf("rows = %q", got)
	}
	i, _ := rex.FindTarget(rowTargets(h.st), "Studio Mac")
	if r := h.st.Targets[i]; r.Target.Source != rex.SourceConfig || r.Target.Host == nil {
		t.Fatalf("Studio Mac row = %+v", r)
	}
}

func rowTargets(s *State) []rex.Target {
	var out []rex.Target
	for _, r := range s.Targets {
		out = append(out, r.Target)
	}
	return out
}

// TestEditTarget (G4).
func TestEditTarget(t *testing.T) {
	h := newHarness(t, withConfig(editConfig))
	h.selectTarget("studio")
	h.fr.Reset()
	h.dispatch(PanelTargets, "editTarget")
	if h.top().Initial != "studio" {
		t.Fatalf("name prefilled %q", h.top().Initial)
	}
	h.answer("mac")
	h.answer("mac.lan")
	raw, _ := os.ReadFile(h.cfgPath)
	if !containsAll(string(raw), "name: mac", "mac.lan", "note: keep me", "# the mac") || strings.Contains(string(raw), "server: studio") {
		t.Fatalf("config:\n%s", raw)
	}
	if got := h.rowNames(); !slices.Equal(got, []string{"local", "mac"}) {
		t.Fatalf("rows = %q", got)
	}
	if h.st.selectedID(PanelTargets) != "mac" {
		t.Fatalf("selection = %q", h.st.selectedID(PanelTargets))
	}
	if n := len(h.fr.Calls()); n != 0 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestEditRemoveRefused (G5).
func TestEditRemoveRefused(t *testing.T) {
	cases := []struct {
		row, initial, want string
	}{
		{"attic", "", "lazyrex never edits rex hosts"},
		{"local", "", "built in"},
		{"studio", "studio", "activate another target first"},
	}
	for _, tc := range cases {
		for _, action := range []string{"editTarget", "removeTarget"} {
			t.Run(tc.row+"/"+action, func(t *testing.T) {
				opts := []harnessOpt{withConfig(editConfig)}
				if tc.initial != "" {
					opts = append(opts, withInitial(tc.initial))
				}
				h := hostsHarness(t, opts...)
				h.selectTarget(tc.row)
				h.dispatch(PanelTargets, action)
				h.expectError(tc.want)
				if raw, _ := os.ReadFile(h.cfgPath); string(raw) != editConfig {
					t.Fatalf("config changed:\n%s", raw)
				}
			})
		}
	}
}

// TestRemoveTarget (G6).
func TestRemoveTarget(t *testing.T) {
	h := newHarness(t, withConfig(editConfig))
	h.selectTarget("studio")
	h.fr.Reset()
	h.dispatch(PanelTargets, "removeTarget")
	p := h.top()
	if p.Kind != PopupConfirm || !containsAll(p.Body, h.cfgPath, "rex hosts are not changed") {
		t.Fatalf("popup %+v", p)
	}
	h.ctrl.Popups.Cancel()
	if raw, _ := os.ReadFile(h.cfgPath); string(raw) != editConfig {
		t.Fatalf("cancel changed the config:\n%s", raw)
	}
	h.dispatch(PanelTargets, "removeTarget")
	accept(h)
	c, err := config.Load(h.cfgPath, true)
	if err != nil || len(c.Targets) != 0 {
		t.Fatalf("config %+v, %v", c.Targets, err)
	}
	if got := h.rowNames(); !slices.Equal(got, []string{"local"}) {
		t.Fatalf("rows = %q", got)
	}
	if n := len(h.fr.Calls()); n != 0 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestActivateRefusedDuringConfigEdit (4a): the row a running config edit
// changes cannot become active meanwhile.
func TestActivateRefusedDuringConfigEdit(t *testing.T) {
	cases := map[string]struct {
		open, finish func(h *harness)
	}{
		"remove": {func(h *harness) { h.dispatch(PanelTargets, "removeTarget") }, accept},
		"edit": {func(h *harness) { h.dispatch(PanelTargets, "editTarget"); h.answer("mac") },
			func(h *harness) { h.answer("mac.lan") }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ui := &queuedUI{}
			h := newHarness(t, withUI(ui), withConfig(editConfig))
			h.selectTarget("studio")
			tc.open(h)
			ui.hold = true
			tc.finish(h)
			if len(ui.queue) != 1 {
				t.Fatalf("%d workers queued, want 1", len(ui.queue))
			}
			ui.hold = false
			h.activateTarget("studio")
			h.expectError("still running")
			if h.st.TargetName != "local" {
				t.Fatalf("target %q", h.st.TargetName)
			}
			ui.drain()
			if !slices.ContainsFunc(h.st.Targets, h.st.isActive) {
				t.Fatalf("active row %q not in %q", h.st.TargetName, h.rowNames())
			}
		})
	}
}

// TestConfigEditRefusedOnceActive (4b): an edit confirmed after its row
// became active is refused.
func TestConfigEditRefusedOnceActive(t *testing.T) {
	h := newHarness(t, withConfig(editConfig))
	h.selectTarget("studio")
	h.dispatch(PanelTargets, "removeTarget")
	h.activateTarget("studio")
	accept(h)
	h.expectError("active target")
	if raw, _ := os.ReadFile(h.cfgPath); string(raw) != editConfig {
		t.Fatalf("config changed:\n%s", raw)
	}
	if !slices.Contains(h.rowNames(), "studio") {
		t.Fatalf("rows = %q", h.rowNames())
	}
}

// TestWriteConfigNotSynced (6d): a file replaced but not synced is applied,
// with an error that does not claim the config was left unchanged.
func TestWriteConfigNotSynced(t *testing.T) {
	h := newHarness(t, withConfig(editConfig))
	cfg := h.ctrl.cfg
	cfg.Targets = append(slices.Clone(cfg.Targets), config.Target{Name: "lab", Kind: "native", Server: "lab"})
	nf := h.ctrl.file
	nf.Sum = sha256.Sum256([]byte("new"))
	h.ctrl.writeConfig("added lab", "lab", "", func(config.File) (config.Config, config.File, error) {
		return cfg, nf, fmt.Errorf("%w: sync: boom", config.ErrNotSynced)
	})
	if !slices.Contains(h.rowNames(), "lab") || h.ctrl.file != nf {
		t.Fatalf("rows %q, file %+v", h.rowNames(), h.ctrl.file)
	}
	h.expectError("boom")
	if strings.Contains(h.top().Body, "config not changed") {
		t.Fatalf("popup %+v", h.top())
	}
}

// TestEditChangedOnDisk (G8).
func TestEditChangedOnDisk(t *testing.T) {
	h := newHarness(t, withConfig(editConfig))
	if err := os.WriteFile(h.cfgPath, []byte(editConfig+"# elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.focus(PanelTargets)
	h.dispatch(PanelTargets, "addTarget")
	h.choosePrefix("native:")
	h.answer("lab")
	h.answer("lab")
	h.expectError("changed on disk")
	if raw, _ := os.ReadFile(h.cfgPath); string(raw) != editConfig+"# elsewhere\n" {
		t.Fatalf("config changed:\n%s", raw)
	}
}

// TestNoControlsAnywhere (S2).
func TestNoControlsAnywhere(t *testing.T) {
	h := hostsHarness(t)
	for _, p := range sidePanels {
		for _, r := range h.st.Rows(p) {
			expectNoControls(t, p.String(), r)
		}
	}
	for _, name := range h.rowNames() {
		h.selectTarget(name)
		expectNoControls(t, "status of "+name, RenderStatus(h.st))
	}
	expectNoControls(t, "log", h.logText())
	expectNoControls(t, "footer", Footer(h.st, h.ctrl.Bindings(PanelTargets)))
	expectNoControls(t, "subtitle", TargetsSubtitle(h.st))
	expectNoControls(t, "toast", h.ctrl.Popups.ToastText())
}

// TestStartWithContext: Start runs the first load and the stream on ctx.
func TestStartWithContext(t *testing.T) {
	h := newHarness(t, withStreams())
	if len(h.streams) != 1 || h.count("whoami --json") != 1 || h.count("hosts list --json") != 1 {
		t.Fatalf("%d streams; calls:\n%s", len(h.streams), h.dumpCalls())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h2 := newHarness(t, withStreams())
	h2.ctrl.Start(ctx)
	if h2.streams[len(h2.streams)-1].ctx.Err() == nil {
		t.Fatalf("stream context not derived from Start's")
	}
}
