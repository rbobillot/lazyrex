package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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

	"gopkg.in/yaml.v3"
)

const workflowPath = "../../.github/workflows/wiki.yml"

type workflowFile struct {
	On          map[string]any    `yaml:"on"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency map[string]string `yaml:"concurrency"`
	Jobs        map[string]job    `yaml:"jobs"`
}

type job struct {
	If          string            `yaml:"if"`
	Needs       any               `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Outputs     map[string]string `yaml:"outputs"`
	Steps       []step            `yaml:"steps"`
}

type step struct {
	ID   string            `yaml:"id"`
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	Run  string            `yaml:"run"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

func loadWorkflow(t *testing.T) (workflowFile, string) {
	t.Helper()
	text, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var wf workflowFile
	if err := yaml.Unmarshal(text, &wf); err != nil {
		t.Fatal(err)
	}
	return wf, string(text)
}

func stepUsing(t *testing.T, j job, action string) step {
	t.Helper()
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, action+"@") {
			return s
		}
	}
	t.Fatalf("no step uses %s", action)
	return step{}
}

func publishStep(t *testing.T) step {
	t.Helper()
	wf, _ := loadWorkflow(t)
	for _, s := range wf.Jobs["publish"].Steps {
		if s.Name == "Publish" {
			return s
		}
	}
	t.Fatal("no Publish step in job publish")
	return step{}
}

func TestWorkflowPins(t *testing.T) {
	_, text := loadWorkflow(t)
	pin := regexp.MustCompile(`^\s*(- )?uses: ([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@[0-9a-f]{40} # v\d+\.\d+\.\d+$`)
	var uses []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.Contains(l, "uses:") {
			continue
		}
		if !pin.MatchString(l) {
			t.Errorf("unpinned or uncommented action: %q", l)
		}
		uses = append(uses, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")))
	}
	want := []string{
		"uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0",
		"uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2",
		"uses: actions/download-artifact@9000827ccba6bdab643e8b6fd33ac0654aef8333 # v8.0.2",
	}
	if !slices.Equal(uses, want) {
		t.Errorf("uses lines:\n%s\nwant\n%s", strings.Join(uses, "\n"), strings.Join(want, "\n"))
	}
}

func TestWorkflowNoExpressionInRun(t *testing.T) {
	wf, _ := loadWorkflow(t)
	runs := 0
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if s.Run != "" {
				runs++
			}
			if strings.Contains(s.Run, "${{") {
				t.Errorf("job %s step %q: ${{ }} inside run: pass it through env", name, s.Name)
			}
		}
	}
	if runs < 4 {
		t.Errorf("only %d run steps found: the YAML did not parse as expected", runs)
	}
}

func TestWorkflowPermissionsAndJobs(t *testing.T) {
	wf, _ := loadWorkflow(t)
	if len(wf.Permissions) != 1 || wf.Permissions["contents"] != "read" {
		t.Errorf("workflow permissions %v, want contents: read", wf.Permissions)
	}
	if got := slices.Sorted(maps.Keys(wf.Jobs)); !slices.Equal(got, []string{"publish", "render"}) {
		t.Fatalf("jobs %v, want publish and render", got)
	}
	render, publish := wf.Jobs["render"], wf.Jobs["publish"]
	if render.Permissions != nil {
		t.Errorf("render overrides permissions: %v", render.Permissions)
	}
	if len(publish.Permissions) != 1 || publish.Permissions["contents"] != "write" {
		t.Errorf("publish permissions %v, want contents: write", publish.Permissions)
	}
	if publish.Needs != "render" {
		t.Errorf("publish needs %v, want render", publish.Needs)
	}
	for _, want := range []string{"github.event_name != 'pull_request'", "github.ref == 'refs/heads/main'"} {
		if !strings.Contains(publish.If, want) {
			t.Errorf("publish if %q lacks %q", publish.If, want)
		}
	}
	if len(publish.Steps) != 2 || !strings.HasPrefix(publish.Steps[0].Uses, "actions/download-artifact@") ||
		publish.Steps[1].Name != "Publish" || publish.Steps[1].Uses != "" {
		t.Errorf("publish steps %+v, want exactly download-artifact then Publish", publish.Steps)
	}
	for _, s := range publish.Steps {
		if strings.Contains(s.Uses, "checkout") || strings.Contains(s.Uses, "setup-go") || strings.Contains(s.Run, "go ") {
			t.Errorf("publish runs repository code: %+v", s)
		}
	}
	if render.Outputs["source-time"] != "${{ steps.source.outputs.time }}" {
		t.Errorf("render outputs %v", render.Outputs)
	}
}

