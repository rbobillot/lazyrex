package workflows

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	installPath   = "../../install.sh"
	releasesURL   = "https://github.com/rbobillot/lazyrex/releases"
	goInstallLine = "go install github.com/rbobillot/lazyrex/cmd/lazyrex@latest"
	curlFlags     = "--proto =https --proto-redir =https --tlsv1.2 -fsSL -o"
	// unset as an env value removes the variable from the script's environment.
	unset = "\x00unset"
)

// fakeCurl serves $FIX_DIR/releases/<tag>/<file> for releases/download/<tag>/<file>, and the files of
// $FAKE_LATEST (default v0.1.0) for releases/latest/download/<file>. It logs each call to $FIX_DIR/curl.log,
// every argument NUL-terminated and a newline after the last, and exits 22 like curl -f on a missing file or
// on $FAKE_FAIL_URL. On $FAKE_BLOCK_URL it creates $FIX_DIR/blocked, then waits for $FIX_DIR/go.
const fakeCurl = `#!/bin/sh
for a in "$@"; do printf '%s\0' "$a"; done >>"$FIX_DIR/curl.log"; printf '\n' >>"$FIX_DIR/curl.log"
[ $# = 9 ] && [ "$1 $2 $3 $4 $5 $6 $7" = "` + curlFlags + `" ] || { echo "fake curl: unexpected $*" >&2; exit 2; }
out=$8 url=$9 base=` + releasesURL + `
if [ "$url" = "${FAKE_BLOCK_URL-}" ]; then
  : >"$FIX_DIR/blocked"
  while [ ! -e "$FIX_DIR/go" ]; do sleep 0.1; done
fi
if [ "$url" = "${FAKE_FAIL_URL-}" ]; then echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; fi
case $url in
"$base"/latest/download/*) f=$FIX_DIR/releases/${FAKE_LATEST:-v0.1.0}/${url#"$base"/latest/download/} ;;
"$base"/download/*) f=$FIX_DIR/releases/${url#"$base"/download/} ;;
*) echo "fake curl: unexpected url $url" >&2; exit 2 ;;
esac
[ -f "$f" ] || { echo "curl: (22) The requested URL returned error: 404" >&2; exit 22; }
cp "$f" "$out"
`

const fakeUname = `#!/bin/sh
printf '%s\n' "$*" >>"$FIX_DIR/uname.log"
case $* in -s) printf '%s\n' "${FAKE_UNAME_S:-Linux}" ;; -m) printf '%s\n' "${FAKE_UNAME_M:-x86_64}" ;; *) exit 2 ;; esac
`

const fakeSysctl = `#!/bin/sh
printf '%s\n' "$*" >>"$FIX_DIR/sysctl.log"
[ "$*" = "-n hw.optional.arm64" ] || exit 2
[ -n "${FAKE_ARM64-}" ] || { echo "sysctl: unknown oid 'hw.optional.arm64'" >&2; exit 1; }
printf '%s\n' "$FAKE_ARM64"
`

// fakeChmod creates $FIX_DIR/blocked, waits for $FIX_DIR/go, then runs $REAL_CHMOD.
const fakeChmod = `#!/bin/sh
: >"$FIX_DIR/blocked"
while [ ! -e "$FIX_DIR/go" ]; do sleep 0.1; done
exec "$REAL_CHMOD" "$@"
`

type program struct{ name, path string }

// shells lists the shells a piped install.sh may meet: sh, dash and bash when distinct, then busybox and zsh
// run under the name sh, when installed. sh is required.
func shells(t *testing.T) []program {
	t.Helper()
	var out []program
	seen := map[string]bool{}
	for _, name := range []string{"sh", "dash", "bash"} {
		p, err := exec.LookPath(name)
		if err != nil {
			if name == "sh" {
				t.Fatal("sh is not installed")
			}
			continue
		}
		key, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		// bash run as sh is in POSIX mode, unlike bash run as bash.
		if name == "sh" && strings.Contains(filepath.Base(key), "bash") {
			key += " as sh"
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, program{name, p})
		}
	}
	dir := t.TempDir()
	for _, name := range []string{"busybox", "zsh"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		// Both behave as a POSIX sh only when run under the name sh.
		link := filepath.Join(dir, name, "sh")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(p, link); err != nil {
			t.Fatal(err)
		}
		if exec.Command(link, "-c", ":").Run() == nil {
			out = append(out, program{name + "-as-sh", link})
		}
	}
	return out
}

// tars lists one tar of each kind installed: GNU tar and bsdtar.
func tars(t *testing.T) []program {
	t.Helper()
	var out []program
	kinds := map[string]bool{}
	for _, name := range []string{"tar", "gtar", "bsdtar"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		v, err := exec.Command(p, "--version").Output()
		if err != nil {
			continue
		}
		kind := "other-tar"
		switch {
		case strings.Contains(string(v), "GNU tar"):
			kind = "gnu-tar"
		case strings.Contains(string(v), "bsdtar"):
			kind = "bsdtar"
		}
		if !kinds[kind] {
			kinds[kind] = true
			out = append(out, program{kind, p})
		}
	}
	if len(out) == 0 {
		t.Skip("tar is not installed")
	}
	return out
}

