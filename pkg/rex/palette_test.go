package rex_test

import (
	"slices"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func TestSplitLine(t *testing.T) {
	cases := []struct {
		in       string
		want     []string
		trailing bool
	}{
		{`'a b'`, []string{"a b"}, false},
		{`"a\"b"`, []string{`a"b`}, false},
		{`"a\\b"`, []string{`a\b`}, false},
		{`a\ b`, []string{"a b"}, false},
		{`do x k='$(id)'`, []string{"do", "x", "k=$(id)"}, false},
		{`''`, []string{""}, false},
		{"api  call ", []string{"api", "call"}, true},
		{"", nil, false},
		{`"$HOME" ~`, []string{"$HOME", "~"}, false},
		{`{"a":1}`, []string{`{"a":1}`}, false},
		{`{"b":"block\u003a01self"}`, []string{`{"b":"block\u003a01self"}`}, false},
		{`k="v"`, []string{`k="v"`}, false},
		{`a\'b`, []string{`a'b`}, false},
	}
	for _, c := range cases {
		got, trailing, err := rex.SplitLine(c.in)
		if err != nil || !slices.Equal(got, c.want) || trailing != c.trailing {
			t.Errorf("SplitLine(%q) = %q, %v, %v", c.in, got, trailing, err)
		}
	}
	for _, bad := range []string{`'x`, `"x`, `x\`} {
		if _, _, err := rex.SplitLine(bad); err == nil {
			t.Errorf("SplitLine(%q) accepted", bad)
		}
	}
}

func TestParsePalette(t *testing.T) {
	c := localClient(rextest.NewFakeRunner())
	G := "rex --autostart=false --timeout=10s "
	cases := []struct {
		line string
		want []string
	}{
		{`api call -s session:01aaaa session.view {"a":1}`, split(G + `api call -s session:01aaaa session.view {"a":1}`)},
		{`api call --session session:01aaaa session.view`, split(G + `api call -s session:01aaaa session.view`)},
		{`do x k='$(id)'`, append(split(G+"do x"), "k=$(id)")},
		{`rex do x --args '{"b":2}'`, split(G + `do x --args {"b":2}`)},
		{`api call server.status`, split(G + "api call server.status")},
		{`do mystery a=1 b=two`, split(G + "do mystery a=1 b=two")},
	}
	for _, tc := range cases {
		cmd, err := rex.ParsePalette(tc.line)
		if err != nil {
			t.Errorf("%s: %v", tc.line, err)
			continue
		}
		if cmd.Err != nil {
			t.Errorf("%s: cmd error %v", tc.line, cmd.Err)
			continue
		}
		if got := c.Argv(cmd); !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.line, got, tc.want)
		}
	}
}

func TestParsePaletteErrors(t *testing.T) {
	for _, line := range []string{
		"-C client:02app do x", "do -C client:02app x", "-S h api call x {}", "api call -S h x", "--timeout=1s do x",
		"--autostart do x", "api call x @file", "api call x -", "api call -s api x {}", "api call -s session:01aaaa",
		"api call x {} extra", "server start --extra-listen", "api call x [1]", "api call x {", "api call",
		"api list", "do", "do x --args", "do x --args {} k=v", "do x -e print(1)", "do x --action y", "do x @file",
		"do x -", "do x k=v --all", "frob", "rex", "'unterminated", "do ./x.lua", "do a/b k=v", "api call -s",
	} {
		cmd, err := rex.ParsePalette(line)
		if err == nil && cmd.Err == nil {
			t.Errorf("%q accepted as %q", line, cmd.Args)
		}
	}
}

func TestPaletteSlot(t *testing.T) {
	cases := []struct {
		line    string
		slot    rex.CompletionSlot
		session rex.SessionID
		prefix  string
	}{
		{"", rex.SlotVerb, "", ""},
		{"a", rex.SlotVerb, "", "a"},
		{"rex d", rex.SlotVerb, "", "d"},
		{"api ", rex.SlotCall, "", ""},
		{"api ca", rex.SlotCall, "", "ca"},
		{"api call ses", rex.SlotMethod, "", "ses"},
		{"api call -s session:01aaaa ses", rex.SlotMethod, "session:01aaaa", "ses"},
		{"do sp", rex.SlotAction, "", "sp"},
		{"do ", rex.SlotAction, "", ""},
		{"api call -s se", rex.SlotTarget, "", "se"},
		{"do x k=se", rex.SlotTarget, "", "se"},
		{"api call -s session:01aaaa session.view {} se", rex.SlotTarget, "session:01aaaa", "se"},
		{"api call -s nope x {} se", rex.SlotTarget, "", "se"},
	}
	for _, c := range cases {
		tokens, trailing, err := rex.SplitLine(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		slot, session, prefix := rex.PaletteSlot(tokens, trailing)
		if slot != c.slot || session != c.session || prefix != c.prefix {
			t.Errorf("PaletteSlot(%q) = %v %q %q, want %v %q %q", c.line, slot, session, prefix, c.slot, c.session, c.prefix)
		}
	}
}