func TestWorkflowTriggersAndConcurrency(t *testing.T) {
	wf, _ := loadWorkflow(t)
	if got := slices.Sorted(maps.Keys(wf.On)); !slices.Equal(got, []string{"pull_request", "push", "schedule", "workflow_dispatch"}) {
		t.Errorf("triggers %v", got)
	}
	if _, ok := wf.On["pull_request_target"]; ok {
		t.Error("pull_request_target is a trigger")
	}
	push, _ := wf.On["push"].(map[string]any)
	if fmt.Sprint(push["branches"]) != "[main]" || len(push) != 1 {
		t.Errorf("push trigger %v, want branches [main] and no path filter", push)
	}
	if wf.Concurrency["group"] != "${{ github.event_name == 'pull_request' && format('wiki-pr-{0}', github.event.pull_request.number) || 'wiki' }}" ||
		wf.Concurrency["cancel-in-progress"] != "${{ github.event_name == 'pull_request' }}" {
		t.Errorf("concurrency %v", wf.Concurrency)
	}
}

func TestWorkflowRenderHardening(t *testing.T) {
	wf, _ := loadWorkflow(t)
	render := wf.Jobs["render"]
	checkout := stepUsing(t, render, "actions/checkout")
	if fmt.Sprint(checkout.With["persist-credentials"]) != "false" || fmt.Sprint(checkout.With["fetch-depth"]) != "0" {
		t.Errorf("checkout with %v, want persist-credentials false and fetch-depth 0", checkout.With)
	}
	if setup := stepUsing(t, render, "actions/setup-go"); fmt.Sprint(setup.With["cache"]) != "false" {
		t.Errorf("setup-go with %v, want cache false", setup.With)
	}
	if upload := stepUsing(t, render, "actions/upload-artifact"); upload.If != "github.event_name != 'pull_request'" {
		t.Errorf("upload-artifact if %q", upload.If)
	}
	var tested bool
	for _, s := range render.Steps {
		tested = tested || strings.Contains(s.Run, "go test ./tools/wikisync")
	}
	if !tested {
		t.Error("render does not run the renderer's tests")
	}
}

func TestWorkflowNeverForces(t *testing.T) {
	run := publishStep(t).Run
	short := regexp.MustCompile(`(^|\s)-f(\s|$)`)
	plus := regexp.MustCompile(`\+[A-Za-z0-9${]`)
	pushes := 0
	for _, l := range strings.Split(run, "\n") {
		if !strings.Contains(l, "push") {
			continue
		}
		pushes++
		if strings.Contains(l, "--force") || short.MatchString(l) || plus.MatchString(l) {
			t.Errorf("forced push: %q", l)
		}
	}
	if pushes == 0 {
		t.Error("no push line in Publish")
	}
}

func TestWorkflowHelperWiring(t *testing.T) {
	s := publishStep(t)
	var wikiGit string
	for _, l := range strings.Split(s.Run, "\n") {
		if strings.Contains(l, "git -c credential.helper=") {
			wikiGit = l
		}
	}
	for _, want := range []string{"-c credential.helper= ", `-c "credential.helper=${CREDENTIAL_HELPER}"`, "-c credential.useHttpPath=true"} {
		if !strings.Contains(wikiGit, want) {
			t.Errorf("wiki_git %q lacks %q", wikiGit, want)
		}
	}
	keys := slices.Sorted(maps.Keys(s.Env))
	want := []string{"CREDENTIAL_HELPER", "PAGES", "SOURCE_REMOTE", "SOURCE_SHA", "SOURCE_TIME", "WIKI_PATH", "WIKI_REMOTE", "WIKI_TOKEN"}
	if !slices.Equal(keys, want) {
		t.Errorf("Publish env %v, want %v", keys, want)
	}
	if s.Env["WIKI_TOKEN"] != "${{ github.token }}" || s.Env["SOURCE_TIME"] != "${{ needs.render.outputs.source-time }}" {
		t.Errorf("Publish env %v", s.Env)
	}
	for _, l := range strings.Split(s.Run, "\n")[:4] {
		if strings.Contains(l, "set +x") {
			return
		}
	}
	t.Error("Publish does not start with set +x")
}