func installScript(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(installPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An install fixture: fakes of curl, uname and sysctl in s.bin, a tools directory of symlinks to the real tools
// the script may use, and the v0.1.0 release under s.root/releases. The script's PATH is s.bin, then tools.
type installFix struct {
	s     *sandbox
	tools string
}

func newInstall(t *testing.T) *installFix {
	t.Helper()
	s := newSandbox(t)
	f := &installFix{s: s, tools: filepath.Join(s.root, "tools")}
	if err := os.Mkdir(f.tools, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tar", "gzip", "mkdir", "mktemp", "rm", "chmod", "mv", "cp", "sleep"} {
		f.link(t, name, lookPath(t, name))
	}
	hashers := 0
	for _, name := range []string{"sha256sum", "shasum"} {
		if p, err := exec.LookPath(name); err == nil {
			f.link(t, name, p)
			hashers++
		}
	}
	if hashers == 0 {
		t.Skip("neither sha256sum nor shasum is installed")
	}
	s.writeBin(t, "curl", fakeCurl)
	s.writeBin(t, "uname", fakeUname)
	s.writeBin(t, "sysctl", fakeSysctl)
	f.release(t, "v0.1.0")
	return f
}

func (f *installFix) link(t *testing.T, name, target string) {
	t.Helper()
	f.unlink(t, name)
	if err := os.Symlink(target, filepath.Join(f.tools, name)); err != nil {
		t.Fatal(err)
	}
}

func (f *installFix) unlink(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(f.tools, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func (f *installFix) defaultDir() string { return filepath.Join(f.s.home, ".local", "bin") }

type tarEntry struct {
	name string
	typ  byte
	mode int64
	body string // the target of a link
}

func tarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: e.mode, ModTime: time.Unix(1767225600, 0)}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		} else {
			h.Linkname = e.body
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func stub(tag, target string) string { return "#!/bin/sh\necho stub " + tag + " " + target + "\n" }

func stubEntries(top, body string) []tarEntry {
	return []tarEntry{
		{top + "/", tar.TypeDir, 0o755, ""},
		{top + "/lazyrex", tar.TypeReg, 0o755, body},
		{top + "/LICENSE", tar.TypeReg, 0o644, "Apache License\n"},
		{top + "/README.md", tar.TypeReg, 0o644, "# lazyrex\n"},
	}
}

func (f *installFix) archivePath(tag, osArch string) string {
	return filepath.Join(f.s.root, "releases", tag, "lazyrex_"+strings.TrimPrefix(tag, "v")+"_"+osArch+".tar.gz")
}

func (f *installFix) sumsPath(tag string) string {
	return filepath.Join(f.s.root, "releases", tag, "SHA256SUMS")
}

// release writes the four archives of tag, each holding a stub naming tag and its target, and SHA256SUMS in
// the format of release.yml.
func (f *installFix) release(t *testing.T, tag string) {
	t.Helper()
	v := strings.TrimPrefix(tag, "v")
	for _, target := range releaseTargets {
		osArch := target[0] + "_" + target[1]
		f.writeArchive(t, tag, osArch, stubEntries("lazyrex_"+v+"_"+osArch, stub(tag, target[0]+"/"+target[1])))
	}
	f.resum(t, tag)
}

func (f *installFix) writeArchive(t *testing.T, tag, osArch string, entries []tarEntry) {
	t.Helper()
	p := f.archivePath(tag, osArch)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, tarGz(t, entries), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *installFix) resum(t *testing.T, tag string) {
	t.Helper()
	var sums strings.Builder
	for _, n := range archiveNames(strings.TrimPrefix(tag, "v")) {
		p := filepath.Join(f.s.root, "releases", tag, n)
		if exists(p) {
			fmt.Fprintf(&sums, "%s  %s\n", sha256File(t, p), n)
		}
	}
	if err := os.WriteFile(f.sumsPath(tag), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *installFix) editSums(t *testing.T, tag string, edit func(string) string) {
	t.Helper()
	before := readFile(t, f.sumsPath(tag))
	after := edit(before)
	if after == before {
		t.Fatal("the SHA256SUMS edit changed nothing")
	}
	if err := os.WriteFile(f.sumsPath(tag), []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sumsLine is the SHA256SUMS line of tag naming the archive of osArch, without its newline.
func (f *installFix) sumsLine(t *testing.T, tag, osArch string) string {
	t.Helper()
	suffix := "  lazyrex_" + strings.TrimPrefix(tag, "v") + "_" + osArch + ".tar.gz"
	for _, l := range strings.Split(readFile(t, f.sumsPath(tag)), "\n") {
		if strings.HasSuffix(l, suffix) {
			return l
		}
	}
	t.Fatalf("SHA256SUMS of %s has no line for %s", tag, osArch)
	return ""
}

func (f *installFix) command(ctx context.Context, sh program, script []byte, env map[string]string, args []string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	all := map[string]string{
		"PATH": f.s.bin + ":" + f.tools, "HOME": f.s.home, "TMPDIR": f.s.tmp, "FIX_DIR": f.s.root,
	}
	for k, v := range env {
		if v == unset {
			delete(all, k)
		} else {
			all[k] = v
		}
	}
	var list []string
	for k, v := range all {
		list = append(list, k+"="+v)
	}
	var argv []string
	if len(args) > 0 {
		argv = append([]string{"-s", "--"}, args...)
	}
	cmd := exec.CommandContext(ctx, sh.path, argv...)
	cmd.Dir = f.s.root
	cmd.Env = list
	cmd.Stdin = bytes.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cmd, &stdout, &stderr
}

func (f *installFix) finish(t *testing.T, err error, stdout, stderr *bytes.Buffer) result {
	t.Helper()
	r := result{0, stdout.String(), stderr.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	f.s.checkHygiene(t, r)
	return r
}

// pipe runs script as curl ... | SHELL [-s -- args] does: the shell reads it on stdin, with a clean environment
// of PATH, HOME, TMPDIR and FIX_DIR, then env.
func (f *installFix) pipe(t *testing.T, sh program, script []byte, env map[string]string, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd, stdout, stderr := f.command(ctx, sh, script, env, args)
	return f.finish(t, cmd.Run(), stdout, stderr)
}

func (f *installFix) curlCalls(t *testing.T) [][]string {
	t.Helper()
	var out [][]string
	for _, rec := range strings.Split(readFile(t, filepath.Join(f.s.root, "curl.log")), "\x00\n") {
		if rec != "" {
			out = append(out, strings.Split(rec, "\x00"))
		}
	}
	return out
}

// wantCurl checks that curl was called once per URL, in order, each time with the hardening flags and an output
// file of the URL's name in the script's temporary directory.
func (f *installFix) wantCurl(t *testing.T, urls ...string) {
	t.Helper()
	var got []string
	for _, c := range f.curlCalls(t) {
		if len(c) != 9 || strings.Join(c[:7], " ") != curlFlags {
			t.Errorf("curl %q, want %s FILE URL", c, curlFlags)
			continue
		}
		out := regexp.MustCompile(`^` + regexp.QuoteMeta(f.s.tmp) + `/lazyrex-install\.[^/]{6}/` + regexp.QuoteMeta(path.Base(c[8])) + `$`)
		if !out.MatchString(c[7]) {
			t.Errorf("curl -o %s, want a file named %s in a lazyrex-install.XXXXXX directory of TMPDIR", c[7], path.Base(c[8]))
		}
		got = append(got, c[8])
	}
	if !slices.Equal(got, urls) {
		t.Errorf("curl URLs %q, want %q", got, urls)
	}
}

func (f *installFix) wantNoLog(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if exists(filepath.Join(f.s.root, n)) {
			t.Errorf("%s exists: %q", n, readFile(t, filepath.Join(f.s.root, n)))
		}
	}
}

func wantNoStaged(t *testing.T, dir string) {
	t.Helper()
	if !exists(dir) {
		return
	}
	for _, n := range listDir(t, dir) {
		if strings.HasPrefix(n, ".lazyrex.") {
			t.Errorf("%s holds the staged file %s", dir, n)
		}
	}
}

func wantInstalled(t *testing.T, dir, tag, target string) {
	t.Helper()
	p := filepath.Join(dir, "lazyrex")
	if got, want := readFile(t, p), stub(tag, target); got != want {
		t.Errorf("%s holds %q, want %q", p, got, want)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("%s: stat %v, err %v, want mode 0755", p, fi, err)
	}
	wantNoStaged(t, dir)
}

// wantNothingDone checks a refusal: exit 1, msg on stderr, and no default install directory.
func (f *installFix) wantNothingDone(t *testing.T, r result, msg string) {
	t.Helper()
	wantCode(t, r, 1)
	if !strings.Contains(r.stderr, msg) {
		t.Errorf("stderr %q lacks %q", r.stderr, msg)
	}
	if exists(filepath.Join(f.s.home, ".local")) {
		t.Errorf("%s/.local exists after a refusal", f.s.home)
	}
}

func TestInstallPlatforms(t *testing.T) {
	script := installScript(t)
	const sysctl = "-n hw.optional.arm64\n"
	cases := []struct{ kernel, machine, arm64, target, sysctl string }{
		{"Linux", "x86_64", unset, "linux/amd64", ""},
		{"Linux", "amd64", unset, "linux/amd64", ""},
		{"Linux", "aarch64", unset, "linux/arm64", ""},
		{"Linux", "arm64", unset, "linux/arm64", ""},
		{"Darwin", "arm64", unset, "darwin/arm64", ""},
		{"Darwin", "x86_64", unset, "darwin/amd64", sysctl},
		{"Darwin", "x86_64", "0", "darwin/amd64", sysctl},
		{"Darwin", "x86_64", "1", "darwin/arm64", sysctl},
	}
	covered, want := map[string]bool{}, map[string]bool{}
	for _, c := range cases {
		covered[c.target] = true
	}
	for _, target := range releaseTargets {
		want[target[0]+"/"+target[1]] = true
	}
	if !maps.Equal(covered, want) {
		t.Errorf("cases cover %v, want the release targets %v", slices.Sorted(maps.Keys(covered)), slices.Sorted(maps.Keys(want)))
	}
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			for _, c := range cases {
				t.Run(c.kernel+"_"+c.machine+"_arm64="+strings.TrimPrefix(c.arm64, unset), func(t *testing.T) {
					t.Parallel()
					f := newInstall(t)
					r := f.pipe(t, sh, script, map[string]string{"FAKE_UNAME_S": c.kernel, "FAKE_UNAME_M": c.machine, "FAKE_ARM64": c.arm64})
					wantCode(t, r, 0)
					dir := f.defaultDir()
					wantInstalled(t, dir, "v0.1.0", c.target)
					if msg := "installed lazyrex v0.1.0 (" + c.target + ") to " + dir + "/lazyrex"; !strings.Contains(r.stdout, msg) {
						t.Errorf("stdout %q lacks %q", r.stdout, msg)
					}
					osArch := strings.Replace(c.target, "/", "_", 1)
					f.wantCurl(t, releasesURL+"/latest/download/SHA256SUMS", releasesURL+"/download/v0.1.0/lazyrex_0.1.0_"+osArch+".tar.gz")
					if got := readFile(t, filepath.Join(f.s.root, "uname.log")); got != "-s\n-m\n" {
						t.Errorf("uname calls %q, want -s then -m", got)
					}
					if got := readFile(t, filepath.Join(f.s.root, "sysctl.log")); got != c.sysctl {
						t.Errorf("sysctl calls %q, want %q", got, c.sysctl)
					}
				})
			}
		})
	}
}

func TestInstallRefusesPlatform(t *testing.T) {
	script := installScript(t)
	cases := []struct{ kernel, machine, msg string }{
		{"MINGW64_NT-10.0-19045", "x86_64", "no lazyrex binary for Windows"},
		{"MSYS_NT-10.0", "x86_64", "no lazyrex binary for Windows"},
		{"CYGWIN_NT-10.0", "x86_64", "no lazyrex binary for Windows"},
		{"FreeBSD", "amd64", "no lazyrex binary for FreeBSD (Linux and macOS only)"},
		{"Linux", "armv7l", "no lazyrex binary for linux/armv7l (amd64 and arm64 only)"},
		{"Linux", "i686", "no lazyrex binary for linux/i686"},
		{"Darwin", "riscv64", "no lazyrex binary for darwin/riscv64"},
	}
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			for _, c := range cases {
				t.Run(c.kernel+"_"+c.machine, func(t *testing.T) {
					t.Parallel()
					f := newInstall(t)
					r := f.pipe(t, sh, script, map[string]string{"FAKE_UNAME_S": c.kernel, "FAKE_UNAME_M": c.machine})
					f.wantNothingDone(t, r, c.msg)
					if !strings.Contains(r.stderr, goInstallLine) {
						t.Errorf("stderr %q does not name %q", r.stderr, goInstallLine)
					}
					f.wantNoLog(t, "curl.log")
				})
			}
		})
	}
}

