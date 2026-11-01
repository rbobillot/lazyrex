package rex

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Loose is text from a JSON field whose type section 8 does not pin down: a
// string as is, null as "", anything else as compact JSON.
type Loose string

func (l *Loose) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case bytes.Equal(b, []byte("null")):
		*l = ""
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = Loose(s)
	default:
		var buf bytes.Buffer
		if err := json.Compact(&buf, b); err != nil {
			return err
		}
		*l = Loose(buf.String())
	}
	return nil
}

type Version struct {
	RexVersion  string `json:"rex_version"`
	Platform    string `json:"platform"`
	Commit      string `json:"commit"`
	BuiltAt     Loose  `json:"built_at"`
	Dirty       bool   `json:"dirty"`
	APIRevision int    `json:"api_revision"`
}

type Whoami struct {
	Principal    Loose           `json:"principal"`
	Account      Loose           `json:"account"`
	Endpoint     string          `json:"endpoint"`
	PID          int             `json:"pid"`
	InstanceID   Loose           `json:"instance_id"`
	HostID       Loose           `json:"host_id"`
	StartedAt    Loose           `json:"started_at"`
	RexVersion   string          `json:"rex_version"`
	ExitEmpty    bool            `json:"exit_empty"`
	SessionCount int             `json:"session_count"`
	Tailscale    json.RawMessage `json:"tailscale"`
}

type ServerStatus struct {
	Endpoint     string `json:"endpoint"`
	PID          int    `json:"pid"`
	InstanceID   Loose  `json:"instance_id"`
	HostID       Loose  `json:"host_id"`
	StartedAt    Loose  `json:"started_at"`
	RexVersion   string `json:"rex_version"`
	APIRevision  int    `json:"api_revision"`
	ExitEmpty    bool   `json:"exit_empty"`
	SessionCount int    `json:"session_count"`
	LogPath      string `json:"log_path"`
	Tailscale    struct {
		State               Loose `json:"state"`
		AllowTailnetMembers bool  `json:"allow_tailnet_members"`
	} `json:"tailscale"`
	You struct {
		Principal Loose `json:"principal"`
	} `json:"you"`
}

type SessionEntry struct {
	SessionID SessionID `json:"session_id"`
	Label     string    `json:"label"`
}

type SessionList struct {
	Sessions []SessionEntry `json:"sessions"`
}

type BlockInfo struct {
	BlockID     BlockID `json:"block_id"`
	Label       string  `json:"label"`
	CreatorName string  `json:"creator_name"`
	Flavor      Loose   `json:"flavor"`
	HostID      Loose   `json:"host_id"`
	Owner       Loose   `json:"owner"`
}

type Layer struct {
	LayerID Loose           `json:"layer_id"`
	Kind    string          `json:"kind"`
	Bounds  json.RawMessage `json:"bounds"`
	Layout  json.RawMessage `json:"layout"`
	Blocks  []BlockInfo     `json:"blocks"`
}

type WindowView struct {
	WindowID       WindowID `json:"window_id"`
	Label          string   `json:"label"`
	FocusedBlockID BlockID  `json:"focused_block_id"`
	Layers         []Layer  `json:"layers"`
}

// Blocks returns the blocks of every layer of the window.
func (w WindowView) Blocks() []BlockInfo {
	var out []BlockInfo
	for _, l := range w.Layers {
		out = append(out, l.Blocks...)
	}
	return out
}

type SessionView struct {
	Revision       int64        `json:"revision"`
	SessionID      SessionID    `json:"session_id"`
	Label          string       `json:"label"`
	ActiveWindowID WindowID     `json:"active_window_id"`
	Windows        []WindowView `json:"windows"`
}

// Blocks returns the blocks of every window of the session.
func (v SessionView) Blocks() []BlockInfo {
	var out []BlockInfo
	for _, w := range v.Windows {
		out = append(out, w.Blocks()...)
	}
	return out
}

type BlockEntry struct {
	BlockInfo
	Placement json.RawMessage `json:"placement"`
	WindowID  WindowID        `json:"window_id"`
}

// Detached reports whether placement says the block is in no window.
func (b BlockEntry) Detached() bool {
	var s string
	if json.Unmarshal(b.Placement, &s) == nil {
		return strings.EqualFold(s, "detached")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(b.Placement, &obj) != nil {
		return false
	}
	for _, k := range []string{"kind", "type", "state"} {
		if json.Unmarshal(obj[k], &s) == nil && strings.EqualFold(s, "detached") {
			return true
		}
	}
	return false
}

type BlockList struct {
	Blocks []BlockEntry `json:"blocks"`
}

type ClientInfo struct {
	Kind     string `json:"kind"`
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Version  string `json:"version"`
}

type ClientEntry struct {
	ClientID        ClientID   `json:"client_id"`
	Info            ClientInfo `json:"info"`
	Principal       Loose      `json:"principal"`
	ConnectedAt     Loose      `json:"connected_at"`
	ConnectionState Loose      `json:"connection_state"`
}

type ClientList struct {
	Clients []ClientEntry `json:"clients"`
}

type HostsHome struct {
	Label       string `json:"label"`
	Endpoint    string `json:"endpoint"`
	ThisMachine bool   `json:"this_machine"`
	Reached     bool   `json:"reached"`
}

type HostsList struct {
	Home     HostsHome         `json:"home"`
	Hosts    []json.RawMessage `json:"hosts"`
	Warnings []json.RawMessage `json:"warnings"`
}

// Host is one entry of `rex hosts list`.
type Host struct {
	ID        Loose   `json:"id"`
	Label     string  `json:"label"`
	Endpoints []Loose `json:"endpoints"`
	HostID    Loose   `json:"host_id"`
	AddedFrom Loose   `json:"added_from"`
	Source    Loose   `json:"source"`
	UpdatedAt Loose   `json:"updated_at"`
}

// HostsView is `rex hosts list` decoded entry by entry: Skipped counts the
// entries that are not a host object with a label.
type HostsView struct {
	Home     HostsHome
	Hosts    []Host
	Warnings []string
	Skipped  int
}

type CheckState int

const (
	CheckUnknown CheckState = iota
	CheckReachable
	CheckNotReachable
	CheckDown
)

func (s CheckState) String() string {
	return [...]string{"unknown", "reachable", "unreachable", "down"}[s]
}

// HostCheck is one entry of `rex hosts check`. Status keeps the raw text;
// Unrecognized marks a status lazyrex does not know.
type HostCheck struct {
	Label                    string
	Status, Endpoint, Detail Loose
	State                    CheckState
	Unrecognized             bool
}

type CheckReport struct {
	Entries               []HostCheck
	Skipped, Unrecognized int
}

type Action struct {
	Name        string          `json:"name"`
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Args        json.RawMessage `json:"args"`
}

type KeyBinding struct {
	Keys   []string `json:"-"`
	Action Loose    `json:"action"`
}

type Keymap struct {
	Bindings []KeyBinding `json:"bindings"`
}

type Process struct {
	Running  *bool
	State    string
	LastExit *int
}

type Modes struct {
	Screen string
}

// AltScreen reports a full-screen program: the capture is the current screen.
func (m Modes) AltScreen() bool { return strings.EqualFold(m.Screen, "alternate") }