// A publish fixture: a bare source repository whose main is SOURCE_SHA, a bare wiki on branch master holding
// a hand-made Home.md, an artifact of three pages, and a wrapper git first in PATH that logs each subcommand
// and, after a successful clone or push, runs the case's hook.
type publish struct {
	dir, source, wiki, pages, tmp, home, log, hook string
	sourceSHA, initial, token                      string
	script                                         string
	env                                            map[string]string
}

type result struct {
	code           int
	stdout, stderr string
}

const sourceTime = "1767225600"

const wrapper = `#!/bin/bash
args=("$@")
i=0
while ((i < ${#args[@]})); do
  case ${args[i]} in
    -c|-C) i=$((i + 2)) ;;
    *) break ;;
  esac
done
sub=${args[i]-}
printf '%s\n' "$sub" >>"$WRAP_LOG"
"$REAL_GIT" "$@" || exit
if [[ ($sub == clone || $sub == push) && -x $WRAP_HOOK ]]; then
  "$WRAP_HOOK" "$sub" >&2 || { echo "hook failed after $sub" >&2; exit 99; }
fi
`

func lookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skip(name + " is not installed")
	}
	return p
}

func randomToken(t *testing.T) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "tok-" + hex.EncodeToString(b)
}

func pageWithBanner(name string) string {
	return bannerPrefix + "[docs/" + name + ".md](https://github.com/o/r/blob/x/docs/" + name + ".md) at `x`: generated.\n\n# " + name + "\n"
}

