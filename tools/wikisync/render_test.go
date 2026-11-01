package main

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
)

const (
	testSHA  = "1111111111111111111111111111111111111111"
	testDocs = "dddddddddddddddddddddddddddddddddddddddd"
	testRepo = "o/r"
	u        = "https://github.com/o/r"
)

var extraFiles = []string{
	"README.md", "LICENSE", "cmd/lazyrex/main.go", "pkg/rex/client.go",
	".agents/skills/multi-agent/SKILL.md", "scripts/a(b).sh", "docs/img/a.png",
}

func src(path, text string) Source { return Source{Path: path, SHA: testSHA, Text: text} }

func input(sources ...Source) Input {
	files := slices.Clone(extraFiles)
	for _, s := range sources {
		files = append(files, s.Path)
	}
	slices.Sort(files)
	return Input{Repo: testRepo, DocsSHA: testDocs, Sources: sources, Files: files}
}

func banner(path, sha string) string {
	return "> Generated from [" + path + "](" + u + "/blob/" + sha + "/" + path + ") at `" + sha[:12] +
		"`: edit the source in the repository, not this wiki."
}

func pageText(t *testing.T, pages []Page, name string) string {
	t.Helper()
	for _, p := range pages {
		if p.Name == name {
			return p.Text
		}
	}
	t.Fatalf("no page %q in %v", name, names(pages))
	return ""
}

func names(pages []Page) []string {
	var out []string
	for _, p := range pages {
		out = append(out, p.Name)
	}
	return out
}

// renderA renders docs/a.md holding text, beside docs/index.md and docs/b.md, and returns a's text after its
// banner and blank line.
func renderA(t *testing.T, text string) (string, []Finding) {
	t.Helper()
	pages, findings := Render(input(src("docs/index.md", "# Home\n"), src("docs/a.md", text), src("docs/b.md", "# B\n")))
	if len(findings) > 0 {
		if pages != nil {
			t.Errorf("findings %v came with pages %v", findings, names(pages))
		}
		return "", findings
	}
	body, ok := strings.CutPrefix(pageText(t, pages, "a"), banner("docs/a.md", testSHA)+"\n\n")
	if !ok {
		t.Fatalf("page a lacks its banner:\n%s", pageText(t, pages, "a"))
	}
	return body, nil
}

// wantOneFinding checks that docs/a.md holding text gives exactly one finding, at line, containing msg.
func wantOneFinding(t *testing.T, text string, line int, msg string) {
	t.Helper()
	_, findings := renderA(t, text)
	if len(findings) != 1 {
		t.Fatalf("Render(%q): findings %v, want exactly one %q", text, findings, msg)
	}
	f := findings[0]
	if f.Path != "docs/a.md" || f.Line != line || !strings.Contains(f.Msg, msg) {
		t.Fatalf("Render(%q): finding %q, want docs/a.md:%d: …%s…", text, f, line, msg)
	}
}

// wantFinding checks that docs/a.md holding text gives at least a finding at line containing msg.
func wantFinding(t *testing.T, text string, line int, msg string) {
	t.Helper()
	_, findings := renderA(t, text)
	for _, f := range findings {
		if f.Path == "docs/a.md" && f.Line == line && strings.Contains(f.Msg, msg) {
			return
		}
	}
	t.Fatalf("Render(%q): findings %v, want one at docs/a.md:%d containing %q", text, findings, line, msg)
}

func TestPageNames(t *testing.T) {
	pages, findings := Render(input(
		src("docs/index.md", "# I\n"), src("docs/getting-started.md", "# G\n"), src("docs/x/y.md", "# Y\n"),
		src("docs/x/index.md", "# X\n"), src("docs/v1.2.md", "# V\n"),
	))
	if findings != nil {
		t.Fatal(findings)
	}
	want := []string{"Home", "_Sidebar", "getting-started", "v1.2", "x", "x_y"}
	if got := names(pages); !slices.Equal(got, want) {
		t.Fatalf("pages %v, want %v", got, want)
	}
}

