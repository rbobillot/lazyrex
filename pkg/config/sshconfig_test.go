package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSSHConfigHosts (1o).
func TestSSHConfigHosts(t *testing.T) {
	got := ReadSSHConfigHosts(filepath.Join("..", "..", "testdata", "ssh_config"))
	if want := []string{"remote-host", "a", "b", "eq", "lower"}; !slices.Equal(got, want) {
		t.Fatalf("hosts = %q, want %q", got, want)
	}
	if got := ReadSSHConfigHosts(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Fatalf("missing file: %q", got)
	}
	if got := SSHConfigHosts(strings.NewReader("Host x\x1b[2J ok\nHost \"q\"\n")); !slices.Equal(got, []string{"ok"}) {
		t.Fatalf("controls and quotes: %q", got)
	}
}

// TestConfigSSHTargets (1j): the structure of kind: ssh entries.
func TestConfigSSHTargets(t *testing.T) {
	c, err := Load(write(t, `
targets:
  - name: studio
    kind: native
    server: studio
  - name: edge
    kind: ssh
    ssh_host: remote-host
    mode: exec
    rex_path: /usr/local/bin/rex
  - name: lab
    kind: ssh
    ssh_host: u@lab
`), true)
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{Name: "studio", Kind: "native", Server: "studio"},
		{Name: "edge", Kind: "ssh", SSHHost: "remote-host", Mode: "exec", RexPath: "/usr/local/bin/rex"},
		{Name: "lab", Kind: "ssh", SSHHost: "u@lab"}}
	if !slices.Equal(c.Targets, want) {
		t.Fatalf("targets = %+v", c.Targets)
	}
	for field, body := range map[string]string{
		"targets[0].ssh_host":  "targets:\n  - name: edge\n    kind: ssh\n",
		"targets[0].server":    "targets:\n  - name: edge\n    kind: ssh\n    ssh_host: edge\n    server: x\n",
		"targets[0].mode":      "targets:\n  - name: edge\n    kind: ssh\n    ssh_host: edge\n    mode: tunnel\n",
		"targets[0].ssh_host ": "targets:\n  - name: edge\n    kind: native\n    server: edge\n    ssh_host: edge\n",
		"targets[0].rex_path":  "targets:\n  - kind: local\n    rex_path: /usr/local/bin/rex\n",
		"targets[1].name":      "targets:\n  - name: edge\n    kind: native\n    server: edge\n  - name: EDGE\n    kind: ssh\n    ssh_host: edge\n",
		"targets[0].name":      "targets:\n  - name: local\n    kind: ssh\n    ssh_host: edge\n",
		"local, native or ssh": "targets:\n  - name: edge\n    ssh_host: edge\n",
	} {
		t.Run(field, func(t *testing.T) {
			_, err := Load(write(t, body), true)
			if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(field)) {
				t.Fatalf("Load = %v, want an error mentioning %q", err, strings.TrimSpace(field))
			}
		})
	}
}

// TestEditSSHTarget (E1): SSH keys are written only when set, and an update
// keeps rex_path and comments.
func TestEditSSHTarget(t *testing.T) {
	p := write(t, "targets:\n  - name: edge # the server\n    kind: ssh\n    ssh_host: remote-host\n    rex_path: /opt/rex/bin/rex\n")
	_, f, err := Edit(loadFile(t, p), AddTarget(Target{Name: "lab", Kind: "ssh", SSHHost: "lab"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	lab := string(raw[strings.Index(string(raw), "name: lab"):])
	if !strings.Contains(lab, "ssh_host: lab") || strings.Contains(lab, "mode:") || strings.Contains(lab, "rex_path:") || strings.Contains(lab, "server:") {
		t.Fatalf("added entry:\n%s", raw)
	}
	c, _, err := Edit(f, UpdateTarget("edge", Target{Name: "edge2", SSHHost: "u@edge", Mode: "exec"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	if !strings.Contains(string(raw), "# the server") || !strings.Contains(string(raw), "rex_path: /opt/rex/bin/rex") {
		t.Fatalf("update lost a key or comment:\n%s", raw)
	}
	if want := (Target{Name: "edge2", Kind: "ssh", SSHHost: "u@edge", Mode: "exec", RexPath: "/opt/rex/bin/rex"}); c.Targets[0] != want {
		t.Fatalf("updated = %+v", c.Targets[0])
	}
}
