package rex

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestStableSocketGolden (C5): the v1 naming is a contract with the hosts a
// human added to the Rex app, so its output is pinned.
func TestStableSocketGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec SSHSpec
		want string
	}{
		{"remote-host", SSHSpec{Host: "remote-host"}, "gui-remote-host_996d7034ffff31a3.sock"},
		{"edge", SSHSpec{Host: "user@edge"}, "gui-edge_ce63845b8c961e37.sock"},
	} {
		if got := stableSocketName(tc.name, tc.spec); got != tc.want {
			t.Errorf("stableSocketName(%q, %+v) = %q, want %q", tc.name, tc.spec, got, tc.want)
		}
	}
}

// TestStableSocketIdentity (C6): the name follows the target identity, as
// unique as target names are (EqualFold), and not the mode.
func TestStableSocketIdentity(t *testing.T) {
	name := func(n string, s SSHSpec) string { return stableSocketName(n, s) }
	host := SSHSpec{Host: "h"}
	for _, tc := range []struct {
		what  string
		names []string
		want  int
	}{
		{"case", []string{name("edge", host), name("Edge", host), name("EDGE", host)}, 1},
		{"host", []string{name("edge", SSHSpec{Host: "a"}), name("edge", SSHSpec{Host: "b"})}, 2},
		{"rex_path", []string{name("edge", host), name("edge", SSHSpec{Host: "h", RexPath: "/usr/local/bin/rex"})}, 2},
		{"mode", []string{name("edge", host), name("edge", SSHSpec{Host: "h", Mode: SSHModeForward}), name("edge", SSHSpec{Host: "h", Mode: SSHModeExec})}, 1},
		{"dotted I", []string{name("i", host), name("İ", host)}, 2},
		{"kelvin", []string{name("k", host), name("\u212a", host)}, 1},
	} {
		distinct := map[string]bool{}
		for _, n := range tc.names {
			distinct[n] = true
		}
		if len(distinct) != tc.want {
			t.Errorf("%s: %d distinct names %q, want %d", tc.what, len(distinct), tc.names, tc.want)
		}
	}
	if strings.EqualFold("i", "İ") || !strings.EqualFold("k", "\u212a") {
		t.Fatal("EqualFold changed: target name uniqueness no longer matches foldKey")
	}
}

// TestStableSocketShape (C7): only safe characters, and no longer than the
// longest private name, so the runtime directory length budget holds.
func TestStableSocketShape(t *testing.T) {
	shape := regexp.MustCompile(`^gui-[a-z0-9.-]{0,14}_[0-9a-f]{16}\.sock$`)
	for name, slug := range map[string]string{
		"my box":                 "my-box",
		"user@h":                 "user-h",
		"日本":                     "",
		strings.Repeat("é", 64):  "",
		strings.Repeat("ab", 32): "abababababab" + "ab",
		"--a__b--":               "a-b",
		"Remote-Host.lan":        "remote-host.la",
		"abcdefghijklm-op":       "abcdefghijklm",
	} {
		n := stableSocketName(name, SSHSpec{Host: "h"})
		if !shape.MatchString(n) || len("/"+n) > 41 {
			t.Errorf("stableSocketName(%q) = %q (%d bytes)", name, n, len("/"+n))
		}
		if got := strings.TrimSuffix(strings.TrimPrefix(n, "gui-"), n[strings.LastIndexByte(n, '_'):]); got != slug {
			t.Errorf("slug of %q = %q, want %q", name, got, slug)
		}
	}
}

// TestParseRexPath: lines keep their internal spaces, and of several known
// paths ls lists, the first in probe order wins whatever ls's sort.
func TestParseRexPath(t *testing.T) {
	const (
		beta   = "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"
		stable = "/Applications/Rex.app/Contents/Resources/bin/rex"
	)
	fixture, err := os.ReadFile("../../testdata/rex_path_probe.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, out, want string }{
		{"beta alone", beta + "\n", beta},
		{"outer spaces", "  " + beta + "  \n", beta},
		{"macOS error line", "ls: /usr/local/bin/rex: No such file or directory\n" + beta + "\n", beta},
		{"quoted line skipped", "'" + beta + "'\n", ""},
		{"beta then stable", beta + "\n" + stable + "\n", stable},
		{"stable then beta", stable + "\n" + beta + "\n", stable},
		{"probe order over ls order", stable + "\n/usr/local/bin/rex\n", "/usr/local/bin/rex"},
		{"command -v", "/home/u/.local/bin/rex\n", "/home/u/.local/bin/rex"},
		{"fixture", string(fixture), "/usr/local/bin/rex"},
	} {
		if got := parseRexPath([]byte(tc.out)); got != tc.want {
			t.Errorf("%s: parseRexPath = %q, want %q", tc.name, got, tc.want)
		}
	}
}
