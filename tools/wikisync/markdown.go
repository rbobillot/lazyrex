package main

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The scanner handles a subset of Markdown and refuses the rest: its only cross-line state is "inside a
// fence", so anything whose meaning depends on more (indented code, fences in containers, code spans or
// links over several lines) is a finding rather than a guess.

// scope is what every page of one render shares.
type scope struct {
	repo    string
	files   map[string]bool   // tracked paths
	dirs    map[string]bool   // their parent directories, "." included
	pages   map[string]string // source path to page name
	collect *[]string         // set by destinations: record each destination, check and rewrite nothing
}

type fence struct {
	char byte
	n    int
}

type page struct { // one source being rendered
	scope     *scope
	path      string
	line      int
	fence     *fence
	fenceLine int
	h1        string
	h1Line    int
	out       strings.Builder
	findings  []Finding
}

var (
	refDefinition = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:`)
	scheme        = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

func (p *page) finding(msg string) {
	p.findings = append(p.findings, Finding{p.path, p.line, msg})
}

// feed renders one line: verbatim inside a fence, as a fence opener or closer, or as prose.
func (p *page) feed(line string) {
	p.line++
	switch {
	case p.fence != nil:
		if closesFence(p.fence, line) {
			p.fence = nil
		}
	case openFence(line) != nil:
		p.fence, p.fenceLine = openFence(line), p.line
	case fenceLike(line):
		p.finding("indented fence or fence in a quote or list: unsupported, start fences at column 0")
	case indentFinding(line) != "":
		p.finding(indentFinding(line))
	case refDefinition.MatchString(line):
		p.finding("reference link definition: unsupported, use an inline link")
	default:
		if p.h1Line == 0 && (line == "#" || strings.HasPrefix(line, "# ")) {
			p.h1, p.h1Line = strings.TrimSpace(line[1:]), p.line
		}
		line = p.prose(line)
	}
	p.out.WriteString(line)
	p.out.WriteByte('\n')
}

// finish reports an unclosed fence: it would hide the rest of the page from the link check.
func (p *page) finish() {
	if p.fence != nil {
		p.findings = append(p.findings, Finding{p.path, p.fenceLine, "unclosed code fence"})
	}
}

// prose rewrites the links of one prose line, left to right.
func (p *page) prose(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\\' && i+1 < len(text):
			b.WriteString(text[i : i+2])
			i += 2
		case c == '`':
			end, ok := codeSpanEnd(text, i)
			if !ok {
				p.finding("unclosed backtick run: keep code spans on one line")
				b.WriteString(text[i:])
				return b.String()
			}
			b.WriteString(text[i:end])
			i = end
		case c == '<' && rawHTMLLink(text[i:]):
			p.finding("raw HTML link <a> or <img>: unsupported, use a Markdown link")
			b.WriteByte(c)
			i++
		case c == '!' && strings.HasPrefix(text[i+1:], "["):
			if s, end, ok := p.link(text, i+1, true); ok {
				b.WriteString("!" + s)
				i = end
			} else {
				b.WriteByte(c)
				i++
			}
		case c == '[':
			if s, end, ok := p.link(text, i, false); ok {
				b.WriteString(s)
				i = end
			} else {
				b.WriteByte(c)
				i++
			}
		case c == ']' && strings.HasPrefix(text[i+1:], "("):
			p.finding("link label spans lines: keep [label](target) on one line")
			b.WriteString("](")
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// link reads the link or image whose label opens at text[at]. ok is false when no link starts there, and the
// '[' is literal text. When it records a finding, end is past the whole construct, so the caller never scans
// its "](" again.
func (p *page) link(text string, at int, image bool) (rewritten string, end int, ok bool) {
	k := labelEnd(text, at)
	if k < 0 {
		return "", 0, false
	}
	rest := text[k+1:]
	if strings.HasPrefix(rest, "[") {
		end := len(text)
		if e := labelEnd(text, k+1); e >= 0 {
			end = e + 1
		}
		p.finding("reference link " + text[at:end] + ": unsupported, use an inline link")
		return text[at:end], end, true
	}
	if !strings.HasPrefix(rest, "(") {
		return "", 0, false
	}
	dest, end, msg := parseDestination(text, k+2)
	if msg != "" {
		p.finding(msg)
		return text[at:end], end, true
	}
	label := p.prose(text[at+1 : k])
	url := dest
	if p.scope.collect != nil {
		*p.scope.collect = append(*p.scope.collect, dest)
	} else if u, msg := p.scope.destination(p.path, dest, image); msg != "" {
		p.finding(msg)
	} else {
		url = u
	}
	return "[" + label + "](" + url + ")", end, true
}

// labelEnd returns the index of the ']' matching the '[' at text[at], skipping escapes and code spans, or -1.
func labelEnd(text string, at int) int {
	depth := 0
	for j := at; j < len(text); {
		switch text[j] {
		case '\\':
			j += 2
			continue
		case '`':
			end, ok := codeSpanEnd(text, j)
			if !ok {
				return -1
			}
			j = end
			continue
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j
			}
		}
		j++
	}
	return -1
}

