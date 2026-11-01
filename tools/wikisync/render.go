package main

import (
	"cmp"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// bannerPrefix starts line 1 of every page; the workflow's publish step relies on it to tell generated pages
// from hand-made ones.
const bannerPrefix = "> Generated from "

// Source is one Markdown file of docs/ at HEAD. Path is "docs/…/x.md"; SHA is the last commit touching it.
type Source struct{ Path, SHA, Text string }

// Input is everything Render reads. Files holds every tracked path at HEAD.
type Input struct {
	Repo, DocsSHA string
	Sources       []Source
	Files         []string
}

type Page struct{ Name, Text string }

type Finding struct {
	Path string
	Line int // 0 when not tied to a line
	Msg  string
}

func (f Finding) String() string {
	if f.Line == 0 {
		return f.Path + ": " + f.Msg
	}
	return fmt.Sprintf("%s:%d: %s", f.Path, f.Line, f.Msg)
}

var segment = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// pageName maps docs/a/b.md to a_b, docs/a/index.md to a and docs/index.md to Home.
func pageName(p string) (string, error) {
	rel, ok := strings.CutPrefix(p, "docs/")
	if !ok || !strings.HasSuffix(rel, ".md") {
		return "", errors.New("unsupported docs path: not docs/…/*.md")
	}
	if rel == "index.md" {
		return "Home", nil
	}
	segs := strings.Split(strings.TrimSuffix(rel, ".md"), "/")
	if segs[len(segs)-1] == "index" {
		segs = segs[:len(segs)-1]
	}
	name := strings.Join(segs, "_")
	if isReserved(name) {
		return "", fmt.Errorf("reserved page name %s", name)
	}
	for _, s := range segs {
		if !segment.MatchString(s) {
			return "", fmt.Errorf("unsupported docs path: segment %q must match %s", s, segment)
		}
	}
	return name, nil
}

func isReserved(name string) bool {
	for _, r := range []string{"Home", "_Sidebar", "_Footer"} {
		if strings.EqualFold(name, r) {
			return true
		}
	}
	return false
}

const titleForbidden = "[]()<>\\`*_|"

// title is the first "# " line outside fences, which feed records; without one, the source path.
func title(src Source, h1 string, line int) (string, *Finding) {
	if line == 0 {
		return src.Path, nil
	}
	if h1 == "" || strings.ContainsAny(h1, titleForbidden) {
		return "", &Finding{src.Path, line, "title: plain text only, without any of " + titleForbidden}
	}
	return h1, nil
}

// Render returns pages sorted by name, or nil and every finding.
func Render(in Input) ([]Page, []Finding) {
	sources := slices.Clone(in.Sources)
	slices.SortFunc(sources, func(a, b Source) int { return strings.Compare(a.Path, b.Path) })
	var findings []Finding
	sc := &scope{
		repo:  in.Repo,
		files: map[string]bool{},
		dirs:  map[string]bool{".": true},
		pages: map[string]string{},
	}
	for _, f := range in.Files {
		sc.files[f] = true
		for d := path.Dir(f); d != "."; d = path.Dir(d) {
			sc.dirs[d] = true
		}
	}
	byName := map[string]string{}
	for _, s := range sources {
		name, err := pageName(s.Path)
		if err != nil {
			findings = append(findings, Finding{s.Path, 0, err.Error()})
			continue
		}
		if other, ok := byName[name]; ok {
			findings = append(findings, Finding{s.Path, 0, fmt.Sprintf("page name %s collides with %s", name, other)})
			continue
		}
		byName[name] = s.Path
		sc.pages[s.Path] = name
	}
	if _, ok := byName["Home"]; !ok {
		findings = append(findings, Finding{"docs/index.md", 0, "docs/index.md is missing: it is the wiki's Home"})
	}

	type rendered struct {
		name, title, text string
	}
	var out []rendered
	for _, s := range sources {
		name, ok := sc.pages[s.Path]
		if !ok {
			continue
		}
		if i := strings.IndexByte(s.Text, '\r'); i >= 0 {
			findings = append(findings, Finding{s.Path, strings.Count(s.Text[:i], "\n") + 1, "CRLF or CR line ending: unsupported"})
			continue
		}
		p := &page{scope: sc, path: s.Path}
		if text := strings.TrimSuffix(s.Text, "\n"); text != "" {
			for _, l := range strings.Split(text, "\n") {
				p.feed(l)
			}
		}
		p.finish()
		t, f := title(s, p.h1, p.h1Line)
		if f != nil {
			p.findings = append(p.findings, *f)
		}
		findings = append(findings, p.findings...)
		banner := bannerPrefix + "[" + s.Path + "](https://github.com/" + in.Repo + "/blob/" + s.SHA + "/" + s.Path +
			") at `" + short(s.SHA) + "`: edit the source in the repository, not this wiki."
		out = append(out, rendered{name, t, banner + "\n\n" + p.out.String()})
	}
	if len(findings) > 0 {
		slices.SortStableFunc(findings, func(a, b Finding) int {
			return cmp.Or(strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
		})
		return nil, findings
	}

	notHome := func(name string) int {
		if name == "Home" {
			return 0
		}
		return 1
	}
	slices.SortFunc(out, func(a, b rendered) int {
		return cmp.Or(cmp.Compare(notHome(a.name), notHome(b.name)), strings.Compare(a.name, b.name))
	})
	var sidebar strings.Builder
	sidebar.WriteString(bannerPrefix + "[docs/](https://github.com/" + in.Repo + "/tree/" + in.DocsSHA + "/docs) at `" +
		short(in.DocsSHA) + "`: the whole wiki is generated from docs/, edit it there.\n\n")
	pages := []Page{{"_Sidebar", ""}}
	for _, r := range out {
		fmt.Fprintf(&sidebar, "- [%s](https://github.com/%s/wiki/%s)\n", r.title, in.Repo, r.name)
		pages = append(pages, Page{r.name, r.text})
	}
	pages[0].Text = sidebar.String()
	slices.SortFunc(pages, func(a, b Page) int { return strings.Compare(a.Name, b.Name) })
	return pages, nil
}

func short(sha string) string {
	if len(sha) < 12 {
		return sha
	}
	return sha[:12]
}
