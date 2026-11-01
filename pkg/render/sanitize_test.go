package render

import (
	"strings"
	"testing"
)

func TestSanitizeVT(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello\tworld\n", "hello\tworld\n"},
		{"sgr kept", "\x1b[1;31mred\x1b[0m", "\x1b[1;31mred\x1b[0m"},
		{"empty sgr normalized", "\x1b[mx", "\x1b[0mx"},
		{"empty param normalized", "\x1b[;1mx", "\x1b[0;1mx"},
		{"truecolor kept", "\x1b[38;2;1;2;3mx", "\x1b[38;2;1;2;3mx"},
		{"256 color kept", "\x1b[48;5;200mx", "\x1b[48;5;200mx"},
		{"broken truecolor dropped", "\x1b[38;2mx", "x"},
		{"colon form dropped", "\x1b[4:3mx", "x"},
		{"private sgr dropped", "\x1b[?1mx", "x"},
		{"cursor move dropped", "a\x1b[2;3Hb", "ab"},
		{"erase dropped", "a\x1b[2Jb\x1b[Kc", "abc"},
		{"alt screen dropped", "\x1b[?1049hfull\x1b[?1049l", "full"},
		{"osc title bel", "\x1b]0;title\x07text", "text"},
		{"osc clipboard st", "\x1b]52;c;aGVsbG8=\x1b\\text", "text"},
		{"dcs dropped", "\x1bPq#0;2;0;0;0\x1b\\after", "after"},
		{"apc dropped", "\x1b_Gf=24;payload\x1b\\after", "after"},
		{"pm and sos dropped", "\x1b^pm\x1b\\\x1bXsos\x1b\\x", "x"},
		{"esc 7 dropped", "a\x1b7b\x1b8c", "abc"},
		{"charset designation dropped", "\x1b(Bplain", "plain"},
		{"bel dropped", "ding\x07", "ding"},
		{"lone cr dropped", "abc\rdef", "abcdef"},
		{"crlf becomes lf", "a\r\nb\r\n", "a\nb\n"},
		{"c1 csi dropped", "a\u009b2Jb", "ab"},
		{"c1 osc dropped", "a\u009d0;t\u009cb", "ab"},
		{"other c0 dropped", "a\x01\x02\x7fb", "ab"},
		{"invalid utf8 dropped", "a\xff\xfeb", "ab"},
		{"unicode kept", "héllo ✓", "héllo ✓"},
		{"unterminated osc dropped", "ok\x1b]0;never ends", "ok"},
		{"unterminated csi dropped", "ok\x1b[31", "ok"},
		{"lone esc at end dropped", "ok\x1b", "ok"},
		{"underline color removed from sgr", "\x1b[1;58;5;3mx", "\x1b[1mx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SanitizeVT([]byte(c.in)); got != c.want {
				t.Errorf("SanitizeVT(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestControlStringsSwallowEmbeddedEscapes(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"osc with embedded esc ]", "a\x1b]0;x\x1b]0;evil\x07b", "ab"},
		{"text after bel is visible", "a\x1b]0;x\x1b]0;evil\x07shown\x1b\\b", "ashownb"},
		{"osc with embedded sgr", "a\x1b]0;x\x1b[31mred\x07b", "ab"},
		{"osc with embedded csi then st", "a\x1b]52;c;\x1b[2Jpayload\x1b\\b", "ab"},
		{"dcs with embedded esc", "a\x1bPq\x1bXinner\x1b[31mred\x1b\\b", "ab"},
		{"dcs ignores bel", "a\x1bPq\x07still\x1b\\b", "ab"},
		{"apc with embedded esc", "a\x1b_G\x1b]x\x1b\\b", "ab"},
		{"pm with embedded esc", "a\x1b^p\x1bq\x1b\\b", "ab"},
		{"sos with embedded esc", "a\x1bXs\x1b7\x1b\\b", "ab"},
		{"c1 osc with embedded esc", "a\u009d0;x\x1b[1mbold\u009cb", "ab"},
		{"esc esc backslash ends", "a\x1b]0;x\x1b\x1b\\b", "ab"},
		{"can aborts osc", "a\x1b]0;x\x18b", "ab"},
		{"sub aborts dcs", "a\x1bPq\x1a\x1b[1mb", "a\x1b[1mb"},
		{"unterminated osc with embedded esc", "ok\x1b]0;x\x1b[31mnever\x1bends", "ok"},
		{"unterminated dcs", "ok\x1bPq\x1bmore\nlines\n", "ok"},
		{"esc at end inside string", "ok\x1b]0;x\x1b", "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for name, f := range map[string]func(string) string{
				"vt":  func(s string) string { return SanitizeVT([]byte(s)) },
				"log": LogText,
			} {
				want := c.want
				if name == "log" {
					want = StripSGR(want)
				}
				if got := f(c.in); got != want {
					t.Errorf("%s(%q) = %q, want %q", name, c.in, got, want)
				}
			}
		})
	}
}

