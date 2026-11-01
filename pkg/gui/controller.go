package gui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rbobillot/lazyrex/pkg/config"
	"github.com/rbobillot/lazyrex/pkg/render"
	"github.com/rbobillot/lazyrex/pkg/rex"
)

// ErrQuit is returned by Dispatch for the quit action.
var ErrQuit = errors.New("quit")

var errTargetChanged = errors.New("the target changed since the action started")

// UI runs work off the UI goroutine (Go) and state changes on it (Update).
type UI interface {
	Update(func())
	Go(func())
}

type Options struct {
	Clock    rex.Clock
	Config   config.Config
	Bindings []Binding
	Warnings []string
	// Targets are the config targets, the local one first. Empty derives
	// them from the client: local, plus its native server if it has one.
	Targets []rex.Target
	// Initial names the first active target; Flag is an ad-hoc --server
	// target missing from Targets.
	Initial string
	Flag    *rex.Target
	// File is the config file Targets come from.
	File config.File
	// StartStream runs a target's event stream until ctx ends; nil runs it
	// on a goroutine.
	StartStream func(ctx context.Context, sup *rex.Supervisor)
	// SSH is what SSH targets need from this machine; nil leaves them in
	// error.
	SSH *rex.SSHEnv
	// SSHConfigHosts are the ~/.ssh/config aliases offered by addTarget.
	SSHConfigHosts []string
}

type action struct {
	// panels is nil for actions available in every panel.
	panels []Panel
	run    func(p Panel)
}

// conn is the connection to the active target: its client, the context of
// its calls and stream, cancelled when another target becomes active, and
// its refresher. An SSH target's client reaches it through link.
type conn struct {
	target    rex.Target
	client    *rex.Client
	link      *rex.SSHLink
	ctx       context.Context
	cancel    context.CancelFunc
	refresher *rex.Refresher
	// statusInflight and statusDirty live on the UI goroutine: a status read
	// of the connection is running, and the largest request made meanwhile.
	statusInflight bool
	statusDirty    statusReq
}

// Controller owns the State: it runs every rex call through the client off
// the UI goroutine and applies the results on it.
type Controller struct {
	st *State
	// client is the base client: a target's client is a copy of it with the
	// target's transport.
	client   *rex.Client
	home     *rex.Client
	ui       UI
	clock    rex.Clock
	cfg      config.Config
	bindings []Binding
	actions  map[string]action
	root     context.Context
	conn     atomic.Pointer[conn]
	started  bool
	stream   func(ctx context.Context, sup *rex.Supervisor)
	// derived names the target reached through client itself.
	derived    string
	cfgTargets []rex.Target
	flag       *rex.Target
	// hosts and checks are the last good hosts list and check entries.
	hosts         []rex.Host
	checks        []rex.HostCheck
	hostsInflight bool
	hostsDirty    bool
	checkedOnce   bool
	file          config.File
	configBusy    bool
	// configEdits names the config entry the running edit changes.
	configEdits string
	// probes numbers the inactive-target probes.
	probes   uint64
	ssh      *rex.SSHEnv
	sshHosts []string
	tunnels  rex.TunnelGroup

	Popups *Popups

	version         atomic.Pointer[rex.Outcome]
	captureFormat   string
	captureInflight bool
	captureDirty    bool
	lastPoll        time.Time
	lastCapture     time.Time
	lastClients     time.Time
	streamSeen      bool
	instance        string
	gen             atomic.Uint64
	pendingSession  rex.SessionID
	quitting        bool
	cache           targetCache
}

var cmds rex.Commands

func NewController(st *State, client *rex.Client, ui UI, o Options) *Controller {
	clock := o.Clock
	if clock == nil {
		clock = rex.RealClock()
	}
	c := &Controller{
		st: st, client: client, ui: ui, clock: clock, cfg: o.Config, bindings: o.Bindings,
		root: context.Background(), captureFormat: o.Config.Capture.Format, stream: o.StartStream,
		cfgTargets: o.Targets, flag: o.Flag, file: o.File, ssh: o.SSH, sshHosts: o.SSHConfigHosts,
	}
	if c.stream == nil {
		c.stream = func(ctx context.Context, sup *rex.Supervisor) { go sup.Run(ctx) }
	}
	c.Popups = NewPopups(clock.Now)
	client.Log = c.logEntry
	c.home = client.With(rex.Local{})
	initial := o.Initial
	if len(c.cfgTargets) == 0 {
		c.cfgTargets = []rex.Target{{Name: rex.LocalName, Kind: rex.TargetLocal, Source: rex.SourceBuiltin}}
		initial = rex.LocalName
		if n, ok := client.Transport.(rex.Native); ok {
			c.flag = &rex.Target{Name: n.Server, Kind: rex.TargetNative, Server: n.Server, Source: rex.SourceFlag}
			initial = n.Server
		}
		c.derived = initial
	}
	all := c.baseTargets()
	i, _ := rex.FindTarget(all, initial)
	t := c.newConn(all[max(i, 0)])
	c.conn.Store(t)
	st.Transport = t.client.Transport
	c.setTarget(t.target.Name, "")
	st.Self = client.Policy.SelfBlock
	for _, w := range o.Warnings {
		st.appendLog("warning: " + render.LogText(w))
	}
	if len(o.Warnings) > 0 {
		c.Popups.Toast("warning: " + o.Warnings[0])
	}
	c.registerActions()
	c.rebuildTargets()
	st.selectID(PanelTargets, t.target.Name)
	return c
}

