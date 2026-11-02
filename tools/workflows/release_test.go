package workflows

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	tagGrammar  = `re='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$'`
	buildLine   = `GOOS=${target%/*} GOARCH=${target#*/} go build -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=$TAG" -o "$work/$name/lazyrex" ./cmd/lazyrex`
	targetsLine = `targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)`
	riskComment = "# A tag moved from here to the PATCH goes unseen until the check after it: the risk the human accepted."
)

// badTags are refused by the tag grammar, each with its printf %q form.
var badTags = map[string]string{
	"0.1.0":              "0.1.0",
	"v0.1":               "v0.1",
	"v01.0.0":            "v01.0.0",
	"v0.1.0-beta.1":      "v0.1.0-beta.1",
	"v0.1.0-rc.0":        "v0.1.0-rc.0",
	"v0.1.0-rc.01":       "v0.1.0-rc.01",
	"v0.1.0+m":           "v0.1.0+m",
	"v0.1.0 ":            `v0.1.0\ `,
	"v0.1.0\n::error::x": `$'v0.1.0\n::error::x'`,
	"":                   "''",
	"v1.2.3;id":          `v1.2.3\;id`,
}

func loadRelease(t *testing.T) (workflowFile, string) {
	t.Helper()
	return load(t, "release.yml")
}

func TestReleaseTrigger(t *testing.T) {
	wf, _ := loadRelease(t)
	if got := fmt.Sprint(wf.On); got != "map[push:map[tags:[v*]]]" {
		t.Errorf("on %s, want push of tags v* only", got)
	}
}

func TestReleaseConcurrency(t *testing.T) {
	wf, _ := loadRelease(t)
	if wf.Concurrency["group"] != "release-${{ github.ref }}" || wf.Concurrency["cancel-in-progress"] != false || len(wf.Concurrency) != 2 {
		t.Errorf("concurrency %v, want group release-${{ github.ref }} and cancel-in-progress false", wf.Concurrency)
	}
}

func TestReleaseJobs(t *testing.T) {
	wf, _ := loadRelease(t)
	if got := slices.Sorted(maps.Keys(wf.Jobs)); !slices.Equal(got, []string{"build", "ci", "publish", "verify"}) {
		t.Fatalf("jobs %v", got)
	}
	want := map[string][]string{"verify": nil, "ci": {"verify"}, "build": {"verify", "ci"}, "publish": {"verify", "build"}}
	for name, needs := range want {
		if got := needsList(wf.Jobs[name].Needs); !slices.Equal(got, needs) {
			t.Errorf("job %s needs %v, want %v", name, got, needs)
		}
	}
	ci := wf.Jobs["ci"]
	if ci.Uses != "./.github/workflows/ci.yml" || len(ci.With) != 1 || ci.With["ref"] != "${{ needs.verify.outputs.commit }}" ||
		!maps.Equal(ci.Permissions, map[string]string{"contents": "read"}) || len(ci.Steps) != 0 {
		t.Errorf("ci job %+v, want ci.yml at the verified commit with contents: read", ci)
	}
}

func TestReleaseCheckouts(t *testing.T) {
	wf, _ := loadRelease(t)
	if c := stepUsing(t, wf.Jobs["verify"], "actions/checkout"); fmt.Sprint(c.With["fetch-depth"]) != "0" || c.With["ref"] != nil {
		t.Errorf("verify checkout with %v, want fetch-depth 0 at the pushed ref", c.With)
	}
	if c := stepUsing(t, wf.Jobs["build"], "actions/checkout"); c.With["ref"] != "${{ needs.verify.outputs.commit }}" {
		t.Errorf("build checkout with %v, want the verified commit", c.With)
	}
}

func TestReleasePublishBoundary(t *testing.T) {
	wf, text := loadRelease(t)
	publish := wf.Jobs["publish"]
	if !maps.Equal(publish.Permissions, map[string]string{"contents": "write"}) {
		t.Errorf("publish permissions %v", publish.Permissions)
	}
	if len(publish.Steps) != 2 || !strings.HasPrefix(publish.Steps[0].Uses, "actions/download-artifact@") ||
		publish.Steps[1].Name != "Publish" || publish.Steps[1].Uses != "" {
		t.Fatalf("publish steps %+v, want exactly download-artifact then Publish", publish.Steps)
	}
	runsGo := regexp.MustCompile(`(^|[;&|({]|\$\()\s*go\s`)
	for _, s := range publish.Steps {
		if strings.Contains(s.Uses, "checkout") || strings.Contains(s.Uses, "setup-go") {
			t.Errorf("publish uses %s", s.Uses)
		}
		for _, l := range strings.Split(s.Run, "\n") {
			if runsGo.MatchString(l) {
				t.Errorf("publish runs go: %q", l)
			}
		}
	}
	env := publish.Steps[1].Env
	if got := slices.Sorted(maps.Keys(env)); !slices.Equal(got, []string{"COMMIT", "DIST", "GH_REPO", "GH_TOKEN", "GO_VERSION", "SOURCE_REMOTE", "TAG"}) {
		t.Errorf("Publish env %v", got)
	}
	if env["GH_TOKEN"] != "${{ github.token }}" || strings.Count(text, "github.token") != 1 {
		t.Errorf("github.token appears %d times, want once, as the Publish GH_TOKEN", strings.Count(text, "github.token"))
	}
	if publish.Env != nil {
		t.Errorf("publish job env %v", publish.Env)
	}
}

func TestReleaseOnlyPathToPublic(t *testing.T) {
	wf, _ := loadRelease(t)
	var lines []string
	for _, name := range []string{"verify", "build", "publish"} {
		lines = append(lines, runLines(wf.Jobs[name])...)
	}
	for _, bad := range []string{"--clobber", "release upload", "release edit", "release delete", "git push", "git tag"} {
		for _, l := range lines {
			if strings.Contains(l, bad) {
				t.Errorf("%q holds %s", strings.TrimSpace(l), bad)
			}
		}
	}
	matching := func(pattern string) []int {
		re := regexp.MustCompile(pattern)
		var out []int
		for i, l := range lines {
			if re.MatchString(l) {
				out = append(out, i)
			}
		}
		return out
	}
	if c := matching(`release create "\$TAG"`); len(c) != 1 || !strings.Contains(lines[c[0]], "--draft") {
		t.Errorf("release create lines %v, want one, with --draft", c)
	}
	if c := matching(`release create`); len(c) != 2 {
		t.Errorf("%d lines mention release create, want the call and its failure message", len(c))
	}
	patch := matching(`--method PATCH`)
	if len(patch) != 1 || !strings.Contains(lines[patch[0]], "-F draft=false") {
		t.Fatalf("PATCH lines %v, want one, with -F draft=false", patch)
	}
	if prev := strings.TrimSpace(lines[patch[0]-1]); prev != riskComment {
		t.Errorf("the PATCH line follows %q, want the comment naming the accepted risk", prev)
	}
	if post := matching(`--method POST`); len(post) != 1 || !strings.Contains(lines[post[0]], "releases/generate-notes") {
		t.Errorf("POST lines %v, want generate-notes only", post)
	}
	if c := matching(`--method`); len(c) != 2 {
		t.Errorf("%d lines set --method, want POST and PATCH only", len(c))
	}
	dl := matching(`-H 'Accept: application/octet-stream'`)
	if len(dl) != 1 || !strings.Contains(lines[dl[0]], `"repos/${GH_REPO}/releases/assets/${aid}"`) || strings.Contains(lines[dl[0]], "--method") {
		t.Errorf("download lines %v, want one GET of releases/assets/${aid}", dl)
	}
	if c := matching(`-H `); len(c) != 1 {
		t.Errorf("%d lines set a header, want the download only", len(c))
	}
}

func TestReleaseTagGrammar(t *testing.T) {
	wf, _ := loadRelease(t)
	for _, js := range [][2]string{{"verify", "Verify"}, {"build", "Build"}, {"publish", "Publish"}} {
		var got []string
		for _, l := range strings.Split(stepNamed(t, wf, js[0], js[1]).Run, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "re=") {
				got = append(got, strings.TrimSpace(l))
			}
		}
		if !slices.Equal(got, []string{tagGrammar}) {
			t.Errorf("%s: grammar lines %q, want %q", js[1], got, tagGrammar)
		}
	}
}

