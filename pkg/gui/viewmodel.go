package gui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rbobillot/lazyrex/pkg/render"
	"github.com/rbobillot/lazyrex/pkg/rex"
)

// Rows returns one display row per item of a side panel.
func (s *State) Rows(p Panel) []string {
	switch p {
	case PanelTargets:
		return TargetRows(s)
	case PanelSessions:
		return SessionRows(s)
	case PanelWindows:
		return WindowRows(s)
	case PanelBlocks:
		return BlockRows(s)
	case PanelClients:
		return ClientRows(s)
	}
	return nil
}

// FilterIndices returns the indices of the rows containing q, ignoring case.
func FilterIndices(rows []string, q string) []int {
	q = strings.ToLower(q)
	out := make([]int, 0, len(rows))
	for i, r := range rows {
		if q == "" || strings.Contains(strings.ToLower(r), q) {
			out = append(out, i)
		}
	}
	return out
}

func label(s, fallback string) string {
	if l := render.Label(s); l != "" {
		return l
	}
	return fallback
}

// statusText is the target status column: online, stopped, unreachable, or
// api mismatch.
func statusText(st rex.TargetStatus) string {
	if st.State == rex.StateOnline && st.APIMismatch {
		return "online, api mismatch"
	}
	return st.State.String()
}

// checkText is what `rex hosts check` says of a host.
func checkText(c rex.HostCheck) string {
	if c.Unrecognized {
		return label(string(c.Status), "unknown")
	}
	return c.State.String()
}

// rowStatus is the status column of an inactive row: its last probe, else
// its last hosts check.
func rowStatus(r TargetRow) string {
	switch {
	case r.Probing:
		return "checking…"
	case r.Status.State != rex.StateUnknown:
		return statusText(r.Status)
	case r.Check != nil:
		return checkText(*r.Check)
	}
	return "unknown"
}

func TargetRows(s *State) []string {
	count := "-"
	if s.Status.State == rex.StateOnline {
		count = fmt.Sprintf("%d sessions", len(s.Sessions))
	}
	if len(s.Targets) == 0 {
		return []string{fmt.Sprintf("* %s  %s  %s  %s", render.Label(s.TargetName), s.Transport.Name(), statusText(s.Status), count)}
	}
	rows := make([]string, len(s.Targets))
	for i, r := range s.Targets {
		mark, status, n := " ", rowStatus(r), "-"
		if s.isActive(r) {
			mark, status, n = "*", statusText(s.Status), count
		}
		rows[i] = fmt.Sprintf("%s %s  %s  %s  %s", mark, render.Label(r.Target.Name), r.Target.Transport().Name(), status, n)
	}
	return rows
}

// TargetsSubtitle flags a stopped home server or a failed hosts list.
func TargetsSubtitle(s *State) string {
	switch {
	case s.Hosts.HomeStopped:
		return "home server stopped"
	case s.Hosts.Err != "":
		return "hosts: error"
	}
	return ""
}

func SessionRows(s *State) []string {
	rows := make([]string, 0, len(s.Sessions))
	for _, e := range s.Sessions {
		windows, blocks := "?", "?"
		if d := s.Data[e.SessionID]; d != nil {
			windows = fmt.Sprint(len(d.View.Windows))
			n := len(sessionBlocks(e.SessionID, d))
			blocks = fmt.Sprint(n)
		}
		rows = append(rows, fmt.Sprintf("%-20s %3sw %3sb", label(e.Label, string(e.SessionID)), windows, blocks))
	}
	return rows
}

func (s *State) sessionLabel(id rex.SessionID) string {
	for _, e := range s.Sessions {
		if e.SessionID == id {
			return label(e.Label, string(id))
		}
	}
	return string(id)
}

func WindowRows(s *State) []string {
	items := s.WindowItems()
	rows := make([]string, 0, len(items))
	for _, w := range items {
		mark := " "
		if w.Active {
			mark = "*"
		}
		row := fmt.Sprintf("%s %-18s %3db", mark, label(w.Window.Label, string(w.Window.WindowID)), len(w.Window.Blocks()))
		if s.WindowsAll {
			row += "  " + s.sessionLabel(w.Session)
		}
		rows = append(rows, row)
	}
	return rows
}

