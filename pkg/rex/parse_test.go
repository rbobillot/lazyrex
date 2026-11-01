package rex_test

import (
	"slices"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func TestParseVersion(t *testing.T) {
	v, err := rex.ParseVersion(rextest.Fixture("version.json"))
	if err != nil {
		t.Fatal(err)
	}
	if v.RexVersion != "0.0.1000000000-gabc1234" || v.Platform != "linux_amd64" || v.APIRevision != 9 || v.Dirty || v.Commit == "" || v.BuiltAt == "" {
		t.Fatalf("version = %+v", v)
	}
}

func TestParseWhoami(t *testing.T) {
	w, err := rex.ParseWhoami(rextest.Fixture("whoami.json"))
	if err != nil {
		t.Fatal(err)
	}
	if w.Principal != "user" || w.Endpoint != "unix:///run/user/1000/rex/server.sock" || w.PID != 4242 ||
		w.SessionCount != 2 || !w.ExitEmpty || w.RexVersion == "" || w.InstanceID != "inst:01hz" || w.Account == "" {
		t.Fatalf("whoami = %+v", w)
	}
}

func TestParseServerStatus(t *testing.T) {
	s, err := rex.ParseServerStatus(rextest.Fixture("server_status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.APIRevision != 9 || s.PID != 4242 || s.LogPath != "/run/user/1000/rex/server.log" || s.SessionCount != 2 ||
		s.Tailscale.State != "off" || s.You.Principal != "user" || s.Endpoint == "" {
		t.Fatalf("server status = %+v", s)
	}
}

func TestParseLs(t *testing.T) {
	l, err := rex.ParseLs(rextest.Fixture("ls.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Sessions) != 2 || l.Sessions[0].SessionID != "session:01aaaa" || l.Sessions[0].Label != "work" {
		t.Fatalf("ls = %+v", l)
	}
}

func TestParseNew(t *testing.T) {
	id, blocks, err := rex.ParseNew(rextest.Fixture("new.json"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "session:03cccc" || len(blocks) != 1 || blocks[0] != "block:03b1" {
		t.Fatalf("new = %v %v", id, blocks)
	}
}

func TestParseSessionView(t *testing.T) {
	v, err := rex.ParseSessionView(rextest.Fixture("session_inspect.json"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Revision != 7 || v.SessionID != "session:01aaaa" || v.ActiveWindowID != "window:01w1" || len(v.Windows) != 2 {
		t.Fatalf("view = %+v", v)
	}
	w := v.Windows[0]
	if w.FocusedBlockID != "block:01b1" || len(w.Layers) != 2 || w.Layers[1].Kind != "floating" {
		t.Fatalf("window = %+v", w)
	}
	if got := len(w.Blocks()); got != 3 {
		t.Fatalf("window blocks = %d", got)
	}
	if got := len(v.Blocks()); got != 4 {
		t.Fatalf("session blocks = %d", got)
	}
	b := w.Blocks()[0]
	if b.BlockID != "block:01b1" || b.Label != "vim" || b.CreatorName != "terminal" || b.Owner != "user" {
		t.Fatalf("block = %+v", b)
	}
	if w.Blocks()[1].Owner == "" {
		t.Fatalf("object owner not kept as text")
	}
}

func TestParseBlockLs(t *testing.T) {
	l, err := rex.ParseBlockLs(rextest.Fixture("block_ls.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Blocks) != 5 {
		t.Fatalf("blocks = %d", len(l.Blocks))
	}
	detached := 0
	for _, b := range l.Blocks {
		if b.Detached() {
			detached++
			if b.BlockID != "block:01b5" {
				t.Fatalf("wrong detached block %s", b.BlockID)
			}
		}
	}
	if detached != 1 {
		t.Fatalf("detached = %d", detached)
	}
	if l.Blocks[3].WindowID != "window:01w2" {
		t.Fatalf("window id = %q", l.Blocks[3].WindowID)
	}
}

func TestParseClientLs(t *testing.T) {
	l, err := rex.ParseClientLs(rextest.Fixture("client_ls_all.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Clients) != 2 || l.Clients[0].Info.Kind != "cli" || l.Clients[1].Info.Name != "Rex.app" ||
		l.Clients[1].ConnectionState != "connected" || l.Clients[1].Principal == "" {
		t.Fatalf("clients = %+v", l)
	}
}

func TestParseHostsList(t *testing.T) {
	h, err := rex.ParseHostsList(rextest.Fixture("hosts_list.json"))
	if err != nil {
		t.Fatal(err)
	}
	if h.Home.Label != "home" || !h.Home.ThisMachine || !h.Home.Reached || len(h.Hosts) != 0 {
		t.Fatalf("hosts = %+v", h)
	}
}

func TestParseActions(t *testing.T) {
	a, err := rex.ParseActions(rextest.Fixture("actions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 || a[0].Name != "session.select" || len(a[0].Args) == 0 || a[1].Description == "" {
		t.Fatalf("actions = %+v", a)
	}
	bare, err := rex.ParseActions([]byte(`[{"name":"x"}]`))
	if err != nil || len(bare) != 1 || bare[0].Name != "x" {
		t.Fatalf("bare actions = %+v, %v", bare, err)
	}
}

func TestParseClientActions(t *testing.T) {
	a, err := rex.ParseActions(rextest.Fixture("client_actions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 10 || a[0].Name != "session.select" || a[1].Name != "openFile" || len(a[1].Args) == 0 || a[2].Description != "Close the window" {
		t.Fatalf("client actions = %+v", a)
	}
	s, err := rex.ParseActions(rextest.Fixture("server_actions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 14 || s[0].Name != "split_right" || len(s[0].Args) == 0 || s[1].Name != "mystery" {
		t.Fatalf("server actions = %+v", s)
	}
}

func TestParseAPIList(t *testing.T) {
	want := []string{"session.view", "session.list", "server.stop", "hosts.list", "hosts.put", "server.pair_device",
		"target.complete", "session.resize_pane"}
	if got := rex.ParseAPIList(rextest.Fixture("api_list.txt")); !slices.Equal(got, want) {
		t.Fatalf("ParseAPIList = %q", got)
	}
	messy := "Methods:\n  session.view  \n\nsession.view\nNot.A.Method\nnodot\nx.y; rm\n\x1b[31mred.x\n  b.c_d\n"
	if got := rex.ParseAPIList([]byte(messy)); !slices.Equal(got, []string{"session.view", "b.c_d"}) {
		t.Fatalf("ParseAPIList(messy) = %q", got)
	}
}

func TestParseCompletions(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{rextest.FixtureString("target_complete.json"), []string{"session:01aaaa", "session:02bbbb"}},
		{`["a","b c","d\u001b","e'f"]`, []string{"a"}},
		{`{"items":[{"value":"x"},{"label":"y","zz":1},{}]}`, []string{"x", "y"}},
		{`{"result":{"completions":["w1"]}}`, []string{"w1"}},
		{`[{"target":"t1","id":""}]`, []string{"t1"}},
	}
	for _, c := range cases {
		got, err := rex.ParseCompletions([]byte(c.in))
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("ParseCompletions(%s) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{`"bad"`, `not json`, `42`} {
		if _, err := rex.ParseCompletions([]byte(bad)); err == nil {
			t.Errorf("ParseCompletions(%s) accepted", bad)
		}
	}
}

func TestParseKeymap(t *testing.T) {
	k, err := rex.ParseKeymap(rextest.Fixture("keymap.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Bindings) != 2 || len(k.Bindings[0].Keys) != 2 || k.Bindings[1].Keys[0] != "ctrl+a d" {
		t.Fatalf("keymap = %+v", k)
	}
}

func TestParseProcessModes(t *testing.T) {
	p, err := rex.ParseProcess(rextest.Fixture("block_process.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.LastExit == nil || *p.LastExit != 3 || p.State != "exited" || p.Running == nil || *p.Running {
		t.Fatalf("process = %+v", p)
	}
	alt, err := rex.ParseModes(rextest.Fixture("block_modes_alternate.json"))
	if err != nil || !alt.AltScreen() {
		t.Fatalf("alternate modes = %+v, %v", alt, err)
	}
	normal, err := rex.ParseModes(rextest.Fixture("block_modes_normal.json"))
	if err != nil || normal.AltScreen() {
		t.Fatalf("normal modes = %+v, %v", normal, err)
	}
	if _, err := rex.ParseModes([]byte("not json")); err == nil {
		t.Fatalf("garbage modes parsed")
	}
}
