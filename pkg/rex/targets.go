package rex

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rbobillot/lazyrex/pkg/render"
)

type TargetKind string

const (
	TargetLocal  TargetKind = "local"
	TargetNative TargetKind = "native"
	TargetSSH    TargetKind = "ssh"
)

// TargetSource is where a target comes from.
type TargetSource int

const (
	SourceBuiltin TargetSource = iota
	SourceConfig
	SourceFlag
	SourceHosts
)

// LocalName is the name of the built-in local target, which `--server
// local` always selects even when the config renames it.
const LocalName = "local"

// Target is one row of the Targets panel.
type Target struct {
	Name string
	Kind TargetKind
	// Server is the -S value of a native target.
	Server string
	// SSH is the configuration of an SSH target.
	SSH    SSHSpec
	Source TargetSource
	// Host holds the discovered details, for display only.
	Host *Host
}

// Transport returns how calls reach the target. An SSH target's real route
// depends on its connection (SSHLink); this one names it only.
func (t Target) Transport() Transport {
	switch t.Kind {
	case TargetNative:
		return Native{Server: t.Server}
	case TargetSSH:
		return SSHExec{Host: t.SSH.Host, RexPath: t.SSH.RexPath, NonPOSIX: true}
	}
	return Local{}
}

func checkText(s string, maxLen int, runes bool) error {
	n := len(s)
	if runes {
		n = utf8.RuneCountInString(s)
	}
	switch {
	case s == "":
		return errors.New("empty")
	case !utf8.ValidString(s):
		return errors.New("not valid UTF-8")
	case n > maxLen && runes:
		return fmt.Errorf("longer than %d characters", maxLen)
	case n > maxLen:
		return fmt.Errorf("longer than %d bytes", maxLen)
	case hasControl(s):
		return errors.New("contains control characters")
	case strings.HasPrefix(s, "-"):
		return errors.New("must not start with '-'")
	case strings.TrimSpace(s) != s:
		return errors.New("has leading or trailing whitespace")
	}
	return nil
}

// CheckServer validates a value passed as `rex -S VALUE`. It stays one argv
// element and no shell reads it, so inner spaces are fine; a leading '-'
// could be read as a flag.
func CheckServer(s string) error { return checkText(s, 255, false) }

// CheckTargetName validates the name of a target.
func CheckTargetName(s string) error { return checkText(s, 64, true) }

// TargetSpec is a config target as written in the config file.
type TargetSpec struct{ Name, Kind, Server, SSHHost, Mode, RexPath string }

func checkRemoteName(i int, name string) error {
	if err := CheckTargetName(name); err != nil {
		return fmt.Errorf("targets[%d].name: %v", i, err)
	}
	if strings.EqualFold(name, LocalName) {
		return fmt.Errorf("targets[%d].name: %s is the built-in local target", i, LocalName)
	}
	return nil
}

func checkSSHSpec(i int, s TargetSpec) (SSHSpec, error) {
	if s.Server != "" {
		return SSHSpec{}, fmt.Errorf("targets[%d].server: an ssh target has ssh_host, not server", i)
	}
	if err := CheckSSHHost(s.SSHHost); err != nil {
		return SSHSpec{}, fmt.Errorf("targets[%d].ssh_host: %v", i, err)
	}
	switch SSHMode(s.Mode) {
	case "", SSHModeForward, SSHModeExec:
	default:
		return SSHSpec{}, fmt.Errorf("targets[%d].mode: want forward or exec, got %q", i, s.Mode)
	}
	if s.RexPath != "" {
		if err := CheckRexPath(s.RexPath); err != nil {
			return SSHSpec{}, fmt.Errorf("targets[%d].rex_path: %v", i, err)
		}
	}
	return SSHSpec{Host: s.SSHHost, Mode: SSHMode(s.Mode), RexPath: s.RexPath}, nil
}

func sshOnlyKey(s TargetSpec) string {
	switch {
	case s.SSHHost != "":
		return "ssh_host"
	case s.Mode != "":
		return "mode"
	case s.RexPath != "":
		return "rex_path"
	}
	return ""
}

