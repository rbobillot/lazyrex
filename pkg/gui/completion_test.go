package gui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jesseduffield/gocui"

	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func (h *harness) openPalette() *Popup {
	h.t.Helper()
	h.ctrl.Popups.Clear()
	h.dispatch(PanelSessions, "palette")
	return h.top()
}

func (h *harness) complete(input string) {
	h.t.Helper()
	if !h.ctrl.Popups.Complete(input) {
		h.t.Fatalf("the top popup has no completion")
	}
}

func TestPaletteCompletion(t *testing.T) {
	h := newHarness(t)
	h.fr.Reset()
	p := h.openPalette()
	h.complete("api call hosts.")
	if h.top() != p || p.Initial != "api call hosts.list" {
		t.Fatalf("method completion: top %+v, input %q", h.top(), p.Initial)
	}
	h.complete("api call hosts.")
	if n := h.count("api list --quiet"); n != 1 {
		t.Fatalf("api list --quiet read %d times", n)
	}

	p = h.openPalette()
	h.complete("do sp")
	if h.top() != p || p.Initial != "do split_right" {
		t.Fatalf("action completion: input %q", p.Initial)
	}

	p = h.openPalette()
	h.complete("api call -s se")
	if h.count(`api describe target.complete --json`) != 1 || h.count(`api call target.complete {"prefix":"se"}`) != 1 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
	m := h.top()
	if m.Kind != PopupMenu || !slices.Equal(menuLabels(m), []string{"session:01aaaa", "session:02bbbb"}) {
		t.Fatalf("target candidates: %+v", m)
	}
	h.choose("session:02bbbb")
	if h.top() != p || p.Initial != "api call -s session:02bbbb" {
		t.Fatalf("after the menu: top %+v, input %q", h.top(), p.Initial)
	}

	h.openPalette()
	h.complete("api call -s session:01aaaa session.view {} se")
	if h.count(`api call -s session:01aaaa target.complete {"prefix":"se"}`) != 1 {
		t.Fatalf("target completion without -s; calls:\n%s", h.dumpCalls())
	}
	if h.count(`api describe target.complete --json`) != 1 {
		t.Fatalf("the completion field was not cached")
	}

	p = h.openPalette()
	h.complete("api ")
	if p.Initial != "api call" {
		t.Fatalf("call completion: %q", p.Initial)
	}
	h.openPalette()
	h.complete("")
	if m := h.top(); m.Kind != PopupMenu || !slices.Equal(menuLabels(m), []string{"api", "do"}) {
		t.Fatalf("verb completion: %+v", m)
	}
	for _, none := range []string{"do zzz", "api call hosts.p", "'unterminated"} {
		p = h.openPalette()
		h.complete(none)
		if h.top() != p || p.Initial != none || h.ctrl.Popups.ToastText() != "no completion" {
			t.Fatalf("%q: top %+v, input %q, toast %q", none, h.top(), p.Initial, h.ctrl.Popups.ToastText())
		}
	}
}

func TestCompletionStale(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui))
	h.openPalette()
	ui.hold = true
	h.complete("do sp")
	if len(ui.queue) == 0 {
		t.Fatalf("completion did not run off the UI goroutine")
	}
	h.ctrl.Popups.Cancel()
	ui.drain()
	if p := h.ctrl.Popups.Top(); p != nil {
		t.Fatalf("a cancelled prompt got %+v", p)
	}

	p := h.openPalette()
	h.complete("api call hosts.")
	if !h.ctrl.Popups.SetInputIf(p, p.inputRev, "api call x") {
		t.Fatalf("SetInputIf refused the current revision")
	}
	ui.drain()
	if h.top() != p || p.Initial != "api call x" {
		t.Fatalf("a stale completion was applied: %q", p.Initial)
	}
	if h.ctrl.Popups.SetInputIf(p, p.inputRev-1, "old") || p.Initial != "api call x" {
		t.Fatalf("SetInputIf accepted an old revision")
	}

	h.ctrl.Popups.CurrentInput = func(*Popup) string { return "api call target.c" }
	defer func() { h.ctrl.Popups.CurrentInput = nil }()
	p = h.openPalette()
	h.complete("api call target.")
	ui.drain()
	if h.top() != p || p.Initial != "api call target." {
		t.Fatalf("a completion for text the user changed was applied: %q", p.Initial)
	}
}

func TestCacheKeyedByTarget(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui))
	whoami := rextest.FixtureString("whoami.json")
	newInstance := func(id string) {
		t.Helper()
		h.fr.On("whoami", "--json").Return(strings.Replace(whoami, "inst:01hz", id, 1), "", 0)
		h.dispatch(PanelTargets, "checkTarget")
	}
	reads := func() int { return h.count("api list --quiet") }

	p := h.openPalette()
	h.complete("api call target.")
	if p.Initial != "api call target.complete" || reads() != 1 {
		t.Fatalf("first completion: %q, %d reads", p.Initial, reads())
	}
	newInstance("inst:02hz")
	p = h.openPalette()
	h.complete("api call target.")
	if p.Initial != "api call target.complete" || reads() != 2 {
		t.Fatalf("after a new instance: %q, %d reads", p.Initial, reads())
	}

	newInstance("inst:03hz")
	p = h.openPalette()
	ui.hold = true
	h.complete("api call hosts.")
	ui.hold = false
	newInstance("inst:04hz")
	ui.drain()
	if reads() != 3 || h.top() != p || p.Initial != "api call hosts." {
		t.Fatalf("a result queued before the change was applied: %q, %d reads", p.Initial, reads())
	}
	h.complete("api call hosts.")
	if reads() != 4 || p.Initial != "api call hosts.list" {
		t.Fatalf("after the change: %q, %d reads", p.Initial, reads())
	}
}

func TestPaletteCompletionHeadless(t *testing.T) {
	h := newHarness(t)
	a, err := newApp(h.ctrl, appOptions{Headless: true, Width: 120, Height: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	redraw := func() {
		t.Helper()
		if err := a.redraw(); err != nil {
			t.Fatal(err)
		}
	}
	h.dispatch(PanelSessions, "palette")
	redraw()
	v, err := a.g.View("popup")
	if err != nil {
		t.Fatal(err)
	}
	v.TextArea.TypeString("do sp")
	if err := a.popupKey("<tab>"); err != nil {
		t.Fatalf("tab: %v", err)
	}
	redraw()
	if v, _ = a.g.View("popup"); v.TextArea.GetContent() != "do split_right" {
		t.Fatalf("text area %q", v.TextArea.GetContent())
	}

	h.ctrl.Popups.Clear()
	h.ctrl.Popups.Prompt("Rename", "", nil)
	redraw()
	if err := a.popupKey("<tab>"); !errors.Is(err, gocui.ErrKeybindingNotHandled) {
		t.Fatalf("tab in a prompt without completion: %v", err)
	}
	h.ctrl.Popups.Clear()
	h.dispatch(PanelBlocks, "pressKey")
	redraw()
	if err := a.popupKey("<tab>"); err != nil || h.ctrl.Popups.Top() == nil {
		t.Fatalf("tab in a menu must be swallowed: %v", err)
	}
}