func TestInstallVersion(t *testing.T) {
	script := installScript(t)
	cases := []struct{ name, version, tag string }{
		{"pinned", "v0.1.0", "v0.1.0"},
		{"without v", "0.1.0", "v0.1.0"},
		{"release candidate", "v0.2.0-rc.1", "v0.2.0-rc.1"},
		{"empty", "", "v0.1.0"},
	}
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					t.Parallel()
					f := newInstall(t)
					if c.tag != "v0.1.0" {
						f.release(t, c.tag)
					}
					r := f.pipe(t, sh, script, map[string]string{"LAZYREX_VERSION": c.version})
					wantCode(t, r, 0)
					wantInstalled(t, f.defaultDir(), c.tag, "linux/amd64")
					if msg := "installed lazyrex " + c.tag + " (linux/amd64)"; !strings.Contains(r.stdout, msg) {
						t.Errorf("stdout %q lacks %q", r.stdout, msg)
					}
					sums := releasesURL + "/download/" + c.tag + "/SHA256SUMS"
					if c.version == "" {
						sums = releasesURL + "/latest/download/SHA256SUMS"
					}
					f.wantCurl(t, sums, releasesURL+"/download/"+c.tag+"/lazyrex_"+strings.TrimPrefix(c.tag, "v")+"_linux_amd64.tar.gz")
				})
			}
		})
	}
}

