package gui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// xMenu opens the x menu of panel p.
func (h *harness) xMenu(p Panel) {
	h.t.Helper()
	h.ctrl.Popups.Clear()
	h.focus(p)
	h.dispatch(p, "menu")
	if top := h.top(); top.Kind != PopupMenu {
		h.t.Fatalf("x opened %v, want a menu", top.Kind)
	}
}

// serverActions opens Targets x, then its server action list.
func (h *harness) serverActions() {
	h.t.Helper()
	h.xMenu(PanelTargets)
	h.chooseContaining("run a server action")
}

// clientActions opens Clients a on client:02app.
func (h *harness) clientActions() {
	h.t.Helper()
	h.ctrl.Popups.Clear()
	h.focus(PanelClients)
	h.dispatch(PanelClients, "runClientAction")
}

func noControls(s string) bool { return !strings.ContainsAny(s, "\x1b\x07") }

func TestTabsKeymapActions(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelTargets)
	h.fr.Reset()
	h.dispatch(PanelTargets, "nextTab")
	if h.st.Tab[PanelTargets] != TabKeymap {
		t.Fatalf("Targets tab = %d", h.st.Tab[PanelTargets])
	}
	if h.count("keymap --json") != 1 || h.count("actions --json") != 1 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
	c := h.st.Main.Content
	if !containsAll(c, "ctrl+a c", "window.new", "split_right [confirm]", "pairDevice [denied]", "update_status [confirm]") {
		t.Fatalf("Keymap & Actions tab:\n%s", c)
	}
	h.focus(PanelClients)
	h.fr.Reset()
	h.dispatch(PanelClients, "nextTab")
	if h.st.Tab[PanelClients] != TabClientActions || h.count("-C client:02app actions --json") != 1 {
		t.Fatalf("Clients tab %d; calls:\n%s", h.st.Tab[PanelClients], h.dumpCalls())
	}
	c = h.st.Main.Content
	if !containsAll(c, "pairDevice [denied]", "session.select [allowed]", "openFile [confirm]", "hostile") || !noControls(c) {
		t.Fatalf("Clients Actions tab:\n%q", c)
	}
}

func TestXMenuDenied(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"pairDevice", "install_update", "updateNow"} {
		h.serverActions()
		found := false
		for _, l := range menuLabels(h.top()) {
			if strings.HasPrefix(l, name+" [denied]") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s not tagged [denied]: %q", name, menuLabels(h.top()))
		}
		h.chooseAction(name)
		h.expectRefusal("")
		if h.doCalls() != 0 {
			t.Fatalf("%s: do ran", name)
		}
	}
}

func TestXMenuConfirm(t *testing.T) {
	cases := []struct {
		panel Panel
		item  string
		call  string
	}{
		{PanelTargets, "stop server", "server stop"},
		{PanelBlocks, "close block", "block close -s session:01aaaa block:01b1"},
	}
	for _, c := range cases {
		h := newHarness(t)
		h.xMenu(c.panel)
		h.chooseContaining(c.item)
		if p := h.top(); p.Kind != PopupConfirm {
			t.Fatalf("%s: popup %+v", c.item, p)
		}
		if h.count(c.call) != 0 {
			t.Fatalf("%s ran before confirmation", c.call)
		}
		h.ctrl.Popups.Accept("")
		if h.count(c.call) != 1 {
			t.Fatalf("%s did not run once confirmed", c.call)
		}
	}
	h := newHarness(t)
	h.serverActions()
	h.chooseAction("mystery")
	if p := h.top(); p.Kind != PopupConfirm || p.Body != G+"do mystery" {
		t.Fatalf("mystery: popup %+v", p)
	}
	if h.doCalls() != 0 {
		t.Fatalf("mystery ran before confirmation")
	}
	h.ctrl.Popups.Accept("")
	if h.count("do mystery") != 1 {
		t.Fatalf("mystery did not run once confirmed; calls:\n%s", h.dumpCalls())
	}
}

func TestXMenuNativeNoStart(t *testing.T) {
	h := newHarness(t, withTransport(rex.Native{Server: "alias"}))
	h.xMenu(PanelTargets)
	for _, l := range menuLabels(h.top()) {
		if strings.Contains(l, "start server") {
			t.Fatalf("start server offered on a native target: %q", menuLabels(h.top()))
		}
	}
	if !strings.Contains(strings.Join(menuLabels(h.top()), "\n"), "stop server") {
		t.Fatalf("x menu: %q", menuLabels(h.top()))
	}
}