// parseDestination reads an inline link destination from text[from], just after "(". end is past the ")".
func parseDestination(text string, from int) (dest string, end int, msg string) {
	i := skipBlanks(text, from)
	if i < len(text) && text[i] == '<' {
		return "", closeParen(text, i), "angle-bracket link destination: unsupported"
	}
	start, depth, backslash := i, 0, false
scan:
	for ; i < len(text); i++ {
		switch text[i] {
		case '\\':
			backslash = true
			i++
		case ' ', '\t':
			break scan
		case '(':
			depth++
		case ')':
			if depth == 0 {
				break scan
			}
			depth--
		}
	}
	if i > len(text) {
		i = len(text)
	}
	dest = text[start:i]
	i = skipBlanks(text, i)
	switch {
	case i == len(text):
		return "", len(text), "link unclosed or spans lines: keep [label](target) on one line"
	case text[i] != ')':
		return "", closeParen(text, i), "link title or text after the destination: unsupported"
	case backslash:
		return "", i + 1, "backslash in a link destination: unsupported"
	case dest == "":
		return "", i + 1, "empty destination in a link"
	}
	return dest, i + 1, ""
}

func skipBlanks(text string, i int) int {
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	return i
}

func closeParen(text string, i int) int {
	if j := strings.IndexByte(text[i:], ')'); j >= 0 {
		return i + j + 1
	}
	return len(text)
}

func rawHTMLLink(s string) bool {
	s = strings.ToLower(s[:min(len(s), 4)])
	return strings.HasPrefix(s, "<a ") || strings.HasPrefix(s, "<a>") || strings.HasPrefix(s, "<a\t") || s == "<img"
}

// destination checks the link target raw found in source from and returns its absolute URL, or a finding.
func (s *scope) destination(from, raw string, image bool) (string, string) {
	if scheme.MatchString(raw) || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "#") {
		return raw, ""
	}
	target, frag := raw, ""
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		target, frag = raw[:i], raw[i:]
	}
	switch {
	case strings.Contains(target, "?"):
		return "", "query in link " + raw + ": unsupported"
	case strings.HasPrefix(target, "/"):
		return "", "absolute link path " + raw + ": use a path relative to " + from
	}
	p := path.Join(path.Dir(from), target)
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", "link " + raw + " escapes the repository"
	}
	base := "https://github.com/" + s.repo
	switch {
	case image && s.files[p]:
		return base + "/raw/main/" + p, ""
	case image && s.dirs[p]:
		return "", "image " + raw + ": " + p + " is a directory, not a tracked file"
	case image:
	case s.pages[p] != "":
		return base + "/wiki/" + s.pages[p] + frag, ""
	case s.dirs[p] && s.pages[path.Join(p, "index.md")] != "":
		return base + "/wiki/" + s.pages[path.Join(p, "index.md")] + frag, ""
	case p == ".":
		return base + "/tree/main" + frag, ""
	case s.dirs[p]:
		return base + "/tree/main/" + p + frag, ""
	case s.files[p]:
		return base + "/blob/main/" + p + frag, ""
	}
	return "", fmt.Sprintf("broken link %s: %s is not tracked", raw, p)
}