// baseTargets are the config targets and the --server one.
func (c *Controller) baseTargets() []rex.Target {
	ts := slices.Clone(c.cfgTargets)
	if c.flag != nil {
		if _, ok := rex.FindTarget(ts, c.flag.Name); !ok {
			ts = append(ts, *c.flag)
		}
	}
	return ts
}

func (c *Controller) clientFor(t rex.Target) *rex.Client {
	if c.derived != "" && t.Name == c.derived {
		return c.client
	}
	return c.client.With(t.Transport())
}

func (c *Controller) newConn(t rex.Target) *conn {
	ctx, cancel := context.WithCancel(c.root)
	cn := &conn{target: t, ctx: ctx, cancel: cancel}
	if t.Kind == rex.TargetSSH {
		cn.link = rex.NewSSHLink(ctx, rex.LinkOptions{
			Base: c.client, Name: t.Name, Spec: t.SSH, Env: c.ssh, Clock: c.clock, Group: &c.tunnels,
			OnTunnel: func(bool) { c.onConn(cn, c.checkTarget) },
		})
		cn.client = c.client.With(cn.link)
	} else {
		cn.client = c.clientFor(t)
	}
	cn.refresher = rex.NewRefresher(c.clock, c.cfg.Refresh.Debounce, connRefresh{c, cn})
	return cn
}

// Close ends the tunnels of SSH targets: none starts any more, the active
// connection is cancelled, and Close waits up to 5s for them to end.
func (c *Controller) Close() {
	c.tunnels.Stop()
	c.conn.Load().cancel()
	c.tunnels.Wait(5 * time.Second)
}

// replaceConn makes t the connection of every later call, cancelling the
// calls and stream of the previous one.
func (c *Controller) replaceConn(t *conn) {
	old := c.conn.Swap(t)
	if old != nil && old != t {
		old.cancel()
		old.refresher.Reset()
	}
}

// active returns the client of the active target; it is safe from any
// goroutine.
func (c *Controller) active() *rex.Client { return c.conn.Load().client }

// onConn runs f on the UI goroutine if t is still the active connection.
func (c *Controller) onConn(t *conn, f func()) {
	c.ui.Update(func() {
		if c.conn.Load() == t {
			f()
		}
	})
}

// Start runs the first load and the event stream on ctx; cancelling it
// cancels every call.
func (c *Controller) Start(ctx context.Context) {
	c.root = ctx
	c.replaceConn(c.newConn(c.conn.Load().target))
	c.LoadAll()
	c.started = true
	c.startStream(c.conn.Load())
}

func (c *Controller) startStream(t *conn) {
	c.stream(t.ctx, &rex.Supervisor{
		Client: t.client, Clock: c.clock, Min: time.Second, Max: 30 * time.Second,
		OnEvent:   func(ev rex.Event) { c.handleEvent(t, ev) },
		OnBadLine: func(_ []byte, err error) { c.LogLine("events: skipped a malformed line: " + err.Error()) },
		OnState:   func(s rex.StreamState) { c.streamStateOn(t, s) },
	})
}

func (c *Controller) logEntry(e rex.LogEntry) {
	ts := c.clock.Now().Format("15:04:05")
	var head string
	switch {
	case e.Note != "":
		head = render.LogText(e.Note)
	case e.Refused != "" && e.Display == "":
		head = "refused: " + e.Refused
	case e.Refused != "":
		head = "refused: " + e.Display + " (" + e.Refused + ")"
	case e.Stream:
		head = fmt.Sprintf("%s  (stream ended after %s, exit %d)", e.Display, e.Duration.Round(time.Millisecond), e.Code)
	default:
		head = fmt.Sprintf("%s  (%s, exit %d)", e.Display, e.Duration.Round(time.Millisecond), e.Code)
	}
	lines := []string{ts + " " + head}
	if e.Stderr != "" {
		for _, l := range strings.Split(strings.TrimRight(e.Stderr, "\n"), "\n") {
			lines = append(lines, "    "+l)
		}
	}
	c.ui.Update(func() { c.st.appendLog(lines...) })
}

