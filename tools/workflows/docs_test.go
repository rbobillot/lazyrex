package workflows

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const installLine = "curl -fsSL https://raw.githubusercontent.com/rbobillot/lazyrex/main/install.sh | sh"

// fenceHolds reports whether text has a ```sh fence holding line as one of its lines.
func fenceHolds(text, line string) bool {
	in := false
	for _, l := range strings.Split(text, "\n") {
		switch {
		case !in && l == "```sh":
			in = true
		case in && l == "```":
			in = false
		case in && strings.TrimSpace(l) == line:
			return true
		}
	}
	return false
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDocsInstall(t *testing.T) {
	for path, link := range map[string]string{"../../README.md": "](install.sh)", "../../docs/getting-started.md": "](../install.sh)"} {
		text := readDoc(t, path)
		if !fenceHolds(text, installLine) {
			t.Errorf("%s: no sh fence holds %q", path, installLine)
		}
		for _, want := range []string{link, "LAZYREX_VERSION", "LAZYREX_INSTALL_DIR", goInstallLine} {
			if !strings.Contains(text, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
		if in, gi := strings.Index(text, installLine), strings.Index(text, goInstallLine); in < 0 || gi < in {
			t.Errorf("%s: the install line does not come before go install", path)
		}
	}
	readme := readDoc(t, "../../README.md")
	for _, gone := range []string{"sha256sum --check", "v=0.1.0 os=", "mkdir -p ~/.local/bin"} {
		if strings.Contains(readme, gone) {
			t.Errorf("README.md still holds %q: the manual steps belong to docs/getting-started.md", gone)
		}
	}
}

func TestDocsInstallByHand(t *testing.T) {
	path := "../../docs/getting-started.md"
	text := readDoc(t, path)
	for _, want := range []string{
		`lazyrex_${v}_${os}_${arch}.tar.gz`, "SHA256SUMS", "mkdir -p ~/.local/bin",
		`install "lazyrex_${v}_${os}_${arch}/lazyrex" ~/.local/bin/`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s lacks %q", path, want)
		}
	}
	if mk, in := strings.Index(text, "mkdir -p ~/.local/bin"), strings.Index(text, `install "lazyrex_`); mk < 0 || in < mk {
		t.Errorf("%s: mkdir -p ~/.local/bin does not come before install", path)
	}
	order := []string{installLine, "### By hand", `install "lazyrex_`, "## Run it"}
	last := -1
	for _, s := range order {
		i := strings.Index(text, s)
		if i <= last {
			t.Errorf("%s: %q is missing or out of the order %q", path, s, order)
		}
		last = i
	}
}

func TestDocsInstallVariables(t *testing.T) {
	re := regexp.MustCompile(`LAZYREX_[A-Z_]+`)
	set := func(text string) map[string]bool {
		out := map[string]bool{}
		for _, m := range re.FindAllString(text, -1) {
			out[m] = true
		}
		return out
	}
	script := readDoc(t, installPath)
	want := map[string]bool{"LAZYREX_INSTALL_DIR": true, "LAZYREX_VERSION": true}
	if got := set(script); !maps.Equal(got, want) {
		t.Errorf("install.sh reads %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	if got := set(readDoc(t, "../../docs/getting-started.md")); !maps.Equal(got, want) {
		t.Errorf("docs/getting-started.md names %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	if n := strings.Count(script, "\nrepo=https://github.com/rbobillot/lazyrex\n"); n != 1 {
		t.Errorf("install.sh sets repo=https://github.com/rbobillot/lazyrex %d times, want 1", n)
	}
	usage := false
	for _, l := range strings.Split(script, "\n") {
		if strings.Contains(l, `"usage: `) {
			usage = strings.Contains(l, installLine)
			break
		}
	}
	if !usage {
		t.Errorf("the usage line of install.sh does not hold %q", installLine)
	}
	_, rel, ok := strings.Cut(strings.Fields(installLine)[2], "rbobillot/lazyrex/main/")
	if !ok || !exists("../../"+rel) {
		t.Errorf("%q names no file of the repository", installLine)
	}
}

func TestDocsReleaseRisk(t *testing.T) {
	b, err := os.ReadFile("../../docs/development.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Residual risk, accepted", "tag ruleset"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("docs/development.md lacks %q", want)
		}
	}
}
