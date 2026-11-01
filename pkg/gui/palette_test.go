package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// palette opens the command palette and submits line.
func (h *harness) palette(line string) {
	h.t.Helper()
	h.ctrl.Popups.Clear()
	h.dispatch(PanelSessions, "palette")
	if p := h.top(); p.Kind != PopupPrompt {
		h.t.Fatalf("palette opened %v, want a prompt", p.Kind)
	}
	h.answer(line)
}

func (h *harness) paletteCmd(line string) rex.Cmd {
	h.t.Helper()
	cmd, err := rex.ParsePalette(line)
	if err != nil || cmd.Err != nil {
		h.t.Fatalf("ParsePalette(%q): %v %v", line, err, cmd.Err)
	}
	return cmd
}

func (h *harness) doCalls() int { return h.countContaining("do") }

func TestPaletteArgv(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{`api call -s session:01aaaa session.view {"a":1}`, strings.Fields(G + `api call -s session:01aaaa session.view {"a":1}`)},
		{`do mystery k='$(id)'`, append(strings.Fields(G+"do mystery"), "k=$(id)")},
		{`rex do mystery --args '{"b":2}'`, strings.Fields(G + `do mystery --args {"b":2}`)},
		{`api call server.status`, strings.Fields(G + "api call server.status")},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			h := newHarness(t)
			h.fr.Reset()
			h.palette(c.line)
			if p := h.ctrl.Popups.Top(); p != nil && p.Kind == PopupConfirm {
				h.ctrl.Popups.Accept("")
			}
			if n := h.countArgv(c.want); n != 1 {
				t.Fatalf("%q ran %d times; calls:\n%s", c.want, n, h.dumpCalls())
			}
			for _, call := range h.fr.Calls() {
				if !slices.Contains(call, "--autostart=false") || !slices.ContainsFunc(call, func(a string) bool { return strings.HasPrefix(a, "--timeout=") }) {
					t.Errorf("call without --autostart=false or --timeout=: %q", call)
				}
			}
			if p := h.ctrl.Popups.Top(); p != nil {
				t.Fatalf("popup left open: %+v", p)
			}
		})
	}
}

func TestPaletteOutputInLog(t *testing.T) {
	h := newHarness(t)
	h.fr.On("session.view").Return(`{"revision":7,"label":"wo\u001b]0;x\u0007rk"}`, "", 0)
	h.palette("api call -s session:01aaaa session.view")
	if !strings.Contains(h.logText(), `"revision": 7`) {
		t.Fatalf("output not in the command log:\n%s", h.logText())
	}
	if strings.Contains(h.logText(), "\x1b") || strings.Contains(h.logText(), "\x07") {
		t.Fatalf("output not sanitized:\n%q", h.logText())
	}
	if got := h.ctrl.Popups.ToastText(); got != "done (output in the command log)" {
		t.Fatalf("toast %q", got)
	}
}

func TestPaletteConfirmGUI(t *testing.T) {
	for _, line := range []string{
		"api call server.stop {}",
		`api call -s session:01aaaa session.close_block {"block_id":"block:01b1"}`,
		`api call -s session:01aaaa block.send_input {"text":"x"}`,
		"do mystery",
		"api call frob.nicate {}",
	} {
		t.Run(line, func(t *testing.T) {
			h := newHarness(t)
			cmd := h.paletteCmd(line)
			argv := h.client.Argv(cmd)
			h.palette(line)
			p := h.top()
			if p.Kind != PopupConfirm || p.Body != h.client.Display(cmd) {
				t.Fatalf("popup %+v, want a confirmation of %q", p, h.client.Display(cmd))
			}
			if n := h.countArgv(argv); n != 0 {
				t.Fatalf("ran before confirmation")
			}
			h.ctrl.Popups.Accept("")
			if n := h.countArgv(argv); n != 1 {
				t.Fatalf("confirmed command ran %d times; calls:\n%s", n, h.dumpCalls())
			}
		})
	}
	t.Run("unlisted do", func(t *testing.T) {
		h := newHarness(t)
		h.fr.Reset()
		h.palette("do notlisted")
		h.expectRefusal("not a server action")
		if n := h.count("actions --json"); n != 1 {
			t.Fatalf("actions --json read %d times; calls:\n%s", n, h.dumpCalls())
		}
		if h.doCalls() != 0 {
			t.Fatalf("do ran; calls:\n%s", h.dumpCalls())
		}
	})
}

