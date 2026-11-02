package workflows

import (
	"os"
	"strings"
	"testing"
)

func TestDocsInstallBinary(t *testing.T) {
	for _, path := range []string{"../../README.md", "../../docs/getting-started.md"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
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
