package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo makes an empty repository on branch main, with git isolated from the user's configuration.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("GIT_AUTHOR_NAME", "Fixture")
	t.Setenv("GIT_AUTHOR_EMAIL", "fixture@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Fixture")
	t.Setenv("GIT_COMMITTER_EMAIL", "fixture@example.com")
	dir := t.TempDir()
	gitIn(t, dir, nil, "-c", "init.defaultBranch=main", "init", "-q")
	return dir
}

func gitIn(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgSign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, rel, text string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commitAll commits the whole worktree at date and returns the commit id.
func commitAll(t *testing.T, dir, msg, date string) string {
	t.Helper()
	gitIn(t, dir, nil, "add", "-A")
	gitIn(t, dir, []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "-m", msg)
	return gitIn(t, dir, nil, "rev-parse", "HEAD")
}

func sourceOf(t *testing.T, in Input, path string) Source {
	t.Helper()
	for _, s := range in.Sources {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("no source %s in %v", path, in.Sources)
	return Source{}
}

func TestCollectShas(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "docs/index.md", "# I\n\n[a](a.md) [b](b.md)\n")
	writeFile(t, dir, "docs/a.md", "# A\n")
	writeFile(t, dir, "docs/img/a.png", "png")
	first := commitAll(t, dir, "one", "2026-01-01T00:00:00Z")
	writeFile(t, dir, "docs/b.md", "# B\n")
	writeFile(t, dir, "README.md", "# R\n")
	second := commitAll(t, dir, "two", "2026-01-02T00:00:00Z")

	in, err := collect(context.Background(), dir, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceOf(t, in, "docs/a.md").SHA; got != first {
		t.Errorf("docs/a.md sha %s, want %s", got, first)
	}
	if got := sourceOf(t, in, "docs/b.md").SHA; got != second {
		t.Errorf("docs/b.md sha %s, want %s", got, second)
	}
	if in.DocsSHA != second || in.Repo != "o/r" {
		t.Errorf("DocsSHA %s repo %s, want %s o/r", in.DocsSHA, in.Repo, second)
	}
	if len(in.Sources) != 3 {
		t.Errorf("sources %v, want the 3 docs/*.md", in.Sources)
	}
	want := []string{"README.md", "docs/a.md", "docs/b.md", "docs/img/a.png", "docs/index.md"}
	if strings.Join(in.Files, ",") != strings.Join(want, ",") {
		t.Errorf("files %v, want %v", in.Files, want)
	}
	pages, findings := Render(in)
	if findings != nil {
		t.Fatal(findings)
	}
	if sidebar := pageText(t, pages, "_Sidebar"); !strings.Contains(sidebar, "/tree/"+second+"/docs) at `"+second[:12]+"`") {
		t.Errorf("_Sidebar does not name docs sha %s:\n%s", second, sidebar)
	}
}

func TestCollectRefusesShallow(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "docs/index.md", "# I\n")
	commitAll(t, dir, "one", "2026-01-01T00:00:00Z")
	writeFile(t, dir, "docs/a.md", "# A\n")
	commitAll(t, dir, "two", "2026-01-02T00:00:00Z")
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, dir, nil, "clone", "-q", "--depth", "1", "file://"+dir, clone)
	if _, err := collect(context.Background(), clone, "o/r"); err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("collect on a shallow clone: %v, want a shallow error", err)
	}
}

func TestCollectRefusesNonRegular(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"symlink page": func(t *testing.T, dir string) {
			writeFile(t, dir, "docs/index.md", "# I\n")
			if err := os.Symlink("index.md", filepath.Join(dir, "docs", "l.md")); err != nil {
				t.Fatal(err)
			}
		},
		"docs symlink": func(t *testing.T, dir string) {
			writeFile(t, dir, "real/index.md", "# I\n")
			if err := os.Symlink("real", filepath.Join(dir, "docs")); err != nil {
				t.Fatal(err)
			}
		},
		"submodule": func(t *testing.T, dir string) {
			writeFile(t, dir, "docs/index.md", "# I\n")
			gitIn(t, dir, nil, "add", "-A")
			gitIn(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("a", 40)+",docs/sub")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			setup(t, dir)
			if name == "submodule" {
				gitIn(t, dir, nil, "commit", "-q", "-m", "sub")
			} else {
				commitAll(t, dir, "one", "2026-01-01T00:00:00Z")
			}
			if _, err := collect(context.Background(), dir, "o/r"); err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("collect: %v, want a not-a-regular-file error", err)
			}
		})
	}
}

func TestCollectReadsHEADOnly(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "docs/index.md", "# I\n\n[b](b.md)\n")
	writeFile(t, dir, "docs/b.md", "# B committed\n")
	commitAll(t, dir, "one", "2026-01-01T00:00:00Z")
	writeFile(t, dir, "docs/b.md", "# B edited\n")
	writeFile(t, dir, "docs/n.md", "# N untracked\n")

	in, err := collect(context.Background(), dir, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if got := sourceOf(t, in, "docs/b.md").Text; got != "# B committed\n" {
		t.Errorf("docs/b.md text %q, want the committed one", got)
	}
	for _, s := range in.Sources {
		if s.Path == "docs/n.md" {
			t.Errorf("untracked docs/n.md became a source")
		}
	}

	writeFile(t, dir, "docs/index.md", "# I\n\n[n](n.md)\n")
	gitIn(t, dir, nil, "add", "docs/index.md")
	gitIn(t, dir, nil, "commit", "-q", "-m", "link n")
	in, err = collect(context.Background(), dir, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	_, findings := Render(in)
	if len(findings) != 1 || !strings.Contains(findings[0].Msg, "broken link n.md") {
		t.Fatalf("findings %v, want a broken link to the untracked docs/n.md", findings)
	}
}