func TestXMenuItemsDispatch(t *testing.T) {
	h := newHarness(t)
	for _, p := range sidePanels {
		h.xMenu(p)
		labels := menuLabels(h.top())
		for _, b := range h.ctrl.Bindings(p) {
			if b.Global {
				continue
			}
			n := 0
			for _, l := range labels {
				if strings.HasSuffix(l, "  "+b.Desc) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%v: action %s (%s) appears %d times in %q", p, b.Action, b.Desc, n, labels)
			}
		}
		for i := range labels {
			h.xMenu(p)
			h.top().Selected = i
			h.ctrl.Popups.Accept("")
		}
	}
	h.focus(PanelBlocks)
	h.focus(PanelMain)
	h.dispatch(PanelMain, "menu")
	if p := h.top(); p.Kind != PopupMenu || !strings.Contains(strings.Join(menuLabels(p), "\n"), "close block") {
		t.Fatalf("x in the main view: %+v", p)
	}
	h.ctrl.Popups.Clear()
	h.focus(PanelLog)
	h.dispatch(PanelLog, "menu")
	if p := h.top(); p.Kind != PopupMenu || !strings.Contains(strings.Join(menuLabels(p), "\n"), "resize pane") {
		t.Fatalf("x in the command log: %+v", p)
	}
}

func TestServerActionForm(t *testing.T) {
	want := `{"block_id":"block:01b1","direction":"right","ratio":30}`
	fill := func(h *harness) {
		h.t.Helper()
		h.serverActions()
		h.chooseAction("split_right")
		h.choose("right")
		if got := h.acceptPrefilled(); got != "block:01b1" {
			h.t.Fatalf("block_id prefilled with %q", got)
		}
	}
	h := newHarness(t)
	fill(h)
	h.answer("30")
	p := h.top()
	if wantBody := fmt.Sprintf("%sdo split_right --args <redacted %d bytes>", G, len(want)); p.Kind != PopupConfirm || p.Body != wantBody {
		t.Fatalf("popup %+v, want a confirmation %q", p, wantBody)
	}
	if h.doCalls() != 0 {
		t.Fatalf("ran before confirmation")
	}
	h.ctrl.Popups.Accept("")
	if n := h.count("do split_right --args " + want); n != 1 {
		t.Fatalf("split_right ran %d times; calls:\n%s", n, h.dumpCalls())
	}

	hs := newHarness(t, withSelf("block:01b1"))
	fill(hs)
	hs.fr.Reset()
	hs.answer("30")
	hs.expectRefusal("block:01b1")
	if n := len(hs.fr.Calls()); n != 0 {
		t.Fatalf("%d calls after the form:\n%s", n, hs.dumpCalls())
	}
}

func TestServerActionClientPrefix(t *testing.T) {
	for _, name := range []string{"pane.split", "session.select"} {
		h := newHarness(t)
		h.serverActions()
		h.chooseAction(name)
		for h.top().Kind == PopupPrompt {
			h.acceptPrefilled()
		}
		h.expectRefusal("Clients")
		if h.doCalls() != 0 {
			t.Fatalf("%s: do ran", name)
		}
	}
}

func TestNoScriptsFromSurfaces(t *testing.T) {
	h := newHarness(t)
	for _, line := range []string{"do ./x.lua", "do a/b k=v"} {
		h.palette(line)
		h.expectRefusal("")
	}
	for _, name := range []string{"scripts/evil.lua", "a/b"} {
		h.serverActions()
		h.chooseAction(name)
		h.expectRefusal("")
	}
	h.clientActions()
	h.chooseAction("x.lua")
	h.expectRefusal("")
	if h.doCalls() != 0 {
		t.Fatalf("do ran; calls:\n%s", h.dumpCalls())
	}
}

func TestClientActionForm(t *testing.T) {
	h := newHarness(t)
	h.clientActions()
	h.chooseAction("openFile")
	h.answer("/x")
	h.answer("7")
	h.choose("edit")
	p := h.top()
	if p.Kind != PopupConfirm || p.Body != G+"-C client:02app do openFile --args <redacted 36 bytes>" {
		t.Fatalf("popup %+v", p)
	}
	if h.doCalls() != 0 {
		t.Fatalf("ran before confirmation")
	}
	h.ctrl.Popups.Accept("")
	if n := h.count(`-C client:02app do openFile --args {"line":7,"mode":"edit","path":"/x"}`); n != 1 {
		t.Fatalf("openFile ran %d times; calls:\n%s", n, h.dumpCalls())
	}
}