func BlockRows(s *State) []string {
	items := s.BlockItems()
	rows := make([]string, 0, len(items))
	for _, b := range items {
		cols := []string{
			fmt.Sprintf("%-14s", label(b.Info.Label, string(b.Info.BlockID))),
			fmt.Sprintf("%-10s", render.Label(b.Info.CreatorName)),
		}
		if l := s.Live[b.Info.BlockID]; l != nil {
			if l.State != "" {
				cols = append(cols, l.State)
			}
			if l.LastExit != nil {
				cols = append(cols, fmt.Sprintf("exit %d", *l.LastExit))
			}
			if l.Program != "" {
				cols = append(cols, render.Label(l.Program))
			}
			if l.Title != "" {
				cols = append(cols, render.Label(l.Title))
			}
			if l.Bell {
				cols = append(cols, "bell")
			}
		}
		if b.Detached {
			cols = append(cols, "detached")
		}
		if s.isSelf(b.Info.BlockID) {
			cols = append(cols, "self")
		}
		if s.Scope == ScopeAll {
			cols = append(cols, s.sessionLabel(b.Session))
		}
		rows = append(rows, strings.TrimRight(strings.Join(cols, " "), " "))
	}
	return rows
}

func ClientRows(s *State) []string {
	items := s.ClientItems()
	rows := make([]string, 0, len(items))
	for _, c := range items {
		rows = append(rows, fmt.Sprintf("%-4s %s %s %s %s",
			render.Label(c.Info.Kind), label(c.Info.Name, string(c.ClientID)), render.Label(c.Info.Platform),
			render.Label(c.Info.Version), render.Label(string(c.ConnectionState))))
	}
	return rows
}

// Tabs lists the main-view tabs of a side panel.
func Tabs(p Panel) []string {
	switch p {
	case PanelTargets:
		return []string{"Status", "Keymap & Actions"}
	case PanelBlocks:
		return []string{"Capture", "Inspect"}
	case PanelClients:
		return []string{"Inspect", "Actions"}
	case PanelSessions, PanelWindows:
		return []string{"Inspect"}
	}
	return nil
}

func prettyJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var b bytes.Buffer
	if err := json.Indent(&b, raw, "", "  "); err != nil {
		return render.LogText(string(raw))
	}
	return render.LogText(b.String())
}

type lines struct{ b strings.Builder }

func (l *lines) add(format string, a ...any) { fmt.Fprintf(&l.b, format+"\n", a...) }

func (l *lines) raw(title string, raw []byte) {
	if len(raw) == 0 {
		return
	}
	l.add("")
	l.add("%s", title)
	l.add("%s", prettyJSON(raw))
}

func (l *lines) apiRevisions(st rex.TargetStatus) {
	var parts []string
	if sv := st.Server; sv != nil && sv.APIRevision != 0 {
		parts = append(parts, fmt.Sprintf("server api_revision %d", sv.APIRevision))
	}
	if v := st.Version; v != nil && v.APIRevision != 0 {
		parts = append(parts, fmt.Sprintf("client api_revision %d", v.APIRevision))
	}
	if len(parts) > 0 {
		l.add("%s (lazyrex expects %d)", strings.Join(parts, " / "), rex.ExpectedAPIRevision)
	}
}

func (l *lines) host(h *rex.Host) {
	if h == nil {
		return
	}
	l.add("")
	l.add("rex host      %s", render.Label(h.Label))
	if h.AddedFrom != "" {
		l.add("added from    %s", render.Label(string(h.AddedFrom)))
	}
	if h.Source != "" {
		l.add("source        %s", render.Label(string(h.Source)))
	}
	for _, e := range h.Endpoints {
		l.add("endpoint      %s", render.Label(string(e)))
	}
}

func (l *lines) check(c *rex.HostCheck) {
	if c == nil {
		return
	}
	line := "hosts check   " + checkText(*c)
	if c.Endpoint != "" {
		line += "  " + render.Label(string(c.Endpoint))
	}
	if c.Detail != "" {
		line += "  (" + render.Label(string(c.Detail)) + ")"
	}
	l.add("%s", line)
}