// LogLine adds a line to the command log from any goroutine.
func (c *Controller) LogLine(s string) {
	line := c.clock.Now().Format("15:04:05") + " " + render.LogText(s)
	c.ui.Update(func() { c.st.appendLog(line) })
}

// Quitting reports that a help-menu entry asked to quit.
func (c *Controller) Quitting() bool { return c.quitting }

// Bindings returns the bindings active and available in panel p.
func (c *Controller) Bindings(p Panel) []Binding {
	var out []Binding
	for _, b := range BindingsFor(c.bindings, p) {
		if c.Available(p, b.Action) {
			out = append(out, b)
		}
	}
	return out
}

// Available reports whether an action is offered in p on this target.
func (c *Controller) Available(p Panel, name string) bool {
	a, ok := c.actions[name]
	if !ok || (a.panels != nil && !slices.Contains(a.panels, p)) {
		return false
	}
	if name == "startServer" {
		if r, ok := c.st.SelectedTarget(); ok {
			return r.Target.Kind != rex.TargetNative
		}
		_, native := c.active().Transport.(rex.Native)
		return !native
	}
	return true
}

// Dispatch runs an action in panel p. It returns ErrQuit for quit and an
// error for an unknown action or one that does not belong to p.
func (c *Controller) Dispatch(p Panel, name string) error {
	a, ok := c.actions[name]
	if !ok {
		return fmt.Errorf("unknown action %q", name)
	}
	if a.panels != nil && !slices.Contains(a.panels, p) {
		return fmt.Errorf("%s is not a %s action", name, p)
	}
	if name == "quit" {
		return ErrQuit
	}
	a.run(p)
	return nil
}

// Focus focuses a panel; a side panel also becomes the main view's source.
func (c *Controller) Focus(p Panel) {
	c.st.Focus = p
	if p.isSide() {
		c.st.Side = p
	}
	c.syncMain()
}

// SelectByID selects the item with id in a side panel.
func (c *Controller) SelectByID(p Panel, id string) bool {
	if !c.st.selectID(p, id) {
		return false
	}
	c.resetChildren(p)
	c.syncMain()
	return true
}

func (c *Controller) resetChildren(p Panel) {
	switch p {
	case PanelSessions:
		c.st.Lists[PanelWindows].Selected = 0
		c.st.Lists[PanelBlocks].Selected = 0
	case PanelWindows:
		c.st.Lists[PanelBlocks].Selected = 0
	}
}

