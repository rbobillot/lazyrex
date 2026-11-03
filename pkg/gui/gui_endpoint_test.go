package gui

import (
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/render"
	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rexhosts"
)

// fakeGUILink is an SSH transport whose Rex app endpoint is fixed.
type fakeGUILink struct{ ep rex.GUIEndpoint }

func (fakeGUILink) Name() string                          { return "ssh" }
func (fakeGUILink) Remote() bool                          { return true }
func (fakeGUILink) SSH() bool                             { return true }
func (fakeGUILink) Argv(b string, g, a []string) []string { return rex.Local{}.Argv(b, g, a) }
func (fakeGUILink) Describe() string                      { return "ssh edge, forward mode" }
func (fakeGUILink) Host() string                          { return "edge" }
func (f fakeGUILink) GUIEndpoint() rex.GUIEndpoint        { return f.ep }

const guiURI = "unix:///run/user/1000/lazyrex/gui-edge_ce63845b8c961e37.sock"

// guiView is a ready Rex app hosts file view declaring edge at guiURI.
func guiView() RexHostsView {
	const dir = "/home/u/.config/rex"
	return RexHostsView{
		Ready:   true,
		Paths:   rexhosts.Paths{Dir: dir, Hosts: dir + "/lazyrex-hosts.lua", Init: dir + "/init.lua"},
		File:    rexhosts.Absent,
		Entries: map[string]rex.HostEntry{"edge": {Target: "edge", Label: "edge-lazyrex", Endpoint: guiURI}},
	}
}

// offHint is the Status block of target name at uri with the Rex app hosts
// file off.
func offHint(name, uri string) string {
	return "Rex app: Settings > Remote Hosts > Add Remote Host, with the label " + name + " and the endpoint " + uri + ".\n" +
		"Or run once (lazyrex never runs it):\n" +
		"rex hosts add " + name + " " + uri + "\n" +
		"It works while lazyrex runs with " + name + " active. Rex has no transport auth: only your user can open this socket.\n" +
		"Rex syncs hosts to your other devices, where this path reaches nothing unless lazyrex forwards it there.\n"
}

// guiCases are the endpoint states with their colored gui endpoint line, and
// whether a Rex app block follows it.
func guiCases() []struct {
	ep   rex.GUIEndpoint
	line string
	hint bool
} {
	return []struct {
		ep   rex.GUIEndpoint
		line string
		hint bool
	}{
		{rex.GUIEndpoint{State: rex.GUIForwarded, URI: guiURI}, sgrGreen + "gui endpoint  " + guiURI + sgrReset + "\n", true},
		{rex.GUIEndpoint{State: rex.GUIDown, URI: guiURI}, sgrYellow + "gui endpoint  " + guiURI + " (tunnel down)" + sgrReset + "\n", true},
		{rex.GUIEndpoint{State: rex.GUIElsewhere, URI: guiURI}, sgrYellow + "gui endpoint  " + guiURI + " (served by another lazyrex for this same target; " +
			"this one uses a private socket. Activate edge again after that one quits to take it over.)" + sgrReset + "\n", true},
		{rex.GUIEndpoint{State: rex.GUIUnavailable, Why: "open lock: permission denied"},
			sgrRed + "gui endpoint  none: open lock: permission denied" + sgrReset + "\n", false},
		{rex.GUIEndpoint{State: rex.GUIExecMode},
			sgrRed + "gui endpoint  none: mode: exec forwards no socket. Remove mode: exec to give the Rex app an endpoint." + sgrReset + "\n", false},
		{rex.GUIEndpoint{State: rex.GUIUnforwardable, Why: `endpoint path "/a%" contains '%'`},
			sgrRed + `gui endpoint  none: the server's endpoint cannot be forwarded (endpoint path "/a%" contains '%'). ` +
				"The Rex app needs Tailscale or your own ssh -L forward to reach it." + sgrReset + "\n", false},
		{rex.GUIEndpoint{}, "", false},
	}
}

