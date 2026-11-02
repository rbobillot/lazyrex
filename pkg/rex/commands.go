package rex

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Kind classifies a command for the safety policy.
type Kind int

const (
	KindRead Kind = iota
	KindMutate
	KindDestroy
	KindInput
	KindServerStart
	KindServerStop
	// KindAPI carries a method, action or block-method name the policy classifies.
	KindAPI
	KindRaw
	KindStream
)

// Redaction hides Args[Index] from displays; ValueOnly keeps the KEY of KEY=VALUE.
type Redaction struct {
	Index     int
	ValueOnly bool
}

// Cmd is one rex invocation, relative to the global flags.
type Cmd struct {
	Args []string
	Kind Kind
	// Blocks are the blocks the command sends to, closes or contains.
	Blocks      []BlockID
	Redact      []Redaction
	NoAutostart bool
	Name        string
	// Payload holds user arguments the policy scans for the own block;
	// PayloadKV marks them as KEY=VALUE pairs rather than JSON or raw text.
	Payload   []string
	PayloadKV bool
	Key       string
	Err       error
	// LocalOnly runs the command on this machine's server only, without
	// REX_SERVER; see IsLocalOnly.
	LocalOnly bool
}

// Touching returns c with ids added to the blocks it affects.
func (c Cmd) Touching(ids ...BlockID) Cmd {
	c.Blocks = append(append([]BlockID{}, c.Blocks...), ids...)
	return c
}

func invalid(format string, a ...any) Cmd { return Cmd{Err: fmt.Errorf(format, a...)} }

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

func checkName(what, s string, allowEmpty bool) error {
	switch {
	case s == "" && !allowEmpty:
		return fmt.Errorf("%s is empty", what)
	case hasControl(s):
		return fmt.Errorf("%s contains control characters", what)
	case strings.HasPrefix(s, "-"):
		return fmt.Errorf("%s must not start with '-'", what)
	}
	return nil
}

func checkWord(what, s string) error {
	if err := checkName(what, s, false); err != nil {
		return err
	}
	if strings.IndexFunc(s, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%s contains whitespace", what)
	}
	return nil
}

func checkSession(s SessionID) error {
	if !s.Valid() {
		return fmt.Errorf("invalid session ID %q", s)
	}
	return nil
}

func checkSB(s SessionID, b BlockID) error {
	if err := checkSession(s); err != nil {
		return err
	}
	if !b.Valid() {
		return fmt.Errorf("invalid block ID %q", b)
	}
	return nil
}

func checkSW(s SessionID, w WindowID) error {
	if err := checkSession(s); err != nil {
		return err
	}
	if !w.Valid() {
		return fmt.Errorf("invalid window ID %q", w)
	}
	return nil
}

func checkDir(dir string) error {
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) || hasControl(dir) {
		return fmt.Errorf("directory must be an absolute path: %q", dir)
	}
	return nil
}