func TestReleaseBuildLines(t *testing.T) {
	wf, _ := loadRelease(t)
	run := stepNamed(t, wf, "build", "Build").Run
	var builds, targets []string
	for _, l := range strings.Split(run, "\n") {
		l = strings.TrimSpace(l)
		if strings.Contains(l, "go build") {
			builds = append(builds, l)
		}
		if strings.HasPrefix(l, "targets=") {
			targets = append(targets, l)
		}
	}
	if !slices.Equal(builds, []string{buildLine}) || !slices.Equal(targets, []string{targetsLine}) {
		t.Errorf("build lines %q, targets %q", builds, targets)
	}
}

func TestReleasePublishStartsQuiet(t *testing.T) {
	wf, _ := loadRelease(t)
	for _, l := range strings.Split(stepNamed(t, wf, "publish", "Publish").Run, "\n")[:4] {
		if strings.TrimSpace(l) == "{ set +x; } 2>/dev/null" {
			return
		}
	}
	t.Error("Publish does not start with { set +x; } 2>/dev/null")
}

func TestReleaseArtifacts(t *testing.T) {
	wf, _ := loadRelease(t)
	up := stepUsing(t, wf.Jobs["build"], "actions/upload-artifact")
	if up.With["name"] != "release-dist" || up.With["path"] != "${{ runner.temp }}/dist" || up.With["if-no-files-found"] != "error" ||
		fmt.Sprint(up.With["retention-days"]) != "1" {
		t.Errorf("upload with %v", up.With)
	}
	down := stepUsing(t, wf.Jobs["publish"], "actions/download-artifact")
	if down.With["name"] != "release-dist" || down.With["path"] != "${{ runner.temp }}/dist" || len(down.With) != 2 {
		t.Errorf("download with %v", down.With)
	}
	if d := stepNamed(t, wf, "build", "Build").Env["DIST"]; d != "${{ runner.temp }}/dist" {
		t.Errorf("Build DIST %q", d)
	}
	if d := stepNamed(t, wf, "publish", "Publish").Env["DIST"]; d != "${{ runner.temp }}/dist" {
		t.Errorf("Publish DIST %q", d)
	}
}

func TestReleaseOutputs(t *testing.T) {
	wf, _ := loadRelease(t)
	if wf.Jobs["verify"].Outputs["commit"] != "${{ steps.verify.outputs.commit }}" || stepNamed(t, wf, "verify", "Verify").ID != "verify" {
		t.Errorf("verify outputs %v", wf.Jobs["verify"].Outputs)
	}
	if wf.Jobs["build"].Outputs["go-version"] != "${{ steps.build.outputs.go-version }}" || stepNamed(t, wf, "build", "Build").ID != "build" {
		t.Errorf("build outputs %v", wf.Jobs["build"].Outputs)
	}
	pub := stepNamed(t, wf, "publish", "Publish").Env
	if pub["COMMIT"] != "${{ needs.verify.outputs.commit }}" || pub["GO_VERSION"] != "${{ needs.build.outputs.go-version }}" {
		t.Errorf("Publish env %v", pub)
	}
}

func stepEnv(s step, overrides map[string]string) map[string]string {
	env := maps.Clone(s.Env)
	if env == nil {
		env = map[string]string{}
	}
	maps.Copy(env, overrides)
	return env
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// A Verify fixture: a clone of a bare source whose main holds c1 then c2 and whose branch side holds c3 on c1,
// with tags v0.1.0 (lightweight, c2), v0.2.0 (annotated, c1), v0.3.0 (c3) and v0.4.0-rc.1 (c2).
type verifyFix struct {
	s                    *sandbox
	clone                string
	c1, c2, c3, tagObj02 string
	step                 step
}

func newVerify(t *testing.T) *verifyFix {
	t.Helper()
	s := newSandbox(t, "git")
	seed := newRepo(t)
	writeFile(t, seed, "a", "1\n")
	c1 := commitAll(t, seed, "one", "2026-01-01T00:00:00Z")
	writeFile(t, seed, "a", "2\n")
	c2 := commitAll(t, seed, "two", "2026-01-02T00:00:00Z")
	gitIn(t, seed, nil, "checkout", "-q", "-b", "side", c1)
	writeFile(t, seed, "a", "3\n")
	c3 := commitAll(t, seed, "three", "2026-01-03T00:00:00Z")
	gitIn(t, seed, nil, "checkout", "-q", "main")
	gitIn(t, seed, nil, "tag", "v0.1.0", c2)
	gitIn(t, seed, nil, "tag", "-a", "-m", "v0.2.0", "v0.2.0", c1)
	gitIn(t, seed, nil, "tag", "v0.3.0", c3)
	gitIn(t, seed, nil, "tag", "v0.4.0-rc.1", c2)
	bare := filepath.Join(t.TempDir(), "source.git")
	gitIn(t, seed, nil, "-c", "init.defaultBranch=main", "init", "-q", "--bare", bare)
	gitIn(t, seed, nil, "push", "-q", bare, "refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*")
	clone := filepath.Join(t.TempDir(), "c")
	gitIn(t, seed, nil, "clone", "-q", "file://"+bare, clone)
	wf, _ := loadRelease(t)
	return &verifyFix{
		s: s, clone: clone, c1: c1, c2: c2, c3: c3, tagObj02: gitIn(t, seed, nil, "rev-parse", "refs/tags/v0.2.0"),
		step: stepNamed(t, wf, "verify", "Verify"),
	}
}

// run checks out checkout when set, runs Verify with TAG, REF refs/tags/TAG and PUSHED_SHA, then overrides,
// and returns the result and what it wrote to GITHUB_OUTPUT.
func (v *verifyFix) run(t *testing.T, checkout, tag, pushed string, overrides ...string) (result, string) {
	t.Helper()
	if checkout != "" {
		gitIn(t, v.clone, nil, "checkout", "-q", "--detach", checkout)
	}
	out := filepath.Join(v.s.root, "output")
	if err := os.WriteFile(out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env := stepEnv(v.step, map[string]string{"TAG": tag, "REF": "refs/tags/" + tag, "PUSHED_SHA": pushed, "GITHUB_OUTPUT": out})
	for _, o := range overrides {
		k, val, _ := strings.Cut(o, "=")
		env[k] = val
	}
	r := v.s.run(t, v.step.Run, v.clone, env)
	return r, readFile(t, out)
}

func TestVerifyAccepts(t *testing.T) {
	v := newVerify(t)
	cases := []struct {
		name, tag, pushed, commit string
	}{
		{"lightweight", "v0.1.0", v.c2, v.c2},
		{"annotated, tag object pushed", "v0.2.0", v.tagObj02, v.c1},
		{"annotated, commit pushed", "v0.2.0", v.c1, v.c1},
		{"release candidate", "v0.4.0-rc.1", v.c2, v.c2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, out := v.run(t, "refs/tags/"+c.tag, c.tag, c.pushed)
			wantCode(t, r, 0)
			if out != "commit="+c.commit+"\n" {
				t.Errorf("GITHUB_OUTPUT %q, want commit=%s", out, c.commit)
			}
		})
	}
}

func TestVerifyRefuses(t *testing.T) {
	v := newVerify(t)
	cases := []struct {
		name, checkout, tag, pushed string
		overrides                   []string
		msg                         string
	}{
		{"off main", "refs/tags/v0.3.0", "v0.3.0", v.c3, nil, "is not on main"},
		{"moved", "refs/tags/v0.1.0", "v0.1.0", v.c1, nil, "it moved"},
		{"no tag", "", "v9.9.9", v.c2, nil, "no tag v9.9.9"},
		{"branch ref", "refs/tags/v0.1.0", "v0.1.0", v.c2, []string{"REF=refs/heads/v0.1.0"}, "is not refs/tags/v0.1.0"},
		{"short sha", "refs/tags/v0.1.0", "v0.1.0", "abc", nil, "bad pushed sha"},
		{"uppercase sha", "refs/tags/v0.1.0", "v0.1.0", strings.ToUpper(v.c2), nil, "bad pushed sha"},
		{"unknown sha", "refs/tags/v0.1.0", "v0.1.0", strings.Repeat("ab", 20), nil, "not in the checkout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, out := v.run(t, c.checkout, c.tag, c.pushed, c.overrides...)
			wantCode(t, r, 1)
			if !strings.Contains(r.stderr, c.msg) {
				t.Errorf("stderr %q lacks %q", r.stderr, c.msg)
			}
			if out != "" {
				t.Errorf("GITHUB_OUTPUT %q, want nothing", out)
			}
		})
	}
	for tag, quoted := range badTags {
		t.Run("tag "+quoted, func(t *testing.T) {
			r, out := v.run(t, "", tag, v.c2)
			wantCode(t, r, 1)
			if want := "release: tag " + quoted + " is not vMAJOR.MINOR.PATCH"; !strings.Contains(r.stderr, want) {
				t.Errorf("stderr %q lacks %q", r.stderr, want)
			}
			if out != "" {
				t.Errorf("GITHUB_OUTPUT %q, want nothing", out)
			}
		})
	}
}

