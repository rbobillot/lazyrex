package gui

import (
	"strings"
	"testing"

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

// TestRenderGUIEndpoint (C22, amended by H31): one Status block per endpoint
// state, and the Rex app host block only where there is an endpoint.
func TestRenderGUIEndpoint(t *testing.T) {
	const uri = "unix:///run/user/1000/lazyrex/gui-edge_ce63845b8c961e37.sock"
	const dir = "/home/u/.config/rex"
	hint := "\nRex app host edge-lazyrex in " + dir + "/lazyrex-hosts.lua: not written yet\n" +
		"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.\n" +
		"It works while lazyrex runs with edge active. Rex has no transport auth: only your user can open this socket.\n"
	view := RexHostsView{
		Ready:   true,
		Paths:   rexhosts.Paths{Dir: dir, Hosts: dir + "/lazyrex-hosts.lua", Init: dir + "/init.lua"},
		File:    rexhosts.Absent,
		Entries: map[string]rex.HostEntry{"edge": {Target: "edge", Label: "edge-lazyrex", Endpoint: uri}},
	}
	for _, tc := range []struct {
		ep   rex.GUIEndpoint
		want string
	}{
		{rex.GUIEndpoint{State: rex.GUIForwarded, URI: uri}, "gui endpoint  " + uri + "\n" + hint},
		{rex.GUIEndpoint{State: rex.GUIDown, URI: uri}, "gui endpoint  " + uri + " (tunnel down)\n" + hint},
		{rex.GUIEndpoint{State: rex.GUIElsewhere, URI: uri}, "gui endpoint  " + uri + " (served by another lazyrex for this same target; " +
			"this one uses a private socket. Activate edge again after that one quits to take it over.)\n" + hint},
		{rex.GUIEndpoint{State: rex.GUIUnavailable, Why: "open lock: permission denied"}, "gui endpoint  none: open lock: permission denied\n"},
		{rex.GUIEndpoint{State: rex.GUIExecMode}, "gui endpoint  none: mode: exec forwards no socket. Remove mode: exec to give the Rex app an endpoint.\n"},
		{rex.GUIEndpoint{State: rex.GUIUnforwardable, Why: `endpoint path "/a%" contains '%'`},
			`gui endpoint  none: the server's endpoint cannot be forwarded (endpoint path "/a%" contains '%'). ` +
				"The Rex app needs Tailscale or your own ssh -L forward to reach it.\n"},
		{rex.GUIEndpoint{}, ""},
	} {
		st := NewState()
		st.TargetName = "edge"
		st.Transport = fakeGUILink{ep: tc.ep}
		st.RexHosts = view
		out := RenderStatus(st)
		// The block sits between the blank line after the state and the
		// event stream line, followed by a blank line of its own.
		_, after, ok := strings.Cut(out, "checking…\n\n")
		block, _, found := strings.Cut(after, "event stream")
		want := tc.want
		if want != "" {
			want += "\n"
		}
		if !ok || !found || block != want {
			t.Errorf("state %v: block before the event stream\n got %q\nwant %q\nstatus tab:\n%s", tc.ep.State, block, want, out)
		}
		if strings.Contains(out, "rex hosts add") {
			t.Errorf("state %v: still shows rex hosts add:\n%s", tc.ep.State, out)
		}
		hasHint := strings.Contains(out, "Rex app host edge-lazyrex")
		if want := tc.ep.State == rex.GUIForwarded || tc.ep.State == rex.GUIDown || tc.ep.State == rex.GUIElsewhere; hasHint != want {
			t.Errorf("state %v: hint shown %v, want %v", tc.ep.State, hasHint, want)
		}
	}
}
