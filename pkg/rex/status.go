package rex

import "strings"

// ExpectedAPIRevision is the Rex API revision lazyrex was built against.
const ExpectedAPIRevision = 9

type TargetState int

const (
	StateUnknown TargetState = iota
	StateOnline
	StateStopped
	StateUnreachable
	StateSSHUnreachable
	StateError
)

func (s TargetState) String() string {
	return [...]string{"unknown", "online", "stopped", "unreachable", "unreachable (ssh)", "error"}[s]
}

// TargetStatus is what the Targets panel and Status tab show.
type TargetStatus struct {
	State       TargetState
	Whoami      *Whoami
	Server      *ServerStatus
	Version     *Version
	APIRevision int
	APIMismatch bool
	Detail      string
}

// IsNotRunning reports `rex server status` exiting 1 because no local server
// runs, which is a normal state.
func IsNotRunning(o Outcome) bool {
	return o.Code == 1 && strings.Contains(strings.ToLower(string(o.Stderr)), "server is not running")
}

// DeriveStatus combines whoami with the optional server status and local
// version outcomes. Exit 4 means stopped locally and unreachable remotely.
func DeriveStatus(t Transport, whoami Outcome, server, version *Outcome) TargetStatus {
	var st TargetStatus
	switch whoami.Kind {
	case ExitOK:
		st.State = StateOnline
		if w, err := ParseWhoami(whoami.Stdout); err == nil {
			st.Whoami = &w
		} else {
			st.Detail = err.Error()
		}
	case ExitUnreachable:
		st.State = StateStopped
		if t.Remote() {
			st.State = StateUnreachable
		}
		st.Detail = whoami.Toast
	case ExitSSH:
		st.State, st.Detail = StateSSHUnreachable, whoami.Toast
	case ExitInterrupted:
		st.State = StateUnknown
	default:
		st.State, st.Detail = StateError, whoami.Toast
	}
	if server != nil {
		switch {
		case server.Kind == ExitOK:
			if s, err := ParseServerStatus(server.Stdout); err == nil {
				st.Server = &s
				st.APIRevision = s.APIRevision
			}
		case IsNotRunning(*server) && st.State != StateOnline:
			st.State = StateStopped
			if t.Remote() {
				st.State = StateUnreachable
			}
		}
	}
	if version != nil && version.Kind == ExitOK {
		if v, err := ParseVersion(version.Stdout); err == nil {
			st.Version = &v
			if st.APIRevision == 0 && !t.Remote() {
				st.APIRevision = v.APIRevision
			}
		}
	}
	st.APIMismatch = (st.Server != nil && st.Server.APIRevision != ExpectedAPIRevision) ||
		(st.Version != nil && st.Version.APIRevision != ExpectedAPIRevision)
	return st
}