func TestInstallRefusesVersion(t *testing.T) {
	script := installScript(t)
	bad := []string{
		"v0.1", "v01.0.0", "v0.1.0-beta.1", "v0.1.0-rc.0", "v0.1.0-rc.01", "v0.1.0+m", "v0.1.0 ", "v1.2.3;id",
		"vv0.1.0", "latest", "../v0.1.0", "v0.1.0/x", "v0.1.0\nx", "v0.1.0-rc.1-rc.2",
	}
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			for _, v := range bad {
				t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
					t.Parallel()
					f := newInstall(t)
					r := f.pipe(t, sh, script, map[string]string{"LAZYREX_VERSION": v})
					f.wantNothingDone(t, r, "is not vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N")
					f.wantNoLog(t, "curl.log")
				})
			}
		})
	}
}

func TestInstallLatest(t *testing.T) {
	script := installScript(t)
	latest := releasesURL + "/latest/download/SHA256SUMS"
	replaceLine := func(t *testing.T, f *installFix, name string) {
		line := f.sumsLine(t, "v0.1.0", "linux_amd64")
		hash, _, _ := strings.Cut(line, "  ")
		f.editSums(t, "v0.1.0", func(s string) string { return strings.Replace(s, line, hash+"  "+name, 1) })
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, f *installFix) map[string]string
		msg   string // empty: the install succeeds
		calls int
	}{
		{"version from SHA256SUMS", func(t *testing.T, f *installFix) map[string]string {
			f.release(t, "v0.3.0")
			return map[string]string{"FAKE_LATEST": "v0.3.0"}
		}, "", 2},
		{"no release", func(t *testing.T, f *installFix) map[string]string {
			return map[string]string{"FAKE_FAIL_URL": latest}
		}, "could not download " + latest, 1},
		{"no archive for the target", func(t *testing.T, f *installFix) map[string]string {
			line := f.sumsLine(t, "v0.1.0", "linux_amd64")
			f.editSums(t, "v0.1.0", func(s string) string { return strings.Replace(s, line+"\n", "", 1) })
			return nil
		}, "lists 0 archives for linux/amd64, want 1", 1},
		{"two archives for the target", func(t *testing.T, f *installFix) map[string]string {
			f.editSums(t, "v0.1.0", func(s string) string {
				return s + strings.Repeat("0", 64) + "  lazyrex_0.0.9_linux_amd64.tar.gz\n"
			})
			return nil
		}, "lists 2 archives", 1},
		{"short version", func(t *testing.T, f *installFix) map[string]string {
			replaceLine(t, f, "lazyrex_0.1_linux_amd64.tar.gz")
			return nil
		}, "names an archive of a bad version", 1},
		{"injection", func(t *testing.T, f *installFix) map[string]string {
			replaceLine(t, f, "lazyrex_0.1.0;id_linux_amd64.tar.gz")
			return nil
		}, "names an archive of a bad version", 1},
		{"traversal", func(t *testing.T, f *installFix) map[string]string {
			replaceLine(t, f, "lazyrex_x/../0.1.0_linux_amd64.tar.gz")
			return nil
		}, "names an archive of a bad version", 1},
		{"no final newline", func(t *testing.T, f *installFix) map[string]string {
			f.editSums(t, "v0.1.0", func(s string) string { return strings.TrimSuffix(s, "\n") })
			return nil
		}, "", 2},
		{"another prefix", func(t *testing.T, f *installFix) map[string]string {
			f.editSums(t, "v0.1.0", func(s string) string {
				return strings.Repeat("0", 64) + "  evil_lazyrex_0.1.0_linux_amd64.tar.gz\n" + s
			})
			return nil
		}, "", 2},
	}
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					t.Parallel()
					f := newInstall(t)
					r := f.pipe(t, sh, script, c.setup(t, f))
					if c.msg != "" {
						f.wantNothingDone(t, r, c.msg)
						if n := len(f.curlCalls(t)); n != c.calls {
							t.Errorf("%d curl calls, want %d", n, c.calls)
						}
						return
					}
					wantCode(t, r, 0)
					tag := "v0.1.0"
					if c.name == "version from SHA256SUMS" {
						tag = "v0.3.0"
					}
					wantInstalled(t, f.defaultDir(), tag, "linux/amd64")
					if msg := "installed lazyrex " + tag; !strings.Contains(r.stdout, msg) {
						t.Errorf("stdout %q lacks %q", r.stdout, msg)
					}
					f.wantCurl(t, latest, releasesURL+"/download/"+tag+"/lazyrex_"+strings.TrimPrefix(tag, "v")+"_linux_amd64.tar.gz")
				})
			}
		})
	}
}

