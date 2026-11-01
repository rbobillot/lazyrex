package gui

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func TestConfirmBeforeRun(t *testing.T) {
	cases := []struct {
		name   string
		panel  Panel
		action string
		key    string
		want   string
		noAuto bool
	}{
		{name: "kill session", panel: PanelSessions, action: "killSession", want: G + "kill session:01aaaa"},
		{name: "close window", panel: PanelWindows, action: "closeWindow", want: G + "window close -s session:01aaaa window:01w1"},
		{name: "close block", panel: PanelBlocks, action: "closeBlock", want: G + "block close -s session:01aaaa block:01b1"},
		{name: "stop server", panel: PanelTargets, action: "stopServer", want: G + "server stop"},
		{name: "start server", panel: PanelTargets, action: "startServer", want: "rex --timeout=10s server start --json", noAuto: true},
		{name: "ctrl+c", panel: PanelBlocks, action: "pressKey", key: "ctrl+c", want: G + "send-key -s session:01aaaa -b block:01b1 ctrl+c"},
		{name: "ctrl+d", panel: PanelBlocks, action: "pressKey", key: "ctrl+d", want: G + "send-key -s session:01aaaa -b block:01b1 ctrl+d"},
		{name: `ctrl+\`, panel: PanelBlocks, action: "pressKey", key: `ctrl+\`, want: G + `send-key -s session:01aaaa -b block:01b1 ctrl+\`},
		{name: "ctrl+z", panel: PanelBlocks, action: "pressKey", key: "ctrl+z", want: G + "send-key -s session:01aaaa -b block:01b1 ctrl+z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.focus(c.panel)
			h.fr.Reset()
			h.dispatch(c.panel, c.action)
			if c.key != "" {
				h.choose(c.key)
			}
			p := h.top()
			if p.Kind != PopupConfirm {
				t.Fatalf("popup kind %v, want confirm", p.Kind)
			}
			if !strings.Contains(p.Body, c.want) {
				t.Fatalf("confirm body %q does not show %q", p.Body, c.want)
			}
			wantArgv := strings.Fields(c.want)
			matches := func() int {
				n := 0
				for _, call := range h.fr.Calls() {
					if slices.Equal(call, wantArgv) {
						n++
					}
				}
				return n
			}
			if matches() != 0 {
				t.Fatalf("command ran before confirmation")
			}
			h.ctrl.Popups.Accept("")
			if matches() != 1 {
				t.Fatalf("confirmed command ran %d times; calls:\n%s", matches(), h.dumpCalls())
			}
			if c.noAuto {
				for _, call := range h.fr.Calls() {
					if slices.Contains(call, "start") && slices.Contains(call, "--autostart=false") {
						t.Fatalf("server start with --autostart=false: %q", call)
					}
				}
			}
		})
	}
}

func TestConfirmCancelRunsNothing(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelSessions)
	h.dispatch(PanelSessions, "killSession")
	h.ctrl.Popups.Cancel()
	if h.countContaining("kill") != 0 || h.ctrl.Popups.Top() != nil {
		t.Fatalf("cancelled kill ran")
	}
}

func TestDispatchActions(t *testing.T) {
	type step struct {
		menu   string
		prompt *string
	}
	str := func(s string) *string { return &s }
	cases := []struct {
		name    string
		panel   Panel
		action  string
		steps   []step
		want    []string
		refresh string
	}{
		{"new session", PanelSessions, "newSession", []step{{prompt: str("api")}, {prompt: str("")}}, []string{"new api --json"}, "ls --json"},
		{"new session in dir", PanelSessions, "newSession", []step{{prompt: str("api")}, {prompt: str("/srv/api")}}, []string{"new api --cwd=/srv/api --json"}, "ls --json"},
		{"rename session", PanelSessions, "renameSession", []step{{prompt: str("renamed")}}, []string{"session rename session:01aaaa renamed"}, "ls --json"},
		{"new window", PanelWindows, "newWindow", []step{{prompt: str("w9")}}, []string{"window new -s session:01aaaa w9 --focus=false --json"}, "session inspect session:01aaaa --json"},
		{"rename window", PanelWindows, "renameWindow", []step{{prompt: str("n2")}}, []string{"window rename -s session:01aaaa window:01w1 n2"}, "session inspect session:01aaaa --json"},
		{"focus window", PanelWindows, "focusWindow", nil, []string{"window focus -s session:01aaaa window:01w1"}, ""},
		{"focus block", PanelBlocks, "focusBlock", nil, []string{"block focus -s session:01aaaa block:01b1"}, ""},
		{"split right", PanelBlocks, "splitRight", nil, []string{"split -s session:01aaaa -b block:01b1 --split=right --focus=false --json"}, "session inspect session:01aaaa --json"},
		{"split below", PanelBlocks, "splitBelow", nil, []string{"split -s session:01aaaa -b block:01b1 --split=below --focus=false --json"}, "session inspect session:01aaaa --json"},
		{"new pane form", PanelBlocks, "newPane", []step{{menu: "below"}, {prompt: str("htop -d 5")}, {prompt: str("mon")}, {prompt: str("30")}},
			[]string{"split -s session:01aaaa -b block:01b1 --split=below --ratio=30 --label=mon --focus=false --json -- htop -d 5"}, "session inspect session:01aaaa --json"},
		{"new pane defaults", PanelBlocks, "newPane", []step{{menu: "left"}, {prompt: str("")}, {prompt: str("")}, {prompt: str("")}},
			[]string{"split -s session:01aaaa -b block:01b1 --split=left --focus=false --json"}, ""},
		{"send text", PanelBlocks, "sendText", []step{{prompt: str("ls")}}, []string{"send -s session:01aaaa -b block:01b1 -- ls"}, ""},
		{"send text enter", PanelBlocks, "sendTextEnter", []step{{prompt: str("make")}},
			[]string{"send -s session:01aaaa -b block:01b1 -- make", "send-key -s session:01aaaa -b block:01b1 enter"}, ""},
		{"press enter", PanelBlocks, "pressKey", []step{{menu: "enter"}}, []string{"send-key -s session:01aaaa -b block:01b1 enter"}, ""},
		{"press other", PanelBlocks, "pressKey", []step{{menu: "other…"}, {prompt: str("shift+g")}}, []string{"send-key -s session:01aaaa -b block:01b1 shift+g"}, ""},
		{"rename block", PanelBlocks, "renameBlock", []step{{prompt: str("n3")}}, []string{"block rename -s session:01aaaa block:01b1 n3"}, "session inspect session:01aaaa --json"},
		{"check target", PanelTargets, "checkTarget", nil, []string{"whoami --json", "server status --json"}, ""},
		{"refresh", PanelSessions, "refresh", nil, []string{"whoami --json", "ls --json", "client ls --all --json"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.focus(c.panel)
			h.fr.Reset()
			h.dispatch(c.panel, c.action)
			for _, s := range c.steps {
				if s.menu != "" {
					h.choose(s.menu)
				} else {
					h.answer(*s.prompt)
				}
			}
			if p := h.ctrl.Popups.Top(); p != nil {
				t.Fatalf("popup left open: %+v", p)
			}
			calls := h.fr.Calls()
			last := -1
			for _, w := range c.want {
				idx := slices.IndexFunc(calls, func(call []string) bool { return slices.Equal(call, strings.Fields(G+w)) })
				if idx < 0 {
					t.Fatalf("no call %q; calls:\n%s", G+w, h.dumpCalls())
				}
				if idx < last {
					t.Fatalf("call %q out of order", w)
				}
				last = idx
			}
			if c.refresh != "" {
				idx := slices.IndexFunc(calls[last+1:], func(call []string) bool { return slices.Equal(call, strings.Fields(G+c.refresh)) })
				if idx < 0 {
					t.Fatalf("no refresh %q after the action; calls:\n%s", c.refresh, h.dumpCalls())
				}
			}
			if t := h.ctrl.Popups.ToastText(); strings.HasPrefix(t, "error") {
				h.t.Fatalf("error toast: %q", t)
			}
		})
	}
}

func TestDispatchStateActions(t *testing.T) {
	h := newHarness(t)
	h.dispatch(PanelTargets, "jump3")
	if h.st.Focus != PanelWindows || h.st.Side != PanelWindows {
		t.Fatalf("jump3 focus = %v", h.st.Focus)
	}
	h.dispatch(PanelWindows, "nextPanel")
	if h.st.Focus != PanelBlocks {
		t.Fatalf("nextPanel focus = %v", h.st.Focus)
	}
	h.dispatch(PanelBlocks, "prevPanel")
	h.dispatch(PanelWindows, "prevPanel")
	h.dispatch(PanelSessions, "prevPanel")
	h.dispatch(PanelTargets, "prevPanel")
	if h.st.Focus != PanelClients {
		t.Fatalf("prevPanel wrap focus = %v", h.st.Focus)
	}
	h.dispatch(PanelClients, "jumpMain")
	if h.st.Focus != PanelMain || h.st.Side != PanelClients {
		t.Fatalf("jumpMain focus = %v side = %v", h.st.Focus, h.st.Side)
	}
	h.dispatch(PanelMain, "commandLog")
	if h.st.Focus != PanelLog {
		t.Fatalf("commandLog focus = %v", h.st.Focus)
	}
	h.dispatch(PanelLog, "back")
	if h.st.Focus != PanelClients {
		t.Fatalf("back from log = %v", h.st.Focus)
	}

	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "nextTab")
	if h.st.Tab[PanelBlocks] != TabInspect {
		t.Fatalf("nextTab = %d", h.st.Tab[PanelBlocks])
	}
	h.dispatch(PanelBlocks, "nextTab")
	if h.st.Tab[PanelBlocks] != TabCapture {
		t.Fatalf("nextTab wrap = %d", h.st.Tab[PanelBlocks])
	}
	h.dispatch(PanelBlocks, "prevTab")
	if h.st.Tab[PanelBlocks] != TabInspect {
		t.Fatalf("prevTab = %d", h.st.Tab[PanelBlocks])
	}

	h.dispatch(PanelBlocks, "bottom")
	if b, _ := h.st.SelectedBlock(); b.Info.BlockID != "block:01b3" {
		t.Fatalf("bottom selected %s", b.Info.BlockID)
	}
	h.dispatch(PanelBlocks, "top")
	h.dispatch(PanelBlocks, "down")
	if b, _ := h.st.SelectedBlock(); b.Info.BlockID != "block:01b2" {
		t.Fatalf("down selected %s", b.Info.BlockID)
	}
	h.dispatch(PanelBlocks, "up")
	h.dispatch(PanelBlocks, "up")
	if b, _ := h.st.SelectedBlock(); b.Info.BlockID != "block:01b1" {
		t.Fatalf("up clamps at the top: %s", b.Info.BlockID)
	}

	h.dispatch(PanelBlocks, "toggleAll")
	if h.st.Scope != ScopeAll || len(h.rows(PanelBlocks)) != 6 {
		t.Fatalf("all blocks = %q", h.rows(PanelBlocks))
	}
	h.focus(PanelWindows)
	h.dispatch(PanelWindows, "toggleAll")
	if !h.st.WindowsAll || len(h.rows(PanelWindows)) != 3 {
		t.Fatalf("all windows = %q", h.rows(PanelWindows))
	}
	h.dispatch(PanelWindows, "toggleAll")
	h.focus(PanelSessions)
	h.dispatch(PanelSessions, "scope")
	if h.st.Scope != ScopeSession || h.st.Focus != PanelWindows {
		t.Fatalf("session scope = %v focus = %v", h.st.Scope, h.st.Focus)
	}
	h.dispatch(PanelWindows, "scope")
	if h.st.Scope != ScopeWindow || h.st.Focus != PanelBlocks {
		t.Fatalf("window scope = %v focus = %v", h.st.Scope, h.st.Focus)
	}

	h.fr.Reset()
	h.focus(PanelClients)
	h.dispatch(PanelClients, "showInspect")
	if h.st.Focus != PanelMain || h.st.Side != PanelClients || h.countContaining("client", "inspect", "client:02app", "--json") != 1 {
		t.Fatalf("client inspect not shown")
	}

	h.dispatch(PanelTargets, "help")
	if p := h.top(); p.Kind != PopupMenu || len(p.Items) == 0 {
		t.Fatalf("help popup = %+v", p)
	}
	h.ctrl.Popups.Cancel()

	if err := h.ctrl.Dispatch(PanelTargets, "quit"); !errors.Is(err, ErrQuit) {
		t.Fatalf("quit returned %v", err)
	}
}

func TestDispatchUnknownAction(t *testing.T) {
	h := newHarness(t)
	if err := h.ctrl.Dispatch(PanelBlocks, "noSuchAction"); err == nil || errors.Is(err, ErrQuit) {
		t.Fatalf("unknown action returned %v", err)
	}
	if err := h.ctrl.Dispatch(PanelSessions, "closeBlock"); err == nil {
		t.Fatalf("action dispatched on the wrong panel")
	}
}

func TestStartNotOfferedOnNative(t *testing.T) {
	h := newHarness(t, withTransport(rex.Native{Server: "alias"}))
	if h.ctrl.Available(PanelTargets, "startServer") {
		t.Fatalf("start offered on a native target")
	}
	h.focus(PanelTargets)
	h.dispatch(PanelTargets, "startServer")
	if h.countContaining("server", "start") != 0 {
		t.Fatalf("server start ran on a native target")
	}
	if p := h.top(); p.Kind != PopupError {
		t.Fatalf("no error shown: %+v", p)
	}
	for _, b := range h.ctrl.Bindings(PanelTargets) {
		if b.Action == "startServer" {
			t.Fatalf("startServer still bound")
		}
	}
	for _, item := range HelpItems(h.ctrl.Bindings(PanelTargets), PanelTargets) {
		if strings.Contains(item, "start server") {
			t.Fatalf("help lists start: %q", item)
		}
	}
	local := newHarness(t)
	if !local.ctrl.Available(PanelTargets, "startServer") {
		t.Fatalf("start not offered locally")
	}
}

func TestSelfBlockActionsRefused(t *testing.T) {
	for _, action := range []string{"sendText", "sendTextEnter", "pressKey", "closeBlock"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t, withSelf("block:01b1"))
			h.focus(PanelBlocks)
			h.dispatch(PanelBlocks, action)
			if p := h.top(); p.Kind != PopupError || !strings.Contains(p.Body, "own block") {
				t.Fatalf("popup = %+v", p)
			}
			for _, sub := range []string{"send", "send-key", "close"} {
				if h.countContaining(sub) != 0 {
					t.Fatalf("%s ran against the own block", sub)
				}
			}
		})
	}
	h := newHarness(t, withSelf("block:01b1"))
	h.focus(PanelSessions)
	h.dispatch(PanelSessions, "killSession")
	if p := h.top(); p.Kind != PopupError {
		t.Fatalf("kill of the session holding the own block not refused: %+v", p)
	}
	if !strings.Contains(strings.Join(h.st.Log, "\n"), "refused") {
		t.Fatalf("refusal not logged: %q", h.st.Log)
	}
}

func TestInspectTabs(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelTargets)
	if !containsAll(h.st.Main.Content, "online", "unix:///run/user/1000/rex/server.sock", "api_revision 9", "/run/user/1000/rex/server.log") {
		t.Fatalf("status tab = %q", h.st.Main.Content)
	}
	h.focus(PanelSessions)
	if !containsAll(h.st.Main.Content, "work", "session:01aaaa", "revision 7", "editor", `"active_window_id"`) {
		t.Fatalf("session inspect = %q", h.st.Main.Content)
	}
	h.focus(PanelWindows)
	if !containsAll(h.st.Main.Content, "editor", "window:01w1", "block:01b1", "floating") {
		t.Fatalf("window inspect = %q", h.st.Main.Content)
	}
	h.focus(PanelBlocks)
	h.fr.Reset()
	h.dispatch(PanelBlocks, "nextTab")
	h.expectCall("block inspect -s session:01aaaa block:01b1 --json")
	h.expectCall("block call -s session:01aaaa -b block:01b1 process")
	h.expectCall("block call -s session:01aaaa -b block:01b1 modes")
	if !containsAll(h.st.Main.Content, "block:01b1", "exited", "last exit 3", "screen normal", `"methods"`) {
		t.Fatalf("block inspect = %q", h.st.Main.Content)
	}
	h.focus(PanelClients)
	h.expectCall("client inspect client:02app --json")
	if !containsAll(h.st.Main.Content, "Rex.app", "darwin_arm64", `"session.select"`) {
		t.Fatalf("client inspect = %q", h.st.Main.Content)
	}
}

func TestBlockInspectFollowsSelection(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "nextTab")
	h.fr.Reset()
	h.dispatch(PanelBlocks, "down")
	h.expectCall("block inspect -s session:01aaaa block:01b2 --json")
	if h.countContaining("capture") != 0 {
		t.Fatalf("capture loaded while the Inspect tab is shown")
	}
}

func TestControllerStreamDownMarksStopped(t *testing.T) {
	h := newHarness(t)
	h.ctrl.StreamState(rex.StreamState{Up: true})
	if !h.st.StreamUp {
		t.Fatalf("stream not up")
	}
	h.ctrl.StreamState(rex.StreamState{Up: false, Code: 4, Kind: rex.ExitUnreachable, Stderr: "Error: no Rex server is running"})
	if h.st.StreamUp || h.st.Status.State != rex.StateStopped || len(h.rows(PanelSessions)) != 0 {
		t.Fatalf("after exit 4: up=%v state=%v sessions=%d", h.st.StreamUp, h.st.Status.State, len(h.rows(PanelSessions)))
	}
	h.fr.Reset()
	h.clk.Advance(5 * time.Second)
	h.ctrl.Tick()
	if h.count("whoami --json") != 1 || h.count("ls --json") != 1 {
		t.Fatalf("no polling while the stream is down; calls:\n%s", h.dumpCalls())
	}
	h.ctrl.StreamState(rex.StreamState{Up: true})
	h.fr.Reset()
	h.clk.Advance(5 * time.Second)
	h.ctrl.Tick()
	if h.count("ls --json") != 0 {
		t.Fatalf("polling while the stream is up")
	}
}

// holdFirst makes the first call containing tokens wait, once armed, until
// release closes; entered closes when it starts.
func holdFirst(t *testing.T, fr *rextest.FakeRunner, tokens ...string) (arm func(), entered, release chan struct{}) {
	var armed atomic.Bool
	entered, release = make(chan struct{}), make(chan struct{})
	holds := rextest.NewHolds(t)
	fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, tokens...) && armed.CompareAndSwap(true, false) {
			close(entered)
			holds.Wait(release, "the release of the held "+strings.Join(tokens, " "))
		}
	}
	return func() { armed.Store(true) }, entered, release
}

// TestStatusRerunKeepsLargestRequest: requests during a status read run once
// it lands, as one read for the largest of them.
func TestStatusRerunKeepsLargestRequest(t *testing.T) {
	ui := &goUI{}
	h := newHarness(t, withUI(ui))
	ui.settle(t)
	whoami, ls := h.count("whoami --json"), h.count("ls --json")
	arm, entered, release := holdFirst(t, h.fr, "whoami", "--json")
	arm()
	h.ctrl.checkTarget()
	rextest.Recv(t, entered, "the held whoami --json")
	h.ctrl.refreshAll()
	h.ctrl.checkTarget()
	stillAt(t, "whoami during the read", func() int { return h.count("whoami --json") }, whoami+1)
	if d := h.ctrl.conn.Load().statusDirty; d != statusRefresh {
		t.Fatalf("dirty = %v, want a refresh", d)
	}
	close(release)
	ui.settle(t)
	if n := h.count("whoami --json"); n != whoami+2 {
		t.Fatalf("whoami = %d, want %d", n, whoami+2)
	}
	if n := h.count("ls --json"); n != ls+1 {
		t.Fatalf("ls = %d, want %d", n, ls+1)
	}
}

// TestLocalStatusInOrder: status reads of a local target land in the order
// they started, so a slow stopped read cannot overwrite a newer online one.
func TestLocalStatusInOrder(t *testing.T) {
	ui := &goUI{}
	h := newHarness(t, withUI(ui))
	ui.settle(t)
	h.fr.On("whoami", "--json").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4).Once()
	before := h.count("whoami --json")
	arm, entered, release := holdFirst(t, h.fr, "whoami", "--json")
	arm()
	h.poll()
	rextest.Recv(t, entered, "the held whoami --json")
	h.poll()
	h.poll()
	stillAt(t, "whoami during the read", func() int { return h.count("whoami --json") }, before+1)
	close(release)
	ui.settle(t)
	if n := h.count("whoami --json"); n != before+2 {
		t.Fatalf("whoami = %d, want %d", n, before+2)
	}
	if h.st.Status.State != rex.StateOnline {
		t.Fatalf("status = %v, want the newer online", h.st.Status.State)
	}
}

// TestStoppedCheckDropsRefreshData: the sessions and clients a refresh reads
// after its online status do not land once a check found the target stopped.
func TestStoppedCheckDropsRefreshData(t *testing.T) {
	ui := &goUI{}
	h := newHarness(t, withUI(ui))
	ui.settle(t)
	if len(h.rows(PanelSessions)) == 0 || len(h.st.Clients) == 0 {
		t.Fatalf("sessions %q, clients %d", h.rows(PanelSessions), len(h.st.Clients))
	}
	arm, entered, release := holdFirst(t, h.fr, "ls", "--json")
	arm()
	h.ctrl.refreshAll()
	rextest.Recv(t, entered, "the held ls --json")
	runQueued(t, ui, 1)
	h.fr.On("whoami", "--json").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	h.ctrl.checkTarget()
	runUntil(t, ui, "the check to land", func() bool { return h.st.Status.State == rex.StateStopped })
	close(release)
	ui.settle(t)
	if rows := h.rows(PanelSessions); len(rows) != 0 || len(h.st.Clients) != 0 || h.st.Status.State != rex.StateStopped {
		t.Fatalf("status %v, sessions %q, clients %d after the refresh landed", h.st.Status.State, rows, len(h.st.Clients))
	}
}

func TestEventMapping(t *testing.T) {
	h := newHarness(t)
	h.ctrl.StreamState(rex.StreamState{Up: true})
	h.focus(PanelBlocks)
	ev := func(line string) rex.Event {
		e, err := rex.ParseEvent([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	inner := func(body string) string {
		return `{"type":"session_event","payload":{"session_id":"session:01aaaa","event":` + body + `}}`
	}

	h.fr.Reset()
	for i := 0; i < 20; i++ {
		h.ctrl.HandleEvent(ev(`{"type":"client_joined"}`))
		h.ctrl.HandleEvent(ev(inner(`{"type":"client_connected"}`)))
	}
	h.clk.Advance(time.Second)
	if n := len(h.fr.Calls()); n != 0 {
		t.Fatalf("client noise triggered %d calls", n)
	}

	h.ctrl.HandleEvent(ev(`{"type":"session_created","payload":{"session_id":"session:03cccc"}}`))
	h.clk.Advance(200 * time.Millisecond)
	if h.count("ls --json") != 1 {
		t.Fatalf("session_created did not refresh the session list")
	}

	updated := strings.Replace(rextest.FixtureString("session_inspect.json"), `"revision": 7`, `"revision": 8`, 1)
	updated = strings.Replace(updated, `"label": "build"`, `"label": "added"`, 1)
	h.fr.On("session", "inspect", "session:01aaaa").Return(updated, "", 0)
	h.fr.Reset()
	h.ctrl.HandleEvent(ev(inner(`{"type":"window_created"}`)))
	h.clk.Advance(200 * time.Millisecond)
	if h.count("session inspect session:01aaaa --json") != 1 {
		t.Fatalf("window_created did not inspect the session; calls:\n%s", h.dumpCalls())
	}
	if !strings.Contains(strings.Join(h.rows(PanelWindows), "\n"), "added") {
		t.Fatalf("windows not refreshed: %q", h.rows(PanelWindows))
	}

	h.fr.Reset()
	h.ctrl.HandleEvent(ev(inner(`{"type":"session_view_changed","revision":8}`)))
	h.clk.Advance(time.Second)
	if h.count("session inspect session:01aaaa --json") != 0 {
		t.Fatalf("revision not newer still inspected")
	}

	h.fr.Reset()
	h.ctrl.HandleEvent(ev(inner(`{"type":"block_event","payload":{"block_id":"block:01b2","name":"child_exited","payload":{"exit_code":7}}}`)))
	if !containsAll(h.rows(PanelBlocks)[1], "exited", "7") {
		t.Fatalf("row not updated in place: %q", h.rows(PanelBlocks)[1])
	}
	if h.countContaining("capture") != 0 {
		t.Fatalf("capture refreshed for a block that is not selected")
	}
	h.ctrl.HandleEvent(ev(inner(`{"type":"block_event","payload":{"block_id":"block:01b1","name":"title_changed","payload":{"title":"x"}}}`)))
	if h.count("capture -s session:01aaaa -b block:01b1 --format=vt") != 1 {
		t.Fatalf("selected block capture not refreshed on its event; calls:\n%s", h.dumpCalls())
	}
}

func TestPollingVisibilityGated(t *testing.T) {
	h := newHarness(t)
	h.ctrl.StreamState(rex.StreamState{Up: true})
	h.focus(PanelBlocks)
	h.fr.Reset()
	h.clk.Advance(500 * time.Millisecond)
	h.ctrl.Tick()
	if h.countContaining("capture") != 0 {
		t.Fatalf("capture refreshed before capture_interval")
	}
	h.clk.Advance(500 * time.Millisecond)
	h.ctrl.Tick()
	if h.countContaining("capture") != 1 {
		t.Fatalf("capture not refreshed after capture_interval")
	}
	h.focus(PanelSessions)
	h.fr.Reset()
	h.clk.Advance(2 * time.Second)
	h.ctrl.Tick()
	if h.countContaining("capture") != 0 {
		t.Fatalf("capture refreshed while hidden")
	}

	h.st.ClientsVisible = false
	h.fr.Reset()
	h.clk.Advance(10 * time.Second)
	h.ctrl.Tick()
	if h.countContaining("client", "ls") != 0 {
		t.Fatalf("clients polled while hidden")
	}
	h.st.ClientsVisible = true
	h.ctrl.Tick()
	if h.countContaining("client", "ls") != 1 {
		t.Fatalf("clients not polled while visible")
	}
	h.clk.Advance(time.Second)
	h.ctrl.Tick()
	if h.countContaining("client", "ls") != 1 {
		t.Fatalf("clients polled faster than their interval")
	}
}

func TestExitCodeUI(t *testing.T) {
	t.Run("exit 3 refreshes", func(t *testing.T) {
		fr := fixtureRunner()
		fr.On("session", "rename").Return("", "Error: session not found", 3)
		h := newHarness(t, withRunner(fr))
		h.focus(PanelSessions)
		h.fr.Reset()
		h.dispatch(PanelSessions, "renameSession")
		h.answer("x")
		if !strings.Contains(h.ctrl.Popups.ToastText(), "session not found") || h.count("ls --json") != 1 {
			t.Fatalf("toast %q, ls calls %d", h.ctrl.Popups.ToastText(), h.count("ls --json"))
		}
	})
	t.Run("exit 4 one-line toast, full log", func(t *testing.T) {
		fr := fixtureRunner()
		fr.On("window", "focus").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
		h := newHarness(t, withRunner(fr))
		h.focus(PanelWindows)
		h.dispatch(PanelWindows, "focusWindow")
		toast := h.ctrl.Popups.ToastText()
		if strings.Contains(toast, "\n") || !strings.Contains(toast, "no Rex server is running") {
			t.Fatalf("toast = %q", toast)
		}
		log := strings.Join(h.st.Log, "\n")
		if !containsAll(log, "window focus", "exit 4", "\ttried unix://", "start one with: rex server start") {
			t.Fatalf("log = %q", log)
		}
	})
	t.Run("exit 2 shows argv", func(t *testing.T) {
		fr := fixtureRunner()
		fr.On("block", "focus").Return("", "Error: unknown flag", 2)
		h := newHarness(t, withRunner(fr))
		h.focus(PanelBlocks)
		h.dispatch(PanelBlocks, "focusBlock")
		if !strings.Contains(h.ctrl.Popups.ToastText(), "block focus -s session:01aaaa block:01b1") {
			t.Fatalf("toast = %q", h.ctrl.Popups.ToastText())
		}
	})
	t.Run("exit 130 silent", func(t *testing.T) {
		fr := fixtureRunner()
		fr.On("block", "focus").Return("", "interrupted", 130)
		h := newHarness(t, withRunner(fr))
		h.focus(PanelBlocks)
		h.dispatch(PanelBlocks, "focusBlock")
		if h.ctrl.Popups.ToastText() != "" || h.ctrl.Popups.Top() != nil {
			t.Fatalf("interrupted call reported")
		}
	})
	t.Run("exit 1 error toast", func(t *testing.T) {
		fr := fixtureRunner()
		fr.On("block", "focus").Return("", "Error: boom\ndetail", 1)
		h := newHarness(t, withRunner(fr))
		h.focus(PanelBlocks)
		h.dispatch(PanelBlocks, "focusBlock")
		if h.ctrl.Popups.ToastText() != "error: Error: boom" {
			t.Fatalf("toast = %q", h.ctrl.Popups.ToastText())
		}
		h.clk.Advance(10 * time.Second)
		h.ctrl.Tick()
		if h.ctrl.Popups.ToastText() != "" {
			t.Fatalf("toast never expires")
		}
	})
}

func TestSendTextIsOneArgument(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelBlocks)
	h.fr.Reset()
	h.dispatch(PanelBlocks, "sendText")
	h.answer("ls -la; echo 'x'")
	calls := h.fr.CallsContaining("send")
	want := strings.Fields(G + "send -s session:01aaaa -b block:01b1 --")
	want = append(want, "ls -la; echo 'x'")
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Fatalf("send calls = %q, want %q", calls, want)
	}
}

func TestCommandLogRedactsSend(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "sendText")
	h.answer("hunter2")
	log := strings.Join(h.st.Log, "\n")
	if strings.Contains(log, "hunter2") || !strings.Contains(log, "<redacted 7 bytes>") {
		t.Fatalf("log = %q", log)
	}
}
