package workflows

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

const workflowDir = "../../.github/workflows"

var workflowNames = []string{"ci.yml", "release.yml", "wiki.yml"}

type workflowFile struct {
	On          map[string]any               `yaml:"on"`
	Permissions map[string]string            `yaml:"permissions"`
	Concurrency map[string]any               `yaml:"concurrency"`
	Defaults    map[string]map[string]string `yaml:"defaults"`
	Jobs        map[string]job               `yaml:"jobs"`
}

type job struct {
	If              string            `yaml:"if"`
	Needs           any               `yaml:"needs"`
	Permissions     map[string]string `yaml:"permissions"`
	Outputs         map[string]string `yaml:"outputs"`
	Env             map[string]string `yaml:"env"`
	RunsOn          any               `yaml:"runs-on"`
	TimeoutMinutes  int               `yaml:"timeout-minutes"`
	Strategy        map[string]any    `yaml:"strategy"`
	Uses            string            `yaml:"uses"`
	With            map[string]any    `yaml:"with"`
	Secrets         any               `yaml:"secrets"`
	ContinueOnError any               `yaml:"continue-on-error"`
	Steps           []step            `yaml:"steps"`
}

type step struct {
	ID              string            `yaml:"id"`
	Name            string            `yaml:"name"`
	Uses            string            `yaml:"uses"`
	If              string            `yaml:"if"`
	Run             string            `yaml:"run"`
	With            map[string]any    `yaml:"with"`
	Env             map[string]string `yaml:"env"`
	ContinueOnError any               `yaml:"continue-on-error"`
}

func load(t *testing.T, name string) (workflowFile, string) {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(workflowDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var wf workflowFile
	if err := yaml.Unmarshal(text, &wf); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return wf, string(text)
}

func stepNamed(t *testing.T, wf workflowFile, jobName, name string) step {
	t.Helper()
	for _, s := range wf.Jobs[jobName].Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no step %q in job %s", name, jobName)
	return step{}
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

// runLines lists the lines of every run of a job, in order.
func runLines(j job) []string {
	var out []string
	for _, s := range j.Steps {
		if s.Run != "" {
			out = append(out, strings.Split(strings.TrimSuffix(s.Run, "\n"), "\n")...)
		}
	}
	return out
}

func needsList(n any) []string {
	switch v := n.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	}
	return nil
}

func lookPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skip(name + " is not installed")
	}
	return p
}

// needGNU skips unless tar and sha256sum are GNU's, as on the Ubuntu runners of the release jobs: the Build and
// Publish fixtures therefore run on Ubuntu only.
func needGNU(t *testing.T) {
	t.Helper()
	for _, c := range [][2]string{{"tar", "GNU tar"}, {"sha256sum", "GNU coreutils"}} {
		out, err := exec.Command(lookPath(t, c[0]), "--version").Output()
		if err != nil || !strings.Contains(string(out), c[1]) {
			t.Skipf("%s is not %s", c[0], c[1])
		}
	}
	lookPath(t, "gzip")
}

func randomToken(t *testing.T) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return "tok-" + hex.EncodeToString(b)
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

// gitEnv is the environment of the fixture's own git calls: no user or system config, a fixed identity.
func gitEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com")
	return append(env, extra...)
}

func gitIn(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgSign=false", "-c", "tag.gpgSign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitEnv(env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	lookPath(t, "git")
	dir := t.TempDir()
	gitIn(t, dir, nil, "-c", "init.defaultBranch=main", "init", "-q")
	return dir
}

// commitAll commits the whole worktree at date and returns the commit id.
func commitAll(t *testing.T, dir, msg, date string) string {
	t.Helper()
	gitIn(t, dir, nil, "add", "-A")
	gitIn(t, dir, []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "-m", msg)
	return gitIn(t, dir, nil, "rev-parse", "HEAD")
}

type result struct {
	code           int
	stdout, stderr string
}

func wantCode(t *testing.T, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, code, r.stdout, r.stderr)
	}
}

// sandbox runs a step's script as the runner does (bash -eo pipefail), with fixture/bin first in PATH, then the
// directories of the tools the test looked up, then /usr/bin:/bin.
type sandbox struct {
	root, bin, tmp, home string
	token                string
	path                 []string
}

