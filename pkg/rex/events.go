package rex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// BlockEvent is the payload of an inner block_event.
type BlockEvent struct {
	BlockID     BlockID
	CreatorName string
	Name        string
	ExitCode    *int
	Error       string
	Status      string
	Reason      string
	Title       string
	Pwd         string
	Running     *bool
	Payload     json.RawMessage
}

// Event is one line of `rex events --all --json`. Inner is the type of the
// event a session_event wraps.
type Event struct {
	Type        string
	SessionID   SessionID
	Inner       string
	Revision    int64
	HasRevision bool
	Block       *BlockEvent
}

type rawEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func str(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		var s string
		if json.Unmarshal(m[k], &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	return m
}

// ParseEvent parses one event line. Unknown types and fields are kept or
// ignored, never an error; only a line that is not a typed JSON object is.
func ParseEvent(line []byte) (Event, error) {
	var outer rawEvent
	if err := json.Unmarshal(line, &outer); err != nil {
		return Event{}, fmt.Errorf("malformed event: %w", err)
	}
	if outer.Type == "" {
		return Event{}, errors.New("malformed event: no type")
	}
	ev := Event{Type: outer.Type}
	if outer.Type != "session_event" {
		return ev, nil
	}
	p := object(outer.Payload)
	ev.SessionID = SessionID(str(p, "session_id"))
	innerRaw := p["event"]
	inner := object(innerRaw)
	ev.Inner = str(inner, "type")
	switch ev.Inner {
	case "session_view_changed":
		for _, m := range []map[string]json.RawMessage{inner, object(inner["payload"])} {
			var rev int64
			if json.Unmarshal(m["revision"], &rev) == nil {
				ev.Revision, ev.HasRevision = rev, true
				break
			}
		}
	case "block_event":
		ev.Block = parseBlockEvent(object(inner["payload"]))
	}
	return ev, nil
}

func parseBlockEvent(p map[string]json.RawMessage) *BlockEvent {
	b := &BlockEvent{
		BlockID:     BlockID(str(p, "block_id")),
		CreatorName: str(p, "creator_name"),
		Name:        str(p, "name"),
		Payload:     p["payload"],
	}
	inner := object(p["payload"])
	if inner == nil {
		return b
	}
	var code int
	if json.Unmarshal(inner["exit_code"], &code) == nil {
		b.ExitCode = &code
	}
	b.Error = str(inner, "error")
	b.Reason = str(inner, "reason")
	b.Title = str(inner, "title")
	b.Pwd = str(inner, "pwd", "cwd", "path")
	var running bool
	if json.Unmarshal(inner["running"], &running) == nil {
		b.Running = &running
	}
	if rec := object(inner["record"]); rec != nil {
		b.Status = str(rec, "state", "status")
	}
	if b.Status == "" {
		b.Status = str(inner, "state", "status")
	}
	if b.Status == "" && b.Name == "program_status_changed" {
		b.Status = b.Reason
	}
	return b
}

// IsClientNoise reports client_* events, outer or inner: every rex call,
// lazyrex's own polls included, emits them, so they never trigger a refresh.
func IsClientNoise(ev Event) bool {
	return strings.HasPrefix(ev.Type, "client_") || strings.HasPrefix(ev.Inner, "client_")
}