func TestPageNameFindings(t *testing.T) {
	index := src("docs/index.md", "# I\n")
	cases := map[string]struct {
		sources []Source
		path    string
		msg     string
	}{
		"collision":       {[]Source{index, src("docs/x.md", "# X\n"), src("docs/x/index.md", "# X\n")}, "", "collides"},
		"home":            {[]Source{index, src("docs/home.md", "# H\n")}, "docs/home.md", "reserved"},
		"sidebar":         {[]Source{index, src("docs/_sidebar.md", "# S\n")}, "docs/_sidebar.md", "reserved"},
		"footer":          {[]Source{index, src("docs/_Footer.md", "# F\n")}, "docs/_Footer.md", "reserved"},
		"home dir":        {[]Source{index, src("docs/home/index.md", "# H\n")}, "docs/home/index.md", "reserved"},
		"upper case":      {[]Source{index, src("docs/A.md", "# A\n")}, "docs/A.md", "unsupported docs path"},
		"space":           {[]Source{index, src("docs/a b.md", "# A\n")}, "docs/a b.md", "unsupported docs path"},
		"underscore":      {[]Source{index, src("docs/a_b.md", "# A\n")}, "docs/a_b.md", "unsupported docs path"},
		"dot":             {[]Source{index, src("docs/.x.md", "# X\n")}, "docs/.x.md", "unsupported docs path"},
		"dash":            {[]Source{index, src("docs/-x.md", "# X\n")}, "docs/-x.md", "unsupported docs path"},
		"no index":        {[]Source{src("docs/a.md", "# A\n")}, "docs/index.md", "docs/index.md is missing"},
		"no sources":      {nil, "docs/index.md", "docs/index.md is missing"},
		"nested segments": {[]Source{index, src("docs/x/A.md", "# A\n")}, "docs/x/A.md", "unsupported docs path"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pages, findings := Render(input(c.sources...))
			if pages != nil {
				t.Errorf("pages %v despite findings", names(pages))
			}
			for _, f := range findings {
				if strings.Contains(f.Msg, c.msg) && (c.path == "" || f.Path == c.path) {
					return
				}
			}
			t.Fatalf("findings %v, want %q at %q", findings, c.msg, c.path)
		})
	}
}

func TestLinks(t *testing.T) {
	cases := map[string]string{
		"[t](b.md)":                   "[t](" + u + "/wiki/b)",
		"[t](./b.md)":                 "[t](" + u + "/wiki/b)",
		"[t](b.md#s)":                 "[t](" + u + "/wiki/b#s)",
		"[t](index.md)":               "[t](" + u + "/wiki/Home)",
		"[t](.)":                      "[t](" + u + "/wiki/Home)",
		"[t](../README.md)":           "[t](" + u + "/blob/main/README.md)",
		"[t](../LICENSE)":             "[t](" + u + "/blob/main/LICENSE)",
		"[t](../cmd/lazyrex/main.go)": "[t](" + u + "/blob/main/cmd/lazyrex/main.go)",
		"[t](../.agents/skills/multi-agent/SKILL.md)": "[t](" + u + "/blob/main/.agents/skills/multi-agent/SKILL.md)",
		"[t](../README.md#install)":                   "[t](" + u + "/blob/main/README.md#install)",
		"[t](../pkg/rex)":                             "[t](" + u + "/tree/main/pkg/rex)",
		"[t](../pkg/rex/)":                            "[t](" + u + "/tree/main/pkg/rex)",
		"[t](..)":                                     "[t](" + u + "/tree/main)",
		"![d](img/a.png)":                             "![d](" + u + "/raw/main/docs/img/a.png)",
		"[![d](img/a.png)](b.md)":                     "[![d](" + u + "/raw/main/docs/img/a.png)](" + u + "/wiki/b)",
		"[t](../scripts/a(b).sh)":                     "[t](" + u + "/blob/main/scripts/a(b).sh)",
		"[a [b](b.md) c](../LICENSE)":                 "[a [b](" + u + "/wiki/b) c](" + u + "/blob/main/LICENSE)",
		"[t]( b.md )":                                 "[t](" + u + "/wiki/b)",
		"[t](https://x/y.md) [m](mailto:a@b) <https://x> [c](//cdn/x) [s](#f)": "[t](https://x/y.md) [m](mailto:a@b) <https://x> [c](//cdn/x) [s](#f)",
		"| `p` \\| press (`ctrl+c`, `ctrl+\\`, `ctrl+z`) [t](b.md) |":          "| `p` \\| press (`ctrl+c`, `ctrl+\\`, `ctrl+z`) [t](" + u + "/wiki/b) |",
		"| `\\|` / `-` \\| split [t](b.md) |":                                  "| `\\|` / `-` \\| split [t](" + u + "/wiki/b) |",
		"`[t](missing.md)`":                                                    "`[t](missing.md)`",
		"``a `[t](missing.md)` b``":                                            "``a `[t](missing.md)` b``",
		"\\[x\\](missing.md)":                                                  "\\[x\\](missing.md)",
		"| [a](b.md) | [c](../LICENSE) |":                                      "| [a](" + u + "/wiki/b) | [c](" + u + "/blob/main/LICENSE) |",
		"[x] and [y] (z) plain":                                                "[x] and [y] (z) plain",
		"> [t](b.md)":                                                          "> [t](" + u + "/wiki/b)",
		"- [t](b.md)":                                                          "- [t](" + u + "/wiki/b)",
		"  - [t](b.md)":                                                        "  - [t](" + u + "/wiki/b)",
		"1. [t](b.md)":                                                         "1. [t](" + u + "/wiki/b)",
		"**[1] Targets**: [t](b.md)":                                           "**[1] Targets**: [t](" + u + "/wiki/b)",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, findings := renderA(t, in+"\n")
			if findings != nil {
				t.Fatalf("findings %v", findings)
			}
			if got != want+"\n" {
				t.Fatalf("got  %q\nwant %q", got, want+"\n")
			}
		})
	}
}

