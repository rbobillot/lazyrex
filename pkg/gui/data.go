package gui

import (
	"fmt"
	"strings"

	"github.com/rbobillot/lazyrex/pkg/render"
	"github.com/rbobillot/lazyrex/pkg/rex"
)

type sessionResult struct {
	id        rex.SessionID
	view      rex.SessionView
	raw       []byte
	blocks    []rex.BlockEntry
	hasBlocks bool
	ok        bool
}

func (r sessionResult) data() *SessionData {
	return &SessionData{View: r.view, Raw: r.raw, Blocks: r.blocks, HasBlocks: r.hasBlocks}
}

// fetchSession runs `session inspect` and `block ls` for one session. It runs
// off the UI goroutine.
func (c *Controller) fetchSession(t *conn, id rex.SessionID) (sessionResult, rex.Outcome, error) {
	r := sessionResult{id: id}
	out, err := readOn(t, cmds.SessionInspect(id))
	if !succeeded(out, err) {
		return r, out, err
	}
	view, perr := rex.ParseSessionView(out.Stdout)
	if perr != nil {
		return r, out, fmt.Errorf("parse session inspect: %w", perr)
	}
	r.view, r.raw, r.ok = view, out.Stdout, true
	if bo, err := readOn(t, cmds.BlockLs(id)); succeeded(bo, err) {
		if bl, err := rex.ParseBlockLs(bo.Stdout); err == nil {
			r.blocks, r.hasBlocks = bl.Blocks, true
		}
	}
	return r, out, nil
}

// statusWork reads the status of t. On the UI goroutine, newest reports
// whether it is still the newest status read of t to land.
func (c *Controller) statusWork(t *conn) (st rex.TargetStatus, newest func() bool) {
	if t.link != nil {
		g := t.link.BeginStatus()
		return t.link.StatusAt(t.ctx, g, c.version.Load()), func() bool { return t.link.ApplyStatus(g) }
	}
	whoami, _ := readOn(t, cmds.Whoami())
	server, _ := readOn(t, cmds.ServerStatus())
	return rex.DeriveStatus(t.client.Transport, whoami, &server, c.version.Load()), func() bool { return true }
}

// statusReq is what a status read is for; a larger one covers a smaller one.
type statusReq int

const (
	statusNone statusReq = iota
	// statusCheck reads the sessions and clients once the target comes online.
	statusCheck
	// statusRefresh reads them whenever the target is online.
	statusRefresh
)

// LoadAll reads the client version once, then refreshes everything.
func (c *Controller) LoadAll() {
	c.markPolled()
	t, ctx := c.conn.Load(), c.root
	c.ui.Go(func() {
		if out, err := c.home.Exec(ctx, cmds.Version(), false); err == nil {
			c.version.Store(&out)
		}
		c.onConn(t, func() { c.readStatus(t, statusRefresh) })
	})
	c.refreshHosts()
}

func (c *Controller) markPolled() {
	now := c.clock.Now()
	c.lastPoll, c.lastClients = now, now
}

// refreshAll re-reads the target status, the sessions and the clients.
func (c *Controller) refreshAll() {
	c.markPolled()
	c.readStatus(c.conn.Load(), statusRefresh)
}

// checkTarget re-reads the active target's status.
func (c *Controller) checkTarget() { c.readStatus(c.conn.Load(), statusCheck) }

// readStatus reads t's status, one read at a time, on the UI goroutine: a
// request during a read runs once that read lands, as the largest of the
// requests made meanwhile.
func (c *Controller) readStatus(t *conn, req statusReq) {
	if t.statusInflight {
		t.statusDirty = max(t.statusDirty, req)
		return
	}
	t.statusInflight = true
	c.ui.Go(func() { c.statusRead(t, req) })
}