// ConfigTargets returns the local target, renamed by a kind: local entry,
// then the native and SSH config targets in file order.
func ConfigTargets(specs []TargetSpec) ([]Target, error) {
	local := Target{Name: LocalName, Kind: TargetLocal, Source: SourceBuiltin}
	var remotes []Target
	seenLocal := false
	for i, s := range specs {
		if k := sshOnlyKey(s); k != "" && TargetKind(s.Kind) != TargetSSH {
			return nil, fmt.Errorf("targets[%d].%s: only an ssh target has %s", i, k, k)
		}
		switch TargetKind(s.Kind) {
		case TargetLocal:
			switch {
			case seenLocal:
				return nil, fmt.Errorf("targets[%d]: at most one kind: local target", i)
			case s.Server != "":
				return nil, fmt.Errorf("targets[%d].server: a local target has no server", i)
			}
			seenLocal = true
			if s.Name != "" {
				if err := CheckTargetName(s.Name); err != nil {
					return nil, fmt.Errorf("targets[%d].name: %v", i, err)
				}
				local.Name = s.Name
			}
		case TargetNative:
			if err := checkRemoteName(i, s.Name); err != nil {
				return nil, err
			}
			if err := CheckServer(s.Server); err != nil {
				return nil, fmt.Errorf("targets[%d].server: %v", i, err)
			}
			remotes = append(remotes, Target{Name: s.Name, Kind: TargetNative, Server: s.Server, Source: SourceConfig})
		case TargetSSH:
			if err := checkRemoteName(i, s.Name); err != nil {
				return nil, err
			}
			spec, err := checkSSHSpec(i, s)
			if err != nil {
				return nil, err
			}
			remotes = append(remotes, Target{Name: s.Name, Kind: TargetSSH, SSH: spec, Source: SourceConfig})
		default:
			return nil, fmt.Errorf("targets[%d].kind: want local, native or ssh, got %q", i, s.Kind)
		}
	}
	out := append([]Target{local}, remotes...)
	for i, t := range out {
		if j, _ := FindTarget(out, t.Name); j != i {
			return nil, fmt.Errorf("targets: duplicate name %q", t.Name)
		}
	}
	return out, nil
}

// FindTarget returns the index of the target named name, ignoring case.
func FindTarget(ts []Target, name string) (int, bool) {
	i := slices.IndexFunc(ts, func(t Target) bool { return strings.EqualFold(t.Name, name) })
	return i, i >= 0
}

// WithFlag resolves --server v against base: empty or local selects the
// local target, a target name that target, anything else a new ad-hoc
// native target appended to the returned list.
func WithFlag(base []Target, v string) ([]Target, int, error) {
	out := slices.Clone(base)
	if v == "" || strings.EqualFold(v, LocalName) {
		i := slices.IndexFunc(out, func(t Target) bool { return t.Kind == TargetLocal })
		if i < 0 {
			out = append([]Target{{Name: LocalName, Kind: TargetLocal, Source: SourceBuiltin}}, out...)
			i = 0
		}
		return out, i, nil
	}
	if err := CheckServer(v); err != nil {
		return nil, 0, fmt.Errorf("--server %q: %v", v, err)
	}
	if i, ok := FindTarget(out, v); ok {
		return out, i, nil
	}
	out = append(out, Target{Name: v, Kind: TargetNative, Server: v, Source: SourceFlag})
	return out, len(out) - 1, nil
}

// WithSSHFlag resolves --ssh host against base: the first SSH target whose
// ssh_host is exactly host, else a new ad-hoc SSH target named host,
// appended to the returned list.
func WithSSHFlag(base []Target, host string) ([]Target, int, error) {
	if err := CheckSSHHost(host); err != nil {
		return nil, 0, fmt.Errorf("--ssh %q: %v", host, err)
	}
	out := slices.Clone(base)
	if i := slices.IndexFunc(out, func(t Target) bool { return t.Kind == TargetSSH && t.SSH.Host == host }); i >= 0 {
		return out, i, nil
	}
	if i, ok := FindTarget(out, host); ok {
		return nil, 0, fmt.Errorf("--ssh %q: the name is taken by target %q", host, out[i].Name)
	}
	if err := CheckTargetName(host); err != nil {
		return nil, 0, fmt.Errorf("--ssh %q: as a target name: %v", host, err)
	}
	if strings.EqualFold(host, LocalName) {
		return nil, 0, fmt.Errorf("--ssh %q: %s is the built-in local target", host, LocalName)
	}
	out = append(out, Target{Name: host, Kind: TargetSSH, SSH: SSHSpec{Host: host}, Source: SourceFlag})
	return out, len(out) - 1, nil
}

// MergeTargets appends the discovered hosts to base. A host whose label is
// the server of a native base target of the same name adds its details to
// that row; any other name collision, the name local and a label unsafe as
// -S hide it, with one line in skipped. The active target stays listed.
func MergeTargets(base []Target, hosts []Host, active Target) (rows []Target, skipped []string) {
	rows = slices.Clone(base)
	merged := map[int]bool{}
	for _, h := range hosts {
		label := h.Label
		if err := CheckServer(label); err != nil {
			skipped = append(skipped, fmt.Sprintf("hosts: skipped %q: %v", render.Label(label), err))
			continue
		}
		if strings.EqualFold(label, LocalName) {
			skipped = append(skipped, fmt.Sprintf("hosts: skipped %q: %s is the built-in local target", render.Label(label), LocalName))
			continue
		}
		if i, ok := FindTarget(rows, label); ok {
			r := &rows[i]
			if r.Kind == TargetNative && r.Source != SourceHosts && strings.EqualFold(r.Server, label) && !merged[i] {
				r.Host, merged[i] = &h, true
				continue
			}
			skipped = append(skipped, fmt.Sprintf("hosts: hid %q: the name is taken by target %q", render.Label(label), render.Label(r.Name)))
			continue
		}
		rows = append(rows, Target{Name: label, Kind: TargetNative, Server: label, Source: SourceHosts, Host: &h})
	}
	if active.Source == SourceHosts {
		if _, ok := FindTarget(rows, active.Name); !ok {
			rows = append(rows, active)
		}
	}
	return rows, skipped
}
