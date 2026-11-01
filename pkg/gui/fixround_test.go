package gui

import (
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// newInstance makes the server report another instance, as after a restart.
func (h *harness) newInstance(id string) {
	h.t.Helper()
	h.fr.On("whoami", "--json").Return(strings.Replace(rextest.FixtureString("whoami.json"), "inst:01hz", id, 1), "", 0)
	h.dispatch(PanelTargets, "checkTarget")
}

// serverActionsReturn sets the `actions --json` answer, which would also
// match the client's actions read, so that one is registered again after it.
func (h *harness) serverActionsReturn(stdout, stderr string, code int) {
	h.fr.On("actions", "--json").Return(stdout, stderr, code)
	h.fr.On("-C", "client:02app", "actions", "--json").Return(rextest.FixtureString("client_actions.json"), "", 0)
}

// withDottedAction makes the server list tool.run, an unknown dotted action
// that no client advertises yet.
func withDottedAction(h *harness) {
	acts := strings.Replace(rextest.FixtureString("server_actions.json"), `{"name": "mystery", "description": "Does something"},`,
		`{"name": "mystery", "description": "Does something"}, {"name": "tool.run"},`, 1)
	h.serverActionsReturn(acts, "", 0)
}

// TestServerDoRecheckedAfterConfirm: the server list and client prefix
// checks run again once confirmed, right before the call.
func TestServerDoRecheckedAfterConfirm(t *testing.T) {
	collide := strings.Replace(rextest.FixtureString("client_actions.json"), `{"name": "closeWindow"`, `{"name": "tool.open"}, {"name": "closeWindow"`, 1)
	starts := map[string]func(h *harness){
		"palette": func(h *harness) { h.palette("do tool.run") },
		"x":       func(h *harness) { h.serverActions(); h.chooseAction("tool.run") },
	}
	changes := map[string]struct {
		change func(h *harness)
		reason string
	}{
		"client collides": {func(h *harness) {
			h.fr.On("-C", "client:02app", "actions", "--json").Return(collide, "", 0)
		}, "Clients"},
		"action withdrawn": {func(h *harness) {
			h.serverActionsReturn(rextest.FixtureString("server_actions.json"), "", 0)
		}, "not a server action"},
		"client ls fails": {func(h *harness) {
			h.fr.On("client", "ls", "--all", "--json").Return("", "boom", 1)
		}, ""},
		"server actions fail": {func(h *harness) {
			h.serverActionsReturn("", "boom", 1)
		}, ""},
	}
	for sn, start := range starts {
		for cn, c := range changes {
			t.Run(sn+" "+cn, func(t *testing.T) {
				h := newHarness(t)
				withDottedAction(h)
				start(h)
				if p := h.top(); p.Kind != PopupConfirm {
					t.Fatalf("popup %+v, want a confirmation", p)
				}
				c.change(h)
				h.ctrl.Popups.Accept("")
				h.expectRefusal(c.reason)
				if h.doCalls() != 0 {
					t.Fatalf("do ran; calls:\n%s", h.dumpCalls())
				}
			})
		}
	}
	h := newHarness(t)
	withDottedAction(h)
	h.palette("do tool.run")
	h.ctrl.Popups.Accept("")
	if h.count("do tool.run") != 1 {
		t.Fatalf("unchanged lists: do did not run; calls:\n%s", h.dumpCalls())
	}
}

// TestClientActionRecheckedAfterConfirm: a client action withdrawn while
// its confirmation waits is refused.
func TestClientActionRecheckedAfterConfirm(t *testing.T) {
	h := newHarness(t)
	h.clientActions()
	h.chooseAction("closeWindow")
	if p := h.top(); p.Kind != PopupConfirm {
		t.Fatalf("popup %+v", p)
	}
	without := strings.Replace(rextest.FixtureString("client_actions.json"), `{"name": "closeWindow", "description": "Close the window"},`, "", 1)
	h.fr.On("-C", "client:02app", "actions", "--json").Return(without, "", 0)
	h.ctrl.Popups.Accept("")
	h.expectRefusal("closeWindow")
	if h.doCalls() != 0 {
		t.Fatalf("do ran; calls:\n%s", h.dumpCalls())
	}
}

// TestShowInAppChecksInRunStep: the session.select call runs in the same
// worker step as the read that checks the app still offers it.
func TestShowInAppChecksInRunStep(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui))
	h.focus(PanelSessions)
	ui.hold = true
	h.dispatch(PanelSessions, "showInApp")
	for len(ui.queue) > 0 {
		before := len(h.fr.Calls())
		f := ui.queue[0]
		ui.queue = ui.queue[1:]
		f()
		step := h.fr.Calls()[before:]
		var read, do bool
		for _, c := range step {
			s := strings.Join(c, " ")
			read = read || strings.HasSuffix(s, "-C client:02app actions --json")
			do = do || strings.Contains(s, "do session.select")
		}
		if do && !read {
			t.Fatalf("session.select ran without a check in the same step: %q", step)
		}
	}
	if h.count("-C client:02app do session.select session_id=session:01aaaa") != 1 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestAsyncFormsSnapshot: forms opened after an async read use the selection
