package gui

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rexhosts"
)

const rexHostsOffError = "the Rex app hosts file is not available: experimental.rex_hosts_file is off in lazyrex's config file " +
	"(the Rex app does not show declared hosts yet); turn it on, then restart lazyrex"

// offStatus is the block r.status gives for target name with the Rex app
// hosts file off.
func offStatus(name string) string { return offHint(name, "unix:///gui.sock") + "\n" }

// addSSHTarget adds the forward-mode SSH target a through the UI.
func (r *rexHarness) addSSHTarget() {
	r.t.Helper()
	r.selectTarget("local")
	r.dispatch(PanelTargets, "addTarget")
	r.choose("ssh: another destination…")
	r.answer("a")
	r.acceptPrefilled()
	r.choosePrefix("forward:")
}

// addNativeTarget adds the native target lab through the UI.
func (r *rexHarness) addNativeTarget() {
	r.t.Helper()
	r.selectTarget("local")
	r.dispatch(PanelTargets, "addTarget")
	r.choosePrefix("native:")
	r.answer("lab")
	r.answer("lab.ts.net")
}

func (r *rexHarness) expectNoRexReads() {
	r.t.Helper()
	if n := r.rexReads.Load(); n != 0 {
		r.t.Fatalf("read lazyrex's environment or home %d times", n)
	}
}

// TestRexHostsOffNeverReads (F8, F9): with the flag off, nothing lazyrex
// does reads its environment or home for Rex's config directory, runs a
// config command, or writes the hosts file.
func TestRexHostsOffNeverReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		file func(string) []byte
	}{{"golden file", golden}, {"no file", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRexHarness(t, rexSetup{off: true, file: tc.file, hosts: loaded})
			r.ctrl.writeHosts = func(rexhosts.Paths, []byte, rexhosts.Snapshot) error {
				t.Errorf("wrote the Rex app hosts file")
				return nil
			}
			r.dispatch(PanelTargets, "refresh")
			for range 2 {
				r.clk.Advance(5 * time.Second)
				r.ctrl.Tick()
			}
			r.w()
			r.expectError(rexHostsOffError)
			r.addNativeTarget()
			if raw, _ := os.ReadFile(r.cfgPath); !strings.Contains(string(raw), "lab.ts.net") {
				t.Fatalf("config:\n%s", raw)
			}
			r.expectNoRexReads()
			if calls := r.fr.CallsContaining("config"); len(calls) != 0 {
				t.Fatalf("config calls %q", calls)
			}
			if !reflect.DeepEqual(r.st.RexHosts, RexHostsView{}) || r.ctrl.rhLoaded {
				t.Fatalf("looked at the file: %+v", r.st.RexHosts)
			}
			if _, err := os.Lstat(filepath.Join(r.home, ".config")); tc.file == nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("touched the config directory: %v", err)
			}
		})
	}
	r := newRexHarness(t, rexSetup{file: golden, hosts: loaded})
	if r.rexReads.Load() == 0 || !r.ctrl.rhLoaded {
		t.Fatalf("on: %d reads, loaded %v", r.rexReads.Load(), r.ctrl.rhLoaded)
	}
}

// TestRexHostsOffRefused (F10, F14): w is refused with the flag named, before
// REX_SERVER, and Status shows the rex hosts add hint instead.
func TestRexHostsOffRefused(t *testing.T) {
	for name, env := range map[string]map[string]string{"plain": nil, "REX_SERVER": {"REX_SERVER": "unix:///tmp/other.sock"}} {
		t.Run(name, func(t *testing.T) {
			r := newRexHarness(t, rexSetup{off: true, env: env})
			r.expectStatus("edge", offStatus("edge"))
			r.fr.Reset()
			r.w()
			r.expectError(rexHostsOffError)
			if p := r.ctrl.Popups.Top(); p != nil {
				t.Fatalf("popup %+v", p)
			}
			if len(r.fr.Calls()) != 0 {
				t.Fatalf("calls:\n%s", r.dumpCalls())
			}
			r.expectNoRexReads()
		})
	}
}