func (c *Controller) statusRead(t *conn, req statusReq) {
	st, newest := c.statusWork(t)
	c.onConn(t, func() {
		t.statusInflight = false
		if newest() && c.applyStatus(st) && req == statusCheck {
			c.refreshSessions()
			c.refreshClients()
		}
		if next := t.statusDirty; next != statusNone {
			t.statusDirty = statusNone
			c.readStatus(t, next)
		}
	})
	if req == statusRefresh && st.State == rex.StateOnline {
		c.sessionsWork(t)
		c.clientsWork(t)
	}
}

// applyStatus records a target status and reports whether the target just
// came online. Leaving online drops the target's data and revisions, and
// starts a target generation, as does another server instance.
func (c *Controller) applyStatus(st rex.TargetStatus) bool {
	was := c.st.Status.State
	inst := ""
	if st.Whoami != nil {
		inst = string(st.Whoami.InstanceID)
	}
	if st.State != rex.StateOnline || (inst != "" && c.instance != "" && inst != c.instance) {
		c.conn.Load().refresher.Reset()
		c.cache = targetCache{}
	}
	if inst != "" {
		c.setTarget(c.st.TargetName, inst)
	}
	if st.State == rex.StateUnknown {
		return false
	}
	if was == rex.StateOnline && st.State != rex.StateOnline {
		c.gen.Add(1)
	}
	c.st.Status = st
	if st.State != rex.StateOnline {
		c.st.keepSelection(func() {
			c.st.Sessions, c.st.Clients = nil, nil
			c.st.Data = map[rex.SessionID]*SessionData{}
			c.st.Live = map[rex.BlockID]*BlockLive{}
		})
	}
	c.syncMain()
	online := st.State == rex.StateOnline && was != rex.StateOnline
	if online && c.conn.Load().target.Kind == rex.TargetLocal && c.st.Hosts.HomeStopped {
		c.refreshHosts()
	}
	return online
}

func (c *Controller) refreshSessions() {
	t := c.conn.Load()
	c.ui.Go(func() { c.sessionsWork(t) })
}

func (c *Controller) sessionsWork(t *conn) {
	out, err := readOn(t, cmds.Ls())
	if !succeeded(out, err) {
		c.onConn(t, func() { c.readFailed(out, err) })
		return
	}
	list, perr := rex.ParseLs(out.Stdout)
	if perr != nil {
		c.onConn(t, func() { c.Popups.Toast("error: parse ls: " + perr.Error()) })
		return
	}
	var entries []rex.SessionEntry
	var results []sessionResult
	for _, e := range list.Sessions {
		if !e.SessionID.Valid() {
			continue
		}
		entries = append(entries, e)
		r, _, _ := c.fetchSession(t, e.SessionID)
		results = append(results, r)
	}
	c.onConn(t, func() {
		if c.st.Status.State == rex.StateOnline {
			c.applySessions(entries, results)
		}
	})
}

func (c *Controller) applySessions(entries []rex.SessionEntry, results []sessionResult) {
	c.st.keepSelection(func() {
		data := map[rex.SessionID]*SessionData{}
		for _, e := range entries {
			if d, ok := c.st.Data[e.SessionID]; ok {
				data[e.SessionID] = d
			}
		}
		for _, r := range results {
			if r.ok && c.conn.Load().refresher.Accept(r.id, r.view.Revision) {
				data[r.id] = r.data()
			}
		}
		c.st.Sessions, c.st.Data = entries, data
	})
	c.trackSelfLabel()
	if c.pendingSession != "" {
		if c.st.selectID(PanelSessions, string(c.pendingSession)) {
			c.resetChildren(PanelSessions)
		}
		c.pendingSession = ""
	}
	c.syncMain()
}

func (c *Controller) hasSession(id rex.SessionID) bool {
	for _, e := range c.st.Sessions {
		if e.SessionID == id {
			return true
		}
	}
	return false
}

func (c *Controller) applySession(r sessionResult) {
	if !c.hasSession(r.id) {
		return
	}
	c.st.keepSelection(func() { c.st.Data[r.id] = r.data() })
	c.trackSelfLabel()
	c.syncMain()
}

