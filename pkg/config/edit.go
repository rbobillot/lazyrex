package config

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// ErrChanged refuses an edit of a file that changed since it was loaded.
var ErrChanged = errors.New("the config file changed on disk since lazyrex loaded it")

// ErrNotSynced reports a config file replaced whose directory could not be
// synced: the edit is visible but may not survive a crash.
var ErrNotSynced = errors.New("the config file was replaced but may not be on disk yet")

// openDir and dirSync open and sync a directory; tests replace them.
var (
	openDir = os.Open
	dirSync = (*os.File).Sync
)

func byHand(format string, a ...any) error {
	return fmt.Errorf(format+"; edit the config file by hand", a...)
}

// Edit changes the targets list of the config file f, keeping its comments
// and unknown keys. It refuses a file that changed since f was loaded, and a
// result that does not load or that check rejects; then the file is left as
// it was. The file is replaced atomically, through its symlink if it is one.
// An ErrNotSynced error comes with the new Config and File: the file was
// replaced.
func Edit(f File, change func(targets *yaml.Node) error, check func(Config) error) (Config, File, error) {
	if f.Path == "" {
		return Config{}, f, errors.New("no config file path (no home directory); pass --config PATH")
	}
	path, err := realPath(f.Path)
	if err != nil {
		return Config{}, f, err
	}
	b, err := os.ReadFile(path)
	exists := err == nil
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return Config{}, f, fmt.Errorf("read config: %w", err)
	case exists != f.Exists, exists && sha256.Sum256(b) != f.Sum:
		return Config{}, f, ErrChanged
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return Config{}, f, fmt.Errorf("%s: %w", f.Path, err)
	}
	var buf bytes.Buffer
	if doc.Kind == 0 && len(bytes.TrimSpace(b)) > 0 {
		// yaml.v3 parses a comment-only file to no node, losing the comments.
		buf.Write(bytes.TrimRight(b, "\n"))
		buf.WriteString("\n")
	}
	seq, err := targetsNode(&doc)
	if err != nil {
		return Config{}, f, err
	}
	if err := change(seq); err != nil {
		return Config{}, f, err
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return Config{}, f, err
	}
	if err := enc.Close(); err != nil {
		return Config{}, f, err
	}
	out := buf.Bytes()
	cfg, err := Parse(out)
	if err != nil {
		return Config{}, f, fmt.Errorf("the edited config would not load: %w", err)
	}
	if check != nil {
		if err := check(cfg); err != nil {
			return Config{}, f, err
		}
	}
	mode := fs.FileMode(0o600)
	if exists {
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
	}
	nf := File{Path: f.Path, Explicit: f.Explicit, Exists: true, Sum: sha256.Sum256(out), Mode: mode}
	if err := writeAtomic(path, out, mode); err != nil {
		if errors.Is(err, ErrNotSynced) {
			return cfg, nf, err
		}
		return Config{}, f, err
	}
	return cfg, nf, nil
}

// realPath resolves symlinks so the link itself is kept.
func realPath(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&fs.ModeSymlink == 0 {
		return path, nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("config %s is a symlink lazyrex cannot follow: %w", path, err)
	}
	return real, nil
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

// targetsNode returns the targets sequence of doc, adding an empty one, and
// a top-level mapping for an empty or comment-only file.
func targetsNode(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind == 0 {
		*doc = yaml.Node{Kind: yaml.DocumentNode}
	}
	if doc.Kind != yaml.DocumentNode {
		return nil, byHand("the config file is not a YAML document")
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	root := doc.Content[0]
	if isNull(root) {
		*root = yaml.Node{Kind: yaml.MappingNode, HeadComment: root.HeadComment, LineComment: root.LineComment, FootComment: root.FootComment}
	}
	if root.Kind != yaml.MappingNode {
		return nil, byHand("the top level of the config file is not a mapping")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Value != "targets" {
			continue
		}
		switch {
		case v.Kind == yaml.AliasNode || v.Anchor != "":
			return nil, byHand("targets is an alias or an anchor")
		case isNull(v):
			*v = yaml.Node{Kind: yaml.SequenceNode, HeadComment: v.HeadComment, LineComment: v.LineComment, FootComment: v.FootComment}
		case v.Kind != yaml.SequenceNode:
			return nil, byHand("targets is not a list")
		}
		v.Style &^= yaml.FlowStyle
		return v, nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "targets"}, seq)
	return seq, nil
}

func str(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func value(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i]; k.Kind == yaml.ScalarNode && k.Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setValue(m *yaml.Node, key, v string) {
	if n := value(m, key); n != nil {
		n.Kind, n.Tag, n.Value, n.Style, n.Content = yaml.ScalarNode, "!!str", v, 0, nil
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, str(v))
}

// findEntry returns the index of the entry named name, ignoring case; a
// kind: local entry without a name is named local.
func findEntry(seq *yaml.Node, name string) (int, error) {
	for i, e := range seq.Content {
		if e.Kind != yaml.MappingNode {
			return 0, byHand("targets[%d] is not a mapping", i)
		}
		n := ""
		if v := value(e, "name"); v != nil {
			n = v.Value
		}
		if k := value(e, "kind"); n == "" && k != nil && k.Value == "local" {
			n = "local"
		}
		if strings.EqualFold(n, name) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no config target named %q", name)
}

// AddTarget appends t to targets.
func AddTarget(t Target) func(*yaml.Node) error {
	return func(seq *yaml.Node) error {
		m := &yaml.Node{Kind: yaml.MappingNode}
		for _, kv := range [][2]string{{"name", t.Name}, {"kind", t.Kind}, {"server", t.Server},
			{"ssh_host", t.SSHHost}, {"mode", t.Mode}, {"rex_path", t.RexPath}} {
			if kv[1] != "" {
				m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: kv[0]}, str(kv[1]))
			}
		}
		seq.Content = append(seq.Content, m)
		return nil
	}
}

// UpdateTarget sets the name and the non-empty server, ssh_host and mode of
// the entry named name, keeping its other keys and comments.
func UpdateTarget(name string, t Target) func(*yaml.Node) error {
	return func(seq *yaml.Node) error {
		i, err := findEntry(seq, name)
		if err != nil {
			return err
		}
		setValue(seq.Content[i], "name", t.Name)
		for _, kv := range [][2]string{{"server", t.Server}, {"ssh_host", t.SSHHost}, {"mode", t.Mode}} {
			if kv[1] != "" {
				setValue(seq.Content[i], kv[0], kv[1])
			}
		}
		return nil
	}
}

// RemoveTarget removes the entry named name.
func RemoveTarget(name string) func(*yaml.Node) error {
	return func(seq *yaml.Node) error {
		i, err := findEntry(seq, name)
		if err != nil {
			return err
		}
		seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
		return nil
	}
}

func writeAtomic(path string, data []byte, mode fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	d, err := openDir(dir)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".lazyrex-config-*.tmp")
	if err != nil {
		d.Close()
		return err
	}
	renamed := false
	defer func() {
		if err != nil && !renamed {
			tmp.Close()
			os.Remove(tmp.Name())
			d.Close()
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	renamed = true
	if err = syncDir(d); err != nil {
		return fmt.Errorf("%w: sync %s: %w", ErrNotSynced, dir, err)
	}
	return nil
}

// syncDir syncs and closes d; a platform or filesystem that cannot sync a
// directory is not an error.
func syncDir(d *os.File) error {
	err := dirSync(d)
	if runtime.GOOS == "windows" || errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported) {
		err = nil
	}
	return errors.Join(err, d.Close())
}
