package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "log.showSignature=false"}, args...)...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// lastCommit is the last commit of HEAD's history touching path.
func lastCommit(ctx context.Context, root, path string) (string, error) {
	out, err := gitOutput(ctx, root, "--literal-pathspecs", "log", "-1", "--format=%H", "HEAD", "--", path)
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(out)
	if !fullSHA.MatchString(sha) {
		return "", fmt.Errorf("no commit of HEAD touches %s", path)
	}
	return sha, nil
}

// collect reads HEAD of the repository at root. It refuses a shallow clone and any non-regular entry at
// docs or under docs/.
func collect(ctx context.Context, root, repo string) (Input, error) {
	in := Input{Repo: repo}
	shallow, err := gitOutput(ctx, root, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return in, err
	}
	if s := strings.TrimSpace(shallow); s != "false" {
		return in, fmt.Errorf("shallow clone (is-shallow-repository: %s): banner shas need the full history", s)
	}
	tree, err := gitOutput(ctx, root, "ls-tree", "-r", "-z", "--full-tree", "HEAD")
	if err != nil {
		return in, err
	}
	for _, rec := range strings.Split(tree, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return in, fmt.Errorf("git ls-tree: unexpected record %q", rec)
		}
		mode, oid := f[0], f[2]
		in.Files = append(in.Files, p)
		if p != "docs" && !strings.HasPrefix(p, "docs/") {
			continue
		}
		if mode != "100644" && mode != "100755" {
			return in, fmt.Errorf("%s: not a regular file (mode %s): only plain files can be published", p, mode)
		}
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		text, err := gitOutput(ctx, root, "cat-file", "blob", oid)
		if err != nil {
			return in, err
		}
		sha, err := lastCommit(ctx, root, p)
		if err != nil {
			return in, err
		}
		in.Sources = append(in.Sources, Source{Path: p, SHA: sha, Text: text})
	}
	slices.Sort(in.Files)
	if len(in.Sources) > 0 {
		if in.DocsSHA, err = lastCommit(ctx, root, "docs"); err != nil {
			return in, err
		}
	}
	return in, nil
}