// trackSelfLabel gives the policy the own block's current label, which Rex
// resolves like its ID. A block no longer listed keeps its last label.
func (c *Controller) trackSelfLabel() {
	if c.st.Self == "" {
		return
	}
	for id, d := range c.st.Data {
		for _, b := range sessionBlocks(id, d) {
			if c.st.isSelf(b.Info.BlockID) {
				c.client.Policy.Labels.Set(b.Info.Label)
				return
			}
		}
	}
}

// refreshSession re-reads one session after a mutation.
func (c *Controller) refreshSession(id rex.SessionID) {
	t := c.conn.Load()
	c.ui.Go(func() {
		r, out, err := c.fetchSession(t, id)
		c.onConn(t, func() {
			if !r.ok {
				c.readFailed(out, err)
				return
			}
			if t.refresher.Accept(id, r.view.Revision) {
				c.applySession(r)
			}
		})
	})
}

func (c *Controller) refreshClients() {
	c.lastClients = c.clock.Now()
	t := c.conn.Load()
	c.ui.Go(func() { c.clientsWork(t) })
}

func (c *Controller) clientsWork(t *conn) {
	out, err := readOn(t, cmds.ClientLs())
	if !succeeded(out, err) {
		c.onConn(t, func() { c.readFailed(out, err) })
		return
	}
	list, perr := rex.ParseClientLs(out.Stdout)
	c.onConn(t, func() {
		if perr != nil {
			c.Popups.Toast("error: parse client ls: " + perr.Error())
			return
		}
		if c.st.Status.State != rex.StateOnline {
			return
		}
		c.st.keepSelection(func() { c.st.Clients = list.Clients })
		c.syncMain()
	})
}

// connRefresh runs the refreshes of t's refresher while t is active.
type connRefresh struct {
	c *Controller
	t *conn
}

func (r connRefresh) RefreshSessions() { r.c.onConn(r.t, r.c.refreshSessions) }

func (r connRefresh) InspectSession(id rex.SessionID, done rex.InspectDone) {
	r.c.onConn(r.t, func() { r.c.inspectSession(r.t, id, done) })
}

func (r connRefresh) BlockEvent(_ rex.SessionID, ev rex.BlockEvent) {
	r.c.onConn(r.t, func() { r.c.blockEvent(ev) })
}

func (c *Controller) inspectSession(t *conn, id rex.SessionID, done rex.InspectDone) {
	c.ui.Go(func() {
		r, out, err := c.fetchSession(t, id)
		c.onConn(t, func() {
			if done(r.view.Revision, r.ok) {
				c.applySession(r)
			} else if !r.ok {
				c.readFailed(out, err)
			}
		})
	})
}

// BlockEvent updates a block row in place and the selected block's views.
func (c *Controller) BlockEvent(_ rex.SessionID, ev rex.BlockEvent) {
	c.ui.Update(func() { c.blockEvent(ev) })
}

func (c *Controller) blockEvent(ev rex.BlockEvent) {
	if !ev.BlockID.Valid() {
		return
	}
	l := c.st.live(ev.BlockID)
	inspect := false
	switch ev.Name {
	case "child_exited":
		l.State = "exited"
		if ev.ExitCode != nil {
			l.LastExit = ev.ExitCode
		}
		inspect = true
	case "process_changed":
		if ev.Running != nil {
			l.State = map[bool]string{true: "running", false: "exited"}[*ev.Running]
		}
		if ev.ExitCode != nil {
			l.LastExit = ev.ExitCode
		}
		inspect = true
	case "title_changed":
		l.Title = ev.Title
	case "pwd_changed":
		l.Pwd = ev.Pwd
	case "bell":
		l.Bell = true
	case "program_status_changed":
		l.Program = ev.Status
	case "program_status_removed":
		l.Program = ""
	}
	sel, ok := c.st.SelectedBlock()
	if !ok || sel.Info.BlockID != ev.BlockID {
		return
	}
	switch {
	case c.captureVisible():
		c.RefreshCapture()
	case inspect && c.st.Side == PanelBlocks && c.st.Tab[PanelBlocks] == TabInspect:
		c.loadBlockInspect()
	default:
		c.renderMain()
	}
}