func TestInstallChecksum(t *testing.T) {
	script := installScript(t)
	const top = "lazyrex_0.1.0_linux_amd64"
	archive := releasesURL + "/download/v0.1.0/" + top + ".tar.gz"
	good := stub("v0.1.0", "linux/amd64")
	member := func(typ byte, body string) tarEntry {
		mode := int64(0o755)
		if typ == tar.TypeSymlink {
			mode = 0o777
		}
		return tarEntry{top + "/lazyrex", typ, mode, body}
	}
	without := func(entries []tarEntry, name string) []tarEntry {
		return slices.DeleteFunc(slices.Clone(entries), func(e tarEntry) bool { return e.name == name })
	}
	// rewrite replaces the member lazyrex of the linux/amd64 archive by members, its checksum kept valid.
	rewrite := func(t *testing.T, f *installFix, members ...tarEntry) {
		entries := without(stubEntries(top, good), top+"/lazyrex")
		f.writeArchive(t, "v0.1.0", "linux_amd64", append(entries, members...))
		f.resum(t, "v0.1.0")
	}
	editHash := func(t *testing.T, f *installFix, edit func(string) string) {
		line := f.sumsLine(t, "v0.1.0", "linux_amd64")
		hash, name, _ := strings.Cut(line, "  ")
		f.editSums(t, "v0.1.0", func(s string) string { return strings.Replace(s, line, edit(hash)+"  "+name, 1) })
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, f *installFix, victim string) map[string]string
		msg   string // empty: the install succeeds
		calls int
	}{
		{"archive changed", func(t *testing.T, f *installFix, _ string) map[string]string {
			f.writeArchive(t, "v0.1.0", "linux_amd64", stubEntries(top, "tampered\n"))
			return nil
		}, top + ".tar.gz does not match SHA256SUMS of v0.1.0: nothing installed", 2},
		{"short hash", func(t *testing.T, f *installFix, _ string) map[string]string {
			editHash(t, f, func(h string) string { return h[:63] })
			return nil
		}, "has no valid checksum for", 1},
		{"uppercase hash", func(t *testing.T, f *installFix, _ string) map[string]string {
			editHash(t, f, strings.ToUpper)
			return nil
		}, "has no valid checksum for", 1},
		{"one space", func(t *testing.T, f *installFix, _ string) map[string]string {
			f.editSums(t, "v0.1.0", func(s string) string { return strings.Replace(s, "  "+top, " "+top, 1) })
			return nil
		}, "lists 0 archives", 1},
		{"archive download fails", func(t *testing.T, f *installFix, _ string) map[string]string {
			return map[string]string{"FAKE_FAIL_URL": archive}
		}, "could not download " + archive, 2},
		{"no member", func(t *testing.T, f *installFix, _ string) map[string]string {
			rewrite(t, f)
			return nil
		}, "could not extract " + top + "/lazyrex", 2},
		{"symlink", func(t *testing.T, f *installFix, victim string) map[string]string {
			rewrite(t, f, member(tar.TypeSymlink, victim))
			return nil
		}, "holds no file " + top + "/lazyrex", 2},
		{"directory", func(t *testing.T, f *installFix, _ string) map[string]string {
			rewrite(t, f, tarEntry{top + "/lazyrex/", tar.TypeDir, 0o755, ""})
			return nil
		}, "holds no file " + top + "/lazyrex", 2},
		{"hard link to a file of the temporary directory", func(t *testing.T, f *installFix, _ string) map[string]string {
			rewrite(t, f, member(tar.TypeLink, "SHA256SUMS"))
			return nil
		}, "could not extract " + top + "/lazyrex", 2},
		{"hard link to an absolute path", func(t *testing.T, f *installFix, victim string) map[string]string {
			rewrite(t, f, member(tar.TypeLink, victim))
			return nil
		}, "could not extract " + top + "/lazyrex", 2},
		{"hard link out of the extraction directory", func(t *testing.T, f *installFix, _ string) map[string]string {
			rewrite(t, f, member(tar.TypeLink, "../../../outside/victim"))
			return nil
		}, "could not extract " + top + "/lazyrex", 2},
		{"file then symlink", func(t *testing.T, f *installFix, victim string) map[string]string {
			rewrite(t, f, member(tar.TypeReg, good), member(tar.TypeSymlink, victim))
			return nil
		}, "holds no file " + top + "/lazyrex", 2},
		{"symlink then file", func(t *testing.T, f *installFix, victim string) map[string]string {
			rewrite(t, f, member(tar.TypeSymlink, victim), member(tar.TypeReg, "written through\n"))
			return nil
		}, "holds no file " + top + "/lazyrex", 2},
		{"file twice", func(t *testing.T, f *installFix, _ string) map[string]string {
			rewrite(t, f, member(tar.TypeReg, good), member(tar.TypeReg, "second\n"))
			return nil
		}, "holds no file " + top + "/lazyrex", 2},
		{"other member escaping", func(t *testing.T, f *installFix, _ string) map[string]string {
			rewrite(t, f, member(tar.TypeReg, good), tarEntry{"../escape", tar.TypeReg, 0o644, "escaped\n"})
			return nil
		}, "", 2},
	}
	for _, sh := range shells(t) {
		for _, tr := range tars(t) {
			t.Run(sh.name+"/"+tr.name, func(t *testing.T) {
				t.Parallel()
				for _, c := range cases {
					t.Run(c.name, func(t *testing.T) {
						t.Parallel()
						f := newInstall(t)
						f.link(t, "tar", tr.path)
						victim := filepath.Join(f.s.root, "outside", "victim")
						writeFile(t, f.s.root, "outside/victim", "victim\n")
						r := f.pipe(t, sh, script, c.setup(t, f, victim))
						if c.msg != "" {
							f.wantNothingDone(t, r, c.msg)
						} else {
							wantCode(t, r, 0)
							wantInstalled(t, f.defaultDir(), "v0.1.0", "linux/amd64")
						}
						if n := len(f.curlCalls(t)); n != c.calls {
							t.Errorf("%d curl calls, want %d", n, c.calls)
						}
						if got := readFile(t, victim); got != "victim\n" {
							t.Errorf("the file outside holds %q: the archive wrote through it", got)
						}
						if fi, err := os.Lstat(victim); err != nil || !fi.Mode().IsRegular() {
							t.Errorf("the file outside was replaced: %v %v", fi, err)
						}
						err := filepath.WalkDir(f.s.root, func(p string, d fs.DirEntry, err error) error {
							if err == nil && d.Name() == "escape" {
								t.Errorf("%s was extracted", p)
							}
							return err
						})
						if err != nil {
							t.Fatal(err)
						}
					})
				}
			})
		}
	}
}