func TestVerifyNeedsMain(t *testing.T) {
	v := newVerify(t)
	gitIn(t, v.clone, nil, "remote", "set-head", "origin", "-d")
	gitIn(t, v.clone, nil, "update-ref", "-d", "refs/remotes/origin/main")
	r, out := v.run(t, "refs/tags/v0.1.0", "v0.1.0", v.c2)
	wantCode(t, r, 1)
	if !strings.Contains(r.stderr, "no origin/main") || out != "" {
		t.Errorf("stderr %q, GITHUB_OUTPUT %q", r.stderr, out)
	}
}

// fakeGo answers go env GOVERSION with $FAKE_GOVERSION, and records each go build in $GO_LOG (CGO_ENABLED, GOOS,
// GOARCH, "set" when GOFLAGS is, then argv, NUL-separated, one call per line) before writing a stub binary.
const fakeGo = `#!/bin/bash
set -euo pipefail
touch "$GO_LOG.ran"
case ${1-} in
env)
  [[ $# == 2 && $2 == GOVERSION ]] || { echo "fake go: unexpected $*" >&2; exit 2; }
  printf '%s\n' "${FAKE_GOVERSION:-go1.26.0}"
  ;;
build)
  printf '%s\0' "${CGO_ENABLED-unset}" "${GOOS-unset}" "${GOARCH-unset}" "${GOFLAGS+set}" "$@" >>"$GO_LOG"
  printf '\n' >>"$GO_LOG"
  args=("$@") out=""
  for ((i = 0; i < ${#args[@]}; i++)); do
    if [[ ${args[i]} == -o ]]; then out=${args[i + 1]}; fi
  done
  if [[ $GOOS/$GOARCH == "${FAKE_FAIL-}" ]]; then echo "fake go: build failed" >&2; exit 1; fi
  printf 'stub %s %s\n' "$GOOS" "$GOARCH" >"$out"
  ;;
*) echo "fake go: unexpected $*" >&2; exit 2 ;;
esac
`

const buildDate = "2026-01-02T03:04:05Z"

// A Build fixture: a clean repository holding LICENSE, README.md and cmd/lazyrex, committed at buildDate, and
// the fake go first in PATH.
type buildFix struct {
	s            *sandbox
	repo, commit string
	goLog        string
	step         step
	runs         int
}

func newBuild(t *testing.T) *buildFix {
	t.Helper()
	needGNU(t)
	s := newSandbox(t, "git", "tar", "gzip", "sha256sum")
	repo := newRepo(t)
	writeFile(t, repo, "LICENSE", "Apache License\n")
	writeFile(t, repo, "README.md", "# lazyrex\n")
	writeFile(t, repo, "cmd/lazyrex/x.go", "package main\n")
	commit := commitAll(t, repo, "one", buildDate)
	s.writeBin(t, "go", fakeGo)
	wf, _ := loadRelease(t)
	return &buildFix{s: s, repo: repo, commit: commit, goLog: filepath.Join(s.root, "go.log"), step: stepNamed(t, wf, "build", "Build")}
}

// run runs Build with a fresh DIST, after prefix lines, and returns the result, DIST and GITHUB_OUTPUT.
func (b *buildFix) run(t *testing.T, prefix string, overrides ...string) (result, string, string) {
	t.Helper()
	b.runs++
	dist := filepath.Join(b.s.root, fmt.Sprintf("dist%d", b.runs))
	out := filepath.Join(b.s.root, "output")
	if err := os.WriteFile(out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{b.goLog, b.goLog + ".ran"} {
		os.Remove(f)
	}
	env := stepEnv(b.step, map[string]string{
		"TAG": "v0.1.0", "COMMIT": b.commit, "DIST": dist, "GITHUB_OUTPUT": out, "GO_LOG": b.goLog, "GOFLAGS": "-mod=mod",
	})
	for _, o := range overrides {
		k, v, _ := strings.Cut(o, "=")
		env[k] = v
	}
	r := b.s.run(t, prefix+b.step.Run, b.repo, env)
	return r, env["DIST"], readFile(t, out)
}

type goCall struct {
	cgo, goos, goarch, goflags string
	argv                       []string
}

func (b *buildFix) goCalls(t *testing.T) []goCall {
	t.Helper()
	var out []goCall
	for _, rec := range strings.Split(readFile(t, b.goLog), "\x00\n") {
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x00")
		out = append(out, goCall{f[0], f[1], f[2], f[3], f[4:]})
	}
	return out
}

var releaseTargets = [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"}}

func archiveNames(version string) []string {
	var out []string
	for _, osArch := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		out = append(out, "lazyrex_"+version+"_"+osArch+".tar.gz")
	}
	return out
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func checkDist(t *testing.T, dist, version string) {
	t.Helper()
	names := archiveNames(version)
	if got := listDir(t, dist); !slices.Equal(got, append([]string{"SHA256SUMS"}, names...)) {
		t.Fatalf("DIST %v, want %v and SHA256SUMS", got, names)
	}
	var want strings.Builder
	for _, n := range names {
		fmt.Fprintf(&want, "%s  %s\n", sha256File(t, filepath.Join(dist, n)), n)
	}
	if got := readFile(t, filepath.Join(dist, "SHA256SUMS")); got != want.String() {
		t.Errorf("SHA256SUMS:\n%s\nwant\n%s", got, want.String())
	}
}

func TestBuildArchives(t *testing.T) {
	b := newBuild(t)
	r, dist, out := b.run(t, "", "FAKE_GOVERSION=go1.26.0")
	wantCode(t, r, 0)
	checkDist(t, dist, "0.1.0")
	if out != "go-version=go1.26.0\n" {
		t.Errorf("GITHUB_OUTPUT %q", out)
	}
	epoch, err := time.Parse(time.RFC3339, buildDate)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range releaseTargets {
		name := "lazyrex_0.1.0_" + target[0] + "_" + target[1]
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dist, name+".tar.gz"))
			if err != nil {
				t.Fatal(err)
			}
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if zr.Name != "" || !zr.ModTime.IsZero() {
				t.Errorf("gzip header name %q, mtime %v: want neither", zr.Name, zr.ModTime)
			}
			plain, err := io.ReadAll(zr)
			if err != nil {
				t.Fatal(err)
			}
			if magic := string(plain[257:265]); magic != "ustar\x0000" {
				t.Errorf("tar magic %q, want POSIX ustar", magic)
			}
			type entry struct {
				name string
				mode int64
				body string
			}
			want := []entry{
				{name + "/", 0o755, ""},
				{name + "/LICENSE", 0o644, "Apache License\n"},
				{name + "/README.md", 0o644, "# lazyrex\n"},
				{name + "/lazyrex", 0o755, "stub " + target[0] + " " + target[1] + "\n"},
			}
			tr := tar.NewReader(bytes.NewReader(plain))
			for i := 0; ; i++ {
				h, err := tr.Next()
				if err == io.EOF {
					if i != len(want) {
						t.Errorf("%d entries, want %d", i, len(want))
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(tr)
				if i >= len(want) {
					t.Fatalf("extra entry %s", h.Name)
				}
				w := want[i]
				if h.Name != w.name || h.Mode != w.mode || string(body) != w.body {
					t.Errorf("entry %d: %s %o %q, want %s %o %q", i, h.Name, h.Mode, body, w.name, w.mode, w.body)
				}
				if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.PAXRecords != nil {
					t.Errorf("%s: owner %d:%d %q:%q, pax %v", h.Name, h.Uid, h.Gid, h.Uname, h.Gname, h.PAXRecords)
				}
				if !h.ModTime.Equal(epoch) {
					t.Errorf("%s: mtime %v, want the commit time %v", h.Name, h.ModTime, epoch)
				}
			}
		})
	}
}