func (c *Controller) registerActions() {
	all := func(f func(Panel)) action { return action{run: f} }
	only := func(f func(Panel), ps ...Panel) action { return action{panels: ps, run: f} }
	noArg := func(f func()) func(Panel) { return func(Panel) { f() } }
	c.actions = map[string]action{
		"quit":           all(func(Panel) {}),
		"back":           all(c.back),
		"help":           all(c.help),
		"filter":         all(c.filter),
		"commandLog":     all(func(Panel) { c.Focus(PanelLog) }),
		"jumpMain":       all(func(Panel) { c.Focus(PanelMain) }),
		"nextPanel":      all(func(Panel) { c.cyclePanel(1) }),
		"prevPanel":      all(func(Panel) { c.cyclePanel(-1) }),
		"down":           all(func(p Panel) { c.move(p, 1) }),
		"up":             all(func(p Panel) { c.move(p, -1) }),
		"pageDown":       all(func(p Panel) { c.move(p, c.page(p)) }),
		"pageUp":         all(func(p Panel) { c.move(p, -c.page(p)) }),
		"bottom":         all(func(p Panel) { c.move(p, 1<<30) }),
		"top":            all(func(p Panel) { c.move(p, -1<<30) }),
		"nextTab":        all(func(Panel) { c.cycleTab(1) }),
		"prevTab":        all(func(Panel) { c.cycleTab(-1) }),
		"scrollDown":     all(func(Panel) { c.st.scrollMain(1) }),
		"scrollUp":       all(func(Panel) { c.st.scrollMain(-1) }),
		"scrollPageDown": all(func(Panel) { c.st.scrollMain(c.st.Main.Height) }),
		"scrollPageUp":   all(func(Panel) { c.st.scrollMain(-c.st.Main.Height) }),
		"refresh":        all(func(Panel) { c.refreshAll(); c.loadMain(); c.refreshHosts() }),
		"menu":           all(c.menu),
		"palette":        all(func(Panel) { c.palette() }),

		"activate":     only(noArg(c.activate), PanelTargets),
		"startServer":  only(noArg(c.startServer), PanelTargets),
		"stopServer":   only(noArg(c.stopServer), PanelTargets),
		"checkTarget":  only(noArg(c.checkSelected), PanelTargets),
		"addTarget":    only(noArg(c.addTarget), PanelTargets),
		"editTarget":   only(noArg(c.editTarget), PanelTargets),
		"removeTarget": only(noArg(c.removeTarget), PanelTargets),

		"scope":         only(c.scope, PanelSessions, PanelWindows),
		"newSession":    only(noArg(c.newSession), PanelSessions),
		"renameSession": only(noArg(c.renameSession), PanelSessions),
		"killSession":   only(noArg(c.killSession), PanelSessions),
		"showInApp":     only(noArg(c.showInApp), PanelSessions),

		"toggleAll":    only(c.toggleAll, PanelWindows, PanelBlocks),
		"focusWindow":  only(noArg(c.focusWindow), PanelWindows),
		"newWindow":    only(noArg(c.newWindow), PanelWindows),
		"renameWindow": only(noArg(c.renameWindow), PanelWindows),
		"closeWindow":  only(noArg(c.closeWindow), PanelWindows),

		"showCapture":   only(noArg(c.showCapture), PanelBlocks),
		"focusBlock":    only(noArg(c.focusBlock), PanelBlocks),
		"toggleZoom":    only(noArg(c.toggleZoom), PanelBlocks),
		"newPane":       only(noArg(c.newPane), PanelBlocks),
		"splitRight":    only(noArg(func() { c.quickSplit("right") }), PanelBlocks),
		"splitBelow":    only(noArg(func() { c.quickSplit("below") }), PanelBlocks),
		"sendText":      only(noArg(func() { c.sendText(false) }), PanelBlocks),
		"sendTextEnter": only(noArg(func() { c.sendText(true) }), PanelBlocks),
		"pressKey":      only(noArg(c.pressKey), PanelBlocks),
		"renameBlock":   only(noArg(c.renameBlock), PanelBlocks),
		"closeBlock":    only(noArg(c.closeBlock), PanelBlocks),

		"showInspect":     only(noArg(c.showInspect), PanelClients),
		"toggleCLI":       only(noArg(c.toggleCLI), PanelClients),
		"runClientAction": only(noArg(c.runClientAction), PanelClients),

		"nextMatch": only(noArg(func() { c.jumpMatch(1) }), PanelMain),
		"prevMatch": only(noArg(func() { c.jumpMatch(-1) }), PanelMain),
	}
	for i, p := range sidePanels {
		c.actions[fmt.Sprintf("jump%d", i+1)] = all(func(Panel) { c.Focus(p) })
	}
}

// Navigation

func (c *Controller) back(p Panel) {
	switch {
	case p.isSide():
		if c.st.Lists[p].Filter != "" {
			c.st.keepSelection(func() { c.st.Lists[p].Filter = "" })
			c.syncMain()
		}
	case p == PanelMain && c.st.Main.Query != "":
		c.st.Main.Query, c.st.Main.Matches, c.st.Main.Match = "", nil, 0
	default:
		c.Focus(c.st.Side)
	}
}

func (c *Controller) help(p Panel) {
	bs := c.Bindings(p)
	labels := HelpItems(bs, p)
	items := make([]MenuItem, len(bs))
	for i, b := range bs {
		items[i] = MenuItem{Label: labels[i], OnSelect: func() {
			if errors.Is(c.Dispatch(p, b.Action), ErrQuit) {
				c.quitting = true
			}
		}}
	}
	c.Popups.Menu("Keybindings: "+p.String(), items)
}

func (c *Controller) filter(p Panel) {
	switch {
	case p.isSide():
		c.Popups.Prompt("Filter "+p.String(), c.st.Lists[p].Filter, func(q string) {
			c.st.Lists[p].Filter = strings.TrimSpace(q)
			c.st.Lists[p].Selected = 0
			c.resetChildren(p)
			c.syncMain()
		})
	case p == PanelMain:
		c.Popups.Prompt("Search", c.st.Main.Query, c.search)
	}
}

func (c *Controller) search(q string) {
	m := &c.st.Main
	m.Query, m.Matches, m.Match = q, FindMatches(m.Content, q), 0
	if len(m.Matches) > 0 {
		m.Origin, m.Follow = m.Matches[0], false
	} else if q != "" {
		c.Popups.Toast("no match for " + q)
	}
}