func (l *lines) hosts(h HostsState) {
	var out []string
	switch {
	case h.HomeStopped:
		out = append(out, "home server stopped: discovered hosts are hidden until it runs again")
	case h.Loaded && !h.Home.Reached:
		out = append(out, "home away: hosts as last reached")
	}
	if h.Err != "" {
		out = append(out, "hosts list failed: "+h.Err)
	}
	if h.Skipped+h.Unrecognized+h.Unmatched > 0 {
		out = append(out, fmt.Sprintf("hosts check: %d skipped, %d unrecognized, %d unmatched", h.Skipped, h.Unrecognized, h.Unmatched))
	}
	for _, u := range h.Unlisted {
		out = append(out, "  unrecognized: "+u)
	}
	for _, w := range h.Warnings {
		out = append(out, "hosts warning: "+w)
	}
	if len(out) == 0 {
		return
	}
	l.add("")
	for _, s := range out {
		l.add("%s", s)
	}
}

// renderInactive is the Status tab of a row that is not active: its last
// probe and hosts check, nothing read on selection.
func renderInactive(s *State, r TargetRow) string {
	var l lines
	t := r.Target
	l.add("%s (%s): %s", render.Label(t.Name), t.Transport().Name(), rowStatus(r))
	if t.Kind == rex.TargetNative {
		l.add("server        %s", render.Label(t.Server))
	}
	if t.Kind == rex.TargetSSH {
		l.add("ssh host      %s", render.Label(t.SSH.Host))
	}
	if r.Status.Detail != "" {
		l.add("%s", render.Label(r.Status.Detail))
	}
	if t.Kind == rex.TargetSSH && r.Status.State == rex.StateSSHUnreachable {
		l.add("%s", sshHint(t.SSH.Host))
	}
	if w := r.Status.Whoami; w != nil {
		l.add("endpoint      %s", render.Label(w.Endpoint))
		l.add("rex version   %s", render.Label(w.RexVersion))
		l.add("sessions      %d", w.SessionCount)
	}
	l.apiRevisions(r.Status)
	l.check(r.Check)
	l.host(t.Host)
	l.hosts(s.Hosts)
	l.add("")
	l.add("Not active: enter activates it, c checks it without activating it.")
	return l.b.String()
}

type sshTransport interface {
	Describe() string
	Host() string
}

func sshHint(host string) string {
	h := render.Label(host)
	return fmt.Sprintf(`ssh could not reach %s (network, authentication or host key; BatchMode=yes never prompts). Run "ssh %s" in a terminal, then c checks again.`, h, h)
}

func RenderStatus(s *State) string {
	r, ok := s.SelectedTarget()
	if ok && !s.isActive(r) {
		return renderInactive(s, r)
	}
	var l lines
	st := s.Status
	l.add("%s (%s): %s", render.Label(s.TargetName), s.Transport.Name(), statusText(st))
	if n, native := s.Transport.(rex.Native); native {
		l.add("server        %s", render.Label(n.Server))
	}
	link, ssh := s.Transport.(sshTransport)
	if ssh {
		l.add("via           %s", render.Label(link.Describe()))
	}
	if st.Detail != "" {
		l.add("%s", render.Label(st.Detail))
	}
	switch st.State {
	case rex.StateStopped:
		l.add("")
		if ssh {
			l.add("The Rex server on %s is not running.", render.Label(link.Host()))
			l.add("s starts it over ssh, after confirming the exact command.")
			break
		}
		l.add("The local Rex server is not running. It exits when its last session ends.")
		l.add("Start it with the start server key (s by default).")
	case rex.StateSSHUnreachable:
		if ssh {
			l.add("")
			l.add("%s", sshHint(link.Host()))
		}
	case rex.StateUnreachable:
		if ssh {
			l.add("")
			l.add("ssh reaches %s, but not its Rex server through the tunnel; c checks again.", render.Label(link.Host()))
			break
		}
		if s.Transport.Remote() {
			l.add("")
			l.add("lazyrex cannot reach %s: its server is down or unreachable from here.", render.Label(s.TargetName))
			l.add("A stopped server there cannot be started from here (s is not offered); c checks again.")
		}
	case rex.StateUnknown:
		l.add("checking…")
	}
	if w := st.Whoami; w != nil {
		l.add("")
		l.add("endpoint      %s", render.Label(w.Endpoint))
		l.add("pid           %d", w.PID)
		l.add("principal     %s", render.Label(string(w.Principal)))
		l.add("account       %s", render.Label(string(w.Account)))
		l.add("instance      %s", render.Label(string(w.InstanceID)))
		l.add("started at    %s", render.Label(string(w.StartedAt)))
		l.add("rex version   %s", render.Label(w.RexVersion))
		l.add("sessions      %d", w.SessionCount)
		l.add("exit empty    %t", w.ExitEmpty)
	}
	if sv := st.Server; sv != nil {
		if st.Whoami == nil {
			l.add("endpoint      %s", render.Label(sv.Endpoint))
		}
		l.add("log           %s", render.Label(sv.LogPath))
		l.add("tailscale     %s", render.Label(string(sv.Tailscale.State)))
	}
	if v := st.Version; v != nil {
		l.add("client        rex %s (%s)", render.Label(v.RexVersion), render.Label(v.Platform))
	}
	l.apiRevisions(st)
	if st.APIMismatch {
		l.add("")
		l.add("WARNING: api mismatch, some calls may fail or show partial data.")
	}
	l.add("")
	if s.StreamUp {
		l.add("event stream  up")
	} else {
		l.add("event stream  down (polling)")
	}
	if ok {
		l.check(r.Check)
		l.host(r.Target.Host)
	}
	l.hosts(s.Hosts)
	return l.b.String()
}

