package gui

import (
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
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

// TestRenderGUIEndpoint (C22): one Status block per endpoint state, the
// command alone on its line and the hint only where there is an endpoint.
func TestRenderGUIEndpoint(t *testing.T) {
	const uri = "unix:///run/user/1000/lazyrex/gui-edge_ce63845b8c961e37.sock"
	hint := "\nRex app: add a host with this endpoint, or run once (lazyrex never runs it):\n" +
		"rex hosts add edge " + uri + "\n" +
		"It works while lazyrex runs with edge active. Rex has no transport auth: only your user can open this socket.\n" +
		"Rex syncs hosts to your other devices, where this path reaches nothing unless lazyrex forwards it there.\n"
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
		hasHint := strings.Contains(out, "rex hosts add")
		if want := tc.ep.State == rex.GUIForwarded || tc.ep.State == rex.GUIDown || tc.ep.State == rex.GUIElsewhere; hasHint != want {
			t.Errorf("state %v: hint shown %v, want %v", tc.ep.State, hasHint, want)
		}
	}
}