func (c *Controller) jumpMatch(d int) {
	m := &c.st.Main
	if len(m.Matches) == 0 {
		return
	}
	m.Match = (m.Match + d + len(m.Matches)) % len(m.Matches)
	m.Origin, m.Follow = m.Matches[m.Match], false
}

func (c *Controller) cyclePanel(d int) {
	i := slices.Index(sidePanels, c.st.Side)
	if c.st.Focus.isSide() {
		i = slices.Index(sidePanels, c.st.Focus)
	}
	n := len(sidePanels)
	c.Focus(sidePanels[((i+d)%n+n)%n])
}

func (c *Controller) cycleTab(d int) {
	n := len(Tabs(c.st.Side))
	if n == 0 {
		return
	}
	c.st.Tab[c.st.Side] = ((c.st.Tab[c.st.Side]+d)%n + n) % n
	c.syncMain()
}

func (c *Controller) page(p Panel) int {
	if p == PanelMain {
		return max(c.st.Main.Height, 1)
	}
	return 10
}

func (c *Controller) move(p Panel, d int) {
	switch {
	case p.isSide():
		before := c.st.selectedID(p)
		c.st.move(p, d)
		if c.st.selectedID(p) != before {
			c.resetChildren(p)
		}
		c.syncMain()
	case p == PanelMain:
		c.st.scrollMain(d)
	case p == PanelLog:
		c.st.scrollLog(d)
	}
}

func (c *Controller) scope(p Panel) {
	if p == PanelSessions {
		c.st.Scope = ScopeSession
		c.Focus(PanelWindows)
		return
	}
	c.st.Scope = ScopeWindow
	c.Focus(PanelBlocks)
}

func (c *Controller) toggleAll(p Panel) {
	c.st.keepSelection(func() {
		if p == PanelWindows {
			c.st.WindowsAll = !c.st.WindowsAll
		} else if c.st.Scope == ScopeAll {
			c.st.Scope = ScopeWindow
		} else {
			c.st.Scope = ScopeAll
		}
	})
	c.syncMain()
}

func (c *Controller) toggleCLI() {
	c.st.keepSelection(func() { c.st.ShowCLI = !c.st.ShowCLI })
	c.syncMain()
}

func (c *Controller) showCapture() {
	c.st.Tab[PanelBlocks] = TabCapture
	c.st.Side = PanelBlocks
	c.Focus(PanelMain)
}

func (c *Controller) showInspect() {
	c.st.Tab[PanelClients] = TabClientInspect
	c.st.Side = PanelClients
	c.Focus(PanelMain)
}

// Running commands

// run executes cmd through the policy: refused commands show an error,
// commands needing confirmation show the exact argv first.
func (c *Controller) run(title string, cmd rex.Cmd, onOK func(rex.Outcome)) {
	c.runOn(c.targetGen(), title, cmd, nil, onOK)
}

// runChecked is run where cmd destroys a container: fresh, when set, reads on
// the worker, right before cmd runs, the blocks it would destroy.
func (c *Controller) runChecked(title string, cmd rex.Cmd, fresh func() ([]rex.BlockID, error), onOK func(rex.Outcome)) {
	c.runOn(c.targetGen(), title, cmd, fresh, onOK)
}

// runOn is runChecked for a command built on target generation gen: it is
// refused, now and right before the rex process starts, once that changed.
func (c *Controller) runOn(gen uint64, title string, cmd rex.Cmd, fresh func() ([]rex.BlockID, error), onOK func(rex.Outcome)) {
	p, err := c.active().Prepare(cmd)
	var denied *rex.DeniedError
	switch {
	case errors.As(err, &denied):
		c.refuse(cmd, denied.Reason)
	case gen != c.targetGen():
		c.refuse(cmd, errTargetChanged.Error())
	case p.Decision.Verdict == rex.Confirm:
		if title == "" {
			title = "Confirm"
		}
		c.Popups.Confirm(title, p.Display, func() { c.exec(gen, p, true, fresh, onOK) })
	default:
		c.exec(gen, p, false, fresh, onOK)
	}
}

func (c *Controller) exec(gen uint64, p *rex.Prepared, confirmed bool, fresh func() ([]rex.BlockID, error), onOK func(rex.Outcome)) {
	t := c.conn.Load()
	guard := func() error {
		if c.targetGen() != gen {
			return errTargetChanged
		}
		return nil
	}
	c.ui.Go(func() {
		refuse := func(reason string) {
			t.client.Refuse(p.Cmd, reason)
			c.ui.Update(func() { c.Popups.Error("refused: " + reason) })
		}
		if fresh != nil {
			if err := guard(); err != nil {
				refuse(err.Error())
				return
			}
			ids, err := fresh()
			if err != nil {
				refuse(err.Error())
				return
			}
			p = p.Touching(ids...)
		}
		out, err := t.client.RunPrepared(t.ctx, p, confirmed, guard)
		c.ui.Update(func() {
			if err == nil && c.conn.Load() != t {
				return
			}
			c.handleOutcome(out, err, onOK)
		})
	})
}