func TestInstallHashers(t *testing.T) {
	script := installScript(t)
	// required names the OS whose CI runner must have the hasher, so the test cannot skip there.
	cases := []struct {
		name     string
		keep     []string
		required string
		msg      string // empty: the install succeeds
	}{
		{"sha256sum only", []string{"sha256sum"}, "linux", ""},
		{"shasum only", []string{"shasum"}, "darwin", ""},
		{"neither", nil, "", "neither sha256sum nor shasum is installed"},
		{"no curl", []string{"sha256sum", "shasum"}, "", "curl is not installed"},
	}
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					t.Parallel()
					f := newInstall(t)
					for _, h := range []string{"sha256sum", "shasum"} {
						if slices.Contains(c.keep, h) && len(c.keep) == 1 && !exists(filepath.Join(f.tools, h)) {
							if c.required == runtime.GOOS {
								t.Fatalf("%s is not installed on %s", h, runtime.GOOS)
							}
							t.Skip(h + " is not installed")
						}
						if !slices.Contains(c.keep, h) {
							f.unlink(t, h)
						}
					}
					if c.name == "no curl" {
						if err := os.Remove(filepath.Join(f.s.bin, "curl")); err != nil {
							t.Fatal(err)
						}
					}
					r := f.pipe(t, sh, script, nil)
					if c.msg != "" {
						f.wantNothingDone(t, r, c.msg)
						f.wantNoLog(t, "curl.log")
						return
					}
					wantCode(t, r, 0)
					wantInstalled(t, f.defaultDir(), "v0.1.0", "linux/amd64")
				})
			}
		})
	}
}

