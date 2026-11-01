// Command wikisync renders docs/ at HEAD as GitHub wiki pages, rewriting every relative link to an absolute
// URL and refusing any Markdown it does not parse. The wiki workflow (.github/workflows/wiki.yml) publishes
// its output.
//
// usage: wikisync [-repo owner/repo] [-out dir]
//
// Without -out it renders HEAD and reports findings only. -out must be absent or an empty directory, so no
// stale page can ride along into the artifact. Exit 0: ok; 1: findings or a git error; 2: usage.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("wikisync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repo := flags.String("repo", "rbobillot/lazyrex", "GitHub `owner/repo` that links and banners point at")
	out := flags.String("out", "", "write the pages to this absent or empty `dir`; without it, only check")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "wikisync: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	owner, name, _ := strings.Cut(*repo, "/")
	if !repoPattern.MatchString(*repo) || strings.Trim(owner, ".") == "" || strings.Trim(name, ".") == "" {
		fmt.Fprintf(stderr, "wikisync: -repo %q is not owner/repo\n", *repo)
		return 2
	}
	if *out != "" {
		if err := absentOrEmpty(*out); err != nil {
			fmt.Fprintln(stderr, "wikisync:", err)
			return 1
		}
	}

	ctx := context.Background()
	top, err := gitOutput(ctx, "", "rev-parse", "--show-toplevel")
	if err != nil {
		fmt.Fprintln(stderr, "wikisync:", err)
		return 1
	}
	in, err := collect(ctx, strings.TrimSpace(top), *repo)
	if err != nil {
		fmt.Fprintln(stderr, "wikisync:", err)
		return 1
	}
	pages, findings := Render(in)
	if len(findings) > 0 {
		for _, f := range findings {
			fmt.Fprintln(stderr, f)
		}
		fmt.Fprintf(stderr, "wikisync: %d findings, nothing rendered\n", len(findings))
		return 1
	}
	if *out == "" {
		fmt.Fprintf(stdout, "wikisync: %d pages, 0 findings\n", len(pages))
		return 0
	}
	if err := writePages(*out, pages); err != nil {
		fmt.Fprintln(stderr, "wikisync:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wikisync: wrote %d pages to %s\n", len(pages), *out)
	return 0
}

func absentOrEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case len(entries) > 0:
		return fmt.Errorf("-out %s is not empty", dir)
	}
	return nil
}

func writePages(dir string, pages []Page) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range pages {
		f, err := os.OpenFile(filepath.Join(dir, p.Name+".md"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(p.Text)
		if err == nil {
			err = f.Chmod(0o644)
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