func (c *Controller) handleOutcome(out rex.Outcome, err error, onOK func(rex.Outcome)) {
	if err != nil {
		var denied *rex.DeniedError
		if errors.As(err, &denied) {
			c.Popups.Error("refused: " + denied.Reason)
		} else {
			c.Popups.Error(err.Error())
		}
		return
	}
	switch out.Kind {
	case rex.ExitOK:
		if onOK != nil {
			onOK(out)
		}
	case rex.ExitInterrupted:
	case rex.ExitNotFound:
		c.Popups.Toast("not found: " + out.Toast)
		c.refreshAll()
	case rex.ExitUsage:
		c.Popups.Toast("invalid command (lazyrex bug or Rex version drift): " + out.Display + ": " + out.Toast)
	case rex.ExitUnreachable, rex.ExitSSH:
		c.Popups.Toast(out.Toast)
		c.checkTarget()
	default:
		c.Popups.Toast("error: " + out.Toast)
	}
}

func (c *Controller) readFailed(out rex.Outcome, err error) {
	if err != nil {
		c.Popups.Toast("error: " + err.Error())
		return
	}
	switch out.Kind {
	case rex.ExitOK, rex.ExitInterrupted, rex.ExitNotFound:
	case rex.ExitUnreachable, rex.ExitSSH:
		c.checkTarget()
	default:
		c.Popups.Toast("error: " + out.Toast)
	}
}

func errText(out rex.Outcome, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case out.Toast != "":
		return out.Toast
	}
	return fmt.Sprintf("exit %d", out.Code)
}

// read runs a read on the active target.
func (c *Controller) read(cmd rex.Cmd) (rex.Outcome, error) { return readOn(c.conn.Load(), cmd) }

// readOn runs a read on t; once t is no longer active it ends interrupted
// without running rex.
func readOn(t *conn, cmd rex.Cmd) (rex.Outcome, error) { return t.client.Exec(t.ctx, cmd, false) }

func succeeded(out rex.Outcome, err error) bool { return err == nil && out.Kind == rex.ExitOK }

// Target actions

func (c *Controller) startServer() {
	if !c.Available(PanelTargets, "startServer") {
		c.Popups.Error("server start is not offered on a native (-S) target: a stopped server there cannot receive it")
		return
	}
	if !c.requireActive() {
		return
	}
	c.run("Start server", cmds.ServerStart(), func(rex.Outcome) { c.refreshAll() })
}

func (c *Controller) stopServer() {
	if !c.requireActive() {
		return
	}
	c.run("Stop server", cmds.ServerStop(), func(rex.Outcome) { c.checkTarget() })
}

// Session actions

func (c *Controller) newSession() {
	gen := c.targetGen()
	c.Popups.Prompt("New session: name (optional)", "", func(name string) {
		c.Popups.Prompt("New session: working directory (optional)", "", func(dir string) {
			c.runOn(gen, "", cmds.New(strings.TrimSpace(name), strings.TrimSpace(dir)), nil, func(o rex.Outcome) {
				if id, _, err := rex.ParseNew(o.Stdout); err == nil {
					c.pendingSession = id
				}
				c.refreshSessions()
			})
		})
	})
}

func (c *Controller) renameSession() {
	e, ok := c.st.SelectedSession()
	if !ok {
		return
	}
	gen := c.targetGen()
	c.Popups.Prompt("Rename session", render.Label(e.Label), func(name string) {
		if name = strings.TrimSpace(name); name == "" {
			return
		}
		c.runOn(gen, "", cmds.SessionRename(e.SessionID, name), nil, func(rex.Outcome) { c.refreshSessions() })
	})
}

func (c *Controller) sessionBlockIDs(id rex.SessionID) []rex.BlockID {
	var ids []rex.BlockID
	if d := c.st.Data[id]; d != nil {
		for _, b := range sessionBlocks(id, d) {
			ids = append(ids, b.Info.BlockID)
		}
	}
	return ids
}

func (c *Controller) killSession() {
	e, ok := c.st.SelectedSession()
	if !ok {
		return
	}
	cmd := cmds.Kill(e.SessionID).Touching(c.sessionBlockIDs(e.SessionID)...)
	c.runChecked("Kill session "+label(e.Label, string(e.SessionID)), cmd, c.freshBlocks(e.SessionID, ""),
		func(rex.Outcome) { c.refreshSessions() })
}