func TestLinksFromNestedPages(t *testing.T) {
	pages, findings := Render(input(
		src("docs/index.md", "[x](x) [y](x/y.md) [z](x/)\n"),
		src("docs/x/index.md", "[h](../index.md) [d](..) [y](y.md) [r](../../README.md) [s](.)\n"),
		src("docs/x/y.md", "[x](index.md) [i](../img/a.png)\n"),
	))
	if findings != nil {
		t.Fatal(findings)
	}
	cases := map[string]string{
		"Home": "[x](" + u + "/wiki/x) [y](" + u + "/wiki/x_y) [z](" + u + "/wiki/x)\n",
		"x": "[h](" + u + "/wiki/Home) [d](" + u + "/wiki/Home) [y](" + u + "/wiki/x_y) [r](" + u +
			"/blob/main/README.md) [s](" + u + "/wiki/x)\n",
		"x_y": "[x](" + u + "/wiki/x) [i](" + u + "/blob/main/docs/img/a.png)\n",
	}
	for name, want := range cases {
		text := pageText(t, pages, name)
		if _, body, _ := strings.Cut(text, "\n\n"); body != want {
			t.Errorf("%s: got %q, want %q", name, body, want)
		}
	}
}

func TestFences(t *testing.T) {
	cases := map[string]string{
		"```sh\n[t](missing.md)\n```\n":                 "```sh\n[t](missing.md)\n```\n",
		"~~~~\n```\n[t](missing.md)\n~~~~\n[t](b.md)\n": "~~~~\n```\n[t](missing.md)\n~~~~\n[t](" + u + "/wiki/b)\n",
		"```\n> ```\n[t](missing.md)\n```\n":            "```\n> ```\n[t](missing.md)\n```\n",
		"```\n[t](missing.md)\n   ```\n[t](b.md)\n":     "```\n[t](missing.md)\n   ```\n[t](" + u + "/wiki/b)\n",
		"````\n```\n[t](missing.md)\n````\n":            "````\n```\n[t](missing.md)\n````\n",
		"```\n    [t](missing.md)\n\t`x\n```\n":         "```\n    [t](missing.md)\n\t`x\n```\n",
		"```mermaid\nflowchart LR\n    a --> b\n```\n":  "```mermaid\nflowchart LR\n    a --> b\n```\n",
		"~~~ `info` ok\n[t](missing.md)\n~~~\n":         "~~~ `info` ok\n[t](missing.md)\n~~~\n",
		"```\n[t](missing.md)\n```  \t\n[t](b.md)\n":    "```\n[t](missing.md)\n```  \t\n[t](" + u + "/wiki/b)\n",
		"```\n``` not a closer\n[t](missing.md)\n```\n": "```\n``` not a closer\n[t](missing.md)\n```\n",
		"~~~\n```\n[t](missing.md)\n~~~\n":              "~~~\n```\n[t](missing.md)\n~~~\n",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, findings := renderA(t, in)
			if findings != nil {
				t.Fatalf("findings %v", findings)
			}
			if got != want {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
		})
	}
}