func TestClientActionFormValidates(t *testing.T) {
	h := newHarness(t)
	h.clientActions()
	h.chooseAction("openFile")
	h.answer("/x")
	h.answer("seven")
	if p := h.top(); p.Kind != PopupError || !strings.Contains(p.Body, "line") {
		t.Fatalf("popup %+v, want an error about line", p)
	}
	h.clientActions()
	h.chooseAction("openFile")
	h.answer("")
	if p := h.top(); p.Kind != PopupError || !strings.Contains(p.Body, "path") {
		t.Fatalf("popup %+v, want an error about the required path", p)
	}
	if h.doCalls() != 0 {
		t.Fatalf("do ran")
	}
}

func TestClientActionDenied(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"installUpdate", "pairDevice", "checkForUpdates"} {
		h.clientActions()
		h.chooseAction(name)
		h.expectRefusal("")
	}
	if h.doCalls() != 0 {
		t.Fatalf("do ran")
	}
	ha := newHarness(t, withAllow("checkForUpdates"))
	ha.clientActions()
	ha.chooseAction("checkForUpdates")
	if p := ha.top(); p.Kind != PopupConfirm {
		t.Fatalf("allow-listed checkForUpdates: popup %+v", p)
	}
}

func TestClientActionConfirm(t *testing.T) {
	h := newHarness(t)
	h.clientActions()
	h.chooseAction("closeWindow")
	if p := h.top(); p.Kind != PopupConfirm || p.Body != G+"-C client:02app do closeWindow" {
		t.Fatalf("closeWindow: popup %+v", p)
	}
	hs := newHarness(t, withSelf("block:01b1"))
	hs.clientActions()
	hs.chooseAction("pane.zoom")
	if got := hs.acceptPrefilled(); got != "block:01b1" {
		t.Fatalf("block_id prefilled with %q", got)
	}
	hs.expectRefusal("block:01b1")
	if hs.doCalls() != 0 {
		t.Fatalf("do ran")
	}
}

func TestSchemaSanitized(t *testing.T) {
	h := newHarness(t)
	h.clientActions()
	for _, l := range append(menuLabels(h.top()), h.top().Title) {
		if !noControls(l) {
			t.Fatalf("action menu label %q", l)
		}
	}
	h.chooseAction("hostile")
	p := h.top()
	if p.Kind != PopupMenu || !noControls(p.Title) || !strings.Contains(p.Title, "kind") {
		t.Fatalf("field popup %+v", p)
	}
	for _, l := range menuLabels(p) {
		if !noControls(l) {
			t.Fatalf("field menu label %q", l)
		}
	}
	h.choose("ok")
	if p := h.top(); p.Kind != PopupConfirm || !noControls(p.Title) {
		t.Fatalf("popup %+v", p)
	}

	bad := newHarness(t)
	bad.fr.On("-C", "client:02app", "actions", "--json").Return(rextest.FixtureString("client_actions_badschema.json"), "", 0)
	bad.clientActions()
	bad.chooseAction("bad")
	if p := bad.top(); p.Kind != PopupError || !strings.Contains(p.Body, "unusable schema") {
		t.Fatalf("bad schema: popup %+v", p)
	}
	if bad.doCalls() != 0 {
		t.Fatalf("do ran")
	}
}

func TestPopupTitlesSanitized(t *testing.T) {
	pp := NewPopups(func() time.Time { return time.Unix(0, 0) })
	pp.Prompt("a\x1b]0;x\x07b", "", nil)
	pp.Confirm("c\x1b[2Jd", "body", nil)
	pp.Menu("e\x1bf", []MenuItem{{Label: "g\x1b[31mh\x07"}})
	for pp.Top() != nil {
		p := pp.Top()
		if !noControls(p.Title) {
			t.Errorf("%v title %q", p.Kind, p.Title)
		}
		for _, l := range menuLabels(p) {
			if !noControls(l) {
				t.Errorf("menu label %q", l)
			}
		}
		pp.Cancel()
	}
}

func TestShowInAppArgv(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelSessions)
	h.fr.Reset()
	h.dispatch(PanelSessions, "showInApp")
	if p := h.ctrl.Popups.Top(); p != nil {
		t.Fatalf("popup %+v", p)
	}
	if n := h.count("-C client:02app do session.select session_id=session:01aaaa"); n != 1 {
		t.Fatalf("session.select ran %d times; calls:\n%s", n, h.dumpCalls())
	}
	if got := h.ctrl.Popups.ToastText(); got != "shown in Rex.app" {
		t.Fatalf("toast %q", got)
	}
}