func RenderSessionInspect(s *State) string {
	e, ok := s.SelectedSession()
	if !ok {
		return "no session selected"
	}
	d := s.Data[e.SessionID]
	var l lines
	l.add("Session %s (%s)", label(e.Label, "(no label)"), e.SessionID)
	if d == nil {
		l.add("loading…")
		return l.b.String()
	}
	l.add("revision %d", d.View.Revision)
	for _, w := range d.View.Windows {
		mark := " "
		if w.WindowID == d.View.ActiveWindowID {
			mark = "*"
		}
		var names []string
		for _, b := range w.Blocks() {
			names = append(names, label(b.Label, string(b.BlockID)))
		}
		l.add("%s %s (%s): %d blocks %s", mark, label(w.Label, "(no label)"), w.WindowID, len(names), strings.Join(names, ", "))
	}
	for _, b := range sessionBlocks(e.SessionID, d) {
		if b.Detached {
			l.add("  detached: %s (%s)", label(b.Info.Label, "(no label)"), b.Info.BlockID)
		}
	}
	l.raw("session inspect:", d.Raw)
	return l.b.String()
}

func windowRaw(sessionRaw []byte, id rex.WindowID) []byte {
	var v struct {
		Windows []json.RawMessage `json:"windows"`
	}
	if json.Unmarshal(sessionRaw, &v) != nil {
		return nil
	}
	for _, w := range v.Windows {
		var head struct {
			WindowID rex.WindowID `json:"window_id"`
		}
		if json.Unmarshal(w, &head) == nil && head.WindowID == id {
			return w
		}
	}
	return nil
}

func RenderWindowInspect(s *State) string {
	w, ok := s.SelectedWindow()
	if !ok {
		return "no window selected"
	}
	var l lines
	l.add("Window %s (%s)", label(w.Window.Label, "(no label)"), w.Window.WindowID)
	l.add("session %s (%s)", s.sessionLabel(w.Session), w.Session)
	if w.Active {
		l.add("active window")
	}
	if w.Window.FocusedBlockID != "" {
		l.add("focused block %s", render.Label(string(w.Window.FocusedBlockID)))
	}
	for _, layer := range w.Window.Layers {
		l.add("layer %s %s", render.Label(layer.Kind), render.Label(string(layer.LayerID)))
		for _, b := range layer.Blocks {
			l.add("  %s (%s) %s", label(b.Label, "(no label)"), b.BlockID, render.Label(b.CreatorName))
		}
	}
	if d := s.Data[w.Session]; d != nil {
		l.raw("window:", windowRaw(d.Raw, w.Window.WindowID))
	}
	return l.b.String()
}

func processText(p *rex.Process) string {
	if p == nil {
		return ""
	}
	state := p.State
	if state == "" && p.Running != nil {
		state = map[bool]string{true: "running", false: "not running"}[*p.Running]
	}
	if p.LastExit != nil {
		return fmt.Sprintf("process %s, last exit %d", render.Label(state), *p.LastExit)
	}
	return "process " + render.Label(state)
}

