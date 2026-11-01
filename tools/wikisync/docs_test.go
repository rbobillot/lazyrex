package main

import (
	"context"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const realWiki = "https://github.com/rbobillot/lazyrex/wiki/"

var (
	inlineDest = regexp.MustCompile(`\]\(([^)\s]*)`)
	codeSpan   = regexp.MustCompile("`[^`]*`")
)

// proseLinks lists the inline link destinations of text outside fences and code spans with regular
// expressions, independently of the renderer's scanner.
func proseLinks(text string) []string {
	var out []string
	fenced := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		for _, m := range inlineDest.FindAllStringSubmatch(codeSpan.ReplaceAllString(l, ""), -1) {
			out = append(out, m[1])
		}
	}
	return out
}

func absolute(dest string) bool {
	return strings.HasPrefix(dest, "https://") || strings.HasPrefix(dest, "mailto:") || strings.HasPrefix(dest, "#")
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %q: %v", args, err)
	}
	return string(out)
}

// TestRealDocs renders this repository's committed docs/ through the production path, collect on HEAD.
func TestRealDocs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	top, err := exec.Command("git", "-C", "../..", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not in a git repository")
	}
	root := strings.TrimSpace(string(top))
	if strings.TrimSpace(gitOut(t, root, "rev-parse", "--is-shallow-repository")) != "false" {
		t.Skip("shallow clone")
	}
	in, err := collect(context.Background(), root, "rbobillot/lazyrex")
	if err != nil {
		t.Fatal(err)
	}
	pages, findings := Render(in)
	if findings != nil {
		t.Fatalf("findings in docs/:\n%v", findings)
	}

	var docs []string
	for _, p := range strings.Split(gitOut(t, root, "ls-tree", "-r", "-z", "--name-only", "HEAD", "--", "docs"), "\x00") {
		if strings.HasSuffix(p, ".md") {
			docs = append(docs, p)
		}
	}
	if len(pages) != len(docs)+1 {
		t.Fatalf("%d pages %v for %d docs %v, want one per doc plus _Sidebar", len(pages), names(pages), len(docs), docs)
	}

	type entry struct{ name, title string }
	var others []entry
	var home entry
	for _, doc := range docs {
		rel := strings.TrimPrefix(doc, "docs/")
		if strings.Contains(rel, "/") {
			t.Fatalf("%s: this test expects flat docs/; teach it nested names", doc)
		}
		source := gitOut(t, root, "cat-file", "blob", "HEAD:"+doc)
		first, _, _ := strings.Cut(source, "\n")
		e := entry{strings.TrimSuffix(path.Base(rel), ".md"), strings.TrimPrefix(first, "# ")}
		if e.name == "index" {
			e.name, home = "Home", e
		} else {
			others = append(others, e)
		}
		text := pageText(t, pages, e.name)
		if want := proseLinks(source); len(proseLinks(text)) != len(want)+1 {
			t.Errorf("%s: %d links rendered, want the %d of the source plus the banner", e.name, len(proseLinks(text)), len(want))
		}
	}
	slices.SortFunc(others, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	want := []string{"- [" + home.title + "](" + realWiki + "Home)"}
	for _, e := range others {
		want = append(want, "- ["+e.title+"]("+realWiki+e.name+")")
	}
	sidebar := strings.Split(strings.TrimSuffix(pageText(t, pages, "_Sidebar"), "\n"), "\n")
	if len(sidebar) < 2 || sidebar[1] != "" || !slices.Equal(sidebar[2:], want) {
		t.Errorf("_Sidebar entries:\n%s\nwant\n%s", strings.Join(sidebar, "\n"), strings.Join(want, "\n"))
	}

	for _, p := range pages {
		if !strings.HasPrefix(p.Text, bannerPrefix) {
			t.Errorf("%s: line 1 lacks the banner", p.Name)
		}
		for _, d := range destinations(p.Text) {
			if !absolute(d) {
				t.Errorf("%s: relative destination %q left (scanner)", p.Name, d)
			}
		}
		for _, d := range proseLinks(p.Text) {
			if !absolute(d) {
				t.Errorf("%s: relative destination %q left (regexp)", p.Name, d)
			}
		}
	}

	if got := pageText(t, pages, "_Sidebar"); !strings.Contains(got, "- [Getting started]("+realWiki+"getting-started)") {
		t.Errorf("getting-started is not titled Getting started:\n%s", got)
	}
	pins := map[string][]string{
		"Home":         {"(" + realWiki + "getting-started)", "(https://github.com/rbobillot/lazyrex/blob/main/README.md)"},
		"architecture": {"(https://github.com/rbobillot/lazyrex/blob/main/cmd/lazyrex/main.go)"},
	}
	for name, links := range pins {
		for _, l := range links {
			if !strings.Contains(pageText(t, pages, name), l) {
				t.Errorf("%s lacks %s", name, l)
			}
		}
	}
}