func TestFenceFindings(t *testing.T) {
	one := []struct {
		text string
		line int
		msg  string
	}{
		{"```\n[t](missing.md)\n    ```\n", 1, "unclosed code fence"},
		{"```\n[t](missing.md)", 1, "unclosed code fence"},
		{"x\n\n~~~\n[t](b.md)\n", 3, "unclosed code fence"},
		{"```js` [t](b.md)\n", 1, "unclosed backtick run"},
		{"2. ~~~\n", 1, "indented fence or fence in a quote or list"},
		{"10) ```\n", 1, "indented fence or fence in a quote or list"},
		{"- - ~~~\n", 1, "indented fence or fence in a quote or list"},
		{"> - ~~~\n", 1, "indented fence or fence in a quote or list"},
		{"* > ```\n", 1, "indented fence or fence in a quote or list"},
		{">>~~~\n", 1, "indented fence or fence in a quote or list"},
		{"+ 1. ```sh\n", 1, "indented fence or fence in a quote or list"},
		{"   ~~~\n", 1, "indented fence or fence in a quote or list"},
	}
	for _, c := range one {
		t.Run(c.text, func(t *testing.T) { wantOneFinding(t, c.text, c.line, c.msg) })
	}
	first := []string{"- ```\n```\n", "> ```\n> ```\n", "  ```\n```\n"}
	for _, text := range first {
		t.Run(text, func(t *testing.T) { wantFinding(t, text, 1, "indented fence or fence in a quote or list") })
	}
}

func TestRefusals(t *testing.T) {
	one := []struct {
		text string
		line int
		msg  string
	}{
		{"x\n\n    [t](b.md)\n", 3, "indentation"},
		{"\t[t](b.md)\n", 1, "indentation"},
		{"  \t[t](b.md)\n", 1, "indentation"},
		{"- a\n\n      x\n", 3, "indentation"},
		{">     [t](b.md)\n", 1, "indentation"},
		{">\t[t](b.md)\n", 1, "indentation"},
		{"-     [t](b.md)\n", 1, "indentation"},
		{"-\t[t](b.md)\n", 1, "indentation"},
		{"1.      x\n", 1, "indentation"},
		{"> -     x\n", 1, "indentation"},
		{"x\n\n[t](missing.md)\n", 3, "broken link missing.md: docs/missing.md is not tracked"},
		{"[t](../../x)\n", 1, "escapes the repository"},
		{"[t](/docs/b.md)\n", 1, "absolute link path"},
		{"[t](b.md?x)\n", 1, "query"},
		{"[t](a\\(b.md)\n", 1, "backslash in a link destination"},
		{"[r]: b.md\n", 1, "reference link"},
		{"   [r]: b.md\n", 1, "reference link"},
		{"[t][r]\n", 1, "reference link"},
		{"[t](b.md \"x\")\n", 1, "link title"},
		{"[t](<b.md>)\n", 1, "angle"},
		{"[t]()\n", 1, "empty destination"},
		{"<a href=x>\n", 1, "raw HTML link"},
		{"<IMG src=x>\n", 1, "raw HTML link"},
		{"see <a>x</a>\n", 1, "raw HTML link"},
		{"[t](b.md\n", 1, "unclosed or spans lines"},
		{"label](b.md)\n", 1, "label spans lines"},
		{"- **[1] Targets**: x label](b.md)\n", 1, "label spans lines"},
		{"a\r\nb\n", 1, "CRLF"},
		{"a\nb\rc\n", 2, "CRLF"},
		{"![d](missing.png)\n", 1, "broken link"},
	}
	for _, c := range one {
		t.Run(c.text, func(t *testing.T) { wantOneFinding(t, c.text, c.line, c.msg) })
	}
	t.Run("multi-line code span", func(t *testing.T) {
		wantFinding(t, "a `b\nc` [t](b.md)\n", 1, "unclosed backtick run")
	})
	t.Run("format", func(t *testing.T) {
		_, findings := renderA(t, "x\n\n[t](missing.md)\n")
		if len(findings) != 1 || findings[0].String() != "docs/a.md:3: broken link missing.md: docs/missing.md is not tracked" {
			t.Fatalf("findings %q", findings)
		}
	})
	t.Run("every finding, no page", func(t *testing.T) {
		pages, findings := Render(input(src("docs/index.md", "[t](missing.md)\n"), src("docs/a.md", "[u](gone.md)\n")))
		if pages != nil || len(findings) != 2 {
			t.Fatalf("pages %v, findings %v", names(pages), findings)
		}
	})
}