func TestSanitizeVTNeverEmitsNonSGREscapes(t *testing.T) {
	in := "\x1b[31mA\x1b[2J\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\\x1b[?25l\x1bc\x1bP1$r\x1b\\\x1b[0m"
	out := SanitizeVT([]byte(in))
	for i := 0; i < len(out); i++ {
		if out[i] != 0x1b {
			continue
		}
		end := strings.IndexByte(out[i:], 'm')
		if end < 0 || out[i+1] != '[' {
			t.Fatalf("non-SGR escape left at %d in %q", i, out)
		}
		for _, r := range out[i+2 : i+end] {
			if (r < '0' || r > '9') && r != ';' {
				t.Fatalf("SGR with unexpected byte %q in %q", r, out)
			}
		}
	}
	if !strings.Contains(out, "link") || !strings.Contains(out, "A") {
		t.Fatalf("visible text lost: %q", out)
	}
}

func TestLogTextSanitize(t *testing.T) {
	in := "a\n\tb\r\x1b[31mc\x1b]0;t\x07\x01\u009b2Jd"
	if got := LogText(in); got != "a\n\tbcd" {
		t.Fatalf("LogText = %q", got)
	}
}

func TestLabel(t *testing.T) {
	if got := Label("logs\x1b]0;evil\x07\n\tx\r"); got != "logs  x" {
		t.Fatalf("Label = %q", got)
	}
}

func TestStripSGR(t *testing.T) {
	if got := StripSGR("\x1b[1;31mError\x1b[0m: x"); got != "Error: x" {
		t.Fatalf("StripSGR = %q", got)
	}
}

func TestTailLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		b.WriteString("line")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	all := b.String()
	if got := TailLines(all, 3); got != "line8\nline9\nline0\n" {
		t.Fatalf("TailLines(3) = %q", got)
	}
	if got := TailLines(strings.TrimSuffix(all, "\n"), 2); got != "line9\nline0" {
		t.Fatalf("TailLines without trailing newline = %q", got)
	}
	if got := TailLines(all, 10); got != all {
		t.Fatalf("TailLines(len) changed the input: %q", got)
	}
	if got := TailLines(all, 50); got != all {
		t.Fatalf("TailLines(>len) changed the input: %q", got)
	}
}

// A sequence opened before the tail boundary must not leak its body into the
// kept lines: sanitizing has to see the whole capture before it is tailed.
func TestCaptureSanitizesBeforeTail(t *testing.T) {
	raw := "old1\nold2\x1b]52;c;line-one\nline-two\nline-three\x07visible-a\n" +
		"x\x1bP+q\nsecret-dcs\nmore-secret\x1b\\visible-b\nlast\n"
	got := Capture([]byte(raw), 3)
	for _, leak := range []string{"line-one", "line-two", "line-three", "secret-dcs", "more-secret", "52;c", "+q"} {
		if strings.Contains(got, leak) {
			t.Fatalf("Capture leaked %q: %q", leak, got)
		}
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("Capture left an escape: %q", got)
	}
	if !strings.HasSuffix(got, "last\n") || !strings.Contains(got, "visible-b") {
		t.Fatalf("Capture lost the tail: %q", got)
	}
	if n := strings.Count(got, "\n"); n != 3 {
		t.Fatalf("Capture kept %d lines, want 3: %q", n, got)
	}
}

func TestCaptureKeepsTailOfLongScrollback(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString("\x1b[32mrow\x1b[0m\r\n")
	}
	b.WriteString("final\n")
	got := Capture([]byte(b.String()), 5000)
	if n := strings.Count(got, "\n"); n != 5000 {
		t.Fatalf("kept %d lines, want 5000", n)
	}
	if !strings.HasSuffix(got, "final\n") {
		t.Fatalf("tail lost")
	}
}