func TestBuildGoCalls(t *testing.T) {
	b := newBuild(t)
	r, _, _ := b.run(t, "")
	wantCode(t, r, 0)
	calls := b.goCalls(t)
	if len(calls) != len(releaseTargets) {
		t.Fatalf("%d go builds, want %d", len(calls), len(releaseTargets))
	}
	for i, c := range calls {
		target := releaseTargets[i]
		name := "lazyrex_0.1.0_" + target[0] + "_" + target[1]
		if c.cgo != "0" || c.goos != target[0] || c.goarch != target[1] || c.goflags != "" {
			t.Errorf("build %d: CGO_ENABLED=%s GOOS=%s GOARCH=%s GOFLAGS %q", i, c.cgo, c.goos, c.goarch, c.goflags)
		}
		if len(c.argv) != 8 {
			t.Fatalf("build %d argv %q", i, c.argv)
		}
		out := c.argv[6]
		c.argv[6] = "OUT"
		want := []string{"build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -buildid= -X main.version=v0.1.0", "-o", "OUT", "./cmd/lazyrex"}
		if !slices.Equal(c.argv, want) {
			t.Errorf("build %d argv %q, want %q", i, c.argv, want)
		}
		if !strings.HasPrefix(out, b.s.tmp+"/") || !strings.HasSuffix(out, "/"+name+"/lazyrex") {
			t.Errorf("build %d writes %s, want TMPDIR/.../%s/lazyrex", i, out, name)
		}
	}
}

func TestBuildReproducible(t *testing.T) {
	first, second := newBuild(t), newBuild(t)
	r1, dist1, _ := first.run(t, "umask 0022\n")
	wantCode(t, r1, 0)
	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, f := range []string{"LICENSE", "README.md"} {
		if err := os.Chtimes(filepath.Join(second.repo, f), old, old); err != nil {
			t.Fatal(err)
		}
	}
	r2, dist2, _ := second.run(t, "umask 0077\n")
	wantCode(t, r2, 0)
	for _, name := range append(archiveNames("0.1.0"), "SHA256SUMS") {
		a, b := readFile(t, filepath.Join(dist1, name)), readFile(t, filepath.Join(dist2, name))
		if a == "" || a != b {
			t.Errorf("%s differs between two builds of the same tree", name)
		}
	}
}

func TestBuildReleaseCandidate(t *testing.T) {
	b := newBuild(t)
	r, dist, _ := b.run(t, "", "TAG=v0.1.0-rc.1")
	wantCode(t, r, 0)
	checkDist(t, dist, "0.1.0-rc.1")
	for _, c := range b.goCalls(t) {
		if c.argv[4] != "-s -w -buildid= -X main.version=v0.1.0-rc.1" {
			t.Errorf("ldflags %q", c.argv[4])
		}
	}
}

func TestBuildRefuses(t *testing.T) {
	b := newBuild(t)
	t.Run("DIST exists", func(t *testing.T) {
		dist := filepath.Join(b.s.root, "taken")
		if err := os.Mkdir(dist, 0o755); err != nil {
			t.Fatal(err)
		}
		r, _, _ := b.run(t, "", "DIST="+dist)
		wantCode(t, r, 1)
		if !strings.Contains(r.stderr, "exists") || exists(b.goLog) {
			t.Errorf("stderr %q, go built: %v", r.stderr, exists(b.goLog))
		}
	})
	cases := map[string]struct {
		overrides []string
		setup     func(t *testing.T)
		msg       string
	}{
		"other commit": {overrides: []string{"COMMIT=" + strings.Repeat("a", 40)}, msg: "the checkout is not at"},
		"bad commit":   {overrides: []string{"COMMIT=abc"}, msg: "bad commit"},
		"untracked": {setup: func(t *testing.T) {
			writeFile(t, b.repo, "stray", "x\n")
			t.Cleanup(func() { os.Remove(filepath.Join(b.repo, "stray")) })
		}, msg: "not clean"},
		"devel go": {overrides: []string{"FAKE_GOVERSION=devel +x"}, msg: "bad go version"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.setup != nil {
				c.setup(t)
			}
			r, dist, out := b.run(t, "", c.overrides...)
			wantCode(t, r, 1)
			if !strings.Contains(r.stderr, c.msg) {
				t.Errorf("stderr %q lacks %q", r.stderr, c.msg)
			}
			if exists(b.goLog) || exists(dist) || out != "" {
				t.Errorf("go built %v, DIST exists %v, output %q", exists(b.goLog), exists(dist), out)
			}
		})
	}
	for tag, quoted := range badTags {
		t.Run("tag "+quoted, func(t *testing.T) {
			r, dist, _ := b.run(t, "", "TAG="+tag)
			wantCode(t, r, 1)
			if !strings.Contains(r.stderr, "release: tag "+quoted+" is not") {
				t.Errorf("stderr %q", r.stderr)
			}
			if exists(b.goLog+".ran") || exists(dist) {
				t.Errorf("go ran %v, DIST exists %v", exists(b.goLog+".ran"), exists(dist))
			}
		})
	}
	t.Run("build fails", func(t *testing.T) {
		r, _, out := b.run(t, "", "FAKE_FAIL=darwin/arm64")
		if r.code == 0 || out != "" {
			t.Errorf("exit %d, output %q: want a failure", r.code, out)
		}
	})
}

func TestBuildRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds lazyrex")
	}
	goBin := lookPath(t, "go")
	wf, _ := loadRelease(t)
	if !strings.Contains(stepNamed(t, wf, "build", "Build").Run, `go build -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=$TAG"`) {
		t.Fatal("the Build flags changed: update this test")
	}
	bin := filepath.Join(t.TempDir(), "lazyrex")
	cmd := exec.Command(goBin, "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -buildid= -X main.version=v0.1.0", "-o", bin, "./cmd/lazyrex")
	cmd.Dir = "../.."
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOFLAGS=") && !strings.HasPrefix(kv, "CGO_ENABLED=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || string(out) != "lazyrex v0.1.0\n" {
		t.Errorf("lazyrex --version: %q, %v", out, err)
	}
}