func TestTitles(t *testing.T) {
	for _, h1 := range []string{"# A [b](b.md)", "# `x`", "# a_b", "# a\\b", "# a*b", "# a|b", "# a<b>", "#", "# "} {
		t.Run(h1, func(t *testing.T) { wantOneFinding(t, h1+"\n", 1, "title: plain text only") })
	}
	sidebarEntry := func(t *testing.T, text string) string {
		t.Helper()
		pages, findings := Render(input(src("docs/index.md", "# Home\n"), src("docs/a.md", text)))
		if findings != nil {
			t.Fatal(findings)
		}
		for _, l := range strings.Split(pageText(t, pages, "_Sidebar"), "\n") {
			if strings.HasSuffix(l, "("+u+"/wiki/a)") {
				return l
			}
		}
		t.Fatalf("no sidebar entry for a")
		return ""
	}
	if got := sidebarEntry(t, "```sh\n# not\n```\n# Real\n"); got != "- [Real]("+u+"/wiki/a)" {
		t.Errorf("fenced H1: %q", got)
	}
	if got := sidebarEntry(t, "no title\n\n## Sub\n"); got != "- [docs/a.md]("+u+"/wiki/a)" {
		t.Errorf("no H1: %q", got)
	}
	if got := sidebarEntry(t, "# First\n\n# Second\n"); got != "- [First]("+u+"/wiki/a)" {
		t.Errorf("two H1: %q", got)
	}
}

func TestBannerHomeSidebar(t *testing.T) {
	const shaB = "2222222222222222222222222222222222222222"
	pages, findings := Render(input(
		src("docs/index.md", "# Docs\n\n[b](b.md)\n"), Source{Path: "docs/b.md", SHA: shaB, Text: "# Bee\nno newline"},
		src("docs/z.md", "# Zed\n"), src("docs/m.md", "# Em\n"),
	))
	if findings != nil {
		t.Fatal(findings)
	}
	if got, want := pageText(t, pages, "b"), banner("docs/b.md", shaB)+"\n\n# Bee\nno newline\n"; got != want {
		t.Errorf("page b:\n%q\nwant\n%q", got, want)
	}
	if got, want := pageText(t, pages, "Home"), banner("docs/index.md", testSHA)+"\n\n# Docs\n\n[b]("+u+"/wiki/b)\n"; got != want {
		t.Errorf("Home:\n%q\nwant\n%q", got, want)
	}
	wantSidebar := "> Generated from [docs/](" + u + "/tree/" + testDocs + "/docs) at `" + testDocs[:12] +
		"`: the whole wiki is generated from docs/, edit it there.\n\n" +
		"- [Docs](" + u + "/wiki/Home)\n" +
		"- [Bee](" + u + "/wiki/b)\n" +
		"- [Em](" + u + "/wiki/m)\n" +
		"- [Zed](" + u + "/wiki/z)\n"
	if got := pageText(t, pages, "_Sidebar"); got != wantSidebar {
		t.Errorf("_Sidebar:\n%s\nwant\n%s", got, wantSidebar)
	}
	for _, p := range pages {
		if !strings.HasPrefix(p.Text, bannerPrefix) || !strings.HasSuffix(p.Text, "\n") {
			t.Errorf("%s: banner or final newline missing: %q", p.Name, p.Text)
		}
	}
}

func TestDeterministic(t *testing.T) {
	sources := []Source{
		src("docs/index.md", "# I\n\n[a](a.md)\n"), src("docs/a.md", "# A\n"), src("docs/x/index.md", "# X\n"),
		src("docs/x/y.md", "# Y\n[a](../a.md)\n"), src("docs/m.md", "# M\n"), src("docs/z.md", "# Z\n"),
	}
	want, findings := Render(input(sources...))
	if findings != nil {
		t.Fatal(findings)
	}
	r := rand.New(rand.NewSource(1))
	for range 20 {
		in := input(slices.Clone(sources)...)
		r.Shuffle(len(in.Sources), func(i, j int) { in.Sources[i], in.Sources[j] = in.Sources[j], in.Sources[i] })
		r.Shuffle(len(in.Files), func(i, j int) { in.Files[i], in.Files[j] = in.Files[j], in.Files[i] })
		got, findings := Render(in)
		if findings != nil || !slices.Equal(got, want) {
			t.Fatalf("shuffled input rendered differently: %v %v", names(got), findings)
		}
	}
}

func TestDestinations(t *testing.T) {
	text := "> Generated from [s](https://x/s)\n\n```\n[f](fenced.md)\n```\n[a](a.md) `[c](code.md)` ![i](i.png) [n [m](m.md)](o.md)\n"
	got := destinations(text)
	want := []string{"https://x/s", "a.md", "i.png", "m.md", "o.md"}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("destinations %q, want %q", got, want)
	}
}