func TestShowInAppGuards(t *testing.T) {
	cliOnly := `{"clients":[{"client_id":"client:01cli","info":{"kind":"cli","name":"rex"}}]}`
	h := newHarness(t)
	h.fr.On("client", "ls", "--all", "--json").Return(cliOnly, "", 0)
	h.focus(PanelSessions)
	h.dispatch(PanelSessions, "showInApp")
	if p := h.top(); p.Kind != PopupError || !strings.Contains(p.Body, "no Rex app is connected") {
		t.Fatalf("no app: popup %+v", p)
	}

	two := strings.Replace(rextest.FixtureString("client_ls_all.json"), `"clients": [`,
		`"clients": [{"client_id":"client:03app","info":{"kind":"APP","platform":"linux_amd64","name":"Studio","version":"2"}},`, 1)
	h2 := newHarness(t)
	h2.fr.On("client", "ls", "--all", "--json").Return(two, "", 0)
	h2.fr.On("-C", "client:03app", "actions", "--json").Return(rextest.FixtureString("client_actions.json"), "", 0)
	h2.focus(PanelSessions)
	h2.dispatch(PanelSessions, "showInApp")
	if p := h2.top(); p.Kind != PopupMenu || len(p.Items) != 2 {
		t.Fatalf("two apps: popup %+v", p)
	}
	h2.chooseContaining("client:03app")
	if n := h2.count("-C client:03app do session.select session_id=session:01aaaa"); n != 1 {
		t.Fatalf("session.select on the chosen app ran %d times; calls:\n%s", n, h2.dumpCalls())
	}

	noSelect := strings.Replace(rextest.FixtureString("client_actions.json"), `"name": "session.select"`, `"name": "session.other"`, 1)
	h3 := newHarness(t)
	h3.fr.On("-C", "client:02app", "actions", "--json").Return(noSelect, "", 0)
	h3.focus(PanelSessions)
	h3.dispatch(PanelSessions, "showInApp")
	if p := h3.top(); p.Kind != PopupError || !strings.Contains(p.Body, "does not offer session.select") {
		t.Fatalf("no session.select: popup %+v", p)
	}
	if h3.doCalls() != 0 {
		t.Fatalf("do ran")
	}
}

func TestResizeArgv(t *testing.T) {
	h := newHarness(t)
	h.fr.Reset()
	h.xMenu(PanelBlocks)
	h.chooseContaining("resize pane")
	if got := h.acceptPrefilled(); got != "session:01aaaa" {
		t.Fatalf("session_id prefilled with %q", got)
	}
	if got := h.acceptPrefilled(); got != "block:01b1" {
		t.Fatalf("block_id prefilled with %q", got)
	}
	h.choose("right")
	h.answer("5")
	if p := h.ctrl.Popups.Top(); p != nil {
		t.Fatalf("popup left open: %+v", p)
	}
	calls := h.dumpCalls()
	describe := strings.Index(calls, G+"api describe session.resize_pane --json")
	call := strings.Index(calls, G+`api call -s session:01aaaa session.resize_pane {"block_id":"block:01b1","delta":5,"direction":"right","session_id":"session:01aaaa"}`)
	if describe < 0 || call < describe {
		t.Fatalf("calls:\n%s", calls)
	}
	if !strings.Contains(calls[call:], "session inspect session:01aaaa --json") {
		t.Fatalf("no refresh after resize:\n%s", calls)
	}

	hf := newHarness(t)
	hf.fr.On("api", "describe", "session.resize_pane", "--json").Return("", "boom", 1)
	hf.xMenu(PanelBlocks)
	hf.chooseContaining("resize pane")
	if p := hf.top(); p.Kind != PopupError || !strings.Contains(p.Body, "cannot read the schema of session.resize_pane") {
		t.Fatalf("failed describe: popup %+v", p)
	}
	hn := newHarness(t)
	hn.fr.On("api", "describe", "session.resize_pane", "--json").Return(`{"name":"session.resize_pane","input_schema":{"type":"object"}}`, "", 0)
	hn.xMenu(PanelBlocks)
	hn.chooseContaining("resize pane")
	if p := hn.top(); p.Kind != PopupError || !strings.Contains(p.Body, "unknown schema; use the palette") {
		t.Fatalf("schema without properties: popup %+v", p)
	}
}
