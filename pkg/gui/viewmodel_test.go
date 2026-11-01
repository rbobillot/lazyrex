package gui

import (
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func TestLoadAllFromFixtures(t *testing.T) {
	h := newHarness(t)
	if h.st.Status.State != rex.StateOnline {
		t.Fatalf("status = %v", h.st.Status.State)
	}
	targets := h.rows(PanelTargets)
	if len(targets) != 1 || !containsAll(targets[0], "local", "online", "2") || strings.Contains(targets[0], "mismatch") {
		t.Fatalf("targets = %q", targets)
	}
	sessions := h.rows(PanelSessions)
	if len(sessions) != 2 || !containsAll(sessions[0], "work", "2", "5") {
		t.Fatalf("sessions = %q", sessions)
	}
	if strings.ContainsRune(sessions[1], 0x1b) || strings.ContainsRune(sessions[1], 0x07) || !strings.Contains(sessions[1], "logs") {
		t.Fatalf("session label not sanitized: %q", sessions[1])
	}
	windows := h.rows(PanelWindows)
	if len(windows) != 2 || !containsAll(windows[0], "editor", "3", "*") || strings.Contains(windows[1], "*") {
		t.Fatalf("windows = %q", windows)
	}
	blocks := h.rows(PanelBlocks)
	if len(blocks) != 3 || !containsAll(blocks[0], "vim", "terminal") {
		t.Fatalf("blocks = %q", blocks)
	}
	h.focus(PanelWindows)
	h.dispatch(PanelWindows, "down")
	if blocks := h.rows(PanelBlocks); len(blocks) != 1 || !strings.Contains(blocks[0], "make") {
		t.Fatalf("blocks of second window = %q", blocks)
	}
	h.focus(PanelSessions)
	h.dispatch(PanelSessions, "scope")
	blocks = h.rows(PanelBlocks)
	if len(blocks) != 5 || !containsAll(strings.Join(blocks, "\n"), "bg-job", "detached") {
		t.Fatalf("session-scoped blocks = %q", blocks)
	}
	clients := h.rows(PanelClients)
	if len(clients) != 1 || !containsAll(clients[0], "app", "Rex.app", "darwin_arm64", "1.2.3", "connected") {
		t.Fatalf("clients = %q", clients)
	}
	h.focus(PanelClients)
	h.dispatch(PanelClients, "toggleCLI")
	if clients := h.rows(PanelClients); len(clients) != 2 {
		t.Fatalf("clients with CLI = %q", clients)
	}
}

func TestSelfMarker(t *testing.T) {
	h := newHarness(t, withSelf("block:01b2"))
	blocks := h.rows(PanelBlocks)
	if !strings.Contains(blocks[1], "self") || strings.Contains(blocks[0], "self") {
		t.Fatalf("blocks = %q", blocks)
	}
}

func TestMalformedSelfWarnsInLog(t *testing.T) {
	h := newHarness(t, withSelf("01b2"))
	if h.ctrl.client.Policy.SelfBlock != "" {
		t.Fatalf("malformed REX_BLOCK accepted")
	}
	if !strings.Contains(strings.Join(h.st.Log, "\n"), "REX_BLOCK") {
		t.Fatalf("no warning in the command log: %q", h.st.Log)
	}
	for _, row := range h.rows(PanelBlocks) {
		if strings.Contains(row, "self") {
			t.Fatalf("suffix-matched self marker: %q", row)
		}
	}
}

func TestAPIMismatchBadge(t *testing.T) {
	fr := fixtureRunner()
	fr.On("server", "status", "--json").Return(`{"endpoint":"unix:///x","api_revision":10,"pid":1}`, "", 0)
	h := newHarness(t, withRunner(fr))
	if row := h.rows(PanelTargets)[0]; !strings.Contains(row, "api mismatch") {
		t.Fatalf("target row = %q", row)
	}
	if !strings.Contains(RenderStatus(h.st), "api_revision 10") {
		t.Fatalf("status tab = %q", RenderStatus(h.st))
	}
	if !strings.Contains(Footer(h.st, nil), "api mismatch") {
		t.Fatalf("footer = %q", Footer(h.st, nil))
	}
}

func TestStoppedTarget(t *testing.T) {
	fr := fixtureRunner()
	fr.On("whoami").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	fr.On("server", "status").Return("", rextest.FixtureString("stderr_server_not_running.txt"), 1)
	fr.On("ls").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	fr.On("client", "ls").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	h := newHarness(t, withRunner(fr))
	if h.st.Status.State != rex.StateStopped {
		t.Fatalf("status = %v", h.st.Status.State)
	}
	if row := h.rows(PanelTargets)[0]; !strings.Contains(row, "stopped") {
		t.Fatalf("target row = %q", row)
	}
	if len(h.rows(PanelSessions)) != 0 {
		t.Fatalf("sessions shown for a stopped server")
	}
	if h.ctrl.Popups.Top() != nil || h.ctrl.Popups.ToastText() != "" {
		t.Fatalf("stopped server reported as an error: %+v %q", h.ctrl.Popups.Top(), h.ctrl.Popups.ToastText())
	}
	if !strings.Contains(Footer(h.st, nil), "local: stopped") {
		t.Fatalf("footer = %q", Footer(h.st, nil))
	}
}

func TestBlockRowsLiveState(t *testing.T) {
	h := newHarness(t)
	code := 3
	h.ctrl.BlockEvent("session:01aaaa", rex.BlockEvent{BlockID: "block:01b2", Name: "child_exited", ExitCode: &code})
	h.ctrl.BlockEvent("session:01aaaa", rex.BlockEvent{BlockID: "block:01b1", Name: "title_changed", Title: "vim main.go\x1b[2J"})
	h.ctrl.BlockEvent("session:01aaaa", rex.BlockEvent{BlockID: "block:01b3", Name: "program_status_changed", Status: "working"})
	blocks := h.rows(PanelBlocks)
	if !containsAll(blocks[1], "exited", "3") {
		t.Fatalf("exited row = %q", blocks[1])
	}
	if !strings.Contains(blocks[0], "vim main.go") || strings.ContainsRune(blocks[0], 0x1b) {
		t.Fatalf("title row = %q", blocks[0])
	}
	if !strings.Contains(blocks[2], "working") {
		t.Fatalf("program status row = %q", blocks[2])
	}
}

func TestFilterSidePanel(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "filter")
	h.answer("SHELL")
	if rows := h.rows(PanelBlocks); len(rows) != 1 || !strings.Contains(rows[0], "shell") {
		t.Fatalf("filtered = %q", rows)
	}
	if b, ok := h.st.SelectedBlock(); !ok || b.Info.BlockID != "block:01b2" {
		t.Fatalf("selection does not follow the filter: %+v", b)
	}
	h.dispatch(PanelBlocks, "back")
	if rows := h.rows(PanelBlocks); len(rows) != 3 {
		t.Fatalf("esc did not clear the filter: %q", rows)
	}
}
