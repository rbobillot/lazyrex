package rex

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"
)

type Verdict int

const (
	Allow Verdict = iota
	Confirm
	Deny
)

func (v Verdict) String() string { return [...]string{"allow", "confirm", "deny"}[v] }

type Decision struct {
	Verdict Verdict
	Reason  string
}

// Policy is the one safety layer every mutating call, key, menu, client
// action and palette entry goes through.
type Policy struct {
	// SelfBlock is lazyrex's own block ($REX_BLOCK), empty when unknown.
	SelfBlock    BlockID
	AllowActions []string
	// Labels is shared by every copy of the policy.
	Labels *SelfLabels
}

// SelfLabels is the current label of lazyrex's own block, which Rex accepts
// in place of its ID.
type SelfLabels struct {
	mu    sync.RWMutex
	label string
}

func (l *SelfLabels) Set(s string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.label = strings.TrimSpace(s)
}

func (l *SelfLabels) Get() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.label
}

// NewPolicy validates $REX_BLOCK (only a full block ID protects anything) and
// the allow-list, returning warnings for what it ignores.
func NewPolicy(rexBlock string, allow []string) (Policy, []string) {
	var warnings []string
	p := Policy{AllowActions: append([]string{}, allow...), Labels: &SelfLabels{}}
	if rexBlock != "" {
		if id := BlockID(rexBlock); id.Valid() {
			p.SelfBlock = id
		} else {
			warnings = append(warnings, fmt.Sprintf("REX_BLOCK %q is not a full block ID; ignored", rexBlock))
		}
	}
	for _, name := range p.IgnoredAllow() {
		warnings = append(warnings, fmt.Sprintf("safety.allow_actions: %q can never be allowed; ignored", name))
	}
	return p, warnings
}

// IsSelf reports whether id is lazyrex's own block.
func (p Policy) IsSelf(id string) bool {
	return p.SelfBlock != "" && strings.EqualFold(id, string(p.SelfBlock))
}

var (
	alwaysAllowed = []string{"hosts.list", "server.status", "server.version", "server.whoami", "server.apple.privacy_status"}
	alwaysDenied  = []string{"server.apple.grant_file_access"}
	knownSafe     = []string{"session.view", "session.list", "session.list_blocks", "client.list", "target.resolve",
		"target.complete", "session.new_split", "session.new_window", "session.new_block", "session.resize_pane",
		"session.float_block", "session.select"}
	neverAllowable = map[string]bool{"host": true, "hosts": true, "pair": true, "pairing": true, "unpair": true,
		"tailscale": true, "listen": true, "listener": true}
	updateTokens     = map[string]bool{"update": true, "updates": true, "updater": true, "upgrade": true, "install": true}
	updateReadTokens = map[string]bool{"status": true, "available": true, "info": true, "get": true, "list": true, "show": true, "version": true}
	confirmTokens    = map[string]bool{"close": true, "kill": true, "destroy": true, "stop": true, "send": true, "input": true}
)

