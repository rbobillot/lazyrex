package rex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

func decode[T any](b []byte) (T, error) {
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return v, fmt.Errorf("parse rex output: %w", err)
	}
	return v, nil
}

func ParseVersion(b []byte) (Version, error)           { return decode[Version](b) }
func ParseWhoami(b []byte) (Whoami, error)             { return decode[Whoami](b) }
func ParseServerStatus(b []byte) (ServerStatus, error) { return decode[ServerStatus](b) }
func ParseLs(b []byte) (SessionList, error)            { return decode[SessionList](b) }
func ParseSessionView(b []byte) (SessionView, error)   { return decode[SessionView](b) }
func ParseBlockLs(b []byte) (BlockList, error)         { return decode[BlockList](b) }
func ParseClientLs(b []byte) (ClientList, error)       { return decode[ClientList](b) }
func ParseHostsList(b []byte) (HostsList, error)       { return decode[HostsList](b) }

// ParseHosts reads `rex hosts list --json`, each host on its own so one odd
// entry does not hide the others.
func ParseHosts(b []byte) (HostsView, error) {
	l, err := ParseHostsList(b)
	if err != nil {
		return HostsView{}, err
	}
	v := HostsView{Home: l.Home}
	for _, raw := range l.Hosts {
		var h Host
		if json.Unmarshal(raw, &h) != nil || h.Label == "" {
			v.Skipped++
			continue
		}
		v.Hosts = append(v.Hosts, h)
	}
	for _, raw := range l.Warnings {
		var w Loose
		if json.Unmarshal(raw, &w) == nil && w != "" {
			v.Warnings = append(v.Warnings, string(w))
		}
	}
	return v, nil
}

var checkLists = []string{"hosts", "results", "checks"}

func checkList(doc any) ([]any, error) {
	switch t := doc.(type) {
	case []any:
		return t, nil
	case map[string]any:
		for _, k := range checkLists {
			v, ok := t[k]
			if !ok {
				continue
			}
			switch l := v.(type) {
			case nil:
				return nil, nil
			case []any:
				return l, nil
			}
			return nil, fmt.Errorf("parse hosts check: %s is not a list", k)
		}
		return nil, errors.New("parse hosts check: no hosts, results or checks list")
	}
	return nil, errors.New("parse hosts check: neither an object nor a list")
}

func looseOf(v any) Loose {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return Loose(t)
	}
	b, _ := json.Marshal(v)
	return Loose(b)
}

func firstLoose(m map[string]any, keys ...string) (Loose, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			return looseOf(v), true
		}
	}
	return "", false
}

func checkState(s string) (CheckState, bool) {
	n := strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(strings.ToLower(s))), " ")
	switch n {
	case "reachable":
		return CheckReachable, true
	case "not reachable from here", "not reachable", "unreachable":
		return CheckNotReachable, true
	case "down":
		return CheckDown, true
	case "unknown":
		return CheckUnknown, true
	}
	return CheckUnknown, false
}

// ParseHostsCheck reads `rex hosts check --json`, whose shape is not pinned
// down: {hosts|results|checks: [...]} or a bare list of objects with a label
// (label, name or host), a status (status, state, or a reachable boolean),
// an endpoint and a detail. Entries without a label are counted as skipped.
func ParseHostsCheck(b []byte) (CheckReport, error) {
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return CheckReport{}, fmt.Errorf("parse hosts check: %w", err)
	}
	list, err := checkList(doc)
	if err != nil {
		return CheckReport{}, err
	}
	var r CheckReport
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			r.Skipped++
			continue
		}
		var c HostCheck
		for _, k := range []string{"label", "name", "host"} {
			if s, ok := m[k].(string); ok && s != "" {
				c.Label = s
				break
			}
		}
		if c.Label == "" {
			r.Skipped++
			continue
		}
		if st, ok := firstLoose(m, "status", "state"); ok {
			c.Status = st
		} else if reachable, ok := m["reachable"].(bool); ok {
			c.Status = map[bool]Loose{true: "reachable", false: "not reachable"}[reachable]
		}
		var known bool
		c.State, known = checkState(string(c.Status))
		if !known {
			c.Unrecognized = true
			r.Unrecognized++
		}
		c.Endpoint, _ = firstLoose(m, "endpoint", "address", "addr", "url")
		c.Detail, _ = firstLoose(m, "detail", "error", "reason")
		r.Entries = append(r.Entries, c)
	}
	return r, nil
}