func TestPaletteDenied(t *testing.T) {
	denied := []string{
		"api call hosts.put {}", "api call hosts.remove {}", "api call server.tailscale.enable {}",
		"api call server.pair_device {}", "api call server.listen_add {}",
		"do pairDevice", "do install_update", "do updateNow", "do performUpdate", "do checkForUpdates",
	}
	allowListed := []string{"hosts.put", "server.pair_device", "server.listen_add", "server.tailscale.enable"}
	run := func(t *testing.T, h *harness, line string) {
		t.Helper()
		h.fr.Reset()
		h.st.Log = nil
		h.palette(line)
		h.expectRefusal("")
		if n := len(h.fr.Calls()); n != 0 {
			t.Fatalf("%s: %d runner calls:\n%s", line, n, h.dumpCalls())
		}
	}
	h := newHarness(t)
	for _, line := range denied {
		run(t, h, line)
	}
	ha := newHarness(t, withAllow(allowListed...))
	for _, m := range allowListed {
		run(t, ha, "api call "+m+" {}")
	}
}

func TestPaletteAllowList(t *testing.T) {
	h := newHarness(t, withAllow("checkForUpdates"))
	h.palette("do checkForUpdates")
	if p := h.top(); p.Kind != PopupConfirm {
		t.Fatalf("popup %+v, want the unknown-name confirmation", p)
	}
	if h.doCalls() != 0 {
		t.Fatalf("ran before confirmation")
	}
}

func TestPaletteNotDenied(t *testing.T) {
	h := newHarness(t)
	h.palette("api call hosts.list {}")
	if p := h.ctrl.Popups.Top(); p != nil {
		t.Fatalf("hosts.list opened %+v", p)
	}
	if n := h.count("api call hosts.list {}"); n != 1 {
		t.Fatalf("hosts.list ran %d times", n)
	}
	for _, name := range []string{"update_status", "getUpdateInfo", "ghostscript_view"} {
		h.palette("do " + name)
		if p := h.top(); p.Kind != PopupConfirm {
			t.Fatalf("do %s: popup %+v, want a confirmation", name, p)
		}
	}
}

func TestPaletteOwnBlock(t *testing.T) {
	h := newHarness(t, withSelf("block:01self"))
	for _, line := range []string{
		`api call session.view {"block_id":"block:01self"}`,
		`api call session.view {"b":"block\u003a01self"}`,
		`api call session.view '{"b":"block\u003a01self"}'`,
		"do mystery b=01SELF",
		"do mystery b=self",
	} {
		h.fr.Reset()
		h.palette(line)
		h.expectRefusal("block:01self")
		if n := len(h.fr.Calls()); n != 0 {
			t.Fatalf("%s: %d runner calls:\n%s", line, n, h.dumpCalls())
		}
	}
	h.palette("do mystery b=elf")
	if p := h.top(); p.Kind != PopupConfirm {
		t.Fatalf("b=elf: popup %+v, want a confirmation", p)
	}
}

// TestOwnBlockLabel: Rex resolves a block by its label, so a value equal to
// the own block's current label is refused, and the label follows refreshes.
func TestOwnBlockLabel(t *testing.T) {
	h := newHarness(t, withSelf("block:01b2"))
	for _, line := range []string{"do mystery b=shell", "do mystery b=SHELL", `api call session.view {"t":"shell"}`} {
		h.fr.Reset()
		h.palette(line)
		h.expectRefusal("block:01b2")
		if n := len(h.fr.Calls()); n != 0 {
			t.Fatalf("%s: %d runner calls:\n%s", line, n, h.dumpCalls())
		}
	}
	renameOwnBlock(h, "sh2")
	h.palette("do mystery b=shell")
	if p := h.top(); p.Kind != PopupConfirm {
		t.Fatalf("old label after the refresh: popup %+v, want a confirmation", p)
	}
	h.fr.Reset()
	h.palette("do mystery b=sh2")
	h.expectRefusal("block:01b2")
	if h.doCalls() != 0 {
		t.Fatalf("do ran")
	}
}

// renameOwnBlock makes the next session refresh report block:01b2 as label.
func renameOwnBlock(h *harness, label string) {
	h.t.Helper()
	inspect := strings.Replace(rextest.FixtureString("session_inspect.json"), `"label": "shell"`, `"label": "`+label+`"`, 1)
	inspect = strings.Replace(inspect, `"revision": 7`, `"revision": 8`, 1)
	ls := strings.Replace(rextest.FixtureString("block_ls.json"), `"label": "shell"`, `"label": "`+label+`"`, 1)
	h.fr.On("session", "inspect", "session:01aaaa").Return(inspect, "", 0)
	h.fr.On("block", "ls", "-s", "session:01aaaa").Return(ls, "", 0)
	h.ctrl.refreshSession("session:01aaaa")
}