var keyName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+\\\[\]_]*$`)

// Commands builds every rex invocation lazyrex makes.
type Commands struct{}

func (Commands) Version() Cmd      { return Cmd{Args: []string{"version", "--json"}} }
func (Commands) Whoami() Cmd       { return Cmd{Args: []string{"whoami", "--json"}} }
func (Commands) ServerStatus() Cmd { return Cmd{Args: []string{"server", "status", "--json"}} }
func (Commands) Ls() Cmd           { return Cmd{Args: []string{"ls", "--json"}} }
func (Commands) ClientLs() Cmd     { return Cmd{Args: []string{"client", "ls", "--all", "--json"}} }
func (Commands) HostsList() Cmd    { return Cmd{Args: []string{"hosts", "list", "--json"}} }

// HostsCheck checks the reachability of the given hosts, or of every host.
func (Commands) HostsCheck(labels ...string) Cmd {
	for _, l := range labels {
		if err := CheckServer(l); err != nil {
			return invalid("host label: %v", err)
		}
	}
	return Cmd{Args: append(append([]string{"hosts", "check"}, labels...), "--json")}
}

// ConfigCheck has this machine's rex check the config file at path without
// loading it.
func (Commands) ConfigCheck(path string) Cmd {
	if !filepath.IsAbs(path) || hasControl(path) {
		return invalid("config file must be an absolute path: %q", path)
	}
	return Cmd{Args: []string{"config", "check", "--json", path}, LocalOnly: true}
}

// ConfigReload has this machine's server read its config again.
func (Commands) ConfigReload() Cmd {
	return Cmd{Args: []string{"config", "reload", "--json"}, Kind: KindAPI, Name: "server.config_reload", LocalOnly: true}
}
func (Commands) Events() Cmd {
	return Cmd{Args: []string{"events", "--all", "--json"}, Kind: KindStream}
}

// ServerStart is the only call without --autostart=false; it runs only on an
// explicit, confirmed request.
func (Commands) ServerStart() Cmd {
	return Cmd{Args: []string{"server", "start", "--json"}, Kind: KindServerStart, NoAutostart: true}
}

func (Commands) ServerStop() Cmd {
	return Cmd{Args: []string{"server", "stop"}, Kind: KindServerStop}
}

func (Commands) New(name, dir string) Cmd {
	if err := checkName("session name", name, true); err != nil {
		return Cmd{Err: err}
	}
	if err := checkDir(dir); err != nil {
		return Cmd{Err: err}
	}
	args := []string{"new"}
	if name != "" {
		args = append(args, name)
	}
	if dir != "" {
		args = append(args, "--cwd="+dir)
	}
	return Cmd{Args: append(args, "--json"), Kind: KindMutate}
}

func (Commands) SessionInspect(s SessionID) Cmd {
	if err := checkSession(s); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"session", "inspect", string(s), "--json"}}
}

func (Commands) SessionRename(s SessionID, name string) Cmd {
	if err := checkSession(s); err != nil {
		return Cmd{Err: err}
	}
	if err := checkName("session name", name, false); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"session", "rename", string(s), name}, Kind: KindMutate}
}

func (Commands) Kill(s SessionID) Cmd {
	if err := checkSession(s); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"kill", string(s)}, Kind: KindDestroy}
}

func (Commands) BlockLs(s SessionID) Cmd {
	if err := checkSession(s); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"block", "ls", "-s", string(s), "--json"}}
}

func (Commands) WindowNew(s SessionID, name string) Cmd {
	if err := checkSession(s); err != nil {
		return Cmd{Err: err}
	}
	if err := checkName("window name", name, true); err != nil {
		return Cmd{Err: err}
	}
	args := []string{"window", "new", "-s", string(s)}
	if name != "" {
		args = append(args, name)
	}
	return Cmd{Args: append(args, "--focus=false", "--json"), Kind: KindMutate}
}

func (Commands) WindowRename(s SessionID, w WindowID, name string) Cmd {
	if err := checkSW(s, w); err != nil {
		return Cmd{Err: err}
	}
	if err := checkName("window name", name, false); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"window", "rename", "-s", string(s), string(w), name}, Kind: KindMutate}
}

func (Commands) WindowClose(s SessionID, w WindowID) Cmd {
	if err := checkSW(s, w); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"window", "close", "-s", string(s), string(w)}, Kind: KindDestroy}
}

func (Commands) WindowFocus(s SessionID, w WindowID) Cmd {
	if err := checkSW(s, w); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"window", "focus", "-s", string(s), string(w)}, Kind: KindMutate}
}

func (Commands) BlockInspect(s SessionID, b BlockID) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"block", "inspect", "-s", string(s), string(b), "--json"}}
}

// BlockCall calls a block method. process and modes are reads; any other
// method is classified by name and refused on the own block.
func (Commands) BlockCall(s SessionID, b BlockID, method string) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	if err := checkWord("method", method); err != nil {
		return Cmd{Err: err}
	}
	c := Cmd{Args: []string{"block", "call", "-s", string(s), "-b", string(b), method}}
	if method != "process" && method != "modes" {
		c.Kind, c.Name, c.Blocks = KindAPI, method, []BlockID{b}
	}
	return c
}

func (Commands) Capture(s SessionID, b BlockID, format string) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	if format != "vt" && format != "text" {
		return invalid("unsupported capture format %q", format)
	}
	return Cmd{Args: []string{"capture", "-s", string(s), "-b", string(b), "--format=" + format}}
}

// Send sends text verbatim; empty text is refused because rex would read stdin.
func (Commands) Send(s SessionID, b BlockID, text string) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	if text == "" {
		return invalid("nothing to send")
	}
	args := []string{"send", "-s", string(s), "-b", string(b), "--", text}
	return Cmd{Args: args, Kind: KindInput, Blocks: []BlockID{b}, Redact: []Redaction{{Index: len(args) - 1}}}
}

func (Commands) SendKey(s SessionID, b BlockID, key string) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	if !keyName.MatchString(key) {
		return invalid("invalid key %q", key)
	}
	return Cmd{Args: []string{"send-key", "-s", string(s), "-b", string(b), key}, Kind: KindInput, Blocks: []BlockID{b}, Key: key}
}

// SplitOpts configures a new pane.
type SplitOpts struct {
	Dir     string
	Ratio   int
	Label   string
	Cwd     string
	Command []string
}

// RunOpts configures `rex run`.
type RunOpts struct {
	Dir     string
	Ratio   int
	Label   string
	Command []string
}

func paneFlags(dir string, ratio int, label string) ([]string, error) {
	switch dir {
	case "right", "left", "above", "below":
	default:
		return nil, fmt.Errorf("invalid split direction %q", dir)
	}
	flags := []string{"--split=" + dir}
	if ratio != 0 {
		if ratio < 1 || ratio > 99 {
			return nil, fmt.Errorf("ratio must be between 1 and 99, got %d", ratio)
		}
		flags = append(flags, "--ratio="+strconv.Itoa(ratio))
	}
	if label != "" {
		if hasControl(label) {
			return nil, errors.New("label contains control characters")
		}
		flags = append(flags, "--label="+label)
	}
	return flags, nil
}

func (Commands) Split(s SessionID, b BlockID, o SplitOpts) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	flags, err := paneFlags(o.Dir, o.Ratio, o.Label)
	if err != nil {
		return Cmd{Err: err}
	}
	if err := checkDir(o.Cwd); err != nil {
		return Cmd{Err: err}
	}
	args := append([]string{"split", "-s", string(s), "-b", string(b)}, flags...)
	args = append(args, "--focus=false")
	if o.Cwd != "" {
		args = append(args, "--cwd="+o.Cwd)
	}
	args = append(args, "--json")
	if len(o.Command) > 0 {
		args = append(append(args, "--"), o.Command...)
	}
	return Cmd{Args: args, Kind: KindMutate}
}

func (Commands) RunPane(s SessionID, b BlockID, o RunOpts) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	flags, err := paneFlags(o.Dir, o.Ratio, o.Label)
	if err != nil {
		return Cmd{Err: err}
	}
	if len(o.Command) == 0 {
		return invalid("run needs a command")
	}
	args := append([]string{"run", "-s", string(s), "-b", string(b)}, flags...)
	args = append(args, "--focus=false", "--json", "--")
	return Cmd{Args: append(args, o.Command...), Kind: KindMutate}
}

func (Commands) BlockRename(s SessionID, b BlockID, name string) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	if err := checkName("block name", name, false); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"block", "rename", "-s", string(s), string(b), name}, Kind: KindMutate}
}

func (Commands) BlockClose(s SessionID, b BlockID) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"block", "close", "-s", string(s), string(b)}, Kind: KindDestroy, Blocks: []BlockID{b}}
}

func (Commands) BlockFocus(s SessionID, b BlockID) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"block", "focus", "-s", string(s), string(b)}, Kind: KindMutate}
}

func (Commands) Zoom(s SessionID, b BlockID, on bool) Cmd {
	if err := checkSB(s, b); err != nil {
		return Cmd{Err: err}
	}
	flag := "--off"
	if on {
		flag = "--on"
	}
	return Cmd{Args: []string{"zoom", "-s", string(s), "-b", string(b), flag}, Kind: KindMutate}
}

func (Commands) ClientInspect(c ClientID) Cmd {
	if !c.Valid() {
		return invalid("invalid client ID %q", c)
	}
	return Cmd{Args: []string{"client", "inspect", string(c), "--json"}}
}

func (Commands) Keymap() Cmd  { return Cmd{Args: []string{"keymap", "--json"}} }
func (Commands) Actions() Cmd { return Cmd{Args: []string{"actions", "--json"}} }

// ClientActions lists the actions a client offers.
func (Commands) ClientActions(cl ClientID) Cmd {
	if !cl.Valid() {
		return invalid("invalid client ID %q", cl)
	}
	return Cmd{Args: []string{"-C", string(cl), "actions", "--json"}}
}

// APIDescribe reads the schemas of an API method.
func (Commands) APIDescribe(method string) Cmd {
	if err := checkWord("method", method); err != nil {
		return Cmd{Err: err}
	}
	return Cmd{Args: []string{"api", "describe", method, "--json"}}
}

// APIList prints the server's method names, one per line.
func (Commands) APIList() Cmd { return Cmd{Args: []string{"api", "list", "--quiet"}} }

// APICall calls a server API method; the payload is redacted from displays.
// An empty payload sends no arguments.
func (Commands) APICall(s SessionID, method, payload string) Cmd {
	if err := checkWord("method", method); err != nil {
		return Cmd{Err: err}
	}
	args := []string{"api", "call"}
	if s != "" {
		if err := checkSession(s); err != nil {
			return Cmd{Err: err}
		}
		args = append(args, "-s", string(s))
	}
	args = append(args, method)
	if payload == "" {
		return Cmd{Args: args, Kind: KindAPI, Name: method}
	}
	args = append(args, payload)
	return Cmd{Args: args, Kind: KindAPI, Name: method, Payload: []string{payload}, Redact: []Redaction{{Index: len(args) - 1}}}
}

// checkActionName refuses what rex do would run as a Lua script (a path, a
// .lua name, - for stdin) or read as a flag or a file.
func checkActionName(name string) error {
	if err := checkWord("action", name); err != nil {
		return err
	}
	switch {
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("action %q is a script path, not an action name", name)
	case strings.HasSuffix(strings.ToLower(name), ".lua"):
		return fmt.Errorf("action %q is a Lua script, not an action name", name)
	case strings.HasPrefix(name, "@"):
		return fmt.Errorf("action %q must not start with '@'", name)
	}
	return nil
}

func checkArgsObject(args string) error {
	if t := strings.TrimSpace(args); !strings.HasPrefix(t, "{") || !json.Valid([]byte(t)) {
		return errors.New("--args must be a JSON object")
	}
	return nil
}

func doArgs(prefix []string, name string, kv []string) Cmd {
	if err := checkActionName(name); err != nil {
		return Cmd{Err: err}
	}
	args := append(append([]string{}, prefix...), "do", name)
	c := Cmd{Kind: KindAPI, Name: name, PayloadKV: true}
	for _, p := range kv {
		k, _, ok := strings.Cut(p, "=")
		if !ok || checkWord("argument key", k) != nil || strings.HasPrefix(k, "@") {
			return invalid("invalid KEY=VALUE argument")
		}
		args = append(args, p)
		c.Redact = append(c.Redact, Redaction{Index: len(args) - 1, ValueOnly: true})
		c.Payload = append(c.Payload, p)
	}
	c.Args = args
	return c
}

func doJSON(prefix []string, name, args string) Cmd {
	c := doArgs(prefix, name, nil)
	if c.Err != nil {
		return c
	}
	if err := checkArgsObject(args); err != nil {
		return Cmd{Err: err}
	}
	c.Args = append(c.Args, "--args", args)
	c.Redact = []Redaction{{Index: len(c.Args) - 1}}
	c.Payload, c.PayloadKV = []string{args}, false
	return c
}

// Do runs a server action with KEY=VALUE arguments (values redacted).
func (Commands) Do(name string, kv ...string) Cmd { return doArgs(nil, name, kv) }

// DoJSON runs a server action with --args JSON (redacted).
func (Commands) DoJSON(name, args string) Cmd { return doJSON(nil, name, args) }

// ClientDo runs a client action with KEY=VALUE arguments (values redacted).
func (Commands) ClientDo(cl ClientID, name string, kv ...string) Cmd {
	if !cl.Valid() {
		return invalid("invalid client ID %q", cl)
	}
	return doArgs([]string{"-C", string(cl)}, name, kv)
}

// ClientDoJSON runs a client action with --args JSON (redacted).
func (Commands) ClientDoJSON(cl ClientID, name, args string) Cmd {
	if !cl.Valid() {
		return invalid("invalid client ID %q", cl)
	}
	return doJSON([]string{"-C", string(cl)}, name, args)
}

// Raw passes args through the safety policy as an unclassified command.
func (Commands) Raw(args ...string) Cmd {
	if len(args) == 0 {
		return invalid("empty command")
	}
	return Cmd{Args: append([]string{}, args...), Kind: KindRaw, Payload: append([]string{}, args...)}
}
