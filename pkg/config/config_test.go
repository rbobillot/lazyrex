package config

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if c.Rex.Binary != "rex" || c.Rex.Timeout != 10*time.Second || c.Refresh.PollInterval != 5*time.Second ||
		c.Refresh.CaptureInterval != time.Second || c.Refresh.Debounce != 200*time.Millisecond ||
		c.Capture.MaxLines != 5000 || c.Capture.Format != "vt" {
		t.Fatalf("defaults = %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yml")
	c, err := Load(missing, false)
	if err != nil || c.Capture.MaxLines != 5000 {
		t.Fatalf("missing default path: %+v, %v", c, err)
	}
	if _, err := Load(missing, true); err == nil {
		t.Fatalf("missing explicit --config accepted")
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadOverlay(t *testing.T) {
	p := write(t, `
rex:
  timeout: 3s
refresh:
  debounce: 50ms
capture:
  max_lines: 100
  format: text
safety:
  allow_actions: [checkForUpdates]
keybindings:
  blocks:
    sendText: t
  universal:
    refresh: [<c-r>, R]
future_key: ignored
`)
	c, err := Load(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rex.Timeout != 3*time.Second || c.Rex.Binary != "rex" || c.Refresh.Debounce != 50*time.Millisecond ||
		c.Refresh.PollInterval != 5*time.Second || c.Capture.MaxLines != 100 || c.Capture.Format != "text" {
		t.Fatalf("overlay = %+v", c)
	}
	if !slices.Equal(c.Safety.AllowActions, []string{"checkForUpdates"}) {
		t.Fatalf("allow = %v", c.Safety.AllowActions)
	}
	if !slices.Equal(c.Keybindings["blocks"]["sendText"], KeyList{"t"}) || !slices.Equal(c.Keybindings["universal"]["refresh"], KeyList{"<c-r>", "R"}) {
		t.Fatalf("keybindings = %v", c.Keybindings)
	}
}

func TestValidate(t *testing.T) {
	for name, body := range map[string]string{
		"bad format":   "capture:\n  format: html\n",
		"zero lines":   "capture:\n  max_lines: 0\n",
		"huge lines":   "capture:\n  max_lines: 2000000\n",
		"zero timeout": "rex:\n  timeout: 0s\n",
		"neg debounce": "refresh:\n  debounce: -1s\n",
		"bad duration": "refresh:\n  poll_interval: soon\n",
		"empty binary": "rex:\n  binary: \"\"\n",
		"not yaml":     "rex: [\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body), true); err == nil {
				t.Fatalf("accepted %q", body)
			}
		})
	}
}

func TestExampleConfigParses(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "config.example.yml"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join("..", "..", "config.example.yml"))
	for _, key := range []string{"binary:", "timeout:", "poll_interval:", "capture_interval:", "debounce:", "max_lines:", "format:", "allow_actions:", "keybindings:",
		"targets:", "kind:", "server:"} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("config.example.yml does not document %s", key)
		}
	}
}

// TestTargets (A17, C1).
func TestTargets(t *testing.T) {
	c, err := Load(write(t, `
targets:
  - name: studio
    kind: native
    server: studio
    note: kept
  - kind: local
    name: home
`), true)
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{{Name: "studio", Kind: "native", Server: "studio"}, {Name: "home", Kind: "local"}}
	if !slices.Equal(c.Targets, want) {
		t.Fatalf("targets = %+v", c.Targets)
	}
	if c, err := Load(write(t, "targets:\n  - kind: local\n"), true); err != nil || len(c.Targets) != 1 {
		t.Fatalf("unnamed local: %+v, %v", c.Targets, err)
	}
}

// TestTargetsValidate (C2): each structural error names its field.
func TestTargetsValidate(t *testing.T) {
	for field, body := range map[string]string{
		"targets[0].kind":     "targets:\n  - name: a\n    server: a\n",
		"targets[0].ssh_host": "targets:\n  - name: a\n    kind: ssh\n    server: a\n",
		"targets[0].kind ":    "targets:\n  - name: a\n    kind: rsh\n    server: a\n",
		"targets[1]":          "targets:\n  - kind: local\n  - kind: local\n    name: b\n",
		"targets[0].server":   "targets:\n  - kind: local\n    server: x\n",
		"targets[0].name":     "targets:\n  - kind: native\n    server: x\n",
		"targets[1].server":   "targets:\n  - kind: native\n    name: a\n    server: a\n  - kind: native\n    name: b\n",
		"targets[1].name":     "targets:\n  - kind: native\n    name: lab\n    server: a\n  - kind: native\n    name: LAB\n    server: b\n",
		"targets[0].name ":    "targets:\n  - kind: native\n    name: Local\n    server: a\n",
		"targets[1].name  ":   "targets:\n  - kind: local\n    name: home\n  - kind: native\n    name: HOME\n    server: a\n",
	} {
		t.Run(field, func(t *testing.T) {
			_, err := Load(write(t, body), true)
			if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(field)) {
				t.Fatalf("Load = %v, want an error mentioning %q", err, strings.TrimSpace(field))
			}
		})
	}
}

func TestLoadFile(t *testing.T) {
	p := write(t, "rex:\n  timeout: 3s\n")
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	c, f, err := LoadFile(p, true)
	if err != nil || c.Rex.Timeout != 3*time.Second {
		t.Fatalf("LoadFile = %+v, %v", c, err)
	}
	raw, _ := os.ReadFile(p)
	if f.Path != p || !f.Explicit || !f.Exists || f.Sum != sha256.Sum256(raw) || f.Mode.Perm() != 0o640 {
		t.Fatalf("file = %+v", f)
	}
	missing := filepath.Join(t.TempDir(), "nope.yml")
	_, f, err = LoadFile(missing, false)
	if err != nil || f.Exists || f.Path != missing {
		t.Fatalf("missing: %+v, %v", f, err)
	}
	if c, err := Parse([]byte("capture:\n  format: text\n")); err != nil || c.Capture.Format != "text" || c.Rex.Binary != "rex" {
		t.Fatalf("Parse = %+v, %v", c, err)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := DefaultPath(); got != "/xdg/lazyrex/config.yml" {
		t.Fatalf("DefaultPath = %q", got)
	}
}
