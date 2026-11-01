package rex

import (
	"os"
	"testing"
)

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