// freshBlocks reads the blocks of session s, or only of its window w when
// set, when lazyrex knows its own block: the cache may be stale or partial.
// It returns nil without $REX_BLOCK, and an error when the blocks cannot be
// read, so the destroy is refused rather than run blind.
func (c *Controller) freshBlocks(s rex.SessionID, w rex.WindowID) func() ([]rex.BlockID, error) {
	self := c.client.Policy.SelfBlock
	if self == "" {
		return nil
	}
	t := c.conn.Load()
	return func() ([]rex.BlockID, error) {
		unknown := func(why string) error {
			return fmt.Errorf("cannot check that %s does not hold lazyrex's own block %s: %s", s, self, why)
		}
		out, err := readOn(t, cmds.SessionInspect(s))
		if !succeeded(out, err) {
			return nil, unknown("session inspect: " + errText(out, err))
		}
		ids, err := rex.ContainedBlocks(out.Stdout, s, w)
		if err != nil {
			return nil, unknown(err.Error())
		}
		if w != "" {
			return ids, nil
		}
		out, err = readOn(t, cmds.BlockLs(s))
		if !succeeded(out, err) {
			return nil, unknown("block ls: " + errText(out, err))
		}
		listed, err := rex.ListedBlocks(out.Stdout)
		if err != nil {
			return nil, unknown(err.Error())
		}
		return append(ids, listed...), nil
	}
}

// Window actions

func (c *Controller) targetSession() (rex.SessionID, bool) {
	if c.st.WindowsAll {
		if w, ok := c.st.SelectedWindow(); ok {
			return w.Session, true
		}
	}
	e, ok := c.st.SelectedSession()
	return e.SessionID, ok
}

func (c *Controller) newWindow() {
	s, ok := c.targetSession()
	if !ok {
		return
	}
	gen := c.targetGen()
	c.Popups.Prompt("New window: name (optional)", "", func(name string) {
		c.runOn(gen, "", cmds.WindowNew(s, strings.TrimSpace(name)), nil, func(rex.Outcome) { c.refreshSession(s) })
	})
}

func (c *Controller) renameWindow() {
	w, ok := c.st.SelectedWindow()
	if !ok {
		return
	}
	gen := c.targetGen()
	c.Popups.Prompt("Rename window", render.Label(w.Window.Label), func(name string) {
		if name = strings.TrimSpace(name); name == "" {
			return
		}
		c.runOn(gen, "", cmds.WindowRename(w.Session, w.Window.WindowID, name), nil, func(rex.Outcome) { c.refreshSession(w.Session) })
	})
}

func (c *Controller) closeWindow() {
	w, ok := c.st.SelectedWindow()
	if !ok {
		return
	}
	var ids []rex.BlockID
	for _, b := range w.Window.Blocks() {
		ids = append(ids, b.BlockID)
	}
	cmd := cmds.WindowClose(w.Session, w.Window.WindowID).Touching(ids...)
	c.runChecked("Close window "+label(w.Window.Label, string(w.Window.WindowID)), cmd, c.freshBlocks(w.Session, w.Window.WindowID),
		func(rex.Outcome) { c.refreshSession(w.Session) })
}

func (c *Controller) focusWindow() {
	w, ok := c.st.SelectedWindow()
	if !ok {
		return
	}
	c.run("", cmds.WindowFocus(w.Session, w.Window.WindowID), func(rex.Outcome) { c.refreshSession(w.Session) })
}

// Block actions

func (c *Controller) focusBlock() {
	if b, ok := c.st.SelectedBlock(); ok {
		c.run("", cmds.BlockFocus(b.Session, b.Info.BlockID), nil)
	}
}

func (c *Controller) toggleZoom() {
	b, ok := c.st.SelectedBlock()
	if !ok {
		return
	}
	on := !c.st.Zoomed[b.Info.BlockID]
	c.run("", cmds.Zoom(b.Session, b.Info.BlockID, on), func(rex.Outcome) {
		c.st.Zoomed[b.Info.BlockID] = on
		c.refreshSession(b.Session)
	})
}

func (c *Controller) quickSplit(dir string) {
	if b, ok := c.st.SelectedBlock(); ok {
		c.run("", cmds.Split(b.Session, b.Info.BlockID, rex.SplitOpts{Dir: dir}), func(rex.Outcome) { c.refreshSession(b.Session) })
	}
}

func (c *Controller) newPane() {
	b, ok := c.st.SelectedBlock()
	if !ok {
		return
	}
	gen := c.targetGen()
	var items []MenuItem
	for _, dir := range []string{"right", "below", "left", "above"} {
		items = append(items, MenuItem{Label: dir, OnSelect: func() { c.paneForm(b, dir, gen) }})
	}
	c.Popups.Menu("New pane next to "+label(b.Info.Label, string(b.Info.BlockID)), items)
}

