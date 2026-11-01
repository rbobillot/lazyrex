package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadFile(t *testing.T, path string) File {
	t.Helper()
	_, f, err := LoadFile(path, true)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err != nil {
		_, f, err = LoadFile(path, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func native(name, server string) Target { return Target{Name: name, Kind: "native", Server: server} }

func stub[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func noTemps(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

const commented = `# my lazyrex config
rex:
  timeout: 3s # fast
future_key: kept
targets:
  # the studio Mac
  - name: studio
    kind: native
    server: studio
    note: hand written
`

// TestEditKeepsComments (E1, E8).
func TestEditKeepsComments(t *testing.T) {
	p := write(t, commented)
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	c, f, err := Edit(loadFile(t, p), AddTarget(native("lab", "lab.ts.net")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Targets, []Target{native("studio", "studio"), native("lab", "lab.ts.net")}) {
		t.Fatalf("targets = %+v", c.Targets)
	}
	raw, _ := os.ReadFile(p)
	for _, part := range []string{"# my lazyrex config", "# fast", "future_key: kept", "# the studio Mac", "note: hand written", "lab.ts.net"} {
		if !strings.Contains(string(raw), part) {
			t.Errorf("lost %q:\n%s", part, raw)
		}
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", st.Mode())
	}
	if !f.Exists || f.Path != p {
		t.Fatalf("file = %+v", f)
	}
	noTemps(t, filepath.Dir(p))

	_, _, err = Edit(f, UpdateTarget("studio", native("mac", "studio.lan")), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	if !strings.Contains(string(raw), "note: hand written") || !strings.Contains(string(raw), "# the studio Mac") ||
		!strings.Contains(string(raw), "name: mac") || !strings.Contains(string(raw), "studio.lan") {
		t.Fatalf("update:\n%s", raw)
	}
}

// TestEditCreates (E2).
func TestEditCreates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "lazyrex")
	p := filepath.Join(dir, "config.yml")
	c, f, err := Edit(loadFile(t, p), AddTarget(native("studio", "studio")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Targets) != 1 || !f.Exists {
		t.Fatalf("created %+v, %+v", c.Targets, f)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", st.Mode())
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v", st.Mode())
	}
	if _, _, err := Edit(File{}, AddTarget(native("x", "x")), nil); err == nil {
		t.Fatalf("edit without a path accepted")
	}
}

// TestEditRefusesChangedFile (E3, E4).
func TestEditRefusesChangedFile(t *testing.T) {
	p := write(t, commented)
	f := loadFile(t, p)
	if err := os.WriteFile(p, []byte(commented+"# edited elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Edit(f, AddTarget(native("lab", "lab")), nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	if raw, _ := os.ReadFile(p); string(raw) != commented+"# edited elsewhere\n" {
		t.Fatalf("file changed:\n%s", raw)
	}
	noTemps(t, filepath.Dir(p))

	missing := filepath.Join(t.TempDir(), "config.yml")
	f = loadFile(t, missing)
	if err := os.WriteFile(missing, []byte("rex:\n  timeout: 3s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Edit(f, AddTarget(native("lab", "lab")), nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("created since load: %v, want ErrChanged", err)
	}
}

// TestEditCheckRejects (E5).
func TestEditCheckRejects(t *testing.T) {
	p := write(t, commented)
	f := loadFile(t, p)
	reject := errors.New("server is bad")
	if _, _, err := Edit(f, AddTarget(native("lab", "lab")), func(Config) error { return reject }); !errors.Is(err, reject) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := Edit(f, AddTarget(native("STUDIO", "x")), nil); err == nil {
		t.Fatalf("a duplicate name passed validation")
	}
	if raw, _ := os.ReadFile(p); string(raw) != commented {
		t.Fatalf("file changed:\n%s", raw)
	}
	noTemps(t, filepath.Dir(p))
}

// TestEditQuoting (E6): values YAML would read as another type stay strings.
func TestEditQuoting(t *testing.T) {
	p := write(t, "")
	f := loadFile(t, p)
	add := []Target{native("v6", "[::1]:7297"), native("yes", "yes"), native("tag", "#tag"), native("Studio Mac", "Studio Mac"), native("007", "007")}
	for _, tg := range add {
		var err error
		if _, f, err = Edit(f, AddTarget(tg), nil); err != nil {
			t.Fatalf("add %+v: %v", tg, err)
		}
	}
	c, err := Load(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Targets, add) {
		t.Fatalf("targets = %+v", c.Targets)
	}
}

// TestEditRemove (E7).
func TestEditRemove(t *testing.T) {
	p := write(t, commented)
	c, _, err := Edit(loadFile(t, p), RemoveTarget("STUDIO"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Targets) != 0 {
		t.Fatalf("targets = %+v", c.Targets)
	}
	if c, err := Load(p, true); err != nil || len(c.Targets) != 0 || c.Rex.Timeout.String() != "3s" {
		t.Fatalf("after removing the last target: %+v, %v", c, err)
	}
	if _, _, err := Edit(loadFile(t, p), RemoveTarget("ghost"), nil); err == nil {
		t.Fatalf("removing a missing target accepted")
	}
}

// TestEditRefusesShapes (E9).
func TestEditRefusesShapes(t *testing.T) {
	for name, body := range map[string]string{
		"alias":     "base: &t\n  - name: a\n    kind: native\n    server: a\ntargets: *t\n",
		"scalar":    "targets: 3\n",
		"top level": "- a\n- b\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := write(t, body)
			f := File{Path: p, Explicit: true, Exists: true}
			raw, _ := os.ReadFile(p)
			f.Sum = sha256.Sum256(raw)
			if _, _, err := Edit(f, AddTarget(native("lab", "lab")), nil); err == nil {
				t.Fatalf("edited %q", body)
			}
			if after, _ := os.ReadFile(p); string(after) != body {
				t.Fatalf("file changed:\n%s", after)
			}
		})
	}
}

// TestEditSymlink (E10).
func TestEditSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yml")
	if err := os.WriteFile(real, []byte(commented), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Edit(loadFile(t, link), AddTarget(native("lab", "lab")), nil); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v, %v", st, err)
	}
	if raw, _ := os.ReadFile(real); !strings.Contains(string(raw), "name: lab") {
		t.Fatalf("target not written:\n%s", raw)
	}
}

// TestEditDirOpenFails (6a): a directory that cannot be opened refuses the
// edit before the file is replaced.
func TestEditDirOpenFails(t *testing.T) {
	p := write(t, commented)
	f := loadFile(t, p)
	stub(t, &openDir, func(string) (*os.File, error) { return nil, errors.New("boom") })
	_, _, err := Edit(f, AddTarget(native("lab", "lab")), nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || errors.Is(err, ErrNotSynced) {
		t.Fatalf("err = %v", err)
	}
	if raw, _ := os.ReadFile(p); string(raw) != commented {
		t.Fatalf("file changed:\n%s", raw)
	}
	noTemps(t, filepath.Dir(p))
}

// TestEditDirSyncFails (6b): a failed directory sync reports the replaced
// file with ErrNotSynced.
func TestEditDirSyncFails(t *testing.T) {
	p := write(t, commented)
	stub(t, &dirSync, func(*os.File) error { return syscall.EIO })
	c, nf, err := Edit(loadFile(t, p), AddTarget(native("lab", "lab")), nil)
	if !errors.Is(err, ErrNotSynced) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("err = %v, want ErrNotSynced", err)
	}
	if !slices.Contains(c.Targets, native("lab", "lab")) {
		t.Fatalf("targets = %+v", c.Targets)
	}
	raw, _ := os.ReadFile(p)
	if !strings.Contains(string(raw), "name: lab") || nf.Sum != sha256.Sum256(raw) || !nf.Exists {
		t.Fatalf("file %+v:\n%s", nf, raw)
	}
	noTemps(t, filepath.Dir(p))
}

// TestEditDirSyncUnsupported (6c): a filesystem that cannot sync a
// directory is not an error.
func TestEditDirSyncUnsupported(t *testing.T) {
	for name, e := range map[string]error{
		"EINVAL":         syscall.EINVAL,
		"ErrUnsupported": fmt.Errorf("sync: %w", errors.ErrUnsupported),
		"PathError":      &os.PathError{Op: "sync", Err: errors.ErrUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			expectSyncIgnored(t, e)
		})
	}
}

func expectSyncIgnored(t *testing.T, e error) {
	t.Helper()
	p := write(t, commented)
	stub(t, &dirSync, func(*os.File) error { return e })
	if _, _, err := Edit(loadFile(t, p), AddTarget(native("lab", "lab")), nil); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(p); !strings.Contains(string(raw), "name: lab") {
		t.Fatalf("file not updated:\n%s", raw)
	}
}

// TestEditFlowAndEmpty (E11): an empty flow list, a null value and a
// comment-only file all take a new entry.
func TestEditFlowAndEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"flow":    "targets: []\n",
		"null":    "targets:\n",
		"comment": "# nothing yet\n",
		"missing": "rex:\n  binary: rex\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := write(t, body)
			c, _, err := Edit(loadFile(t, p), AddTarget(native("studio", "studio")), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(c.Targets, []Target{native("studio", "studio")}) {
				t.Fatalf("targets = %+v", c.Targets)
			}
			raw, _ := os.ReadFile(p)
			var doc yaml.Node
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "[") {
				t.Fatalf("still a flow list:\n%s", raw)
			}
			if name == "comment" && !strings.Contains(string(raw), "# nothing yet") {
				t.Fatalf("comment lost:\n%s", raw)
			}
		})
	}
}