// NameTokens splits a method or action name on . _ - / space and camelCase,
// lowercased.
func NameTokens(name string) []string {
	var tokens []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			tokens = append(tokens, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		switch {
		case r == '.' || r == '_' || r == '-' || r == '/' || unicode.IsSpace(r):
			flush()
			continue
		case len(cur) > 0:
			prev := cur[len(cur)-1]
			switch {
			case unicode.IsUpper(r) && unicode.IsLower(prev),
				unicode.IsDigit(r) != unicode.IsDigit(prev),
				unicode.IsUpper(r) && unicode.IsUpper(prev) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return tokens
}

func isUpdateRead(tokens []string) bool {
	sawUpdate := false
	for _, t := range tokens {
		switch {
		case t == "update" || t == "updates":
			sawUpdate = true
		case updateReadTokens[t]:
		default:
			return false
		}
	}
	return sawUpdate
}

func (p Policy) classifyCore(name string, useAllowList bool) Decision {
	lower := strings.ToLower(name)
	if slices.Contains(alwaysAllowed, lower) {
		return Decision{Verdict: Allow}
	}
	if slices.Contains(alwaysDenied, lower) {
		return Decision{Verdict: Deny, Reason: name + " is a denied operation"}
	}
	tokens := NameTokens(name)
	for _, t := range tokens {
		if neverAllowable[t] {
			return Decision{Verdict: Deny, Reason: name + " changes hosts, pairing, listeners or Tailscale"}
		}
	}
	if slices.ContainsFunc(tokens, func(t string) bool { return updateTokens[t] }) && !isUpdateRead(tokens) {
		if !useAllowList || !slices.Contains(p.AllowActions, name) {
			return Decision{Verdict: Deny, Reason: name + " is an update or install operation"}
		}
	}
	if lower == "server.config_reload" || slices.ContainsFunc(tokens, func(t string) bool { return confirmTokens[t] }) {
		return Decision{Verdict: Confirm, Reason: name + " is destructive or sends input"}
	}
	if slices.Contains(knownSafe, lower) {
		return Decision{Verdict: Allow}
	}
	return Decision{Verdict: Confirm, Reason: name + " is not a known operation"}
}

// ClassifyName classifies an API method, `rex do` name or client action.
func (p Policy) ClassifyName(name string) Decision { return p.classifyCore(name, true) }

// IgnoredAllow lists allow-list entries the policy denies regardless.
func (p Policy) IgnoredAllow() []string {
	var out []string
	for _, name := range p.AllowActions {
		if p.classifyCore(name, true).Verdict == Deny {
			out = append(out, name)
		}
	}
	return out
}

// cliCommand returns the command words of args after leading global flags.
func cliCommand(args []string) []string {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-C" || a == "-S" || a == "--client" || a == "--server" || a == "--timeout" || a == "--color":
			i += 2
		case strings.HasPrefix(a, "-"):
			i++
		default:
			return args[i:]
		}
	}
	return nil
}

// IsLocalOnly reports whether cmd checks or reloads a server's config,
// whatever builds it: rex config, or an API method, action or block method
// whose name holds both config and reload.
func IsLocalOnly(c Cmd) bool {
	if c.LocalOnly {
		return true
	}
	if w := cliCommand(c.Args); len(w) > 0 && w[0] == "config" {
		return true
	}
	if c.Kind == KindAPI {
		t := NameTokens(c.Name)
		return slices.Contains(t, "config") && slices.Contains(t, "reload")
	}
	return false
}

// namesServer reports whether args pick a server with -S or --server.
func namesServer(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if strings.HasPrefix(a, "-S") || a == "--server" || strings.HasPrefix(a, "--server=") {
			return true
		}
	}
	return false
}

func cliDenied(args []string) string {
	words := cliCommand(args)
	if len(words) < 2 {
		return ""
	}
	switch {
	case words[0] == "hosts" && slices.Contains([]string{"add", "edit", "remove"}, words[1]):
		return "rex hosts " + words[1] + " changes hosts"
	case words[0] == "server" && words[1] == "tailscale":
		return "rex server tailscale changes Tailscale settings"
	case words[0] == "server" && (words[1] == "start" || words[1] == "run"):
		for _, a := range words[2:] {
			if a == "--" {
				break
			}
			for _, f := range []string{"--extra-listen", "--allow-anonymous", "--tailscale"} {
				if strings.HasPrefix(a, f) {
					return "rex server " + words[1] + " " + f + " adds a listener or changes Tailscale"
				}
			}
		}
	}
	return ""
}