func (c *Controller) paneForm(b BlockItem, dir string, gen uint64) {
	c.Popups.Prompt("New pane: command (empty: default shell)", "", func(command string) {
		c.Popups.Prompt("New pane: label (optional)", "", func(lbl string) {
			c.Popups.Prompt("New pane: ratio 1-99 (optional)", "", func(r string) {
				ratio := 0
				if r = strings.TrimSpace(r); r != "" {
					n, err := strconv.Atoi(r)
					if err != nil || n < 1 || n > 99 {
						c.Popups.Error("ratio must be a number between 1 and 99, got " + r)
						return
					}
					ratio = n
				}
				o := rex.SplitOpts{Dir: dir, Ratio: ratio, Label: strings.TrimSpace(lbl), Command: strings.Fields(command)}
				c.runOn(gen, "", cmds.Split(b.Session, b.Info.BlockID, o), nil, func(rex.Outcome) { c.refreshSession(b.Session) })
			})
		})
	})
}

// guardInput refuses before any prompt when input would reach the own block.
func (c *Controller) guardInput(b BlockItem) bool {
	probe := cmds.SendKey(b.Session, b.Info.BlockID, "enter")
	if d := c.active().Decide(probe); d.Verdict == rex.Deny {
		c.active().Refuse(probe, d.Reason)
		c.Popups.Error("refused: " + d.Reason)
		return false
	}
	return true
}

func (c *Controller) afterInput(b BlockItem) {
	if c.captureVisible() {
		if sel, ok := c.st.SelectedBlock(); ok && sel.Info.BlockID == b.Info.BlockID {
			c.RefreshCapture()
		}
	}
}

func (c *Controller) sendText(enter bool) {
	b, ok := c.st.SelectedBlock()
	if !ok || !c.guardInput(b) {
		return
	}
	title := "Send text to " + label(b.Info.Label, string(b.Info.BlockID))
	if enter {
		title += ", then Enter"
	} else {
		title += " (no Enter)"
	}
	gen := c.targetGen()
	c.Popups.Prompt(title, "", func(text string) {
		if text == "" {
			return
		}
		c.runOn(gen, "", cmds.Send(b.Session, b.Info.BlockID, text), nil, func(rex.Outcome) {
			if !enter {
				c.afterInput(b)
				return
			}
			c.runOn(gen, "Send key", cmds.SendKey(b.Session, b.Info.BlockID, "enter"), nil, func(rex.Outcome) { c.afterInput(b) })
		})
	})
}

var menuKeys = []string{"enter", "tab", "esc", "up", "down", "ctrl+c", "ctrl+d", `ctrl+\`, "ctrl+z"}

func (c *Controller) pressKey() {
	b, ok := c.st.SelectedBlock()
	if !ok || !c.guardInput(b) {
		return
	}
	gen := c.targetGen()
	send := func(key string) {
		c.runOn(gen, "Send "+key+" to "+label(b.Info.Label, string(b.Info.BlockID)), cmds.SendKey(b.Session, b.Info.BlockID, key), nil,
			func(rex.Outcome) { c.afterInput(b) })
	}
	var items []MenuItem
	for _, k := range menuKeys {
		items = append(items, MenuItem{Label: k, OnSelect: func() { send(k) }})
	}
	items = append(items, MenuItem{Label: "other…", OnSelect: func() {
		c.Popups.Prompt("Key (e.g. shift+g, f5, ctrl+a)", "", func(k string) {
			if k = strings.TrimSpace(k); k != "" {
				send(k)
			}
		})
	}})
	c.Popups.Menu("Press a key in "+label(b.Info.Label, string(b.Info.BlockID)), items)
}

func (c *Controller) renameBlock() {
	b, ok := c.st.SelectedBlock()
	if !ok {
		return
	}
	gen := c.targetGen()
	c.Popups.Prompt("Rename block", render.Label(b.Info.Label), func(name string) {
		if name = strings.TrimSpace(name); name == "" {
			return
		}
		c.runOn(gen, "", cmds.BlockRename(b.Session, b.Info.BlockID, name), nil, func(rex.Outcome) { c.refreshSession(b.Session) })
	})
}

func (c *Controller) closeBlock() {
	b, ok := c.st.SelectedBlock()
	if !ok {
		return
	}
	c.run("Close block "+label(b.Info.Label, string(b.Info.BlockID)), cmds.BlockClose(b.Session, b.Info.BlockID),
		func(rex.Outcome) { c.refreshSession(b.Session) })
}