// guiStatus renders the Status tab of active target name over ep.
func guiStatus(name string, ep rex.GUIEndpoint, on bool, view RexHostsView) string {
	st := NewState()
	st.TargetName = name
	st.Transport = fakeGUILink{ep: ep}
	st.RexHostsFile = on
	st.RexHosts = view
	return RenderStatus(st)
}

// guiBlock is the block between the blank line after the state and the
// event stream line.
func guiBlock(t *testing.T, out string) string {
	t.Helper()
	_, after, ok := strings.Cut(out, "checking…\n\n")
	block, _, found := strings.Cut(after, "event stream")
	if !ok || !found {
		t.Fatalf("status tab:\n%s", out)
	}
	return block
}

// TestRenderGUIEndpoint (C22, amended by H31 and K1): with the Rex app hosts
// file on, one Status block per endpoint state, and the Rex app host block
// only where there is an endpoint.
func TestRenderGUIEndpoint(t *testing.T) {
	hint := "\nRex app host edge-lazyrex in /home/u/.config/rex/lazyrex-hosts.lua: not written yet\n" +
		"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.\n" +
		"It works while lazyrex runs with edge active. Rex has no transport auth: only your user can open this socket.\n"
	for _, tc := range guiCases() {
		out := guiStatus("edge", tc.ep, true, guiView())
		want := tc.line
		if tc.hint {
			want += hint
		}
		if want != "" {
			want += "\n"
		}
		if block := guiBlock(t, out); block != want {
			t.Errorf("state %v: block before the event stream\n got %q\nwant %q\nstatus tab:\n%s", tc.ep.State, block, want, out)
		}
		if strings.Contains(out, "rex hosts add") {
			t.Errorf("state %v: still shows rex hosts add:\n%s", tc.ep.State, out)
		}
		if hasHint := strings.Contains(out, "Rex app host edge-lazyrex"); hasHint != tc.hint {
			t.Errorf("state %v: hint shown %v, want %v", tc.ep.State, hasHint, tc.hint)
		}
	}
}

// TestRenderGUIEndpointHint (C22 before H31, F6, F24): with the Rex app
// hosts file off, the endpoint is followed by the Rex app path and the
// rex hosts add command, whatever the hosts file view holds.
func TestRenderGUIEndpointHint(t *testing.T) {
	for _, tc := range guiCases() {
		out := guiStatus("edge", tc.ep, false, guiView())
		want := tc.line
		if tc.hint {
			want += "\n" + offHint("edge", guiURI)
		}
		if want != "" {
			want += "\n"
		}
		if block := guiBlock(t, out); block != want {
			t.Errorf("state %v: block before the event stream\n got %q\nwant %q\nstatus tab:\n%s", tc.ep.State, block, want, out)
		}
		if hasHint := strings.Contains(out, "rex hosts add edge "+guiURI+"\n"); hasHint != tc.hint {
			t.Errorf("state %v: hint shown %v, want %v", tc.ep.State, hasHint, tc.hint)
		}
		if strings.Contains(out, "Rex app host ") || strings.Contains(out, "lazyrex-hosts.lua") {
			t.Errorf("state %v: shows the hosts file:\n%s", tc.ep.State, out)
		}
	}
}

// TestRenderGUIEndpointColor (F20): the gui endpoint line takes one fixed
// color by state, the same in both modes, and nothing else is colored.
func TestRenderGUIEndpointColor(t *testing.T) {
	want := map[rex.GUIState]string{rex.GUIForwarded: sgrGreen, rex.GUIDown: sgrYellow, rex.GUIElsewhere: sgrYellow,
		rex.GUIUnavailable: sgrRed, rex.GUIExecMode: sgrRed, rex.GUIUnforwardable: sgrRed}
	for _, tc := range guiCases() {
		var lines [2]string
		for i, on := range []bool{false, true} {
			out := guiStatus("edge", tc.ep, on, guiView())
			if got := statusColor(t, "status tab", out); got != want[tc.ep.State] {
				t.Errorf("state %v, on %v: color %q, want %q", tc.ep.State, on, got, want[tc.ep.State])
			}
			lines[i], _, _ = strings.Cut(guiBlock(t, out), "\n")
		}
		if lines[0] != lines[1] {
			t.Errorf("state %v: endpoint line off %q, on %q", tc.ep.State, lines[0], lines[1])
		}
		if tc.line != "" && !strings.HasSuffix(lines[0], sgrReset) {
			t.Errorf("state %v: line %q does not end in a reset", tc.ep.State, lines[0])
		}
	}
}

