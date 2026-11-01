package gui

import (
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

const ownBlock = "block:01b9"

type destroyCase struct {
	panel  Panel
	action string
	call   string
}

var containerDestroys = []destroyCase{
	{PanelSessions, "killSession", "kill session:01aaaa"},
	{PanelWindows, "closeWindow", "window close -s session:01aaaa window:01w1"},
}

const inspectCall = "session inspect session:01aaaa --json"

// confirmDestroy runs a container destroy and accepts its confirmation, which
// the cache, not knowing the own block, lets through.
func confirmDestroy(t *testing.T, h *harness, c destroyCase) {
	t.Helper()
	h.focus(c.panel)
	h.dispatch(c.panel, c.action)
	if p := h.top(); p.Kind != PopupConfirm {
		t.Fatalf("%s: popup = %+v, want a confirmation", c.action, p)
	}
	h.ctrl.Popups.Accept("")
}

func expectRefused(t *testing.T, h *harness, c destroyCase, body string) {
	t.Helper()
	if n := h.count(c.call); n != 0 {
		t.Fatalf("%s ran %d times; calls:\n%s", c.call, n, h.dumpCalls())
	}
	p := h.ctrl.Popups.Top()
	if p == nil || p.Kind != PopupError || !strings.Contains(p.Body, body) {
		t.Fatalf("popup = %+v, want an error containing %q", p, body)
	}
	if !strings.Contains(strings.Join(h.st.Log, "\n"), "refused") {
		t.Fatalf("refusal not logged: %q", h.st.Log)
	}
}

func TestContainerDestroyRechecksOwnBlockFresh(t *testing.T) {
	for _, c := range containerDestroys {
		t.Run(c.action+"/stale cache", func(t *testing.T) {
			h := newHarness(t, withSelf(ownBlock))
			fresh := strings.ReplaceAll(rextest.FixtureString("session_inspect.json"), "block:01b2", ownBlock)
			h.fr.On("session", "inspect", "session:01aaaa").Return(fresh, "", 0)
			before := h.count(inspectCall)
			confirmDestroy(t, h, c)
			if h.count(inspectCall) != before+1 {
				t.Fatalf("no fresh session inspect after confirmation; calls:\n%s", h.dumpCalls())
			}
			expectRefused(t, h, c, "own block")
		})
		t.Run(c.action+"/inspect fails", func(t *testing.T) {
			h := newHarness(t, withSelf(ownBlock))
			h.fr.On("session", "inspect", "session:01aaaa").Return("", "boom", 1)
			confirmDestroy(t, h, c)
			expectRefused(t, h, c, "own block")
		})
		t.Run(c.action+"/inspect unparsable", func(t *testing.T) {
			h := newHarness(t, withSelf(ownBlock))
			h.fr.On("session", "inspect", "session:01aaaa").Return("not json", "", 0)
			confirmDestroy(t, h, c)
			expectRefused(t, h, c, "own block")
		})
		t.Run(c.action+"/own block elsewhere", func(t *testing.T) {
			h := newHarness(t, withSelf("block:02c1"))
			before := h.count(inspectCall)
			confirmDestroy(t, h, c)
			if h.inspectsBefore(c.call) != before+1 {
				t.Fatalf("no fresh session inspect before %s; calls:\n%s", c.call, h.dumpCalls())
			}
		})
		t.Run(c.action+"/no own block", func(t *testing.T) {
			h := newHarness(t)
			before := h.count(inspectCall)
			confirmDestroy(t, h, c)
			if h.inspectsBefore(c.call) != before {
				t.Fatalf("extra session inspect without $REX_BLOCK; calls:\n%s", h.dumpCalls())
			}
		})
	}
}

func TestCloseWindowIgnoresOwnBlockInAnotherWindow(t *testing.T) {
	h := newHarness(t, withSelf(ownBlock))
	fresh := strings.ReplaceAll(rextest.FixtureString("session_inspect.json"), "block:01b4", ownBlock)
	h.fr.On("session", "inspect", "session:01aaaa").Return(fresh, "", 0)
	c := containerDestroys[1]
	confirmDestroy(t, h, c)
	if h.count(c.call) != 1 {
		t.Fatalf("close of a window without the own block did not run; calls:\n%s", h.dumpCalls())
	}
	if p := h.ctrl.Popups.Top(); p != nil && p.Kind == PopupError {
		t.Fatalf("unexpected error popup: %+v", p)
	}
}

func TestKillSessionRechecksDetachedOwnBlock(t *testing.T) {
	c := containerDestroys[0]
	t.Run("detached own block", func(t *testing.T) {
		h := newHarness(t, withSelf(ownBlock))
		fresh := strings.ReplaceAll(rextest.FixtureString("block_ls.json"), "block:01b5", ownBlock)
		h.fr.On("block", "ls", "-s", "session:01aaaa").Return(fresh, "", 0)
		confirmDestroy(t, h, c)
		expectRefused(t, h, c, "own block")
	})
	t.Run("block ls fails", func(t *testing.T) {
		h := newHarness(t, withSelf(ownBlock))
		h.fr.On("block", "ls", "-s", "session:01aaaa").Return("", "boom", 1)
		confirmDestroy(t, h, c)
		expectRefused(t, h, c, "own block")
	})
}

// inspectsBefore counts the session inspects run before the single call equal
// to G + call, failing when that call did not run exactly once.
func (h *harness) inspectsBefore(call string) int {
	h.t.Helper()
	if n := h.count(call); n != 1 {
		h.t.Fatalf("%s ran %d times; calls:\n%s", call, n, h.dumpCalls())
	}
	k := 0
	for _, c := range h.fr.Calls() {
		switch strings.Join(c, " ") {
		case G + call:
			return k
		case G + inspectCall:
			k++
		}
	}
	return k
}