// TestOwnBlockTargetKeyStaleLabel: a block-bearing argument needs a full block
// ID, so the own block's real label is refused even when the cached one is stale.
func TestOwnBlockTargetKeyStaleLabel(t *testing.T) {
	h := newHarness(t, withSelf("block:01b2"))
	renameOwnBlock(h, "sh2")
	for _, line := range []string{"do mystery block_id=shell", `api call session.view {"block_id":"shell"}`, "do mystery pane_id=01b2"} {
		h.fr.Reset()
		h.palette(line)
		h.expectRefusal("full block ID")
		if n := len(h.fr.Calls()); n != 0 {
			t.Fatalf("%s: %d runner calls", line, n)
		}
	}
	h.palette("do mystery block_id=block:01b2")
	h.expectRefusal("own block")
	h.palette("do mystery block_id=block:01b1")
	if p := h.top(); p.Kind != PopupConfirm {
		t.Fatalf("another block: popup %+v, want a confirmation", p)
	}
}

// TestPaletteClientPrefix: rex do sends a name with a client prefix to a
// client, so the palette refuses one that overlaps a connected client's
// actions, and refuses everything when it cannot read them.
func TestPaletteClientPrefix(t *testing.T) {
	for _, line := range []string{"do session.select session_id=session:01aaaa", "do pane.split"} {
		h := newHarness(t)
		h.palette(line)
		h.expectRefusal("Clients")
		if h.doCalls() != 0 {
			t.Fatalf("%s: do ran; calls:\n%s", line, h.dumpCalls())
		}
		if h.count("-C client:02app actions --json") == 0 {
			t.Fatalf("%s: the app's actions were not read", line)
		}
	}
	failing := map[string][]string{
		"client ls fails":      {"client", "ls", "--all", "--json"},
		"client actions fail":  {"-C", "client:02app", "actions", "--json"},
		"server actions fail":  {"actions", "--json"},
		"client ls unparsable": nil,
	}
	for name, tokens := range failing {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			if tokens == nil {
				h.fr.On("client", "ls", "--all", "--json").Return("not json", "", 0)
			} else {
				h.fr.On(tokens...).Return("", "boom", 1)
			}
			h.palette("do mystery")
			h.expectRefusal("")
			if h.doCalls() != 0 {
				t.Fatalf("do ran; calls:\n%s", h.dumpCalls())
			}
		})
	}
}

// sshExecRunner answers the reads a palette do needs in SSH exec mode, where
// every remote argument is single-quoted.
func sshExecRunner() *rextest.FakeRunner {
	q := rex.ShellQuote
	fr := rextest.NewFakeRunner()
	fr.On().Return("{}", "", 0)
	fr.On(q("actions"), q("--json")).Return(rextest.FixtureString("server_actions.json"), "", 0)
	fr.On(q("client"), q("ls"), q("--all"), q("--json")).Return(rextest.FixtureString("client_ls_all.json"), "", 0)
	fr.On(q("-C"), q("client:02app"), q("actions"), q("--json")).Return(rextest.FixtureString("client_actions.json"), "", 0)
	return fr
}

func TestPaletteRedactedInConfirmAndLog(t *testing.T) {
	ssh := rex.SSHExec{Host: "remote-host", RexPath: "/usr/local/bin/rex", ControlDir: "/tmp/lz"}
	cases := map[string][]harnessOpt{"local": {withTransport(rex.Local{})}, "ssh": {withTransport(ssh), withRunner(sshExecRunner())}}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, opts...)
			h.palette("do mystery k=secret")
			p := h.top()
			if p.Kind != PopupConfirm || !strings.Contains(p.Body, "k=<redacted 6 bytes>") || strings.Contains(p.Body, "secret") {
				t.Fatalf("confirmation %+v", p)
			}
			h.ctrl.Popups.Accept("")
			log := h.logText()
			if !strings.Contains(log, "k=<redacted 6 bytes>") || strings.Contains(log, "secret") {
				t.Fatalf("command log:\n%s", log)
			}
			if h.countContaining("k=secret") == 0 && h.countContaining("'k=secret'") == 0 {
				t.Fatalf("the real argv lost the value; calls:\n%s", h.dumpCalls())
			}
		})
	}
}

func TestPaletteRefusesGrammar(t *testing.T) {
	h := newHarness(t)
	for _, line := range []string{"-C client:02app do session.select", "api call x @file", "api call x -", "api call -s api x {}",
		"server start --extra-listen", "do ./x.lua", "do a/b k=v", "do mystery -e print(1)"} {
		h.fr.Reset()
		h.palette(line)
		h.expectRefusal("")
		if n := len(h.fr.Calls()); n != 0 {
			t.Fatalf("%s: %d runner calls", line, n)
		}
	}
	h.palette("   ")
	if p := h.ctrl.Popups.Top(); p != nil {
		t.Fatalf("an empty line opened %+v", p)
	}
}