// expectOnlyStatusColor checks that out holds exactly the two escapes of the
// gui endpoint color, and that out without them holds no control.
func expectOnlyStatusColor(t *testing.T, what, out, color string) {
	t.Helper()
	if got := statusColor(t, what, out); got != color {
		t.Errorf("%s: color %q, want %q", what, got, color)
	}
	if n := strings.Count(out, "\x1b["); n != 2 {
		t.Errorf("%s: %d escapes, want 2:\n%q", what, n, out)
	}
	if strings.ContainsAny(render.StripSGR(out), "\x1b\x07\u009b") {
		t.Errorf("%s: a control is left:\n%q", what, out)
	}
}

// TestRenderGUIEndpointInjection (F18, F21 to F23): a target name, an
// endpoint or a reason holding escapes never adds one, and a % in them never
// reaches a format string.
func TestRenderGUIEndpointInjection(t *testing.T) {
	const name = "edge\x1b[31mX\x1b]0;t\x07"
	for _, tc := range []struct {
		state rex.GUIState
		on    bool
		color string
	}{
		{rex.GUIElsewhere, true, sgrYellow},
		{rex.GUIElsewhere, false, sgrYellow},
		{rex.GUIForwarded, false, sgrGreen},
	} {
		out := guiStatus(name, rex.GUIEndpoint{State: tc.state, URI: guiURI}, tc.on, guiView())
		expectOnlyStatusColor(t, "name", out, tc.color)
		if !strings.Contains(out, "edgeX") {
			t.Errorf("name dropped:\n%q", out)
		}
	}
	const uri = "unix:///a\x1b[32m\x1b[2J\u009b31mb"
	for _, state := range []rex.GUIState{rex.GUIForwarded, rex.GUIDown, rex.GUIElsewhere} {
		color := map[rex.GUIState]string{rex.GUIForwarded: sgrGreen, rex.GUIDown: sgrYellow, rex.GUIElsewhere: sgrYellow}[state]
		for _, on := range []bool{false, true} {
			out := guiStatus("edge", rex.GUIEndpoint{State: state, URI: uri}, on, guiView())
			expectOnlyStatusColor(t, "uri", out, color)
			if !strings.Contains(out, "unix:///ab") {
				t.Errorf("uri dropped:\n%q", out)
			}
		}
	}
	out := guiStatus("edge", rex.GUIEndpoint{State: rex.GUIUnavailable, Why: "lock\x1b[32mgreen\x1b[0m\x07"}, false, guiView())
	expectOnlyStatusColor(t, "unavailable why", out, sgrRed)
	if !strings.Contains(out, sgrRed+"gui endpoint  none: lockgreen"+sgrReset+"\n") {
		t.Errorf("unavailable:\n%q", out)
	}
	out = guiStatus("edge", rex.GUIEndpoint{State: rex.GUIUnforwardable, Why: "p \x1b[33m%s%d\u009b2J"}, false, guiView())
	expectOnlyStatusColor(t, "unforwardable why", out, sgrRed)
	if !strings.Contains(out, sgrRed+"gui endpoint  none: the server's endpoint cannot be forwarded (p %s%d). ") {
		t.Errorf("unforwardable:\n%q", out)
	}

	out = guiStatus("日本", rex.GUIEndpoint{State: rex.GUIForwarded, URI: guiURI}, false, RexHostsView{})
	if !strings.Contains(out, "\nrex hosts add '日本' "+guiURI+"\n") || !strings.Contains(out, "with the label 日本 and the endpoint "+guiURI+".\n") {
		t.Errorf("quoted name:\n%s", out)
	}
}
