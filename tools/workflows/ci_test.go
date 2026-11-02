package workflows

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func loadCI(t *testing.T) (workflowFile, string) {
	t.Helper()
	return load(t, "ci.yml")
}

func TestCITriggers(t *testing.T) {
	wf, _ := loadCI(t)
	if got := slices.Sorted(maps.Keys(wf.On)); !slices.Equal(got, []string{"pull_request", "push", "workflow_call"}) {
		t.Fatalf("triggers %v", got)
	}
	push, _ := wf.On["push"].(map[string]any)
	if fmt.Sprint(push["branches"]) != "[main]" || len(push) != 1 {
		t.Errorf("push trigger %v, want branches [main] only", push)
	}
	if wf.On["pull_request"] != nil {
		t.Errorf("pull_request trigger %v, want no filter", wf.On["pull_request"])
	}
	call, _ := wf.On["workflow_call"].(map[string]any)
	inputs, _ := call["inputs"].(map[string]any)
	ref, _ := inputs["ref"].(map[string]any)
	if len(call) != 1 || len(inputs) != 1 || ref["type"] != "string" || ref["required"] != false {
		t.Errorf("workflow_call %v, want one input ref {type: string, required: false}", call)
	}
	if d, ok := ref["default"]; ok && d != "" {
		t.Errorf("input ref default %v, want empty", d)
	}
}

func TestCIConcurrency(t *testing.T) {
	wf, _ := loadCI(t)
	if wf.Concurrency["group"] != "ci-${{ github.workflow }}-${{ github.event.pull_request.number || github.sha }}" ||
		wf.Concurrency["cancel-in-progress"] != "${{ github.event_name == 'pull_request' }}" || len(wf.Concurrency) != 2 {
		t.Errorf("concurrency %v", wf.Concurrency)
	}
}

func TestCIJobs(t *testing.T) {
	wf, _ := loadCI(t)
	if got := slices.Sorted(maps.Keys(wf.Jobs)); !slices.Equal(got, []string{"cross", "harness", "lint", "test"}) {
		t.Fatalf("jobs %v", got)
	}
	test := wf.Jobs["test"]
	matrix, _ := test.Strategy["matrix"].(map[string]any)
	if test.Strategy["fail-fast"] != false || fmt.Sprint(matrix["os"]) != "[ubuntu-latest macos-latest]" || len(matrix) != 1 {
		t.Errorf("test strategy %v", test.Strategy)
	}
	if test.RunsOn != "${{ matrix.os }}" {
		t.Errorf("test runs-on %v", test.RunsOn)
	}
}

func TestCITestCommands(t *testing.T) {
	wf, _ := loadCI(t)
	var got []string
	for _, l := range runLines(wf.Jobs["test"]) {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "go "):
			got = append(got, l)
		case strings.Contains(l, "gofmt -l"):
			got = append(got, "gofmt -l .")
			if l != "unformatted=$(gofmt -l .)" {
				t.Errorf("gofmt line %q", l)
			}
		}
	}
	want := []string{
		"gofmt -l .", "go mod verify", "go mod tidy -diff", "go vet ./...", "go vet -tags smoke ./...",
		"go test ./...", "go test -race ./...",
	}
	if !slices.Equal(got, want) {
		t.Errorf("test commands:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCINeverRunsSmoke(t *testing.T) {
	_, text := loadCI(t)
	if regexp.MustCompile(`go test.*smoke`).MatchString(text) || strings.Contains(text, "LAZYREX_SMOKE") {
		t.Error("ci.yml runs the smoke test")
	}
}

func TestCICheckouts(t *testing.T) {
	wf, _ := loadCI(t)
	n := 0
	for jn, j := range wf.Jobs {
		for _, s := range j.Steps {
			if !strings.HasPrefix(s.Uses, "actions/checkout@") {
				continue
			}
			n++
			if s.With["ref"] != "${{ inputs.ref }}" {
				t.Errorf("job %s checkout ref %v, want ${{ inputs.ref }}", jn, s.With["ref"])
			}
		}
	}
	if n != 4 {
		t.Errorf("%d checkouts, want one per job", n)
	}
	if c := stepUsing(t, wf.Jobs["test"], "actions/checkout"); fmt.Sprint(c.With["fetch-depth"]) != "0" {
		t.Errorf("test checkout with %v, want fetch-depth 0", c.With)
	}
}

// loopTargets returns the words of the first line of lines matching re.
func loopTargets(t *testing.T, lines []string, re string) []string {
	t.Helper()
	pattern := regexp.MustCompile(re)
	for _, l := range lines {
		if m := pattern.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			return strings.Fields(m[1])
		}
	}
	t.Fatalf("no line matches %s", re)
	return nil
}

func TestCICrossTargets(t *testing.T) {
	ci, _ := loadCI(t)
	release, _ := load(t, "release.yml")
	cross := loopTargets(t, runLines(ci.Jobs["cross"]), `^for target in ([^;]*); do$`)
	built := loopTargets(t, runLines(release.Jobs["build"]), `^targets=\(([^)]*)\)$`)
	if want := append(slices.Clone(built), "windows/amd64"); !slices.Equal(cross, want) {
		t.Errorf("cross vets %v, want the release targets plus windows/amd64: %v", cross, want)
	}
	if !slices.ContainsFunc(runLines(ci.Jobs["cross"]), func(l string) bool { return strings.Contains(l, "CGO_ENABLED=0 go vet ./...") }) {
		t.Error("cross does not run CGO_ENABLED=0 go vet ./...")
	}
}

func TestCIHarness(t *testing.T) {
	wf, _ := loadCI(t)
	j := wf.Jobs["harness"]
	var got []string
	for _, l := range runLines(j) {
		got = append(got, strings.TrimSpace(l))
	}
	if !slices.Equal(got, []string{"scripts/harness.sh check", "scripts/test/harness.sh"}) {
		t.Errorf("harness runs %q", got)
	}
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "actions/setup-go@") {
			t.Error("the harness job sets up Go")
		}
	}
}

func TestCIFormatScript(t *testing.T) {
	s := newSandbox(t, "gofmt")
	wf, _ := loadCI(t)
	run := stepNamed(t, wf, "test", "Format").Run
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\nfunc  F( ) {}\n")
	writeFile(t, dir, "b.go", "package a\n\nfunc G() {}\n")
	r := s.run(t, run, dir, nil)
	wantCode(t, r, 1)
	if !strings.Contains(r.stderr, "a.go") || strings.Contains(r.stderr, "b.go") {
		t.Errorf("stderr %q, want a.go named and b.go not", r.stderr)
	}
	writeFile(t, dir, "a.go", "package a\n\nfunc F() {}\n")
	wantCode(t, s.run(t, run, dir, nil), 0)
}

func TestCILint(t *testing.T) {
	wf, _ := loadCI(t)
	j := wf.Jobs["lint"]
	line := regexp.MustCompile(`^go run github.com/rhysd/actionlint/cmd/actionlint@v\d+\.\d+\.\d+ -shellcheck= -pyflakes=$`)
	var got []string
	for _, l := range runLines(j) {
		got = append(got, strings.TrimSpace(l))
	}
	if len(got) != 1 || !line.MatchString(got[0]) {
		t.Errorf("lint runs %q, want one pinned actionlint line", got)
	}
	stepUsing(t, j, "actions/setup-go")
}