// HandleEvent routes one event of the active target's stream; it is safe
// from any goroutine.
func (c *Controller) HandleEvent(ev rex.Event) { c.handleEvent(c.conn.Load(), ev) }

// handleEvent routes an event of t's stream while t is active.
func (c *Controller) handleEvent(t *conn, ev rex.Event) {
	c.onConn(t, func() {
		if ev.Type == "hosts_changed" {
			c.hostsDirty = true
		}
		t.refresher.Handle(ev)
	})
}

// StreamState records the active target's event stream going up or down.
func (c *Controller) StreamState(s rex.StreamState) { c.streamStateOn(c.conn.Load(), s) }

func (c *Controller) streamStateOn(t *conn, s rex.StreamState) {
	c.onConn(t, func() {
		if s.Up {
			resync := c.streamSeen || c.st.Status.State != rex.StateOnline
			c.st.StreamUp, c.streamSeen = true, true
			if resync {
				c.refreshAll()
			}
			c.syncMain()
			return
		}
		c.st.StreamUp = false
		detail := rex.ToastLine([]byte(s.Stderr))
		switch s.Kind {
		case rex.ExitUnreachable:
			switch {
			case t.link != nil && s.Remote:
				// Exit 4 through the tunnel may be the tunnel or the server.
				c.checkTarget()
			case t.link == nil && t.client.Transport.Remote():
				c.applyStatus(rex.TargetStatus{State: rex.StateUnreachable, Detail: detail})
			default:
				c.applyStatus(rex.TargetStatus{State: rex.StateStopped, Detail: detail})
			}
		case rex.ExitSSH:
			c.applyStatus(rex.TargetStatus{State: rex.StateSSHUnreachable, Detail: detail})
		}
		c.syncMain()
	})
}

// Tick runs the timed refreshes: polling while no stream is up, the visible
// capture, and the visible Clients panel.
func (c *Controller) Tick() {
	now := c.clock.Now()
	poll := c.cfg.Refresh.PollInterval
	if !c.st.StreamUp && now.Sub(c.lastPoll) >= poll {
		c.refreshAll()
	}
	if c.captureVisible() && now.Sub(c.lastCapture) >= c.cfg.Refresh.CaptureInterval {
		c.RefreshCapture()
	}
	if c.st.ClientsVisible && c.st.Status.State == rex.StateOnline && now.Sub(c.lastClients) >= poll {
		c.refreshClients()
	}
	if c.hostsDirty && !c.hostsInflight {
		c.hostsDirty = false
		c.refreshHosts()
	}
}

// Main view

func (c *Controller) captureVisible() bool {
	return c.st.Side == PanelBlocks && c.st.Tab[PanelBlocks] == TabCapture
}

func (c *Controller) mainKey() string {
	return fmt.Sprintf("%d/%d/%s", c.st.Side, c.st.Tab[c.st.Side], c.st.selectedID(c.st.Side))
}

// syncMain reloads the main view when its panel, tab or selection changed,
// and re-renders it.
func (c *Controller) syncMain() {
	if key := c.mainKey(); key != c.st.Main.Key {
		m := &c.st.Main
		m.Key, m.Origin, m.Follow = key, 0, c.captureVisible()
		m.Query, m.Matches, m.Match = "", nil, 0
		c.loadMain()
	}
	c.renderMain()
}

func (c *Controller) loadMain() {
	switch {
	case c.captureVisible():
		c.RefreshCapture()
	case c.st.Side == PanelBlocks:
		c.loadBlockInspect()
	case c.st.Side == PanelTargets && c.st.Tab[PanelTargets] == TabKeymap:
		if _, inactive := c.selectedInactive(); !inactive {
			c.loadServerActions()
		}
	case c.st.Side == PanelClients && c.st.Tab[PanelClients] == TabClientActions:
		c.loadClientActions()
	case c.st.Side == PanelClients:
		c.loadClientInspect()
	}
}