// ParseNew returns the created session and the blocks of its first window.
func ParseNew(b []byte) (SessionID, []BlockID, error) {
	v, err := decode[struct {
		SessionID      SessionID `json:"session_id"`
		InitialWindows []struct {
			BlockIDs []BlockID `json:"block_ids"`
		} `json:"initial_windows"`
	}](b)
	if err != nil {
		return "", nil, err
	}
	if v.SessionID == "" {
		return "", nil, errors.New("parse rex new: no session_id")
	}
	var blocks []BlockID
	if len(v.InitialWindows) > 0 {
		blocks = v.InitialWindows[0].BlockIDs
	}
	return v.SessionID, blocks, nil
}

// ParseActions accepts {actions:[...]} or a bare array.
func ParseActions(b []byte) ([]Action, error) {
	b = bytes.TrimSpace(b)
	var list []Action
	if len(b) > 0 && b[0] == '[' {
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("parse rex actions: %w", err)
		}
	} else {
		v, err := decode[struct {
			Actions []Action `json:"actions"`
		}](b)
		if err != nil {
			return nil, err
		}
		list = v.Actions
	}
	for i := range list {
		if list[i].Name == "" {
			list[i].Name = list[i].ID
		}
	}
	return list, nil
}

// ParseKeymap reads .bindings[].keys as a string or an array of strings.
func ParseKeymap(b []byte) (Keymap, error) {
	v, err := decode[struct {
		Bindings []struct {
			Keys   json.RawMessage `json:"keys"`
			Action Loose           `json:"action"`
		} `json:"bindings"`
	}](b)
	if err != nil {
		return Keymap{}, err
	}
	var k Keymap
	for _, raw := range v.Bindings {
		kb := KeyBinding{Action: raw.Action}
		var one string
		if json.Unmarshal(raw.Keys, &one) == nil {
			kb.Keys = []string{one}
		} else {
			_ = json.Unmarshal(raw.Keys, &kb.Keys)
		}
		k.Bindings = append(k.Bindings, kb)
	}
	return k, nil
}

var methodName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// ParseAPIList keeps the lines of `api list --quiet` that are method names,
// in order and without duplicates.
func ParseAPIList(b []byte) []string {
	var out []string
	for line := range strings.Lines(string(b)) {
		name := strings.TrimSpace(line)
		if methodName.MatchString(name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

var completionLists = []string{"candidates", "completions", "items", "results", "matches", "targets"}

// ParseCompletions reads target.complete output: a list of strings or of
// objects, bare or under one of completionLists, possibly inside "result".
// Candidates that could not be one palette word are dropped.
func ParseCompletions(b []byte) ([]string, error) {
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parse completions: %w", err)
	}
	list, ok := completionList(doc)
	if !ok {
		return nil, errors.New("parse completions: no candidate list")
	}
	var out []string
	for _, e := range list {
		c := candidateText(e)
		if c != "" && !strings.ContainsFunc(c, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`'"\`, r)
		}) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func completionList(doc any) ([]any, bool) {
	switch t := doc.(type) {
	case []any:
		return t, true
	case map[string]any:
		for _, k := range completionLists {
			if l, ok := t[k].([]any); ok {
				return l, true
			}
		}
		if inner, ok := t["result"]; ok {
			return completionList(inner)
		}
	}
	return nil, false
}

func candidateText(e any) string {
	switch t := e.(type) {
	case string:
		return t
	case map[string]any:
		for _, k := range []string{"id", "target", "value", "text", "label"} {
			if s, ok := t[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// unwrapResult descends into a top-level "result" object when there is one.
func unwrapResult(b []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, fmt.Errorf("parse rex block call: %w", err)
	}
	if inner, ok := obj["result"]; ok {
		var r map[string]json.RawMessage
		if json.Unmarshal(inner, &r) == nil {
			return r, nil
		}
	}
	return obj, nil
}

func exitCodeOf(raw json.RawMessage) *int {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return &n
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	for _, k := range []string{"exit_code", "code"} {
		if json.Unmarshal(obj[k], &n) == nil {
			return &n
		}
	}
	return nil
}

// ParseProcess reads the block `process` method: running, state, last_exit.
func ParseProcess(b []byte) (Process, error) {
	obj, err := unwrapResult(b)
	if err != nil {
		return Process{}, err
	}
	var p Process
	var running bool
	if json.Unmarshal(obj["running"], &running) == nil {
		p.Running = &running
	}
	_ = json.Unmarshal(obj["state"], &p.State)
	if p.State == "" && p.Running != nil {
		p.State = "stopped"
		if running {
			p.State = "running"
		}
	}
	p.LastExit = exitCodeOf(obj["last_exit"])
	return p, nil
}

// ParseModes reads the block `modes` method.
func ParseModes(b []byte) (Modes, error) {
	obj, err := unwrapResult(b)
	if err != nil {
		return Modes{}, err
	}
	var m Modes
	_ = json.Unmarshal(obj["screen"], &m.Screen)
	return m, nil
}
