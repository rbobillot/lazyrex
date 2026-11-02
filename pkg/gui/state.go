package gui

import (
	"strings"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

// Panel is a focusable area: the five side panels, the main view and the
// command log.
type Panel int

const (
	PanelTargets Panel = iota
	PanelSessions
	PanelWindows
	PanelBlocks
	PanelClients
	PanelMain
	PanelLog
)

var sidePanels = []Panel{PanelTargets, PanelSessions, PanelWindows, PanelBlocks, PanelClients}

var panelNames = [...]string{"targets", "sessions", "windows", "blocks", "clients", "main", "log"}

func (p Panel) String() string { return panelNames[p] }

func (p Panel) isSide() bool { return p >= PanelTargets && p <= PanelClients }

func viewName(p Panel) string { return panelNames[p] }

// Main-view tabs of the Blocks panel.
const (
	TabCapture = 0
	TabInspect = 1
)

// Main-view tabs of the Targets panel.
const (
	TabStatus = 0
	TabKeymap = 1
)

// Main-view tabs of the Clients panel.
const (
	TabClientInspect = 0
	TabClientActions = 1
)

// BlockScope selects which blocks the Blocks panel lists.
type BlockScope int

const (
	ScopeWindow BlockScope = iota
	ScopeSession
	ScopeAll
)

// BlockLive is block state kept up to date from events.
type BlockLive struct {
	State    string
	Program  string
	Title    string
	Pwd      string
	LastExit *int
	Bell     bool
}

// SessionData is the last applied `session inspect` and `block ls`.
type SessionData struct {
	View      rex.SessionView
	Raw       []byte
	Blocks    []rex.BlockEntry
	HasBlocks bool
}

type ListState struct {
	Selected int
	Filter   string
}

type WindowItem struct {
	Session rex.SessionID
	Window  rex.WindowView
	Active  bool
}

type BlockItem struct {
	Session  rex.SessionID
	Window   rex.WindowID
	Info     rex.BlockInfo
	Detached bool
}

// MainState is the content and scroll position of the main view.
type MainState struct {
	Key     string
	Content string
	Version int
	Origin  int
	Follow  bool
	Height  int
	Query   string
	Matches []int
	Match   int
}

type CaptureState struct {
	Block   rex.BlockID
	Content string
	Err     string
}

type BlockInspectState struct {
	Block      rex.BlockID
	Raw        []byte
	Process    *rex.Process
	ProcessRaw []byte
	Modes      *rex.Modes
	ModesRaw   []byte
	Err        string
}

type ClientInspectState struct {
	Client rex.ClientID
	Raw    []byte
	Err    string
}

// ActionsState is a rendered keymap and action list: the server's on the
// Targets panel, a client's on the Clients panel.
type ActionsState struct {
	Loaded  bool
	Client  rex.ClientID
	Keymap  []string
	Actions []string
	Err     string
}

// TargetRow is one row of the Targets panel. Status is the last probe of an
// inactive target; the active one shows State.Status.
type TargetRow struct {
	Target  rex.Target
	Status  rex.TargetStatus
	Check   *rex.HostCheck
	Probing bool
	probe   uint64 // the probe its Probing waits on
}

// HostsState is what `rex hosts list` and `rex hosts check` last returned.
type HostsState struct {
	Loaded      bool
	HomeStopped bool
	Checking    bool
	Home        rex.HostsHome
	Warnings    []string
	Err         string
	// Skipped, Unrecognized and Unmatched count the entries of the last
	// check: without a label, with an unknown status, matching no row.
	Skipped, Unrecognized, Unmatched int
	// Unlisted are the unrecognized entries matching no row, sanitized.
	Unlisted []string
}

// State is everything the views render. It is only touched on the UI
// goroutine.
type State struct {
	// TargetName is written only through Controller.setTarget, which starts
	// a target generation.
	TargetName string
	Transport  rex.Transport
	Status     rex.TargetStatus
	StreamUp   bool
	Targets    []TargetRow
	Hosts      HostsState
	RexHosts   RexHostsView

	Sessions []rex.SessionEntry
	Data     map[rex.SessionID]*SessionData
	Live     map[rex.BlockID]*BlockLive
	Clients  []rex.ClientEntry
	Zoomed   map[rex.BlockID]bool

	ShowCLI    bool
	WindowsAll bool
	Scope      BlockScope

	Lists [5]ListState
	Focus Panel
	// Side is the side panel whose selection the main view shows.
	Side Panel
	Tab  [5]int

	Capture       CaptureState
	BlockInspect  BlockInspectState
	ClientInspect ClientInspectState
	ServerActions ActionsState
	ClientActions ActionsState
	Main          MainState

	Log       []string
	LogOrigin int
	LogFollow bool
	LogHeight int
	// LogSeq counts appended log lines, trimmed ones included.
	LogSeq int

	ClientsVisible bool
	Self           rex.BlockID
}

func NewState() *State {
	return &State{
		TargetName:     "local",
		Transport:      rex.Local{},
		Data:           map[rex.SessionID]*SessionData{},
		Live:           map[rex.BlockID]*BlockLive{},
		Zoomed:         map[rex.BlockID]bool{},
		Main:           MainState{Follow: true, Height: 20},
		LogFollow:      true,
		LogHeight:      6,
		ClientsVisible: true,
	}
}

const maxLogLines = 5000

func (s *State) appendLog(lines ...string) {
	s.LogSeq += len(lines)
	s.Log = append(s.Log, lines...)
	if over := len(s.Log) - maxLogLines; over > 0 {
		s.Log = append([]string{}, s.Log[over:]...)
	}
}

func (s *State) live(id rex.BlockID) *BlockLive {
	l, ok := s.Live[id]
	if !ok {
		l = &BlockLive{}
		s.Live[id] = l
	}
	return l
}

func (s *State) isSelf(id rex.BlockID) bool {
	return s.Self != "" && strings.EqualFold(string(id), string(s.Self))
}

// WindowItems lists the windows of the selected session, or of every session.
func (s *State) WindowItems() []WindowItem {
	var sessions []rex.SessionID
	if s.WindowsAll {
		for _, e := range s.Sessions {
			sessions = append(sessions, e.SessionID)
		}
	} else if e, ok := s.SelectedSession(); ok {
		sessions = []rex.SessionID{e.SessionID}
	}
	var out []WindowItem
	for _, id := range sessions {
		d := s.Data[id]
		if d == nil {
			continue
		}
		for _, w := range d.View.Windows {
			out = append(out, WindowItem{Session: id, Window: w, Active: w.WindowID == d.View.ActiveWindowID})
		}
	}
	return out
}

func sessionBlocks(id rex.SessionID, d *SessionData) []BlockItem {
	var out []BlockItem
	seen := map[rex.BlockID]bool{}
	for _, w := range d.View.Windows {
		for _, b := range w.Blocks() {
			seen[b.BlockID] = true
			out = append(out, BlockItem{Session: id, Window: w.WindowID, Info: b})
		}
	}
	for _, b := range d.Blocks {
		if !seen[b.BlockID] && b.Detached() {
			out = append(out, BlockItem{Session: id, Window: b.WindowID, Info: b.BlockInfo, Detached: true})
		}
	}
	return out
}

// BlockItems lists the blocks of the current scope.
func (s *State) BlockItems() []BlockItem {
	switch s.Scope {
	case ScopeAll:
		var out []BlockItem
		for _, e := range s.Sessions {
			if d := s.Data[e.SessionID]; d != nil {
				out = append(out, sessionBlocks(e.SessionID, d)...)
			}
		}
		return out
	case ScopeSession:
		e, ok := s.SelectedSession()
		if !ok || s.Data[e.SessionID] == nil {
			return nil
		}
		return sessionBlocks(e.SessionID, s.Data[e.SessionID])
	default:
		w, ok := s.SelectedWindow()
		if !ok {
			return nil
		}
		var out []BlockItem
		for _, b := range w.Window.Blocks() {
			out = append(out, BlockItem{Session: w.Session, Window: w.Window.WindowID, Info: b})
		}
		return out
	}
}

// ClientItems lists clients, without CLI clients unless ShowCLI.
func (s *State) ClientItems() []rex.ClientEntry {
	var out []rex.ClientEntry
	for _, c := range s.Clients {
		if !s.ShowCLI && strings.EqualFold(c.Info.Kind, "cli") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (s *State) itemIDs(p Panel) []string {
	var ids []string
	switch p {
	case PanelTargets:
		for _, r := range s.Targets {
			ids = append(ids, r.Target.Name)
		}
		if len(ids) == 0 {
			ids = []string{s.TargetName}
		}
	case PanelSessions:
		for _, e := range s.Sessions {
			ids = append(ids, string(e.SessionID))
		}
	case PanelWindows:
		for _, w := range s.WindowItems() {
			ids = append(ids, string(w.Window.WindowID))
		}
	case PanelBlocks:
		for _, b := range s.BlockItems() {
			ids = append(ids, string(b.Info.BlockID))
		}
	case PanelClients:
		for _, c := range s.ClientItems() {
			ids = append(ids, string(c.ClientID))
		}
	}
	return ids
}

// Visible returns the indices of the items matching the panel's filter.
func (s *State) Visible(p Panel) []int {
	if !p.isSide() {
		return nil
	}
	return FilterIndices(s.Rows(p), s.Lists[p].Filter)
}

// SelectedIndex returns the item index of the panel's selection.
func (s *State) SelectedIndex(p Panel) (int, bool) {
	vis := s.Visible(p)
	if len(vis) == 0 {
		return 0, false
	}
	sel := min(max(s.Lists[p].Selected, 0), len(vis)-1)
	return vis[sel], true
}

func (s *State) SelectedTarget() (TargetRow, bool) {
	i, ok := s.SelectedIndex(PanelTargets)
	if !ok || i >= len(s.Targets) {
		return TargetRow{}, false
	}
	return s.Targets[i], true
}

// isActive reports whether r is the active target's row.
func (s *State) isActive(r TargetRow) bool { return strings.EqualFold(r.Target.Name, s.TargetName) }

func (s *State) SelectedSession() (rex.SessionEntry, bool) {
	i, ok := s.SelectedIndex(PanelSessions)
	if !ok || i >= len(s.Sessions) {
		return rex.SessionEntry{}, false
	}
	return s.Sessions[i], true
}

func (s *State) SelectedWindow() (WindowItem, bool) {
	i, ok := s.SelectedIndex(PanelWindows)
	items := s.WindowItems()
	if !ok || i >= len(items) {
		return WindowItem{}, false
	}
	return items[i], true
}

func (s *State) SelectedBlock() (BlockItem, bool) {
	i, ok := s.SelectedIndex(PanelBlocks)
	items := s.BlockItems()
	if !ok || i >= len(items) {
		return BlockItem{}, false
	}
	return items[i], true
}

func (s *State) SelectedClient() (rex.ClientEntry, bool) {
	i, ok := s.SelectedIndex(PanelClients)
	items := s.ClientItems()
	if !ok || i >= len(items) {
		return rex.ClientEntry{}, false
	}
	return items[i], true
}

func (s *State) selectedID(p Panel) string {
	i, ok := s.SelectedIndex(p)
	ids := s.itemIDs(p)
	if !ok || i >= len(ids) {
		return ""
	}
	return ids[i]
}

// selectID selects the visible item with id and reports whether it exists.
func (s *State) selectID(p Panel, id string) bool {
	ids := s.itemIDs(p)
	for vi, i := range s.Visible(p) {
		if i < len(ids) && strings.EqualFold(ids[i], id) {
			s.Lists[p].Selected = vi
			return true
		}
	}
	return false
}

func (s *State) clampLists() {
	for _, p := range sidePanels {
		n := len(s.Visible(p))
		s.Lists[p].Selected = min(max(s.Lists[p].Selected, 0), max(n-1, 0))
	}
}

// keepSelection runs f and then reselects the items selected before it,
// falling back to the nearest row.
func (s *State) keepSelection(f func()) {
	var before [5]string
	for _, p := range sidePanels {
		before[p] = s.selectedID(p)
	}
	f()
	for _, p := range sidePanels {
		if before[p] == "" || !s.selectID(p, before[p]) {
			n := len(s.Visible(p))
			s.Lists[p].Selected = min(max(s.Lists[p].Selected, 0), max(n-1, 0))
		}
	}
}

func (s *State) move(p Panel, d int) {
	n := len(s.Visible(p))
	if n == 0 {
		s.Lists[p].Selected = 0
		return
	}
	s.Lists[p].Selected = min(max(s.Lists[p].Selected+d, 0), n-1)
}

func (s *State) mainLines() int {
	if s.Main.Content == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s.Main.Content, "\n"), "\n") + 1
}

// scrollMain moves the main view origin; reaching the bottom resumes follow.
func (s *State) scrollMain(d int) {
	lines := s.mainLines()
	bottom := max(lines-s.Main.Height, 0)
	origin := s.Main.Origin
	if s.Main.Follow {
		origin = bottom
	}
	origin = min(max(origin+d, 0), bottom)
	s.Main.Origin = origin
	s.Main.Follow = origin >= bottom
}

func (s *State) scrollLog(d int) {
	bottom := max(len(s.Log)-s.LogHeight, 0)
	origin := s.LogOrigin
	if s.LogFollow {
		origin = bottom
	}
	origin = min(max(origin+d, 0), bottom)
	s.LogOrigin = origin
	s.LogFollow = origin >= bottom
}