// fakeGH stands in for gh. It logs each call's argv to $GH_LOG (NUL-separated, one call per line) and its
// environment to $FIX_DIR/gh.env, answers the calls Publish makes as the FAKE_* variables say, evaluates --jq
// with the real jq, fails the step named by FAKE_FAIL, and runs $FIX_DIR/hook after the one named by
// FAKE_HOOK_AFTER.
const fakeGH = `#!/bin/bash
set -euo pipefail
printf '%s\0' "$@" >>"$GH_LOG"
printf '\n' >>"$GH_LOG"
token=no
if [[ $(printf '%s' "${GH_TOKEN-}" | sha256sum | cut -d ' ' -f 1) == "$FIX_TOKEN_SHA" ]]; then token=yes; fi
printf 'token=%s GH_REPO=%s GH_CONFIG_DIR=%s GH_PROMPT_DISABLED=%s GH_NO_UPDATE_NOTIFIER=%s\n' \
  "$token" "${GH_REPO-}" "${GH_CONFIG_DIR-}" "${GH_PROMPT_DISABLED-}" "${GH_NO_UPDATE_NOTIFIER-}" >>"$FIX_DIR/gh.env"

all=("$@")
jq=""
unexpected() { printf 'fake gh: unexpected call: %s\n' "${all[*]}" >&2; exit 2; }
fail() { if [[ ${FAKE_FAIL-} == "$1" ]]; then printf 'fake gh: %s failed\n' "$1" >&2; exit 1; fi; }
hook() { if [[ ${FAKE_HOOK_AFTER-} == "$1" ]]; then "$FIX_DIR/hook" >&2 || { echo "fake gh: hook failed" >&2; exit 99; }; fi; }
emit() { if [[ -n $jq ]]; then "$REAL_JQ" -r "$jq"; else cat; fi; }

asset() {
  "$REAL_JQ" -cn --arg id "$1" --arg name "$2" --argjson size "$3" --arg digest "$4" --arg state "${FAKE_ASSET_STATE:-uploaded}" \
    '{id: ($id | tonumber? // $id), name: $name, state: $state, size: $size}
      + (if $digest == "absent" then {} elif $digest == "null" then {digest: null} else {digest: $digest} end)'
}

# assets prints one JSON object per uploaded file, ids 1001 on in upload order.
assets() {
  local i path name id size sum digest
  mapfile -t paths <"$FIX_DIR/assets"
  for i in "${!paths[@]}"; do
    path=${paths[i]} name=${paths[i]##*/} id=$((1001 + i))
    [[ $name != "${FAKE_DROP_ASSET-}" ]] || continue
    sum=$(sha256sum -- "$path" | cut -d ' ' -f 1)
    size=$(($(wc -c <"$path") + ${FAKE_SIZE_DELTA:-0}))
    case ${FAKE_DIGEST:-real} in
    real) digest=sha256:$sum ;;
    absent | null) digest=$FAKE_DIGEST ;;
    wrong) digest=sha256:$(printf '0%.0s' {1..64}) ;;
    malformed) digest=md5:abc ;;
    mixed) if ((i == 0)); then digest=absent; else digest=sha256:$sum; fi ;;
    *) echo "fake gh: bad FAKE_DIGEST" >&2; exit 2 ;;
    esac
    if ((i == 0)); then id=${FAKE_ASSET_ID:-$id} name=${FAKE_ASSET_NAME:-$name}; fi
    asset "$id" "$name" "$size" "$digest"
    if [[ $name == "${FAKE_DUP_ASSET-}" ]]; then asset $((1201 + i)) "$name" "$size" "$digest"; fi
  done
  if [[ -n ${FAKE_EXTRA_ASSET-} ]]; then
    path=${paths[${#paths[@]} - 1]}
    asset 1101 "$FAKE_EXTRA_ASSET" "$(wc -c <"$path")" "sha256:$(sha256sum -- "$path" | cut -d ' ' -f 1)"
  fi
}

draft_json() {
  assets | "$REAL_JQ" -cs --arg tag "${FAKE_TAG_NAME:-$TAG}" --argjson draft "${FAKE_DRAFT:-true}" --arg order "${FAKE_ASSET_ORDER-}" \
    '{id: 42, draft: $draft, tag_name: $tag, assets: (if $order == "reverse" then reverse else . end)}'
}

download() {
  local i path
  mapfile -t paths <"$FIX_DIR/assets"
  [[ $1 =~ ^[0-9]+$ ]] || unexpected
  i=$(($1 - 1001))
  ((i >= 0 && i < ${#paths[@]})) || { echo "fake gh: no asset $1" >&2; exit 1; }
  path=${paths[i]}
  if [[ ${path##*/} == "${FAKE_CORRUPT-}" ]]; then
    head -c "$(($(wc -c <"$path") - 1))" -- "$path"
    printf X
  else
    cat -- "$path"
  fi
}

case ${1-} in
release)
  [[ ${2-} == create && $# -ge 3 ]] || unexpected
  shift 3
  notes="" files=()
  while (($#)); do
    case $1 in
    --draft | --verify-tag | --prerelease) shift ;;
    --title) shift 2 ;;
    --notes-file) notes=$2; shift 2 ;;
    -*) unexpected ;;
    *) files+=("$1"); shift ;;
    esac
  done
  fail create
  cp -- "$notes" "$FIX_DIR/notes.copy"
  printf '%s\n' "${files[@]}" >"$FIX_DIR/assets"
  touch "$FIX_DIR/created"
  hook create
  exit 0
  ;;
api) shift ;;
*) unexpected ;;
esac

method=GET paginate=false header="" endpoint="" fields=""
while (($#)); do
  case $1 in
  --method) method=$2; shift 2 ;;
  --paginate) paginate=true; shift ;;
  --jq) jq=$2; shift 2 ;;
  -H) header=$2; shift 2 ;;
  -f | -F) fields+=" $1 $2"; shift 2 ;;
  -*) unexpected ;;
  *) [[ -z $endpoint ]] || unexpected; endpoint=$1; shift ;;
  esac
done

case "$method $paginate $endpoint" in
"GET true repos/o/r/releases?per_page=100")
  [[ -z $header$fields ]] || unexpected
  fail list
  if [[ ! -e $FIX_DIR/created ]]; then
    printf '%s\n' "${FAKE_LIST_BEFORE:-[]}" | emit
  else
    tries=$(cat "$FIX_DIR/empty-tries" 2>/dev/null || echo 0)
    if ((tries < ${FAKE_LIST_EMPTY_TRIES:-0})); then
      echo $((tries + 1)) >"$FIX_DIR/empty-tries"
      printf '[]\n' | emit
    else
      created='[{"id":7,"draft":false,"tag_name":"v0.0.9"},{"id":42,"draft":true,"tag_name":"'"$TAG"'"}]'
      printf '%s\n' "${FAKE_LIST_AFTER:-$created}" | emit
    fi
  fi
  ;;
"POST false repos/o/r/releases/generate-notes")
  [[ -z $header && $fields == " -f tag_name=$TAG" ]] || unexpected
  fail notes
  "$REAL_JQ" -n --arg tag "$TAG" '{name: $tag, body: "## What'\''s Changed\n* x"}' | emit
  ;;
"GET false repos/o/r/releases/42")
  [[ -z $header$fields ]] || unexpected
  fail get
  draft_json | emit
  hook get
  ;;
"GET false repos/o/r/releases/assets/"*)
  [[ $header == "Accept: application/octet-stream" && -z $jq$fields ]] || unexpected
  fail download
  download "${endpoint##*/}"
  ;;
"PATCH false repos/o/r/releases/42")
  [[ -z $header && ($fields == " -F draft=false -f make_latest=true" || $fields == " -F draft=false -f make_latest=false") ]] || unexpected
  fail patch
  touch "$FIX_DIR/published"
  printf '{"id":42,"draft":%s}\n' "${FAKE_PATCH_DRAFT:-false}" | emit
  hook patch
  ;;
*) unexpected ;;
esac
`

// gitWrapper logs each git subcommand to $GIT_LOG, then runs the real git.
const gitWrapper = `#!/bin/bash
args=("$@")
i=0
while ((i < ${#args[@]})); do
  case ${args[i]} in
  -c | -C) i=$((i + 2)) ;;
  *) break ;;
  esac
done
printf '%s\n' "${args[i]-}" >>"$GIT_LOG"
exec "$REAL_GIT" "$@"
`

// A Publish fixture: a bare source whose tags v0.1.0 (lightweight), v0.2.0 and v0.4.0-rc.1 (annotated) name
// commit, with other on another branch; an artifact of four stub archives and their SHA256SUMS; the fake gh and
// the git wrapper first in PATH; and a working directory that is no git repository.
type publishFix struct {
	s                        *sandbox
	source, dist, cwd, ghLog string
	commit, other, tag       string
	step                     step
	env                      map[string]string
}