func newPublish(t *testing.T) *publish {
	t.Helper()
	lookPath(t, "bash")
	realGit := lookPath(t, "git")
	seed := newRepo(t)
	writeFile(t, seed, "README.md", "r\n")
	sha := commitAll(t, seed, "one", "2026-01-01T00:00:00Z")

	dir := t.TempDir()
	p := &publish{
		dir: dir, source: filepath.Join(dir, "source.git"), wiki: filepath.Join(dir, "wiki.git"),
		pages: filepath.Join(dir, "pages"), tmp: filepath.Join(dir, "tmp"), home: filepath.Join(dir, "home"),
		log: filepath.Join(dir, "git.log"), hook: filepath.Join(dir, "hook"), sourceSHA: sha, token: randomToken(t),
	}
	gitIn(t, dir, nil, "init", "-q", "--bare", p.source)
	gitIn(t, seed, nil, "push", "-q", p.source, "HEAD:refs/heads/main")

	gitIn(t, dir, nil, "init", "-q", "--bare", p.wiki)
	gitIn(t, p.wiki, nil, "symbolic-ref", "HEAD", "refs/heads/master")
	wseed := t.TempDir()
	gitIn(t, wseed, nil, "-c", "init.defaultBranch=master", "init", "-q")
	writeFile(t, wseed, "Home.md", "Welcome to the r wiki!\n")
	p.initial = commitAll(t, wseed, "Initial Home page", "2026-01-01T00:00:00Z")
	gitIn(t, wseed, nil, "push", "-q", p.wiki, "master")

	for _, name := range []string{"Home", "_Sidebar", "a"} {
		writeFile(t, p.pages, name+".md", pageWithBanner(name))
	}
	for _, d := range []string{p.tmp, p.home, filepath.Join(dir, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	s := publishStep(t)
	p.script = filepath.Join(dir, "publish.sh")
	if err := os.WriteFile(p.script, []byte(s.Run), 0o644); err != nil {
		t.Fatal(err)
	}
	p.env = map[string]string{}
	for k, v := range s.Env {
		p.env[k] = v
	}
	for k, v := range map[string]string{
		"WIKI_TOKEN": p.token, "WIKI_PATH": "o/r.wiki.git", "WIKI_REMOTE": "file://" + p.wiki,
		"SOURCE_REMOTE": "file://" + p.source, "SOURCE_SHA": sha, "SOURCE_TIME": sourceTime, "PAGES": p.pages,
	} {
		p.env[k] = v
	}
	p.env["PATH"] = filepath.Join(dir, "bin") + ":" + filepath.Dir(realGit) + ":/usr/bin:/bin"
	p.env["HOME"] = p.home
	p.env["TMPDIR"] = p.tmp
	p.env["GIT_CONFIG_GLOBAL"] = os.DevNull
	p.env["GIT_CONFIG_NOSYSTEM"] = "1"
	p.env["REAL_GIT"] = realGit
	p.env["WRAP_LOG"] = p.log
	p.env["WRAP_HOOK"] = p.hook
	p.env["FIX_DIR"] = dir
	p.env["FIX_SOURCE"] = p.source
	p.env["FIX_WIKI"] = p.wiki
	return p
}

// setHook installs the bash body run by the wrapper with the subcommand as $1.
func (p *publish) setHook(t *testing.T, body string) {
	t.Helper()
	script := "#!/bin/bash\nset -euo pipefail\nexport GIT_AUTHOR_NAME=h GIT_AUTHOR_EMAIL=h@h GIT_COMMITTER_NAME=h GIT_COMMITTER_EMAIL=h@h\n" +
		body + "\ntouch \"$FIX_DIR/hook.ran\"\n"
	if err := os.WriteFile(p.hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (p *publish) run(t *testing.T, overrides ...string) result {
	t.Helper()
	env := map[string]string{}
	for k, v := range p.env {
		env[k] = v
	}
	for _, o := range overrides {
		k, v, _ := strings.Cut(o, "=")
		env[k] = v
	}
	var list []string
	for k, v := range env {
		if strings.Contains(v, "${{") {
			t.Fatalf("env %s=%q was not overridden", k, v)
		}
		list = append(list, k+"="+v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-eo", "pipefail", p.script)
	cmd.Dir = p.dir
	cmd.Env = list
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{0, stdout.String(), stderr.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	p.checkHygiene(t, r)
	return r
}

// checkHygiene: the token reaches no output and no file of the case, and the script left nothing in TMPDIR.
func (p *publish) checkHygiene(t *testing.T, r result) {
	t.Helper()
	if strings.Contains(r.stdout, p.token) || strings.Contains(r.stderr, p.token) {
		t.Errorf("the token leaked into the output")
	}
	err := filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(b, []byte(p.token)) {
			t.Errorf("the token leaked into %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if left := listDir(t, p.tmp); len(left) > 0 {
		t.Errorf("TMPDIR holds %v after the run: no cleanup", left)
	}
	for _, l := range strings.Split(r.stdout+r.stderr, "\n") {
		if strings.HasPrefix(l, "::") {
			t.Errorf("output line starts a workflow command: %q", l)
		}
	}
}

func (p *publish) gitWiki(t *testing.T, args ...string) string {
	t.Helper()
	return gitIn(t, p.wiki, nil, args...)
}

func (p *publish) wikiHead(t *testing.T) string {
	return p.gitWiki(t, "rev-parse", "refs/heads/master")
}

func (p *publish) wikiFiles(t *testing.T) []string {
	t.Helper()
	out := p.gitWiki(t, "ls-tree", "-z", "--name-only", "refs/heads/master")
	return strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
}

// handEdit commits files to the wiki by hand, as a wiki editor would.
func (p *publish) handEdit(t *testing.T, files map[string]string) {
	t.Helper()
	w := t.TempDir()
	gitIn(t, w, nil, "clone", "-q", p.wiki, "w")
	for name, text := range files {
		writeFile(t, filepath.Join(w, "w"), name, text)
	}
	commitAll(t, filepath.Join(w, "w"), "hand edit", "2026-01-03T00:00:00Z")
	gitIn(t, filepath.Join(w, "w"), nil, "push", "-q", "origin", "master")
}

func (p *publish) called(t *testing.T, sub string) bool {
	t.Helper()
	b, err := os.ReadFile(p.log)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(strings.Split(string(b), "\n"), sub)
}

func wantCode(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
}

func TestPublishFirstRunAndIdempotency(t *testing.T) {
	p := newPublish(t)
	p.setHook(t, `[[ $1 == push ]] || exit 0; cp -a "$TMPDIR" "$FIX_DIR/snapshot"`)
	r := p.run(t)
	wantCode(t, r, 0)
	if !strings.Contains(r.stdout, "wiki: published "+p.sourceSHA[:12]) {
		t.Errorf("stdout %q", r.stdout)
	}
	if !strings.Contains(r.stdout, "wiki: Home.md had no generated banner") {
		t.Errorf("the hand-made Home.md is not reported: %q", r.stdout)
	}
	if snap := listDir(t, filepath.Join(p.dir, "snapshot")); len(snap) == 0 {
		t.Error("the work directory was not under TMPDIR during the run")
	}
	first := p.wikiHead(t)
	if got := p.gitWiki(t, "rev-parse", first+"^"); got != p.initial {
		t.Errorf("parent %s, want the initial wiki commit %s", got, p.initial)
	}
	if got := p.wikiFiles(t); !slices.Equal(got, []string{"Home.md", "_Sidebar.md", "a.md"}) {
		t.Errorf("wiki tree %v", got)
	}
	for _, name := range []string{"Home", "_Sidebar", "a"} {
		if got := p.gitWiki(t, "cat-file", "blob", "master:"+name+".md"); got+"\n" != pageWithBanner(name) {
			t.Errorf("%s.md in the wiki: %q", name, got)
		}
	}
	bot := "github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>"
	if got := p.gitWiki(t, "log", "-1", "--date=raw", "--format=%an <%ae>|%cn <%ce>|%ad|%cd", first); got != bot+"|"+bot+"|"+sourceTime+" +0000|"+sourceTime+" +0000" {
		t.Errorf("identity and dates %q", got)
	}
	msg := p.gitWiki(t, "log", "-1", "--format=%B", first)
	if want := "docs: mirror docs/ at " + p.sourceSHA[:12] + "\n\nHand-made pages replaced or removed (kept in this history):\nHome.md"; msg != want {
		t.Errorf("message %q, want %q", msg, want)
	}

	os.Remove(p.hook)
	r = p.run(t)
	wantCode(t, r, 0)
	if !strings.Contains(r.stdout, "wiki: up to date") || p.wikiHead(t) != first {
		t.Errorf("second run: stdout %q, head %s, want up to date at %s", r.stdout, p.wikiHead(t), first)
	}

	p.gitWiki(t, "update-ref", "refs/heads/master", p.initial)
	wantCode(t, p.run(t), 0)
	if got := p.wikiHead(t); got != first {
		t.Errorf("publishing again from the same wiki made %s, want the same commit %s", got, first)
	}
}

func TestPublishRemovesStalePages(t *testing.T) {
	p := newPublish(t)
	writeFile(t, p.pages, "c.md", pageWithBanner("c"))
	wantCode(t, p.run(t), 0)
	p.handEdit(t, map[string]string{"Foo.md": "hand\n", "Bar.mediawiki": "hand\n", "sub/x.md": "hand\n"})
	if err := os.Remove(filepath.Join(p.pages, "c.md")); err != nil {
		t.Fatal(err)
	}
	r := p.run(t)
	wantCode(t, r, 0)
	if got := p.wikiFiles(t); !slices.Equal(got, []string{"Home.md", "_Sidebar.md", "a.md"}) {
		t.Errorf("wiki tree %v, want the artifact only", got)
	}
	msg := p.gitWiki(t, "log", "-1", "--format=%B", "master")
	for _, name := range []string{"Foo.md", "Bar.mediawiki", "sub/x.md"} {
		if !strings.Contains(msg, "\n"+name) {
			t.Errorf("message lacks %s: %q", name, msg)
		}
	}
	if strings.Contains(msg, "c.md") {
		t.Errorf("message lists the generated c.md as hand-made: %q", msg)
	}
}

func TestPublishQuotesWikiPaths(t *testing.T) {
	p := newPublish(t)
	p.handEdit(t, map[string]string{"x\n::warning::y.md": "hand\n"})
	r := p.run(t)
	wantCode(t, r, 0)
	if !strings.Contains(r.stdout, `wiki: $'x\n::warning::y.md' had no generated banner`) {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestPublishNeverForces(t *testing.T) {
	p := newPublish(t)
	p.setHook(t, `[[ $1 == clone ]] || exit 0
w=$FIX_DIR/compete
"$REAL_GIT" clone -q "$FIX_WIKI" "$w"
echo compete >"$w/Compete.md"
"$REAL_GIT" -C "$w" add -A
"$REAL_GIT" -C "$w" commit -qm compete
"$REAL_GIT" -C "$w" push -q origin HEAD
"$REAL_GIT" -C "$w" rev-parse HEAD >"$FIX_DIR/compete.sha"`)
	r := p.run(t)
	wantCode(t, r, 1)
	if !strings.Contains(r.stderr, "nothing forced") {
		t.Errorf("stderr %q", r.stderr)
	}
	want, _ := os.ReadFile(filepath.Join(p.dir, "compete.sha"))
	if got := p.wikiHead(t); got != strings.TrimSpace(string(want)) {
		t.Errorf("wiki head %s, want the competing commit %s", got, want)
	}
}

// advanceMain is a hook body pushing a new commit to the source's main after the given subcommand.
func advanceMain(after string) string {
	return `[[ $1 == ` + after + ` ]] || exit 0
s=$FIX_DIR/advance
"$REAL_GIT" clone -q -b main "$FIX_SOURCE" "$s"
echo more >"$s/more"
"$REAL_GIT" -C "$s" add -A
"$REAL_GIT" -C "$s" commit -qm more
"$REAL_GIT" -C "$s" push -q origin HEAD:refs/heads/main`
}

func TestPublishStaleRun(t *testing.T) {
	t.Run("main moved before the clone", func(t *testing.T) {
		p := newPublish(t)
		r := p.run(t, "SOURCE_SHA="+strings.Repeat("a", 40))
		wantCode(t, r, 0)
		if !strings.Contains(r.stdout, "the queued run of the newer commit publishes") || p.called(t, "clone") {
			t.Errorf("stdout %q, clone called: %v", r.stdout, p.called(t, "clone"))
		}
		if p.wikiHead(t) != p.initial {
			t.Error("the wiki changed")
		}
	})
	t.Run("main moved after the clone", func(t *testing.T) {
		p := newPublish(t)
		p.setHook(t, advanceMain("clone"))
		r := p.run(t)
		wantCode(t, r, 0)
		if !strings.Contains(r.stdout, "wiki: main moved to") || !strings.Contains(r.stdout, "the queued run of the newer commit publishes") {
			t.Errorf("stdout %q", r.stdout)
		}
		if p.called(t, "push") || p.wikiHead(t) != p.initial {
			t.Error("the stale run pushed")
		}
	})
	t.Run("main moved during the push", func(t *testing.T) {
		p := newPublish(t)
		p.setHook(t, advanceMain("push"))
		r := p.run(t)
		wantCode(t, r, 0)
		if !strings.Contains(r.stdout, "during the publication") || p.wikiHead(t) == p.initial {
			t.Errorf("stdout %q, wiki head %s", r.stdout, p.wikiHead(t))
		}
	})
	t.Run("main unreadable after the push", func(t *testing.T) {
		p := newPublish(t)
		p.setHook(t, `[[ $1 == push ]] || exit 0; mv "$FIX_SOURCE" "$FIX_SOURCE.gone"`)
		r := p.run(t)
		wantCode(t, r, 0)
		if !strings.Contains(r.stdout, "wiki: published") || !strings.Contains(r.stdout, "could not re-read main") || p.wikiHead(t) == p.initial {
			t.Errorf("stdout %q, wiki head %s", r.stdout, p.wikiHead(t))
		}
		if _, err := os.Stat(filepath.Join(p.dir, "hook.ran")); err != nil {
			t.Error("the hook did not run")
		}
	})
}

func TestPublishRefuses(t *testing.T) {
	cases := map[string]struct {
		setup     func(t *testing.T, p *publish)
		overrides []string
		msg       string
	}{
		"no main": {setup: func(t *testing.T, p *publish) {
			gitIn(t, p.source, nil, "update-ref", "-d", "refs/heads/main")
			gitIn(t, p.source, nil, "update-ref", "refs/heads/dev", p.sourceSHA)
		}, msg: "no refs/heads/main"},
		"bad sha":       {overrides: []string{"SOURCE_SHA=abc"}, msg: "bad source sha"},
		"bad time":      {overrides: []string{"SOURCE_TIME=x"}, msg: "bad source time"},
		"long time":     {overrides: []string{"SOURCE_TIME=1234567890123"}, msg: "bad source time"},
		"empty sha":     {overrides: []string{"SOURCE_SHA="}, msg: "bad source sha"},
		"bad name":      {setup: func(t *testing.T, p *publish) { writeFile(t, p.pages, "Bad Name.md", pageWithBanner("x")) }, msg: "unexpected artifact entry"},
		"injected name": {setup: func(t *testing.T, p *publish) { writeFile(t, p.pages, "a\n::error::b.md", pageWithBanner("x")) }, msg: "unexpected artifact entry $'a\\n::error::b.md'"},
		"symlink": {setup: func(t *testing.T, p *publish) {
			if err := os.Symlink("Home.md", filepath.Join(p.pages, "b.md")); err != nil {
				t.Fatal(err)
			}
		}, msg: "unexpected artifact entry b.md"},
		"subdirectory": {setup: func(t *testing.T, p *publish) { writeFile(t, p.pages, "sub/x.md", pageWithBanner("x")) }, msg: "unexpected artifact entry sub"},
		"no banner":    {setup: func(t *testing.T, p *publish) { writeFile(t, p.pages, "b.md", "# hand\n") }, msg: "b.md has no generated banner"},
		"no home":      {setup: func(t *testing.T, p *publish) { os.Remove(filepath.Join(p.pages, "Home.md")) }, msg: "no rendered wiki"},
		"empty": {setup: func(t *testing.T, p *publish) {
			os.RemoveAll(p.pages)
			os.Mkdir(p.pages, 0o755)
		}, msg: "empty artifact"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := newPublish(t)
			if c.setup != nil {
				c.setup(t, p)
			}
			r := p.run(t, c.overrides...)
			wantCode(t, r, 1)
			if !strings.Contains(r.stderr, c.msg) {
				t.Errorf("stderr %q lacks %q", r.stderr, c.msg)
			}
			if p.wikiHead(t) != p.initial || p.called(t, "clone") {
				t.Error("the wiki was cloned or changed")
			}
		})
	}
}

func TestWorkflowPublishIgnoresGitConfig(t *testing.T) {
	run := publishStep(t).Run
	if !regexp.MustCompile(`(?m)^export .*GIT_CONFIG_NOSYSTEM=1`).MatchString(run) ||
		!regexp.MustCompile(`(?m)^export .*GIT_CONFIG_GLOBAL=/dev/null`).MatchString(run) {
		t.Error("Publish does not export GIT_CONFIG_NOSYSTEM=1 and GIT_CONFIG_GLOBAL=/dev/null")
	}
	if !regexp.MustCompile(`wiki_git clone .*--no-checkout`).MatchString(run) {
		t.Error("the wiki clone checks out a working tree")
	}
}

// The runner's system or global git config may define filters (git-lfs registers one); a wiki .gitattributes
// naming them must not run any during the publication.
func TestPublishIgnoresWikiFilters(t *testing.T) {
	p := newPublish(t)
	p.handEdit(t, map[string]string{
		".gitattributes": "* filter=evil\n",
		".lfsconfig":     "[lfs]\n\turl = https://evil.example/\n",
		"Home.md":        "Welcome, filtered\n",
	})
	writeFile(t, p.dir, "evil.gitconfig", "[filter \"evil\"]\n"+
		"\tsmudge = touch \"$FIX_DIR/smudged\"; cat\n\tclean = touch \"$FIX_DIR/cleaned\"; cat\n\trequired = true\n")
	delete(p.env, "GIT_CONFIG_NOSYSTEM")
	p.env["GIT_CONFIG_SYSTEM"] = filepath.Join(p.dir, "evil.gitconfig")
	p.env["GIT_CONFIG_GLOBAL"] = filepath.Join(p.dir, "evil.gitconfig")
	r := p.run(t)
	wantCode(t, r, 0)
	for _, marker := range []string{"smudged", "cleaned"} {
		if _, err := os.Stat(filepath.Join(p.dir, marker)); err == nil {
			t.Errorf("the wiki's filter ran (%s)", marker)
		}
	}
	if got := p.wikiFiles(t); !slices.Equal(got, []string{"Home.md", "_Sidebar.md", "a.md"}) {
		t.Errorf("wiki tree %v, want the artifact only", got)
	}
	msg := p.gitWiki(t, "log", "-1", "--format=%B", "master")
	for _, name := range []string{".gitattributes", ".lfsconfig", "Home.md"} {
		if !strings.Contains(msg, "\n"+name) {
			t.Errorf("message lacks %s: %q", name, msg)
		}
	}
	if got := p.gitWiki(t, "cat-file", "blob", "master:Home.md"); got+"\n" != pageWithBanner("Home") {
		t.Errorf("Home.md in the wiki: %q", got)
	}
}

func TestPublishWikiGitlink(t *testing.T) {
	p := newPublish(t)
	w := filepath.Join(t.TempDir(), "w")
	gitIn(t, filepath.Dir(w), nil, "clone", "-q", p.wiki, "w")
	gitIn(t, w, nil, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("a", 40)+",sub")
	gitIn(t, w, nil, "commit", "-q", "-m", "gitlink")
	gitIn(t, w, nil, "push", "-q", "origin", "master")

	r := p.run(t)
	wantCode(t, r, 0)
	if !strings.Contains(r.stdout, "wiki: sub had no generated banner") {
		t.Errorf("the gitlink is not reported as hand-made: %q", r.stdout)
	}
	if got := p.wikiFiles(t); !slices.Equal(got, []string{"Home.md", "_Sidebar.md", "a.md"}) {
		t.Errorf("wiki tree %v, want the artifact only", got)
	}
	if msg := p.gitWiki(t, "log", "-1", "--format=%B", "master"); !strings.Contains(msg, "\nsub") {
		t.Errorf("message lacks the gitlink: %q", msg)
	}
	r = p.run(t)
	wantCode(t, r, 0)
	if !strings.Contains(r.stdout, "wiki: up to date") {
		t.Errorf("second run: %q", r.stdout)
	}
}

func credentialHelper(t *testing.T) string {
	t.Helper()
	h := publishStep(t).Env["CREDENTIAL_HELPER"]
	body, ok := strings.CutPrefix(h, "!")
	if !ok || strings.Contains(body, "\n") {
		t.Fatalf("CREDENTIAL_HELPER %q: want one line starting with !", h)
	}
	return body
}

func TestCredentialHelper(t *testing.T) {
	sh := lookPath(t, "sh")
	body := credentialHelper(t)
	const ask = "protocol=https\nhost=github.com\npath=o/r.wiki.git\n"
	const answer = "username=x-access-token\npassword=tok\n"
	cases := []struct {
		name, action, stdin, token, path, want string
	}{
		{"wiki", "get", ask, "tok", "o/r.wiki.git", answer},
		{"no final newline", "get", strings.TrimSuffix(ask, "\n"), "tok", "o/r.wiki.git", answer},
		{"then blank line", "get", ask + "\n", "tok", "o/r.wiki.git", answer},
		{"http", "get", "protocol=http\nhost=github.com\npath=o/r.wiki.git\n", "tok", "o/r.wiki.git", ""},
		{"other host", "get", "protocol=https\nhost=evil.example\npath=o/r.wiki.git\n", "tok", "o/r.wiki.git", ""},
		{"suffix host", "get", "protocol=https\nhost=github.com.evil.example\npath=o/r.wiki.git\n", "tok", "o/r.wiki.git", ""},
		{"repository", "get", "protocol=https\nhost=github.com\npath=o/r.git\n", "tok", "o/r.wiki.git", ""},
		{"other wiki", "get", "protocol=https\nhost=github.com\npath=o/x.wiki.git\n", "tok", "o/r.wiki.git", ""},
		{"no path", "get", "protocol=https\nhost=github.com\n", "tok", "o/r.wiki.git", ""},
		{"blank before path", "get", "protocol=https\nhost=github.com\n\npath=o/r.wiki.git\n", "tok", "o/r.wiki.git", ""},
		{"empty token", "get", ask, "", "o/r.wiki.git", ""},
		{"empty path", "get", "protocol=https\nhost=github.com\npath=\n", "tok", "", ""},
		{"store", "store", ask + "username=x\npassword=y\n", "tok", "o/r.wiki.git", ""},
		{"erase", "erase", ask, "tok", "o/r.wiki.git", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(sh, "-c", body+" "+c.action)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "WIKI_TOKEN=" + c.token, "WIKI_PATH=" + c.path}
			cmd.Stdin = strings.NewReader(c.stdin)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("helper: %v", err)
			}
			if string(out) != c.want {
				t.Fatalf("helper answered %q, want %q", out, c.want)
			}
		})
	}
}

func TestCredentialFill(t *testing.T) {
	git := lookPath(t, "git")
	body := credentialHelper(t)
	const token = "tok-fill-7f3a"
	cases := map[string]bool{
		"https://github.com/o/r.wiki.git":   true,
		"https://github.com/o/x.wiki.git":   false,
		"https://github.com/o/r.git":        false,
		"https://evil.example/o/r.wiki.git": false,
		"http://github.com/o/r.wiki.git":    false,
	}
	for url, answers := range cases {
		t.Run(url, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, git, "-c", "credential.helper=", "-c", "credential.helper=!"+body,
				"-c", "credential.useHttpPath=true", "credential", "fill")
			cmd.Dir = t.TempDir()
			cmd.Env = []string{
				"PATH=" + filepath.Dir(git) + ":/usr/bin:/bin", "HOME=" + cmd.Dir, "GIT_CONFIG_GLOBAL=" + os.DevNull,
				"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "WIKI_TOKEN=" + token, "WIKI_PATH=o/r.wiki.git",
			}
			cmd.Stdin = strings.NewReader("url=" + url + "\n\n")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if ctx.Err() != nil {
				t.Fatal("git credential fill hung")
			}
			if answers {
				if err != nil || !strings.Contains(stdout.String(), "password="+token+"\n") {
					t.Fatalf("no password for the wiki: %v %q %q", err, stdout.String(), stderr.String())
				}
				return
			}
			if err == nil {
				t.Errorf("credential fill succeeded for %s: %q", url, stdout.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), token) {
				t.Errorf("the token reached %s: %q %q", url, stdout.String(), stderr.String())
			}
		})
	}
}