func (c *Controller) mainContent() string {
	switch c.st.Side {
	case PanelTargets:
		if c.st.Tab[PanelTargets] == TabKeymap {
			return RenderKeymapActions(c.st)
		}
		return RenderStatus(c.st)
	case PanelSessions:
		return RenderSessionInspect(c.st)
	case PanelWindows:
		return RenderWindowInspect(c.st)
	case PanelClients:
		if c.st.Tab[PanelClients] == TabClientActions {
			return RenderClientActions(c.st)
		}
		return RenderClientInspect(c.st)
	case PanelBlocks:
		if c.st.Tab[PanelBlocks] == TabInspect {
			return RenderBlockInspect(c.st)
		}
		b, ok := c.st.SelectedBlock()
		switch cs := c.st.Capture; {
		case !ok:
			return "no block selected"
		case cs.Block != b.Info.BlockID:
			return "loading…"
		case cs.Err != "":
			return "capture failed: " + cs.Err
		default:
			return cs.Content
		}
	}
	return ""
}

func (c *Controller) renderMain() {
	content := c.mainContent()
	m := &c.st.Main
	if content == m.Content {
		return
	}
	m.Content = content
	m.Version++
	if m.Query != "" {
		m.Matches = FindMatches(content, m.Query)
		m.Match = min(m.Match, max(len(m.Matches)-1, 0))
	}
}

// RefreshCapture re-reads the selected block's capture and screen mode. A
// request during an in-flight capture runs once it ends.
func (c *Controller) RefreshCapture() {
	b, ok := c.st.SelectedBlock()
	if !ok {
		c.st.Capture = CaptureState{}
		c.renderMain()
		return
	}
	if c.captureInflight {
		c.captureDirty = true
		return
	}
	c.captureInflight = true
	c.lastCapture = c.clock.Now()
	s, id, format := b.Session, b.Info.BlockID, c.captureFormat
	maxLines := c.cfg.Capture.MaxLines
	t := c.conn.Load()
	c.ui.Go(func() {
		alt := false
		if mo, err := readOn(t, cmds.BlockCall(s, id, "modes")); succeeded(mo, err) {
			if m, err := rex.ParseModes(mo.Stdout); err == nil {
				alt = m.AltScreen()
			}
		}
		out, err := readOn(t, cmds.Capture(s, id, format))
		fellBack := false
		if err == nil && out.Kind == rex.ExitUsage && format == "vt" {
			out, err = readOn(t, cmds.Capture(s, id, "text"))
			fellBack = true
		}
		c.onConn(t, func() {
			c.captureInflight = false
			ok := succeeded(out, err)
			if fellBack && ok {
				c.captureFormat = "text"
				c.st.appendLog("capture: --format=vt unsupported, using --format=text from now on")
			}
			if err == nil && out.Kind == rex.ExitInterrupted {
				return
			}
			cs := CaptureState{Block: id}
			if ok {
				cs.Content = RenderCapture(out.Stdout, maxLines, alt)
			} else {
				cs.Err = errText(out, err)
			}
			if sel, ok := c.st.SelectedBlock(); ok && sel.Info.BlockID == id {
				c.st.Capture = cs
			}
			c.renderMain()
			if c.captureDirty {
				c.captureDirty = false
				if c.captureVisible() {
					c.RefreshCapture()
				}
			}
		})
	})
}