func newPublish(t *testing.T, tag string) *publishFix {
	t.Helper()
	needGNU(t)
	s := newSandbox(t, "git", "jq", "sha256sum")
	realGit, realJQ := lookPath(t, "git"), lookPath(t, "jq")
	seed := newRepo(t)
	writeFile(t, seed, "a", "1\n")
	commit := commitAll(t, seed, "one", "2026-01-01T00:00:00Z")
	gitIn(t, seed, nil, "checkout", "-q", "-b", "other")
	writeFile(t, seed, "a", "2\n")
	other := commitAll(t, seed, "two", "2026-01-02T00:00:00Z")
	gitIn(t, seed, nil, "checkout", "-q", "main")
	gitIn(t, seed, nil, "tag", "v0.1.0", commit)
	gitIn(t, seed, nil, "tag", "-a", "-m", "v0.2.0", "v0.2.0", commit)
	gitIn(t, seed, nil, "tag", "-a", "-m", "v0.4.0-rc.1", "v0.4.0-rc.1", commit)
	p := &publishFix{
		s: s, source: filepath.Join(s.root, "source.git"), dist: filepath.Join(s.root, "dist"), cwd: filepath.Join(s.root, "cwd"),
		ghLog: filepath.Join(s.root, "gh.log"), commit: commit, other: other, tag: tag,
	}
	gitIn(t, seed, nil, "-c", "init.defaultBranch=main", "init", "-q", "--bare", p.source)
	gitIn(t, seed, nil, "push", "-q", p.source, "refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*")
	if err := os.Mkdir(p.cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	version := strings.TrimPrefix(tag, "v")
	var sums strings.Builder
	for _, n := range archiveNames(version) {
		writeFile(t, p.dist, n, "archive "+n+"\n")
		fmt.Fprintf(&sums, "%s  %s\n", sha256File(t, filepath.Join(p.dist, n)), n)
	}
	writeFile(t, p.dist, "SHA256SUMS", sums.String())
	s.writeBin(t, "gh", fakeGH)
	s.writeBin(t, "git", gitWrapper)
	wf, _ := loadRelease(t)
	p.step = stepNamed(t, wf, "publish", "Publish")
	sum := sha256.Sum256([]byte(s.token))
	p.env = stepEnv(p.step, map[string]string{
		"TAG": tag, "COMMIT": commit, "GO_VERSION": "go1.26.0", "GH_REPO": "o/r", "GH_TOKEN": s.token,
		"SOURCE_REMOTE": "file://" + p.source, "DIST": p.dist,
		"REAL_GIT": realGit, "REAL_JQ": realJQ, "GH_LOG": p.ghLog, "GIT_LOG": filepath.Join(s.root, "git.log"),
		"FIX_DIR": s.root, "FIX_SOURCE": p.source, "FIX_TOKEN_SHA": hex.EncodeToString(sum[:]),
	})
	return p
}

func (p *publishFix) run(t *testing.T, overrides ...string) result {
	t.Helper()
	env := maps.Clone(p.env)
	for _, o := range overrides {
		k, v, _ := strings.Cut(o, "=")
		env[k] = v
	}
	return p.s.run(t, p.step.Run, p.cwd, env)
}

// setHook installs the bash body the fake gh runs after the step named by FAKE_HOOK_AFTER.
func (p *publishFix) setHook(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p.s.root, "hook"), []byte("#!/bin/bash\nset -euo pipefail\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (p *publishFix) moveTag() string {
	return `"$REAL_GIT" -C "$FIX_SOURCE" update-ref refs/tags/` + p.tag + " " + p.other
}

func (p *publishFix) calls(t *testing.T) [][]string {
	t.Helper()
	var out [][]string
	for _, rec := range strings.Split(readFile(t, p.ghLog), "\x00\n") {
		if rec != "" {
			out = append(out, strings.Split(rec, "\x00"))
		}
	}
	return out
}

func ghKind(argv []string) string {
	switch {
	case len(argv) > 1 && argv[0] == "release" && argv[1] == "create":
		return "create"
	case slices.Contains(argv, "POST"):
		return "notes"
	case slices.Contains(argv, "PATCH"):
		return "patch"
	case slices.Contains(argv, "-H"):
		return "download"
	case slices.Contains(argv, "--paginate"):
		return "list"
	case slices.Contains(argv, "repos/o/r/releases/42"):
		return "get"
	}
	return strings.Join(argv, " ")
}

func (p *publishFix) kinds(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range p.calls(t) {
		out = append(out, ghKind(c))
	}
	return out
}

var (
	listArgv = []string{"api", "--paginate", "repos/o/r/releases?per_page=100", "--jq", `.[] | "\(.id) \(.draft) \(.tag_name)"`}
	fullRun  = []string{"list", "notes", "create", "list", "get", "patch"}
)

func TestPublishFirstRelease(t *testing.T) {
	p := newPublish(t, "v0.1.0")
	r := p.run(t)
	wantCode(t, r, 0)
	if !strings.Contains(r.stdout, "release: published v0.1.0 from "+p.commit[:12]) {
		t.Errorf("stdout %q", r.stdout)
	}
	calls := p.calls(t)
	if got := p.kinds(t); !slices.Equal(got, fullRun) {
		t.Fatalf("gh calls %q, want %q", got, fullRun)
	}
	if !slices.Equal(calls[0], listArgv) || !slices.Equal(calls[3], listArgv) {
		t.Errorf("list calls %q and %q, want %q", calls[0], calls[3], listArgv)
	}
	if want := []string{"api", "--method", "POST", "repos/o/r/releases/generate-notes", "-f", "tag_name=v0.1.0", "--jq", ".body"}; !slices.Equal(calls[1], want) {
		t.Errorf("notes call %q, want %q", calls[1], want)
	}
	create := calls[2]
	if len(create) < 9 || !strings.HasPrefix(create[8], p.s.tmp+"/") || !strings.HasSuffix(create[8], "/notes.md") {
		t.Fatalf("create call %q: want the notes under TMPDIR", create)
	}
	want := []string{"release", "create", "v0.1.0", "--draft", "--verify-tag", "--title", "v0.1.0", "--notes-file", create[8]}
	for _, n := range append(archiveNames("0.1.0"), "SHA256SUMS") {
		want = append(want, filepath.Join(p.dist, n))
	}
	if !slices.Equal(create, want) {
		t.Errorf("create call %q, want %q", create, want)
	}
	if get := calls[4]; len(get) != 4 || get[0] != "api" || get[1] != "repos/o/r/releases/42" || get[2] != "--jq" || !strings.Contains(get[3], `.digest // "none"`) {
		t.Errorf("get call %q", get)
	}
	if want := []string{"api", "--method", "PATCH", "repos/o/r/releases/42", "-F", "draft=false", "-f", "make_latest=true", "--jq", ".draft"}; !slices.Equal(calls[5], want) {
		t.Errorf("patch call %q, want %q", calls[5], want)
	}

	envLine := regexp.MustCompile(`^token=yes GH_REPO=o/r GH_CONFIG_DIR=` + regexp.QuoteMeta(p.s.tmp) + `/[^/ ]+/gh GH_PROMPT_DISABLED=1 GH_NO_UPDATE_NOTIFIER=1$`)
	lines := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(p.s.root, "gh.env")), "\n"), "\n")
	if len(lines) != len(fullRun) {
		t.Errorf("%d gh env lines, want %d", len(lines), len(fullRun))
	}
	for _, l := range lines {
		if !envLine.MatchString(l) {
			t.Errorf("gh env %q", l)
		}
	}

	notes := readFile(t, filepath.Join(p.s.root, "notes.copy"))
	ldflags := `-ldflags "-s -w -buildid= -X main.version=v0.1.0"`
	for _, w := range []string{"Built from " + p.commit + " with go1.26.0", "GOOS=<linux|darwin> GOARCH=<amd64|arm64>", ldflags, "SHA256SUMS"} {
		if !strings.Contains(notes, w) {
			t.Errorf("notes lack %q:\n%s", w, notes)
		}
	}
	if !strings.HasSuffix(notes, "\n## What's Changed\n* x\n") || strings.Index(notes, ldflags) > strings.Index(notes, "## What's Changed") {
		t.Errorf("notes do not end with the generated body:\n%s", notes)
	}
}

func TestPublishReleaseCandidate(t *testing.T) {
	p := newPublish(t, "v0.4.0-rc.1")
	wantCode(t, p.run(t), 0)
	calls := p.calls(t)
	if got := p.kinds(t); !slices.Equal(got, fullRun) {
		t.Fatalf("gh calls %q", got)
	}
	if create := calls[2]; len(create) < 10 || create[9] != "--prerelease" || create[10] != filepath.Join(p.dist, "lazyrex_0.4.0-rc.1_darwin_amd64.tar.gz") {
		t.Errorf("create call %q, want --prerelease before the archives", create)
	}
	if !slices.Contains(calls[5], "make_latest=false") {
		t.Errorf("patch call %q, want make_latest=false", calls[5])
	}
}