// of the moment the action started.
func TestAsyncFormsSnapshot(t *testing.T) {
	t.Run("resize", func(t *testing.T) {
		ui := &queuedUI{}
		h := newHarness(t, withUI(ui))
		h.xMenu(PanelBlocks)
		ui.hold = true
		h.chooseContaining("resize pane")
		ui.hold = false
		h.selectID(PanelSessions, "session:02bbbb")
		ui.drain()
		if got := h.acceptPrefilled(); got != "session:01aaaa" {
			t.Fatalf("session_id prefilled with %q", got)
		}
		if got := h.acceptPrefilled(); got != "block:01b1" {
			t.Fatalf("block_id prefilled with %q", got)
		}
		h.choose("right")
		h.answer("5")
		if h.count(`api call -s session:01aaaa session.resize_pane {"block_id":"block:01b1","delta":5,"direction":"right","session_id":"session:01aaaa"}`) != 1 {
			t.Fatalf("calls:\n%s", h.dumpCalls())
		}
	})
	t.Run("server action", func(t *testing.T) {
		ui := &queuedUI{}
		h := newHarness(t, withUI(ui))
		h.xMenu(PanelTargets)
		ui.hold = true
		h.chooseContaining("run a server action")
		ui.hold = false
		h.selectID(PanelSessions, "session:02bbbb")
		ui.drain()
		h.chooseAction("split_right")
		h.choose("right")
		if got := h.acceptPrefilled(); got != "block:01b1" {
			t.Fatalf("block_id prefilled with %q", got)
		}
	})
	t.Run("client action", func(t *testing.T) {
		ui := &queuedUI{}
		h := newHarness(t, withUI(ui))
		ui.hold = true
		h.clientActions()
		ui.hold = false
		h.selectID(PanelSessions, "session:02bbbb")
		ui.drain()
		h.chooseAction("pane.zoom")
		if got := h.acceptPrefilled(); got != "block:01b1" {
			t.Fatalf("block_id prefilled with %q", got)
		}
	})
	t.Run("show in app", func(t *testing.T) {
		ui := &queuedUI{}
		h := newHarness(t, withUI(ui))
		h.focus(PanelSessions)
		ui.hold = true
		h.dispatch(PanelSessions, "showInApp")
		ui.hold = false
		h.selectID(PanelSessions, "session:02bbbb")
		ui.drain()
		if h.count("-C client:02app do session.select session_id=session:01aaaa") != 1 || h.countContaining("session_id=session:02bbbb") != 0 {
			t.Fatalf("calls:\n%s", h.dumpCalls())
		}
	})
}

// TestAsyncFormsDiscardedOnTargetChange: a result read for another server
// instance opens nothing and runs nothing.
func TestAsyncFormsDiscardedOnTargetChange(t *testing.T) {
	starts := map[string]func(h *harness){
		"resize":        func(h *harness) { h.xMenu(PanelBlocks); h.chooseContaining("resize pane") },
		"server action": func(h *harness) { h.xMenu(PanelTargets); h.chooseContaining("run a server action") },
		"client action": func(h *harness) { h.clientActions() },
		"show in app":   func(h *harness) { h.focus(PanelSessions); h.dispatch(PanelSessions, "showInApp") },
		"palette do":    func(h *harness) { h.palette("do mystery") },
	}
	for name, start := range starts {
		t.Run(name, func(t *testing.T) {
			ui := &queuedUI{}
			h := newHarness(t, withUI(ui))
			ui.hold = true
			start(h)
			if len(ui.queue) == 0 {
				t.Fatalf("nothing queued")
			}
			ui.hold = false
			h.newInstance("inst:02hz")
			ui.drain()
			if p := h.ctrl.Popups.Top(); p != nil {
				t.Fatalf("a stale result opened %+v", p)
			}
			if h.doCalls() != 0 || h.countContaining("session.resize_pane") > h.countContaining("describe") {
				t.Fatalf("a stale result ran; calls:\n%s", h.dumpCalls())
			}
		})
	}
}
