package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRunRefusesUnsafeServer (1k): an unsafe --server/-S value or config
// server exits 2 before the UI starts.
func TestRunRefusesUnsafeServer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	empty := writeConfig(t, "")
	cases := map[string][]string{
		"-S flag":       {"--config", empty, "-S", "-oProxyCommand=x"},
		"--server":      {"--config", empty, "--server", " x"},
		"control":       {"--config", empty, "-S", "a\x1b[2J"},
		"config server": {"--config", writeConfig(t, "targets:\n  - name: x\n    kind: native\n    server: \"-x\"\n")},
		"config kind":   {"--config", writeConfig(t, "targets:\n  - name: x\n    kind: ssh\n    server: x\n")},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code := run(args); code != 2 {
				t.Fatalf("run(%q) = %d, want 2", args, code)
			}
		})
	}
}

// TestRunRefusesUnsafeSSH (1k): an unsafe --ssh destination, --ssh with
// --server, or an unsafe config ssh_host exits 2 before the UI starts.
func TestRunRefusesUnsafeSSH(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	empty := writeConfig(t, "")
	cases := map[string]struct {
		args []string
		why  string
	}{
		"option":         {[]string{"--config", empty, "--ssh", "-oProxyCommand=x"}, `--ssh "-oProxyCommand=x"`},
		"space":          {[]string{"--config", empty, "--ssh", "a b"}, `--ssh "a b"`},
		"empty":          {[]string{"--config", empty, "--ssh", ""}, `--ssh ""`},
		"with --server":  {[]string{"--config", empty, "--ssh", "h", "--server", "x"}, "exclusive"},
		"with -S":        {[]string{"--config", empty, "-S", "x", "--ssh", "h"}, "exclusive"},
		"config option":  {[]string{"--config", writeConfig(t, "targets:\n  - name: x\n    kind: ssh\n    ssh_host: \"-oProxyCommand=x\"\n")}, "targets[0].ssh_host"},
		"config space":   {[]string{"--config", writeConfig(t, "targets:\n  - name: x\n    kind: ssh\n    ssh_host: edge server\n")}, "targets[0].ssh_host"},
		"config relpath": {[]string{"--config", writeConfig(t, "targets:\n  - name: x\n    kind: ssh\n    ssh_host: edge\n    rex_path: bin/rex\n")}, "targets[0].rex_path"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var code int
			stderr := captureStderr(t, func() { code = run(c.args) })
			if code != 2 || !strings.Contains(stderr, c.why) {
				t.Fatalf("run(%q) = %d, stderr %q, want 2 and %q", c.args, code, stderr, c.why)
			}
		})
	}
}

func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	out := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	f()
	os.Stderr = saved
	w.Close()
	return <-out
}

func TestRunHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--version"}} {
		if code := run(args); code != 0 {
			t.Errorf("run(%q) = %d", args, code)
		}
	}
}