func TestPublishSucceeds(t *testing.T) {
	withDownloads := func(n int) []string {
		return slices.Concat(fullRun[:5], slices.Repeat([]string{"download"}, n), fullRun[5:])
	}
	cases := map[string]struct {
		tag      string
		env      []string
		setup    func(t *testing.T, p *publishFix)
		calls    []string
		download []string
		stdout   string
	}{
		"annotated tag":     {tag: "v0.2.0", calls: fullRun},
		"similar releases":  {env: []string{`FAKE_LIST_BEFORE=[{"id":1,"draft":false,"tag_name":"v0.1.0-rc.1"},{"id":2,"draft":true,"tag_name":"v0.1.00"}]`}, calls: fullRun},
		"listed late":       {env: []string{"FAKE_LIST_EMPTY_TRIES=1"}, calls: []string{"list", "notes", "create", "list", "list", "get", "patch"}},
		"any asset order":   {env: []string{"FAKE_ASSET_ORDER=reverse"}, calls: fullRun},
		"no digest":         {env: []string{"FAKE_DIGEST=absent"}, calls: withDownloads(5), download: []string{"1001", "1002", "1003", "1004", "1005"}},
		"null digest":       {env: []string{"FAKE_DIGEST=null"}, calls: withDownloads(5), download: []string{"1001", "1002", "1003", "1004", "1005"}},
		"one digest absent": {env: []string{"FAKE_DIGEST=mixed"}, calls: withDownloads(1), download: []string{"1001"}},
		"source gone after": {
			env: []string{"FAKE_HOOK_AFTER=patch"}, setup: func(t *testing.T, p *publishFix) { p.setHook(t, `mv "$FIX_SOURCE" "$FIX_SOURCE.gone"`) },
			calls: fullRun, stdout: "could not re-read tag v0.1.0 after publishing",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tag := c.tag
			if tag == "" {
				tag = "v0.1.0"
			}
			p := newPublish(t, tag)
			if c.setup != nil {
				c.setup(t, p)
			}
			r := p.run(t, c.env...)
			wantCode(t, r, 0)
			if !strings.Contains(r.stdout, "release: published "+tag) || !strings.Contains(r.stdout, c.stdout) {
				t.Errorf("stdout %q", r.stdout)
			}
			if got := p.kinds(t); !slices.Equal(got, c.calls) {
				t.Errorf("gh calls %q, want %q", got, c.calls)
			}
			var got []string
			for _, call := range p.calls(t) {
				if ghKind(call) != "download" {
					continue
				}
				id, ok := strings.CutPrefix(strings.Join(call, " "), "api -H Accept: application/octet-stream repos/o/r/releases/assets/")
				if !ok {
					t.Errorf("download call %q", call)
				}
				got = append(got, id)
			}
			if !slices.Equal(got, c.download) {
				t.Errorf("downloads %q, want %q", got, c.download)
			}
		})
	}
}

