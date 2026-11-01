package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// docsRepo makes a committed repository holding docs/index.md, docs/a.md and the extra docs given, and runs
// the test from it.
func docsRepo(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := newRepo(t)
	writeFile(t, dir, "docs/index.md", "# I\n\n[a](a.md)\n")
	writeFile(t, dir, "docs/a.md", "# A\n")
	for path, text := range extra {
		writeFile(t, dir, path, text)
	}
	commitAll(t, dir, "docs", "2026-01-01T00:00:00Z")
	t.Chdir(dir)
	return dir
}

func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestRunWritesPages(t *testing.T) {
	docsRepo(t, nil)
	out := filepath.Join(t.TempDir(), "wiki")
	code, _, stderr := runCLI("-repo", "o/r", "-out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got, want := listDir(t, out), []string{"Home.md", "_Sidebar.md", "a.md"}; !slices.Equal(got, want) {
		t.Fatalf("pages %v, want %v", got, want)
	}
	for _, name := range listDir(t, out) {
		info, err := os.Stat(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode %v, want 0644", name, info.Mode().Perm())
		}
		text, _ := os.ReadFile(filepath.Join(out, name))
		if !strings.HasPrefix(string(text), bannerPrefix) {
			t.Errorf("%s has no banner", name)
		}
	}
	home, _ := os.ReadFile(filepath.Join(out, "Home.md"))
	if !strings.Contains(string(home), "(https://github.com/o/r/wiki/a)") {
		t.Errorf("Home.md does not link page a through -repo o/r:\n%s", home)
	}
}

func TestRunEmptyOutDir(t *testing.T) {
	docsRepo(t, nil)
	out := t.TempDir()
	if code, _, stderr := runCLI("-out", out); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(listDir(t, out)) != 3 {
		t.Fatalf("pages %v", listDir(t, out))
	}
}

func TestRunCheckOnly(t *testing.T) {
	docsRepo(t, nil)
	if code, _, stderr := runCLI(); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestRunRefusesNonEmptyOut(t *testing.T) {
	docsRepo(t, nil)
	out := t.TempDir()
	writeFile(t, out, "stale.md", "old\n")
	if code, _, _ := runCLI("-out", out); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if got := listDir(t, out); !slices.Equal(got, []string{"stale.md"}) {
		t.Fatalf("out holds %v, want only stale.md", got)
	}
}

func TestRunFindings(t *testing.T) {
	docsRepo(t, map[string]string{"docs/b.md": "# B\n\n[x](missing.md)\n"})
	out := filepath.Join(t.TempDir(), "wiki")
	code, _, stderr := runCLI("-out", out)
	if code != 1 || !strings.Contains(stderr, "docs/b.md:3: broken link missing.md") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("out was created despite findings: %v", err)
	}
}

func TestRunUsage(t *testing.T) {
	docsRepo(t, nil)
	for _, args := range [][]string{{"-repo", "a/b;c"}, {"-repo", "a"}, {"-repo", "a/b/c"}, {"-nope"}, {"extra"}} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
	}
}

func TestRunOutsideRepository(t *testing.T) {
	newRepo(t)
	t.Chdir(t.TempDir())
	if code, _, _ := runCLI(); code != 1 {
		t.Fatalf("exit %d outside a repository, want 1", code)
	}
}
