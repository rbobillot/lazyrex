// Package config loads ~/.config/lazyrex/config.yml.
package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// KeyList is one key or a list of keys in YAML.
type KeyList []string

func (k *KeyList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*k = KeyList{n.Value}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := n.Decode(&list); err != nil {
			return err
		}
		*k = list
		return nil
	}
	return fmt.Errorf("line %d: a key binding is a key or a list of keys", n.Line)
}

// Target is one entry of targets: kind local renames the built-in local
// target, kind native reaches a Rex server through rex -S SERVER, kind ssh
// reaches Rex on another machine through ssh.
type Target struct {
	Name    string `yaml:"name"`
	Kind    string `yaml:"kind"`
	Server  string `yaml:"server"`
	SSHHost string `yaml:"ssh_host"`
	Mode    string `yaml:"mode"`
	RexPath string `yaml:"rex_path"`
}

type Config struct {
	Rex struct {
		Binary  string        `yaml:"binary"`
		Timeout time.Duration `yaml:"timeout"`
	} `yaml:"rex"`
	Refresh struct {
		PollInterval    time.Duration `yaml:"poll_interval"`
		CaptureInterval time.Duration `yaml:"capture_interval"`
		Debounce        time.Duration `yaml:"debounce"`
	} `yaml:"refresh"`
	Capture struct {
		MaxLines int    `yaml:"max_lines"`
		Format   string `yaml:"format"`
	} `yaml:"capture"`
	Safety struct {
		AllowActions []string `yaml:"allow_actions"`
	} `yaml:"safety"`
	Experimental struct {
		RexHostsFile bool `yaml:"rex_hosts_file"`
	} `yaml:"experimental"`
	// Keybindings maps panel (universal, targets, sessions, windows, blocks,
	// clients, main) to action to the keys replacing the action's defaults.
	Keybindings map[string]map[string]KeyList `yaml:"keybindings"`
	Targets     []Target                      `yaml:"targets"`
}

// File is the config file as lazyrex loaded it.
type File struct {
	Path     string
	Explicit bool
	Exists   bool
	Sum      [32]byte
	Mode     fs.FileMode
}

// Default returns the built-in configuration.
func Default() Config {
	var c Config
	c.Rex.Binary = "rex"
	c.Rex.Timeout = 10 * time.Second
	c.Refresh.PollInterval = 5 * time.Second
	c.Refresh.CaptureInterval = time.Second
	c.Refresh.Debounce = 200 * time.Millisecond
	c.Capture.MaxLines = 5000
	c.Capture.Format = "vt"
	return c
}

// DefaultPath is $XDG_CONFIG_HOME/lazyrex/config.yml, else ~/.config/....
func DefaultPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "lazyrex", "config.yml")
}

// Parse overlays a config file's content on the defaults and validates it.
func Parse(b []byte) (Config, error) {
	c := Default()
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// LoadFile overlays the file at path on the defaults. A missing file is an
// error only when the path was given explicitly.
func LoadFile(path string, explicit bool) (Config, File, error) {
	f := File{Path: path, Explicit: explicit}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		return Default(), f, nil
	case err != nil:
		return Default(), f, fmt.Errorf("read config: %w", err)
	}
	f.Exists, f.Sum = true, sha256.Sum256(b)
	if st, err := os.Stat(path); err == nil {
		f.Mode = st.Mode().Perm()
	}
	c, err := Parse(b)
	if err != nil {
		return c, f, fmt.Errorf("%s: %w", path, err)
	}
	return c, f, nil
}

// Load is LoadFile without the file's details.
func Load(path string, explicit bool) (Config, error) {
	c, _, err := LoadFile(path, explicit)
	return c, err
}

// Validate checks ranges and enumerations.
func (c Config) Validate() error {
	switch {
	case c.Rex.Binary == "":
		return errors.New("rex.binary is empty")
	case c.Rex.Timeout <= 0:
		return errors.New("rex.timeout must be positive")
	case c.Refresh.PollInterval <= 0:
		return errors.New("refresh.poll_interval must be positive")
	case c.Refresh.CaptureInterval <= 0:
		return errors.New("refresh.capture_interval must be positive")
	case c.Refresh.Debounce <= 0:
		return errors.New("refresh.debounce must be positive")
	case c.Capture.MaxLines < 1 || c.Capture.MaxLines > 1_000_000:
		return errors.New("capture.max_lines must be between 1 and 1000000")
	case c.Capture.Format != "vt" && c.Capture.Format != "text":
		return fmt.Errorf("capture.format must be vt or text, got %q", c.Capture.Format)
	}
	return validateTargets(c.Targets)
}

// validateTargets checks the structure of targets; whether a name or server
// is safe to pass to rex is checked by package rex.
func validateTargets(ts []Target) error {
	localName, seenLocal := "local", false
	for i, t := range ts {
		if t.Kind != "ssh" {
			for _, kv := range [][2]string{{"ssh_host", t.SSHHost}, {"mode", t.Mode}, {"rex_path", t.RexPath}} {
				if kv[1] != "" && t.Kind != "" {
					return fmt.Errorf("targets[%d].%s: only kind: ssh targets have it", i, kv[0])
				}
			}
		}
		switch t.Kind {
		case "":
			return fmt.Errorf("targets[%d].kind is required (local, native or ssh)", i)
		case "local":
			switch {
			case seenLocal:
				return fmt.Errorf("targets[%d]: at most one kind: local target", i)
			case t.Server != "":
				return fmt.Errorf("targets[%d].server: a local target has no server", i)
			}
			seenLocal = true
			if t.Name != "" {
				localName = t.Name
			}
		case "native":
			switch {
			case t.Name == "":
				return fmt.Errorf("targets[%d].name is required", i)
			case t.Server == "":
				return fmt.Errorf("targets[%d].server is required", i)
			case strings.EqualFold(t.Name, "local"):
				return fmt.Errorf("targets[%d].name: local is the built-in local target", i)
			}
		case "ssh":
			switch {
			case t.Name == "":
				return fmt.Errorf("targets[%d].name is required", i)
			case t.SSHHost == "":
				return fmt.Errorf("targets[%d].ssh_host is required", i)
			case t.Server != "":
				return fmt.Errorf("targets[%d].server: an ssh target has ssh_host, not server", i)
			case t.Mode != "" && t.Mode != "forward" && t.Mode != "exec":
				return fmt.Errorf("targets[%d].mode: want forward or exec, got %q", i, t.Mode)
			case strings.EqualFold(t.Name, "local"):
				return fmt.Errorf("targets[%d].name: local is the built-in local target", i)
			}
		default:
			return fmt.Errorf("targets[%d].kind: want local, native or ssh, got %q", i, t.Kind)
		}
	}
	names := map[string]string{strings.ToLower(localName): "the local target"}
	for i, t := range ts {
		if t.Kind != "native" && t.Kind != "ssh" {
			continue
		}
		if other, ok := names[strings.ToLower(t.Name)]; ok {
			return fmt.Errorf("targets[%d].name: %q is already the name of %s", i, t.Name, other)
		}
		names[strings.ToLower(t.Name)] = fmt.Sprintf("targets[%d]", i)
	}
	return nil
}