func TestPublishRefuses(t *testing.T) {
	var (
		none      []string
		listOnly  = []string{"list"}
		upToGet   = fullRun[:5]
		keep      = "the draft (id 42) is left for review, nothing published"
		removeArc = func(name string) func(t *testing.T, p *publishFix) {
			return func(t *testing.T, p *publishFix) { os.Remove(filepath.Join(p.dist, name)) }
		}
	)
	type refusal struct {
		tag       string
		env       []string
		setup     func(t *testing.T, p *publishFix)
		msg       []string
		calls     []string
		noGit     bool
		published bool
	}
	cases := map[string]refusal{
		"release exists, draft":     {env: []string{`FAKE_LIST_BEFORE=[{"id":9,"draft":true,"tag_name":"v0.1.0"}]`}, msg: []string{"a release of v0.1.0 exists (id 9, draft true): nothing replaced"}, calls: listOnly},
		"release exists, published": {env: []string{`FAKE_LIST_BEFORE=[{"id":9,"draft":false,"tag_name":"v0.1.0"}]`}, msg: []string{"exists (id 9, draft false): nothing replaced"}, calls: listOnly},
		"release exists, page 2": {
			env: []string{"FAKE_LIST_BEFORE=[{\"id\":1,\"draft\":false,\"tag_name\":\"v0.0.1\"}]\n[{\"id\":2,\"draft\":false,\"tag_name\":\"v0.1.0\"}]"},
			msg: []string{"exists (id 2"}, calls: listOnly,
		},
		"list fails":  {env: []string{"FAKE_FAIL=list"}, msg: []string{"could not list the releases"}, calls: listOnly},
		"tag moved":   {setup: func(t *testing.T, p *publishFix) { gitIn(t, p.source, nil, "update-ref", "refs/tags/v0.1.0", p.other) }, msg: []string{"moved to", "before the publication", "nothing published"}, calls: none},
		"tag deleted": {setup: func(t *testing.T, p *publishFix) { gitIn(t, p.source, nil, "update-ref", "-d", "refs/tags/v0.1.0") }, msg: []string{"tag v0.1.0 was deleted before the publication: nothing published"}, calls: none},
		"source unreadable": {
			setup: func(t *testing.T, p *publishFix) {
				p.env["SOURCE_REMOTE"] = "file://" + filepath.Join(p.s.root, "private.git")
			},
			msg: []string{"could not read tag v0.1.0 from"}, calls: none,
		},
		"tag moved after create": {
			env: []string{"FAKE_HOOK_AFTER=create"}, setup: func(t *testing.T, p *publishFix) { p.setHook(t, p.moveTag()) },
			msg: []string{"moved to", "after the upload", keep}, calls: upToGet,
		},
		"create fails":              {env: []string{"FAKE_FAIL=create"}, msg: []string{"gh release create failed"}, calls: fullRun[:3]},
		"notes fail":                {env: []string{"FAKE_FAIL=notes"}, msg: []string{"could not generate the release notes"}, calls: fullRun[:2]},
		"never listed":              {env: []string{"FAKE_LIST_AFTER=[]"}, msg: []string{"found 0 releases of v0.1.0"}, calls: []string{"list", "notes", "create", "list", "list", "list"}},
		"listed twice":              {env: []string{`FAKE_LIST_AFTER=[{"id":42,"draft":true,"tag_name":"v0.1.0"},{"id":43,"draft":true,"tag_name":"v0.1.0"}]`}, msg: []string{"found 2 releases"}, calls: fullRun[:4]},
		"listed published":          {env: []string{`FAKE_LIST_AFTER=[{"id":42,"draft":false,"tag_name":"v0.1.0"}]`}, msg: []string{"(id 42) is not a draft"}, calls: fullRun[:4]},
		"draft lacks SHA256SUMS":    {env: []string{"FAKE_DROP_ASSET=SHA256SUMS"}, msg: []string{"the draft lacks SHA256SUMS: " + keep}, calls: upToGet},
		"asset not uploaded":        {env: []string{"FAKE_ASSET_STATE=starter"}, msg: []string{"not uploaded: " + keep}, calls: upToGet},
		"asset size":                {env: []string{"FAKE_SIZE_DELTA=1"}, msg: []string{"another size in the draft: " + keep}, calls: upToGet},
		"release not a draft":       {env: []string{"FAKE_DRAFT=false"}, msg: []string{"release 42 is not the draft of v0.1.0: " + keep}, calls: upToGet},
		"release of another tag":    {env: []string{"FAKE_TAG_NAME=v9.9.9"}, msg: []string{"is not the draft of v0.1.0: " + keep}, calls: upToGet},
		"extra asset":               {env: []string{"FAKE_EXTRA_ASSET=evil.sh"}, msg: []string{"the draft holds an unexpected asset evil.sh: " + keep}, calls: upToGet},
		"duplicate asset":           {env: []string{"FAKE_DUP_ASSET=SHA256SUMS"}, msg: []string{"the draft holds SHA256SUMS twice: " + keep}, calls: upToGet},
		"asset name with a space":   {env: []string{"FAKE_ASSET_NAME=lazyrex 0.1.0.tar.gz"}, msg: []string{"unexpected asset line in the draft: " + keep}, calls: upToGet},
		"asset name with newline":   {env: []string{"FAKE_ASSET_NAME=lazyrex_0.1.0_darwin_amd64.tar.gz\nevil"}, msg: []string{"unexpected line in the draft: " + keep}, calls: upToGet},
		"asset id zero":             {env: []string{"FAKE_ASSET_ID=0"}, msg: []string{"unexpected asset line in the draft: " + keep}, calls: upToGet},
		"asset id not a number":     {env: []string{"FAKE_ASSET_ID=x"}, msg: []string{"unexpected asset line in the draft: " + keep}, calls: upToGet},
		"get fails":                 {env: []string{"FAKE_FAIL=get"}, msg: []string{"could not read the draft of v0.1.0: " + keep}, calls: upToGet},
		"patch fails":               {env: []string{"FAKE_FAIL=patch"}, msg: []string{"could not publish the draft of v0.1.0 (id 42): it is left for review"}, calls: fullRun},
		"still a draft":             {env: []string{"FAKE_PATCH_DRAFT=true"}, msg: []string{"(id 42) is still a draft"}, calls: fullRun, published: true},
		"wrong digest":              {env: []string{"FAKE_DIGEST=wrong"}, msg: []string{"in the draft does not match the built archive: " + keep}, calls: upToGet},
		"malformed digest":          {env: []string{"FAKE_DIGEST=malformed"}, msg: []string{"has an unknown digest md5:abc in the draft: " + keep}, calls: upToGet},
		"corrupt download":          {env: []string{"FAKE_DIGEST=absent", "FAKE_CORRUPT=lazyrex_0.1.0_linux_amd64.tar.gz"}, msg: []string{"lazyrex_0.1.0_linux_amd64.tar.gz in the draft does not match the built archive: " + keep}, calls: slices.Concat(upToGet, []string{"download", "download", "download"})},
		"download fails":            {env: []string{"FAKE_DIGEST=absent", "FAKE_FAIL=download"}, msg: []string{"could not download lazyrex_0.1.0_darwin_amd64.tar.gz from the draft: " + keep}, calls: slices.Concat(upToGet, []string{"download"})},
		"bad commit":                {env: []string{"COMMIT=abc"}, msg: []string{"bad commit"}, calls: none, noGit: true},
		"bad go version":            {env: []string{"GO_VERSION=devel +x"}, msg: []string{"bad go version"}, calls: none, noGit: true},
		"repository with a slash":   {env: []string{"GH_REPO=o/r/x"}, msg: []string{"bad repository"}, calls: none, noGit: true},
		"repository with a space":   {env: []string{"GH_REPO=o r"}, msg: []string{"bad repository"}, calls: none, noGit: true},
		"artifact lacks archive":    {setup: removeArc("lazyrex_0.1.0_linux_arm64.tar.gz"), msg: []string{"the artifact holds 4 entries"}, calls: none, noGit: true},
		"artifact lacks SHA256SUMS": {setup: removeArc("SHA256SUMS"), msg: []string{"the artifact holds 4 entries"}, calls: none, noGit: true},
		"artifact extra file":       {setup: func(t *testing.T, p *publishFix) { writeFile(t, p.dist, "notes.txt", "x\n") }, msg: []string{"the artifact holds 6 entries"}, calls: none, noGit: true},
		"artifact hidden file":      {setup: func(t *testing.T, p *publishFix) { writeFile(t, p.dist, ".hidden", "x\n") }, msg: []string{"the artifact holds 6 entries"}, calls: none, noGit: true},
		"artifact symlink": {setup: func(t *testing.T, p *publishFix) {
			name := filepath.Join(p.dist, "lazyrex_0.1.0_linux_arm64.tar.gz")
			os.Rename(name, filepath.Join(p.s.root, "real.tar.gz"))
			if err := os.Symlink(filepath.Join(p.s.root, "real.tar.gz"), name); err != nil {
				t.Fatal(err)
			}
		}, msg: []string{"the artifact lacks lazyrex_0.1.0_linux_arm64.tar.gz"}, calls: none, noGit: true},
		"artifact subdirectory": {setup: func(t *testing.T, p *publishFix) {
			os.Remove(filepath.Join(p.dist, "lazyrex_0.1.0_linux_arm64.tar.gz"))
			writeFile(t, p.dist, "lazyrex_0.1.0_linux_arm64.tar.gz/x", "x\n")
		}, msg: []string{"the artifact lacks lazyrex_0.1.0_linux_arm64.tar.gz"}, calls: none, noGit: true},
		"SHA256SUMS extra line": {setup: func(t *testing.T, p *publishFix) {
			sums := readFile(t, filepath.Join(p.dist, "SHA256SUMS"))
			writeFile(t, p.dist, "SHA256SUMS", sums+strings.Repeat("0", 64)+"  x\n")
		}, msg: []string{"SHA256SUMS has extra lines"}, calls: none, noGit: true},
		"SHA256SUMS order": {setup: func(t *testing.T, p *publishFix) {
			l := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(p.dist, "SHA256SUMS")), "\n"), "\n")
			l[0], l[1] = l[1], l[0]
			writeFile(t, p.dist, "SHA256SUMS", strings.Join(l, "\n")+"\n")
		}, msg: []string{"SHA256SUMS line 1 is not the checksum of lazyrex_0.1.0_darwin_amd64.tar.gz"}, calls: none, noGit: true},
		"SHA256SUMS short": {setup: func(t *testing.T, p *publishFix) {
			l := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(p.dist, "SHA256SUMS")), "\n"), "\n")
			writeFile(t, p.dist, "SHA256SUMS", strings.Join(l[:3], "\n")+"\n")
		}, msg: []string{"SHA256SUMS lists 3 archives, want 4"}, calls: none, noGit: true},
		"archive changed": {setup: func(t *testing.T, p *publishFix) {
			writeFile(t, p.dist, "lazyrex_0.1.0_linux_amd64.tar.gz", "archive lazyrex_0.1.0_linux_amd64.tar.gX\n")
		}, msg: []string{"an archive does not match SHA256SUMS"}, calls: none, noGit: true},
	}
	for tag, quoted := range badTags {
		cases["tag "+quoted] = refusal{env: []string{"TAG=" + tag}, msg: []string{"release: tag " + quoted + " is not"}, calls: none, noGit: true}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tag := c.tag
			if tag == "" {
				tag = "v0.1.0"
			}
			p := newPublish(t, tag)
			if c.setup != nil {
				c.setup(t, p)
			}
			r := p.run(t, c.env...)
			wantCode(t, r, 1)
			for _, m := range c.msg {
				if !strings.Contains(r.stderr, m) {
					t.Errorf("stderr %q lacks %q", r.stderr, m)
				}
			}
			if got := p.kinds(t); !slices.Equal(got, c.calls) {
				t.Errorf("gh calls %q, want %q", got, c.calls)
			}
			if c.noGit && exists(filepath.Join(p.s.root, "git.log")) {
				t.Error("git ran before the inputs were checked")
			}
			if published := exists(filepath.Join(p.s.root, "published")); published != c.published {
				t.Errorf("published %v, want %v", published, c.published)
			}
		})
	}
}

func TestPublishTagMovedAfterPublishing(t *testing.T) {
	p := newPublish(t, "v0.1.0")
	p.setHook(t, p.moveTag())
	r := p.run(t, "FAKE_HOOK_AFTER=patch")
	wantCode(t, r, 1)
	if want := "tag v0.1.0 moved to " + p.other + " while " + p.commit[:12] + " was published: review the release (id 42)"; !strings.Contains(r.stderr, want) {
		t.Errorf("stderr %q lacks %q", r.stderr, want)
	}
	if got := p.kinds(t); !slices.Equal(got, fullRun) || !exists(filepath.Join(p.s.root, "published")) {
		t.Errorf("gh calls %q, want the full run, published", got)
	}
}