func newSandbox(t *testing.T, tools ...string) *sandbox {
	t.Helper()
	root := t.TempDir()
	s := &sandbox{
		root: root, bin: filepath.Join(root, "bin"), tmp: filepath.Join(root, "tmp"), home: filepath.Join(root, "home"),
		token: randomToken(t),
	}
	for _, d := range []string{s.bin, s.tmp, s.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s.path = []string{s.bin}
	for _, name := range append([]string{"bash"}, tools...) {
		if d := filepath.Dir(lookPath(t, name)); !slices.Contains(s.path, d) {
			s.path = append(s.path, d)
		}
	}
	s.path = append(s.path, "/usr/bin", "/bin")
	return s
}

func (s *sandbox) writeBin(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.bin, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (s *sandbox) run(t *testing.T, script, dir string, env map[string]string) result {
	t.Helper()
	file := filepath.Join(s.root, "step.sh")
	if err := os.WriteFile(file, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	all := map[string]string{
		"PATH": strings.Join(s.path, ":"), "HOME": s.home, "TMPDIR": s.tmp,
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
	}
	maps.Copy(all, env)
	var list []string
	for k, v := range all {
		if strings.Contains(v, "${{") {
			t.Fatalf("env %s=%q was not overridden", k, v)
		}
		list = append(list, k+"="+v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, lookPath(t, "bash"), "--noprofile", "--norc", "-eo", "pipefail", file)
	cmd.Dir = dir
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
	s.checkHygiene(t, r)
	return r
}

// checkHygiene: the token reaches no output and no file of the sandbox, the script left nothing in TMPDIR, and
// no output line is a workflow command.
func (s *sandbox) checkHygiene(t *testing.T, r result) {
	t.Helper()
	if strings.Contains(r.stdout, s.token) || strings.Contains(r.stderr, s.token) {
		t.Errorf("the token leaked into the output")
	}
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		b, err := os.ReadFile(path)
		if err == nil && bytes.Contains(b, []byte(s.token)) {
			t.Errorf("the token leaked into %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if left := listDir(t, s.tmp); len(left) > 0 {
		t.Errorf("TMPDIR holds %v after the run: no cleanup", left)
	}
	for _, l := range strings.Split(r.stdout+r.stderr, "\n") {
		if strings.HasPrefix(l, "::") {
			t.Errorf("output line starts a workflow command: %q", l)
		}
	}
}

func TestWorkflowSet(t *testing.T) {
	if got := listDir(t, workflowDir); !slices.Equal(got, workflowNames) {
		t.Errorf("workflows %v, want %v: pin every new workflow here", got, workflowNames)
	}
}

func TestWorkflowActionPins(t *testing.T) {
	pin := regexp.MustCompile(`^\s*(- )?uses: ([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@([0-9a-f]{40} # v\d+\.\d+\.\d+)$`)
	allowed := []string{"actions/checkout", "actions/setup-go", "actions/upload-artifact", "actions/download-artifact"}
	versions := map[string]map[string]bool{}
	local := 0
	for _, name := range workflowNames {
		wf, text := load(t, name)
		for _, l := range strings.Split(text, "\n") {
			if !strings.Contains(l, "uses:") {
				continue
			}
			if strings.TrimSpace(l) == "uses: ./.github/workflows/ci.yml" {
				local++
				if name != "release.yml" || wf.Jobs["ci"].Uses != "./.github/workflows/ci.yml" {
					t.Errorf("%s: local workflow call %q outside release.yml jobs.ci", name, l)
				}
				continue
			}
			m := pin.FindStringSubmatch(l)
			if m == nil {
				t.Errorf("%s: unpinned or uncommented action: %q", name, l)
				continue
			}
			if !slices.Contains(allowed, m[2]) {
				t.Errorf("%s: action %s is not one of %v", name, m[2], allowed)
			}
			if versions[m[2]] == nil {
				versions[m[2]] = map[string]bool{}
			}
			versions[m[2]][m[3]] = true
		}
		for jn, j := range wf.Jobs {
			if j.Uses != "" && (name != "release.yml" || jn != "ci") {
				t.Errorf("%s: job %s calls %s", name, jn, j.Uses)
			}
		}
	}
	if local != 1 {
		t.Errorf("%d local workflow calls, want release.yml jobs.ci only", local)
	}
	for action, v := range versions {
		if len(v) != 1 {
			t.Errorf("%s is pinned at %d versions: %v", action, len(v), slices.Sorted(maps.Keys(v)))
		}
	}
}

func TestWorkflowPermissions(t *testing.T) {
	jobPerms := map[string]map[string]string{
		"release.yml/publish": {"contents": "write"},
		"release.yml/ci":      {"contents": "read"},
		"wiki.yml/publish":    {"contents": "write"},
	}
	for _, name := range workflowNames {
		wf, text := load(t, name)
		if !maps.Equal(wf.Permissions, map[string]string{"contents": "read"}) {
			t.Errorf("%s: permissions %v, want contents: read", name, wf.Permissions)
		}
		for jn, j := range wf.Jobs {
			if want := jobPerms[name+"/"+jn]; !maps.Equal(j.Permissions, want) {
				t.Errorf("%s: job %s permissions %v, want %v", name, jn, j.Permissions, want)
			}
		}
		for _, scope := range []string{"id-token", "attestations", "packages", "actions:"} {
			if strings.Contains(text, scope) {
				t.Errorf("%s mentions %s", name, scope)
			}
		}
	}
}

func TestWorkflowNoExpressionInRun(t *testing.T) {
	runs := 0
	for _, name := range workflowNames {
		wf, _ := load(t, name)
		for jn, j := range wf.Jobs {
			for _, s := range j.Steps {
				if s.Run != "" {
					runs++
				}
				if strings.Contains(s.Run, "${{") {
					t.Errorf("%s: job %s step %q: ${{ }} inside run: pass it through env", name, jn, s.Name)
				}
			}
		}
	}
	if runs < 15 {
		t.Errorf("only %d run steps found: the YAML did not parse as expected", runs)
	}
}

func TestWorkflowTriggersAreSafe(t *testing.T) {
	for _, name := range workflowNames {
		wf, _ := load(t, name)
		for _, bad := range []string{"pull_request_target", "workflow_run", "issue_comment"} {
			if _, ok := wf.On[bad]; ok {
				t.Errorf("%s triggers on %s", name, bad)
			}
		}
	}
}

func TestWorkflowJobsBounded(t *testing.T) {
	for _, name := range workflowNames {
		wf, _ := load(t, name)
		for jn, j := range wf.Jobs {
			if j.ContinueOnError != nil || j.Secrets != nil {
				t.Errorf("%s: job %s continue-on-error %v, secrets %v", name, jn, j.ContinueOnError, j.Secrets)
			}
			if j.Uses == "" && j.TimeoutMinutes <= 0 {
				t.Errorf("%s: job %s has no timeout-minutes", name, jn)
			}
			for _, s := range j.Steps {
				if s.ContinueOnError != nil {
					t.Errorf("%s: job %s step %q continues on error", name, jn, s.Name)
				}
			}
		}
	}
}

func TestWorkflowCheckoutAndGo(t *testing.T) {
	for _, name := range workflowNames {
		wf, _ := load(t, name)
		for jn, j := range wf.Jobs {
			goJob := false
			for _, s := range j.Steps {
				switch {
				case strings.HasPrefix(s.Uses, "actions/checkout@"):
					if fmt.Sprint(s.With["persist-credentials"]) != "false" {
						t.Errorf("%s: job %s checkout with %v, want persist-credentials false", name, jn, s.With)
					}
				case strings.HasPrefix(s.Uses, "actions/setup-go@"):
					goJob = true
					if s.With["go-version-file"] != "go.mod" || fmt.Sprint(s.With["cache"]) != "false" {
						t.Errorf("%s: job %s setup-go with %v, want go-version-file go.mod and cache false", name, jn, s.With)
					}
				}
			}
			if goJob && j.Env["GOTOOLCHAIN"] != "local" {
				t.Errorf("%s: job %s env %v, want GOTOOLCHAIN local", name, jn, j.Env)
			}
		}
	}
}

func TestWorkflowShell(t *testing.T) {
	for _, name := range workflowNames {
		if wf, _ := load(t, name); wf.Defaults["run"]["shell"] != "bash" {
			t.Errorf("%s: defaults %v, want run.shell bash", name, wf.Defaults)
		}
	}
}

var shellSeparators = regexp.MustCompile(`\$\(|[|;&()]`)

// operandBeforeDashDash reports a sed or chmod call whose "--" follows a non-option operand: BSD getopt stops
// at the first operand, so the "--" becomes a file name.
func operandBeforeDashDash(line string) bool {
	for _, seg := range shellSeparators.Split(line, -1) {
		f := strings.Fields(seg)
		for i := range f {
			if f[i] != "sed" && f[i] != "chmod" {
				continue
			}
			operand := false
		args:
			for j := i + 1; j < len(f); j++ {
				switch a := f[j]; {
				case a == "--":
					if operand {
						return true
					}
					break args
				case !operand && strings.HasPrefix(a, "-"):
					if f[i] == "sed" && !strings.HasPrefix(a, "--") && (strings.HasSuffix(a, "e") || strings.HasSuffix(a, "f")) {
						j++
					}
				default:
					operand = true
				}
			}
		}
	}
	return false
}

func TestOperandBeforeDashDash(t *testing.T) {
	cases := map[string]bool{
		`first=$(sed -n 1p -- "$page")`:    true,
		`first=$(sed -n -e 1p -- "$page")`: false,
		`first=$(sed -ne 1p -- "$page")`:   false,
		`sed -n 1p`:                        false,
		`x | sed -n 1p`:                    false,
		`chmod 0755 -- "$f"`:               true,
		`chmod -- 0755 "$f"`:               false,
		`chmod 0755 "$f"`:                  false,
		`sed -e 1p -e 2p -- f`:             false,
		`echo sed -- x`:                    false,
	}
	for line, want := range cases {
		if got := operandBeforeDashDash(line); got != want {
			t.Errorf("operandBeforeDashDash(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestWorkflowPortableDashDash(t *testing.T) {
	for _, name := range workflowNames {
		wf, _ := load(t, name)
		for jn, j := range wf.Jobs {
			for _, l := range runLines(j) {
				if operandBeforeDashDash(l) {
					t.Errorf("%s: job %s: %q puts -- after an operand: BSD sed and chmod read it as a file", name, jn, strings.TrimSpace(l))
				}
			}
		}
	}
}