// openFence opens a fence only at column 0: a run of 3 or more '`' or '~'. A backtick fence's info string
// holds no backtick, else the line is prose.
func openFence(line string) *fence {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return nil
	}
	n := runLen(line, 0)
	if n < 3 || (line[0] == '`' && strings.IndexByte(line[n:], '`') >= 0) {
		return nil
	}
	return &fence{line[0], n}
}

// closesFence: 0 to 3 spaces, a run of the opener's character at least as long, then only blanks.
func closesFence(f *fence, line string) bool {
	s := line
	for k := 0; k < 3 && strings.HasPrefix(s, " "); k++ {
		s = s[1:]
	}
	if s == "" || s[0] != f.char {
		return false
	}
	n := runLen(s, 0)
	return n >= f.n && strings.Trim(s[n:], " \t") == ""
}

// fenceLike tells a fence run that does not start at column 0: after blanks, '>' markers and list markers,
// nested in any order and number.
func fenceLike(line string) bool {
	s := line
	for {
		t := strings.TrimLeft(s, " \t")
		if strings.HasPrefix(t, ">") {
			s = t[1:]
			continue
		}
		if rest, ok := cutListMarker(t); ok {
			s = rest
			continue
		}
		s = t
		break
	}
	return s != line && (strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~"))
}

// cutListMarker removes a leading "-", "*", "+", "1." or "1)" (1 to 9 digits) followed by a blank or the end.
func cutListMarker(t string) (string, bool) {
	n := 0
	switch {
	case t == "":
		return "", false
	case t[0] == '-' || t[0] == '*' || t[0] == '+':
		n = 1
	default:
		for n < len(t) && n < 10 && t[n] >= '0' && t[n] <= '9' {
			n++
		}
		if n == 0 || n > 9 || n == len(t) || (t[n] != '.' && t[n] != ')') {
			return "", false
		}
		n++
	}
	if n < len(t) && t[n] != ' ' && t[n] != '\t' {
		return "", false
	}
	return t[n:], true
}

// indentFinding refuses what Markdown could read as indented code: a tab in the indentation, 4 or more
// spaces at the start, after a '>' marker and its optional space, or 5 or more after a list marker.
func indentFinding(line string) string {
	const tab = "tab in the indentation: unsupported, indent with spaces"
	const deep = "indentation of 4 or more columns outside a fence: use a fence or less indentation"
	rest := strings.TrimLeft(line, " \t")
	if rest == "" {
		return ""
	}
	lead := line[:len(line)-len(rest)]
	if strings.Contains(lead, "\t") {
		return tab
	}
	if len(lead) >= 4 {
		return deep
	}
	for s := rest; ; {
		limit := 0
		if strings.HasPrefix(s, ">") {
			s, limit = strings.TrimPrefix(s[1:], " "), 4
		} else if r, ok := cutListMarker(s); ok {
			s, limit = r, 5
		} else {
			return ""
		}
		t := strings.TrimLeft(s, " \t")
		if t == "" {
			return ""
		}
		ws := s[:len(s)-len(t)]
		if strings.Contains(ws, "\t") {
			return tab
		}
		if len(ws) >= limit {
			return deep
		}
		s = t
	}
}

// codeSpanEnd returns the index past the run of exactly as many backticks as the run at text[i] opens. A
// backslash inside a code span is literal.
func codeSpanEnd(text string, i int) (end int, ok bool) {
	n := runLen(text, i)
	for j := i + n; j < len(text); {
		if text[j] != '`' {
			j++
			continue
		}
		m := runLen(text, j)
		if m == n {
			return j + m, true
		}
		j += m
	}
	return 0, false
}

func runLen(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == s[i] {
		n++
	}
	return n
}

// destinations returns every link and image destination of the prose lines of text; tests run it on
// rendered pages.
func destinations(text string) []string {
	var out []string
	p := &page{scope: &scope{collect: &out}}
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		p.feed(l)
	}
	return out
}