func TestInstallDir(t *testing.T) {
	script := installScript(t)
	const hint = "choose a directory you own with LAZYREX_INSTALL_DIR (this script never uses sudo)"
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			t.Run("default", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				wantCode(t, f.pipe(t, sh, script, nil), 0)
				wantInstalled(t, f.defaultDir(), "v0.1.0", "linux/amd64")
			})
			t.Run("override", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				dir := filepath.Join(f.s.root, "opt", "bin")
				r := f.pipe(t, sh, script, map[string]string{"LAZYREX_INSTALL_DIR": dir})
				wantCode(t, r, 0)
				wantInstalled(t, dir, "v0.1.0", "linux/amd64")
				if msg := "to " + dir + "/lazyrex\n"; !strings.Contains(r.stdout, msg) {
					t.Errorf("stdout %q lacks %q", r.stdout, msg)
				}
				if exists(filepath.Join(f.s.home, ".local")) {
					t.Error("the default directory was created too")
				}
			})
			t.Run("trailing slashes", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				dir := filepath.Join(f.s.root, "opt", "bin")
				r := f.pipe(t, sh, script, map[string]string{"LAZYREX_INSTALL_DIR": dir + "//"})
				wantCode(t, r, 0)
				wantInstalled(t, dir, "v0.1.0", "linux/amd64")
				if msg := "to " + dir + "/lazyrex\n"; !strings.Contains(r.stdout, msg) {
					t.Errorf("stdout %q lacks %q", r.stdout, msg)
				}
				if msg := dir + " is not in PATH"; !strings.Contains(r.stdout, msg) {
					t.Errorf("stdout %q lacks %q", r.stdout, msg)
				}
			})
			t.Run("relative", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				r := f.pipe(t, sh, script, map[string]string{"LAZYREX_INSTALL_DIR": "lazybin"})
				f.wantNothingDone(t, r, "LAZYREX_INSTALL_DIR lazybin is not an absolute path")
				f.wantNoLog(t, "curl.log")
				if exists(filepath.Join(f.s.root, "lazybin")) {
					t.Error("a relative directory was created")
				}
			})
			t.Run("no HOME", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				r := f.pipe(t, sh, script, map[string]string{"HOME": unset})
				f.wantNothingDone(t, r, "HOME is not set: set LAZYREX_INSTALL_DIR")
				f.wantNoLog(t, "curl.log")
			})
			t.Run("update", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				writeFile(t, f.defaultDir(), "lazyrex", "old\n")
				wantCode(t, f.pipe(t, sh, script, nil), 0)
				wantInstalled(t, f.defaultDir(), "v0.1.0", "linux/amd64")
			})
			t.Run("read-only", func(t *testing.T) {
				t.Parallel()
				if os.Geteuid() == 0 {
					t.Skip("root writes to a read-only directory")
				}
				f := newInstall(t)
				dir := f.defaultDir()
				writeFile(t, dir, "lazyrex", "old\n")
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
				r := f.pipe(t, sh, script, nil)
				wantCode(t, r, 1)
				if msg := "cannot write to " + dir + ": " + hint; !strings.Contains(r.stderr, msg) {
					t.Errorf("stderr %q lacks %q", r.stderr, msg)
				}
				if got := readFile(t, filepath.Join(dir, "lazyrex")); got != "old\n" {
					t.Errorf("the old binary holds %q", got)
				}
				wantNoStaged(t, dir)
			})
			t.Run("file in the way", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				writeFile(t, f.s.root, "file", "x\n")
				p := filepath.Join(f.s.root, "file")
				r := f.pipe(t, sh, script, map[string]string{"LAZYREX_INSTALL_DIR": p})
				f.wantNothingDone(t, r, "cannot create "+p+": "+hint)
				if got := readFile(t, p); got != "x\n" {
					t.Errorf("the file holds %q", got)
				}
			})
			t.Run("directory in the way", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				dir := f.defaultDir()
				writeFile(t, dir, "lazyrex/keep", "keep\n")
				r := f.pipe(t, sh, script, nil)
				wantCode(t, r, 1)
				if msg := dir + "/lazyrex is a directory"; !strings.Contains(r.stderr, msg) {
					t.Errorf("stderr %q lacks %q", r.stderr, msg)
				}
				if got := listDir(t, filepath.Join(dir, "lazyrex")); !slices.Equal(got, []string{"keep"}) {
					t.Errorf("%s/lazyrex holds %v", dir, got)
				}
				wantNoStaged(t, dir)
			})
		})
	}
}

func TestInstallHints(t *testing.T) {
	script := installScript(t)
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			t.Run("not in PATH", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				r := f.pipe(t, sh, script, nil)
				wantCode(t, r, 0)
				dir := f.defaultDir()
				if msg := dir + ` is not in PATH: add it, for example with export PATH="` + dir + `:$PATH"`; !strings.Contains(r.stdout, msg) {
					t.Errorf("stdout %q lacks %q", r.stdout, msg)
				}
				if msg := "rex is not in PATH: lazyrex needs it (API revision 9)"; !strings.Contains(r.stdout, msg) {
					t.Errorf("stdout %q lacks %q", r.stdout, msg)
				}
				if strings.Contains(r.stdout, "comes first in PATH") {
					t.Errorf("stdout %q warns of another lazyrex", r.stdout)
				}
			})
			t.Run("in PATH, with rex", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				f.s.writeBin(t, "rex", "#!/bin/sh\n")
				r := f.pipe(t, sh, script, map[string]string{"PATH": f.s.bin + ":" + f.tools + ":" + f.defaultDir()})
				wantCode(t, r, 0)
				for _, noise := range []string{"is not in PATH", "comes first in PATH"} {
					if strings.Contains(r.stdout, noise) {
						t.Errorf("stdout %q holds %q", r.stdout, noise)
					}
				}
			})
			t.Run("shadowed", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				f.s.writeBin(t, "lazyrex", "#!/bin/sh\n")
				r := f.pipe(t, sh, script, map[string]string{"PATH": f.s.bin + ":" + f.tools + ":" + f.defaultDir()})
				wantCode(t, r, 0)
				if msg := filepath.Join(f.s.bin, "lazyrex") + " comes first in PATH"; !strings.Contains(r.stdout, msg) {
					t.Errorf("stdout %q lacks %q", r.stdout, msg)
				}
			})
		})
	}
}

func TestInstallArguments(t *testing.T) {
	script := installScript(t)
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			for _, arg := range []string{"--help", "-h"} {
				t.Run(arg, func(t *testing.T) {
					t.Parallel()
					f := newInstall(t)
					r := f.pipe(t, sh, script, nil, arg)
					wantCode(t, r, 0)
					for _, want := range []string{installLine, "LAZYREX_VERSION", "LAZYREX_INSTALL_DIR"} {
						if !strings.Contains(r.stdout, want) {
							t.Errorf("stdout %q lacks %q", r.stdout, want)
						}
					}
					f.wantNoLog(t, "curl.log", "uname.log", "sysctl.log")
				})
			}
			t.Run("flag", func(t *testing.T) {
				t.Parallel()
				f := newInstall(t)
				r := f.pipe(t, sh, script, nil, "--version", "v0.1.0")
				f.wantNothingDone(t, r, "unexpected argument --version")
				f.wantNoLog(t, "curl.log")
			})
		})
	}
}