var interruptKeys = []string{"ctrl+c", "ctrl+d", `ctrl+\`, "ctrl+z"}

var targetKeys = []string{"block", "block_id", "pane", "pane_id", "target", "anchor"}

// isTargetKey reports an argument that names a block.
func isTargetKey(key string) bool {
	k := strings.ToLower(key)
	if slices.Contains(targetKeys, k) {
		return true
	}
	for _, suffix := range []string{"_block", "_block_id", "_pane", "_pane_id"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// payloadSelf returns why the arguments of cmd may reach the own block, or "".
// Rex resolves a block by full ID, label or an ID suffix of 4+ characters, and
// a JSON payload can escape any of them, so values are checked decoded.
// Block-bearing keys need a full block ID even when the own block is unknown.
func (p Policy) payloadSelf(cmd Cmd) string {
	for _, v := range cmd.Payload {
		var reason string
		switch {
		case cmd.Kind != KindAPI:
			if p.SelfBlock != "" && strings.Contains(strings.ToLower(v), strings.ToLower(string(p.SelfBlock))) {
				reason = p.namesSelf("")
			}
		case cmd.PayloadKV:
			k, val, _ := strings.Cut(v, "=")
			if isTargetKey(k) {
				reason = p.targetSelf(k, val)
			} else {
				reason = p.valueSelf(val)
			}
		default:
			reason = p.valueSelf(v)
		}
		if reason != "" {
			return reason
		}
	}
	return ""
}

func (p Policy) namesSelf(how string) string {
	return "arguments name lazyrex's own block " + string(p.SelfBlock) + how
}

// targetSelf requires a full block ID other than the own block, so a label
// or suffix that resolves to it after lazyrex last read it cannot slip by.
func (p Policy) targetSelf(key string, v any) string {
	s, ok := v.(string)
	if !ok || !BlockID(s).Valid() {
		reason := fmt.Sprintf("%s must be a full block ID (block:...), not a label, suffix or short ID", key)
		if p.SelfBlock != "" {
			reason += ", while lazyrex runs in block " + string(p.SelfBlock)
		}
		return reason
	}
	if p.IsSelf(s) {
		return p.namesSelf("")
	}
	return ""
}

// valueSelf checks a raw argument as plain text and, when it is JSON, every
// decoded key and value.
func (p Policy) valueSelf(raw string) string {
	if r := p.plainSelf(raw); r != "" {
		return r
	}
	var doc any
	if json.Unmarshal([]byte(raw), &doc) == nil {
		return p.walkSelf(doc)
	}
	return ""
}

func (p Policy) walkSelf(v any) string {
	switch t := v.(type) {
	case string:
		return p.plainSelf(t)
	case []any:
		for _, e := range t {
			if r := p.walkSelf(e); r != "" {
				return r
			}
		}
	case map[string]any:
		for k, e := range t {
			if p.SelfBlock != "" && strings.Contains(strings.ToLower(k), strings.ToLower(string(p.SelfBlock))) {
				return p.namesSelf("")
			}
			var r string
			if isTargetKey(k) {
				r = p.targetSelf(k, e)
			} else {
				r = p.walkSelf(e)
			}
			if r != "" {
				return r
			}
		}
	}
	return ""
}

func (p Policy) plainSelf(s string) string {
	if p.SelfBlock == "" {
		return ""
	}
	lower := strings.ToLower(strings.TrimSpace(s))
	self := strings.ToLower(string(p.SelfBlock))
	switch {
	case strings.Contains(lower, self):
		return p.namesSelf("")
	case lower != "" && strings.EqualFold(lower, p.Labels.Get()):
		return p.namesSelf(" by its label")
	case len(lower) >= 4 && strings.HasSuffix(strings.TrimPrefix(self, "block:"), lower):
		return p.namesSelf(" by an ID suffix")
	}
	return ""
}

// Check returns the verdict for cmd: denied operations are refused outright,
// destructive and unknown ones need confirmation.
func (p Policy) Check(cmd Cmd) Decision {
	if cmd.Err != nil {
		return Decision{Verdict: Deny, Reason: cmd.Err.Error()}
	}
	if reason := cliDenied(cmd.Args); reason != "" {
		return Decision{Verdict: Deny, Reason: reason}
	}
	var named Decision
	if cmd.Kind == KindAPI {
		named = p.ClassifyName(cmd.Name)
		if named.Verdict == Deny {
			return named
		}
	}
	for _, b := range cmd.Blocks {
		if p.IsSelf(string(b)) {
			return Decision{Verdict: Deny, Reason: "targets lazyrex's own block " + string(p.SelfBlock)}
		}
	}
	if reason := p.payloadSelf(cmd); reason != "" {
		return Decision{Verdict: Deny, Reason: reason}
	}
	switch cmd.Kind {
	case KindAPI:
		return named
	case KindDestroy, KindServerStart, KindServerStop:
		return Decision{Verdict: Confirm, Reason: "destructive or disruptive"}
	case KindInput:
		if cmd.Key != "" && slices.Contains(interruptKeys, strings.ToLower(cmd.Key)) {
			return Decision{Verdict: Confirm, Reason: "interrupt key"}
		}
	case KindRaw:
		return Decision{Verdict: Confirm, Reason: "unclassified command"}
	}
	return Decision{Verdict: Allow}
}