func RenderBlockInspect(s *State) string {
	b, ok := s.SelectedBlock()
	if !ok {
		return "no block selected"
	}
	bi := s.BlockInspect
	var l lines
	l.add("Block %s (%s)", label(b.Info.Label, "(no label)"), b.Info.BlockID)
	l.add("session %s, window %s", s.sessionLabel(b.Session), render.Label(string(b.Window)))
	l.add("creator %s", render.Label(b.Info.CreatorName))
	if b.Detached {
		l.add("detached")
	}
	if s.isSelf(b.Info.BlockID) {
		l.add("self: lazyrex runs in this block")
	}
	if bi.Block != b.Info.BlockID {
		l.add("loading…")
		return l.b.String()
	}
	if bi.Err != "" {
		l.add("error: %s", bi.Err)
	}
	if t := processText(bi.Process); t != "" {
		l.add("%s", t)
	}
	if bi.Modes != nil {
		l.add("screen %s", render.Label(bi.Modes.Screen))
	}
	if live := s.Live[b.Info.BlockID]; live != nil {
		if live.Title != "" {
			l.add("title %s", render.Label(live.Title))
		}
		if live.Pwd != "" {
			l.add("pwd %s", render.Label(live.Pwd))
		}
		if live.Program != "" {
			l.add("program status %s", render.Label(live.Program))
		}
	}
	l.raw("block inspect:", bi.Raw)
	l.raw("process:", bi.ProcessRaw)
	l.raw("modes:", bi.ModesRaw)
	return l.b.String()
}

func RenderClientInspect(s *State) string {
	c, ok := s.SelectedClient()
	if !ok {
		return "no client selected"
	}
	var l lines
	l.add("Client %s (%s)", label(c.Info.Name, "(no name)"), c.ClientID)
	l.add("kind %s, platform %s, version %s", render.Label(c.Info.Kind), render.Label(c.Info.Platform), render.Label(c.Info.Version))
	l.add("state %s", render.Label(string(c.ConnectionState)))
	l.add("principal %s", render.Label(string(c.Principal)))
	l.add("connected at %s", render.Label(string(c.ConnectedAt)))
	ci := s.ClientInspect
	switch {
	case ci.Client != c.ClientID:
		l.add("loading…")
	case ci.Err != "":
		l.add("error: %s", ci.Err)
	default:
		l.raw("client inspect:", ci.Raw)
	}
	return l.b.String()
}

func (l *lines) list(title string, items []string) {
	l.add("%s", title)
	if len(items) == 0 {
		l.add("  (none)")
	}
	for _, it := range items {
		l.add("  %s", it)
	}
}

func RenderKeymapActions(s *State) string {
	if r, ok := s.SelectedTarget(); ok && !s.isActive(r) {
		name := render.Label(r.Target.Name)
		return fmt.Sprintf("The keymap and actions of %s are read once it is active: activate %s first (enter).", name, name)
	}
	a := s.ServerActions
	if !a.Loaded {
		return "loading…"
	}
	var l lines
	if a.Err != "" {
		l.add("error: %s", a.Err)
		l.add("")
	}
	l.list("Keymap", a.Keymap)
	l.add("")
	l.list("Server actions (run one from x, or : do NAME)", a.Actions)
	return l.b.String()
}

func RenderClientActions(s *State) string {
	c, ok := s.SelectedClient()
	if !ok {
		return "no client selected"
	}
	a := s.ClientActions
	if !a.Loaded || a.Client != c.ClientID {
		return "loading…"
	}
	var l lines
	if a.Err != "" {
		l.add("error: %s", a.Err)
		return l.b.String()
	}
	l.list(fmt.Sprintf("Actions of %s (%s), run one with a", label(c.Info.Name, "(no name)"), c.ClientID), a.Actions)
	return l.b.String()
}

// Footer is the key hints of the focused panel, then the target and status.
func Footer(s *State, bs []Binding) string {
	var hints []string
	for _, b := range BindingsFor(bs, s.Focus) {
		if b.Footer {
			hints = append(hints, b.Key+" "+b.Desc)
		}
	}
	status := render.Label(s.TargetName) + ": " + statusText(s.Status)
	if len(hints) == 0 {
		return status
	}
	return strings.Join(hints, "  ") + "  |  " + status
}