// TestInstallInterrupted stops the script with TERM while a download or the staging of the binary is blocked:
// it must exit 1 and leave neither its temporary directory nor a staged file.
func TestInstallInterrupted(t *testing.T) {
	script := installScript(t)
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			for _, stage := range []string{"download", "staging"} {
				t.Run(stage, func(t *testing.T) {
					t.Parallel()
					f := newInstall(t)
					env := map[string]string{"FAKE_BLOCK_URL": releasesURL + "/download/v0.1.0/lazyrex_0.1.0_linux_amd64.tar.gz"}
					if stage == "staging" {
						env = map[string]string{"REAL_CHMOD": lookPath(t, "chmod")}
						f.s.writeBin(t, "chmod", fakeChmod)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					defer cancel()
					cmd, stdout, stderr := f.command(ctx, sh, script, env, nil)
					if err := cmd.Start(); err != nil {
						t.Fatal(err)
					}
					blocked := filepath.Join(f.s.root, "blocked")
					for deadline := time.Now().Add(time.Minute); !exists(blocked); time.Sleep(10 * time.Millisecond) {
						if time.Now().After(deadline) {
							_ = cmd.Process.Kill()
							t.Fatalf("the script never reached the %s: %s", stage, stderr)
						}
					}
					if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
						t.Fatal(err)
					}
					writeFile(t, f.s.root, "go", "")
					r := f.finish(t, cmd.Wait(), stdout, stderr)
					wantCode(t, r, 1)
					wantNoStaged(t, f.defaultDir())
					if strings.Contains(r.stdout, "installed lazyrex") {
						t.Errorf("stdout %q reports an install", r.stdout)
					}
				})
			}
		})
	}
}

// TestInstallTruncated pipes the script cut at every line start, and at every byte of its last line short of
// its final newline: a truncated download must run nothing.
func TestInstallTruncated(t *testing.T) {
	script := installScript(t)
	text := string(script)
	body := strings.TrimSuffix(text, "\n")
	lastStart := strings.LastIndex(body, "\n") + 1
	var cuts []int
	for i := 0; i < lastStart; i++ {
		if i == 0 || text[i-1] == '\n' {
			cuts = append(cuts, i)
		}
	}
	for i := lastStart + 1; i < len(body); i++ {
		cuts = append(cuts, i)
	}
	for _, sh := range shells(t) {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			f := newInstall(t)
			for _, n := range cuts {
				r := f.pipe(t, sh, script[:n], nil)
				for _, log := range []string{"curl.log", "uname.log", "sysctl.log"} {
					if exists(filepath.Join(f.s.root, log)) {
						t.Fatalf("cut at byte %d (%q): %s exists, exit %d, stderr %q", n, text[max(0, n-40):n], log, r.code, r.stderr)
					}
				}
				if left := listDir(t, f.s.home); len(left) > 0 {
					t.Fatalf("cut at byte %d: HOME holds %v", n, left)
				}
				if n == lastStart && r.code != 0 {
					t.Errorf("the script without its last line: exit %d, stderr %q", r.code, r.stderr)
				}
			}
		})
	}
}

func TestInstallScriptShape(t *testing.T) {
	text := string(installScript(t))
	if fi, err := os.Stat(installPath); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("install.sh is not executable: %v %v", fi, err)
	}
	if strings.Contains(text, "\r") {
		t.Error("install.sh has CR line endings")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if lines[0] != "#!/bin/sh" {
		t.Errorf("line 1 %q, want #!/bin/sh", lines[0])
	}
	if l := lines[len(lines)-1]; l != `{ main "$@"; }` {
		t.Errorf("last line %q, want { main \"$@\"; }", l)
	}
	opener := regexp.MustCompile(`^[a-z_]+\(\) \{$`)
	oneLine := regexp.MustCompile(`^[a-z_]+\(\) \{ .*; \}$`)
	inBody := false
	for i, l := range lines[:len(lines)-1] {
		switch {
		case inBody:
			inBody = l != "}"
		case opener.MatchString(l):
			inBody = true
		case l == "", strings.HasPrefix(l, "#"), l == "set -eu", l == "repo=https://github.com/rbobillot/lazyrex", oneLine.MatchString(l):
		default:
			t.Errorf("line %d runs at the top level: %q", i+1, l)
		}
	}
	if inBody {
		t.Error("a function body is not closed by } at column 0")
	}
	var code []string
	for _, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			code = append(code, l)
		}
	}
	for _, l := range code {
		for _, bad := range []string{"[[", "local ", "function ", "$'", "<<<", "pipefail", "--insecure", " -k ", "echo -e"} {
			if strings.Contains(l, bad) {
				t.Errorf("install.sh holds %q: %q", bad, l)
			}
		}
	}
	command := func(name string) *regexp.Regexp { return regexp.MustCompile(`(^\s*|[;{&|(]\s*)` + name + `(\s|$)`) }
	for _, name := range []string{"sudo", "eval", "source"} {
		for _, l := range code {
			if command(name).MatchString(l) {
				t.Errorf("install.sh runs %s: %q", name, l)
			}
		}
	}
	var curls []string
	for _, l := range code {
		if command("curl").MatchString(l) {
			curls = append(curls, l)
		}
	}
	if len(curls) != 1 || !strings.HasPrefix(curls[0], "fetch() { curl "+curlFlags+" ") {
		t.Errorf("curl runs in %q, want once, in fetch, as curl %s", curls, curlFlags)
	}
}
