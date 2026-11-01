package gui

import (
	"slices"
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

func TestCaptureAltNote(t *testing.T) {
	fr := fixtureRunner()
	fr.On("modes").Return(rextest.FixtureString("block_modes_alternate.json"), "", 0)
	h := newHarness(t, withRunner(fr))
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "showCapture")
	if h.st.Focus != PanelMain || h.st.Tab[PanelBlocks] != TabCapture {
		t.Fatalf("focus %v tab %d", h.st.Focus, h.st.Tab[PanelBlocks])
	}
	if !strings.HasPrefix(h.st.Main.Content, AltNote) {
		t.Fatalf("content = %q", h.st.Main.Content)
	}
	h.expectCall("capture -s session:01aaaa -b block:01b1 --format=vt")
	h.expectCall("block call -s session:01aaaa -b block:01b1 modes")
	if strings.Contains(h.st.Main.Content, "title") || strings.Contains(h.st.Main.Content, "\x1b]") {
		t.Fatalf("capture not sanitized: %q", h.st.Main.Content)
	}
	if !strings.Contains(h.st.Main.Content, "\x1b[32mhello\x1b[0m") {
		t.Fatalf("colors lost: %q", h.st.Main.Content)
	}
}

func TestCaptureNoNoteOnNormalScreen(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "showCapture")
	if strings.Contains(h.st.Main.Content, AltNote) || !strings.Contains(h.st.Main.Content, "world") {
		t.Fatalf("content = %q", h.st.Main.Content)
	}
	for _, c := range h.fr.CallsContaining("capture") {
		if !slices.Contains(c, "-b") || !slices.Contains(c, "-s") {
			t.Fatalf("capture without -s/-b: %q", c)
		}
	}
}

func TestCaptureFormatFallback(t *testing.T) {
	fr := fixtureRunner()
	fr.On("capture", "-s", "session:01aaaa", "-b", "block:01b1", "--format=vt").Return("", "Error: unknown format", 2)
	fr.On("capture", "-s", "session:01aaaa", "-b", "block:01b1", "--format=text").Return("plain text\n", "", 0)
	h := newHarness(t, withRunner(fr))
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "showCapture")
	if !strings.Contains(h.st.Main.Content, "plain text") {
		t.Fatalf("content = %q", h.st.Main.Content)
	}
	calls := h.fr.CallsContaining("capture")
	if len(calls) != 2 || !slices.Contains(calls[1], "--format=text") {
		t.Fatalf("capture calls = %q", calls)
	}
	h.ctrl.RefreshCapture()
	calls = h.fr.CallsContaining("capture")
	if len(calls) != 3 || !slices.Contains(calls[2], "--format=text") {
		t.Fatalf("fallback not remembered: %q", calls)
	}
}

func TestCaptureLineCap(t *testing.T) {
	fr := fixtureRunner()
	var b strings.Builder
	for i := 0; i < 6000; i++ {
		b.WriteString("row\n")
	}
	b.WriteString("last\n")
	fr.On("capture").Return(b.String(), "", 0)
	h := newHarness(t, withRunner(fr))
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "showCapture")
	if n := strings.Count(h.st.Main.Content, "\n"); n != 5000 {
		t.Fatalf("kept %d lines", n)
	}
	if !strings.HasSuffix(h.st.Main.Content, "last\n") {
		t.Fatalf("tail lost")
	}
}

func TestRenderCapture(t *testing.T) {
	got := RenderCapture([]byte("a\nb\x1b[2J\nc\x1b[31m!\x1b[0m\n"), 2, true)
	if got != AltNote+"\n"+"b\nc\x1b[31m!\x1b[0m\n" {
		t.Fatalf("RenderCapture = %q", got)
	}
}

func TestFindMatches(t *testing.T) {
	content := "zero\n\x1b[31mERROR\x1b[0m one\ntwo\nerror three\n"
	if got := FindMatches(content, "error"); !slices.Equal(got, []int{1, 3}) {
		t.Fatalf("FindMatches = %v", got)
	}
	if got := FindMatches(content, "31m"); len(got) != 0 {
		t.Fatalf("matched inside an escape: %v", got)
	}
	if got := FindMatches(content, ""); len(got) != 0 {
		t.Fatalf("empty query matched: %v", got)
	}
}

func TestMainSearch(t *testing.T) {
	h := newHarness(t)
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "showCapture")
	h.dispatch(PanelMain, "filter")
	h.answer("WORLD")
	if !slices.Equal(h.st.Main.Matches, []int{1}) || h.st.Main.Origin != 1 || h.st.Main.Follow {
		t.Fatalf("search state = %+v", h.st.Main)
	}
	h.dispatch(PanelMain, "nextMatch")
	h.dispatch(PanelMain, "prevMatch")
	if h.st.Main.Match != 0 {
		t.Fatalf("match index = %d", h.st.Main.Match)
	}
	h.dispatch(PanelMain, "back")
	if h.st.Main.Query != "" {
		t.Fatalf("esc did not clear the search")
	}
	h.dispatch(PanelMain, "back")
	if h.st.Focus != PanelBlocks {
		t.Fatalf("esc did not return to the side panel: %v", h.st.Focus)
	}
}