func (c *Controller) loadBlockInspect() {
	b, ok := c.st.SelectedBlock()
	if !ok {
		c.st.BlockInspect = BlockInspectState{}
		c.renderMain()
		return
	}
	s, id := b.Session, b.Info.BlockID
	t := c.conn.Load()
	c.ui.Go(func() {
		r := BlockInspectState{Block: id}
		var errs []string
		if out, err := readOn(t, cmds.BlockInspect(s, id)); succeeded(out, err) {
			r.Raw = out.Stdout
		} else {
			errs = append(errs, "inspect: "+errText(out, err))
		}
		if out, err := readOn(t, cmds.BlockCall(s, id, "process")); succeeded(out, err) {
			if p, err := rex.ParseProcess(out.Stdout); err == nil {
				r.Process, r.ProcessRaw = &p, out.Stdout
			}
		} else {
			errs = append(errs, "process: "+errText(out, err))
		}
		if out, err := readOn(t, cmds.BlockCall(s, id, "modes")); succeeded(out, err) {
			if m, err := rex.ParseModes(out.Stdout); err == nil {
				r.Modes, r.ModesRaw = &m, out.Stdout
			}
		} else {
			errs = append(errs, "modes: "+errText(out, err))
		}
		r.Err = strings.Join(errs, "; ")
		c.onConn(t, func() {
			if sel, ok := c.st.SelectedBlock(); !ok || sel.Info.BlockID != id {
				return
			}
			c.st.BlockInspect = r
			if p := r.Process; p != nil {
				l := c.st.live(id)
				if p.State != "" {
					l.State = render.Label(p.State)
				} else if p.Running != nil {
					l.State = map[bool]string{true: "running", false: "exited"}[*p.Running]
				}
				if p.LastExit != nil {
					l.LastExit = p.LastExit
				}
			}
			c.renderMain()
		})
	})
}

func (c *Controller) loadServerActions() {
	t := c.conn.Load()
	c.ui.Go(func() {
		ko, kerr := readOn(t, cmds.Keymap())
		ao, aerr := readOn(t, cmds.Actions())
		c.onConn(t, func() {
			r := ActionsState{Loaded: true}
			var errs []string
			if succeeded(ko, kerr) {
				if km, err := rex.ParseKeymap(ko.Stdout); err == nil {
					for _, b := range km.Bindings {
						r.Keymap = append(r.Keymap, fmt.Sprintf("%-16s %s", render.Label(strings.Join(b.Keys, " ")), render.Label(string(b.Action))))
					}
				} else {
					errs = append(errs, "keymap: "+err.Error())
				}
			} else {
				errs = append(errs, "keymap: "+errText(ko, kerr))
			}
			if succeeded(ao, aerr) {
				if acts, err := rex.ParseActions(ao.Stdout); err == nil {
					for _, a := range acts {
						r.Actions = append(r.Actions, c.actionLabel(a, cmds.Do(a.Name)))
					}
				} else {
					errs = append(errs, "actions: "+err.Error())
				}
			} else {
				errs = append(errs, "actions: "+errText(ao, aerr))
			}
			r.Err = strings.Join(errs, "; ")
			c.st.ServerActions = r
			c.renderMain()
		})
	})
}

func (c *Controller) loadClientActions() {
	cl, ok := c.st.SelectedClient()
	if !ok {
		c.st.ClientActions = ActionsState{}
		c.renderMain()
		return
	}
	id := cl.ClientID
	t := c.conn.Load()
	c.ui.Go(func() {
		out, err := readOn(t, cmds.ClientActions(id))
		c.onConn(t, func() {
			r := ActionsState{Loaded: true, Client: id}
			if acts, perr := parseActionsOutcome(out, err); perr != nil {
				r.Err = perr.Error()
			} else {
				for _, a := range acts {
					r.Actions = append(r.Actions, c.actionLabel(a, cmds.ClientDo(id, a.Name)))
				}
			}
			if sel, ok := c.st.SelectedClient(); ok && sel.ClientID == id {
				c.st.ClientActions = r
			}
			c.renderMain()
		})
	})
}

func (c *Controller) loadClientInspect() {
	cl, ok := c.st.SelectedClient()
	if !ok {
		c.st.ClientInspect = ClientInspectState{}
		c.renderMain()
		return
	}
	id := cl.ClientID
	t := c.conn.Load()
	c.ui.Go(func() {
		out, err := readOn(t, cmds.ClientInspect(id))
		c.onConn(t, func() {
			r := ClientInspectState{Client: id}
			if succeeded(out, err) {
				r.Raw = out.Stdout
			} else {
				r.Err = errText(out, err)
			}
			if sel, ok := c.st.SelectedClient(); ok && sel.ClientID == id {
				c.st.ClientInspect = r
			}
			c.renderMain()
		})
	})
}