// TestRexHostsOffFlagTarget (F15): a --ssh target gets the hint too, as
// before the hosts file.
func TestRexHostsOffFlagTarget(t *testing.T) {
	r := newRexHarness(t, rexSetup{off: true, cfg: "targets:\n  - name: other\n    kind: ssh\n    ssh_host: other\n    mode: exec\n",
		opts: []harnessOpt{withSSHFlag("u@lab"), withSSH(nil)}})
	r.expectStatus("u@lab", offStatus("u@lab"))
}

// TestRexHostsOffHidesOwnHosts (F13): hosts a file written while the flag was
// on still declares stay hidden in Targets, with no look at the file.
func TestRexHostsOffHidesOwnHosts(t *testing.T) {
	r := newRexHarness(t, rexSetup{off: true, file: golden, hosts: loaded})
	r.fr.On("hosts", "check").Return(`{"hosts": [{"label": "edge-lazyrex", "status": "quantum"}, {"label": "remote-host-lazyrex", "status": "quantum"}, `+
		`{"label": "studio", "status": "reachable", "endpoint": "tcp://192.168.1.20:7297"}]}`, "", 0)
	r.setHosts(append(loaded(r.run), host{label: "studio", endpoint: "tcp://192.168.1.20:7297"})...)
	r.ctrl.refreshHosts()
	if rows := r.rowNames(); !slices.Contains(rows, "studio") || slices.ContainsFunc(rows, func(n string) bool { return strings.HasSuffix(n, "-lazyrex") }) {
		t.Fatalf("rows %q", rows)
	}
	if !strings.Contains(r.logText(), `hosts: hid "edge-lazyrex": lazyrex's own host`) {
		t.Fatalf("log:\n%s", r.logText())
	}
	if h := r.st.Hosts; h.Unmatched != 0 || len(h.Unlisted) != 0 {
		t.Fatalf("hosts %+v", h)
	}
	r.expectStatus("edge", offStatus("edge"))
	r.expectNoRexReads()
}

// TestRexHostsFlagFromFile (F12): the config file turns the flag on or off
// at start; neither a UI edit nor a hand edit changes it afterwards.
func TestRexHostsFlagFromFile(t *testing.T) {
	on := "experimental:\n  rex_hosts_file: true\n" + rexCfg
	off := "experimental:\n  rex_hosts_file: false\n" + rexCfg

	r := newRexHarness(t, rexSetup{off: true, cfg: on})
	r.expectStatus("edge", block(r.head("edge-lazyrex", "not written yet"),
		"w (Targets) writes it, then reloads the local Rex server, each after a confirmation.", works("edge")))
	r.addSSHTarget()
	if raw, _ := os.ReadFile(r.cfgPath); !containsAll(string(raw), "rex_hosts_file: true", "ssh_host: a") {
		t.Fatalf("config:\n%s", raw)
	}
	r.w()
	r.expectConfirm("Write the Rex app hosts file")

	r = newRexHarness(t, rexSetup{off: true, cfg: off})
	r.addSSHTarget()
	if raw, _ := os.ReadFile(r.cfgPath); !strings.Contains(string(raw), "ssh_host: a") {
		t.Fatalf("config:\n%s", raw)
	}
	r.w()
	r.expectError(rexHostsOffError)
	r.expectNoRexReads()

	r = newRexHarness(t, rexSetup{off: true, cfg: off})
	if err := os.WriteFile(r.cfgPath, []byte(on), 0o600); err != nil {
		t.Fatal(err)
	}
	r.addNativeTarget()
	r.harness.expectError("config not changed: the config file changed on disk since lazyrex loaded it")
	r.ctrl.Popups.Cancel()
	r.w()
	r.expectError(rexHostsOffError)
	r.expectNoRexReads()
}
