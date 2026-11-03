package gui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rbobillot/lazyrex/pkg/config"
	"github.com/rbobillot/lazyrex/pkg/render"
	"github.com/rbobillot/lazyrex/pkg/rex"
)

// ConfigTargets returns the targets of cfg, the local one first.
func ConfigTargets(cfg config.Config) ([]rex.Target, error) {
	specs := make([]rex.TargetSpec, len(cfg.Targets))
	for i, t := range cfg.Targets {
		specs[i] = rex.TargetSpec{Name: t.Name, Kind: t.Kind, Server: t.Server, SSHHost: t.SSHHost, Mode: t.Mode, RexPath: t.RexPath}
	}
	return rex.ConfigTargets(specs)
}

// setConfigTargets replaces the config targets and rebuilds the rows.
func (c *Controller) setConfigTargets(ts []rex.Target) {
	c.cfgTargets = ts
	c.rebuildTargets()
	c.syncMain()
}

// rebuildTargets lists the config, --server and discovered targets, keeping
// each row's last probe and selection, and returns the hosts it hid.
func (c *Controller) rebuildTargets() []string {
	var hosts []rex.Host
	var own []string
	for _, h := range c.hosts {
		if managedOwn(h) {
			own = append(own, fmt.Sprintf("hosts: hid %q: lazyrex's own host", h.Label))
			continue
		}
		hosts = append(hosts, h)
	}
	rows, hidden := rex.MergeTargets(c.baseTargets(), hosts, c.conn.Load().target)
	hidden = append(hidden, own...)
	old := c.st.Targets
	c.st.keepSelection(func() {
		c.st.Targets = make([]TargetRow, len(rows))
		for i, t := range rows {
			r := TargetRow{Target: t, Check: c.checkFor(t)}
			if j := slices.IndexFunc(old, func(o TargetRow) bool { return sameTarget(o.Target, t) }); j >= 0 {
				r.Status, r.Probing, r.probe = old[j].Status, old[j].Probing, old[j].probe
			}
			c.st.Targets[i] = r
		}
	})
	return hidden
}

func sameTarget(a, b rex.Target) bool {
	return strings.EqualFold(a.Name, b.Name) && a.Kind == b.Kind && a.Server == b.Server && a.SSH == b.SSH
}

func (c *Controller) checkFor(t rex.Target) *rex.HostCheck {
	if t.Kind != rex.TargetNative {
		return nil
	}
	for _, e := range c.checks {
		if strings.EqualFold(e.Label, t.Server) {
			return &e
		}
	}
	return nil
}

func (c *Controller) setRow(name string, f func(r *TargetRow)) {
	for i := range c.st.Targets {
		if strings.EqualFold(c.st.Targets[i].Target.Name, name) {
			f(&c.st.Targets[i])
			return
		}
	}
}

// selectedInactive returns the selected row when it is not the active
// target's.
func (c *Controller) selectedInactive() (TargetRow, bool) {
	r, ok := c.st.SelectedTarget()
	return r, ok && !c.st.isActive(r)
}

// requireActive refuses a server key on a row that is not active.
func (c *Controller) requireActive() bool {
	if r, ok := c.selectedInactive(); ok {
		c.Popups.Error(fmt.Sprintf("activate %s first (enter)", render.Label(r.Target.Name)))
		return false
	}
	return true
}

// activate switches to the selected target, or re-reads the active one.
func (c *Controller) activate() {
	r, ok := c.selectedInactive()
	if !ok {
		c.refreshAll()
		c.refreshHosts()
		return
	}
	if c.configBusy && strings.EqualFold(r.Target.Name, c.configEdits) {
		c.Popups.Error("a config edit of " + render.Label(r.Target.Name) + " is still running")
		return
	}
	c.switchTo(r.Target)
}

// switchTo makes t the active target: a new connection and generation, so
// pending work of the previous one neither runs nor lands.
func (c *Controller) switchTo(t rex.Target) {
	prev := c.st.TargetName
	status := c.st.Status
	c.setTarget(t.Name, "")
	cn := c.newConn(t)
	c.replaceConn(cn)
	c.setRow(prev, func(r *TargetRow) { r.Status = status })
	c.resetTargetData()
	c.st.Transport = cn.client.Transport
	c.rebuildTargets()
	c.st.selectID(PanelTargets, t.Name)
	c.refreshAll()
	c.syncMain()
	if c.started {
		c.startStream(cn)
	}
}

// resetTargetData drops everything read from the previous target.
func (c *Controller) resetTargetData() {
	s := c.st
	s.Status, s.StreamUp, c.streamSeen = rex.TargetStatus{}, false, false
	s.Sessions, s.Clients = nil, nil
	s.Data = map[rex.SessionID]*SessionData{}
	s.Live = map[rex.BlockID]*BlockLive{}
	s.Zoomed = map[rex.BlockID]bool{}
	s.Capture, s.BlockInspect, s.ClientInspect = CaptureState{}, BlockInspectState{}, ClientInspectState{}
	s.ServerActions, s.ClientActions = ActionsState{}, ActionsState{}
	c.captureInflight, c.captureDirty, c.pendingSession = false, false, ""
	c.cache = targetCache{}
	for _, p := range []Panel{PanelSessions, PanelWindows, PanelBlocks, PanelClients} {
		s.Lists[p].Selected = 0
	}
	s.Main.Key = ""
}

// checkSelected re-reads the active target, or probes the selected one
// without activating it.
func (c *Controller) checkSelected() {
	r, ok := c.st.SelectedTarget()
	if ok && r.Target.Kind == rex.TargetSSH && c.ssh != nil && c.ssh.Probes != nil {
		c.ssh.Probes.Forget(rex.ProbeKey(r.Target.Name, r.Target.SSH))
	}
	if ok && !c.st.isActive(r) {
		c.probeRow(r.Target)
		return
	}
	c.checkTarget()
	if ok && r.Target.Kind == rex.TargetNative && r.Target.Host != nil {
		c.checkHosts(r.Target.Server)
	}
}

// probeRow reads an inactive target's status into its row only.
func (c *Controller) probeRow(t rex.Target) {
	c.probes++
	id := c.probes
	c.setRow(t.Name, func(r *TargetRow) { r.Probing, r.probe = true, id })
	c.renderMain()
	cl, ctx := c.clientFor(t), c.root
	c.ui.Go(func() {
		var st rex.TargetStatus
		if t.Kind == rex.TargetSSH {
			st = rex.ProbeSSH(ctx, c.client, t.Name, t.SSH, c.ssh)
		} else {
			whoami, _ := cl.Exec(ctx, cmds.Whoami(), false)
			server, _ := cl.Exec(ctx, cmds.ServerStatus(), false)
			st = rex.DeriveStatus(cl.Transport, whoami, &server, c.version.Load())
		}
		c.ui.Update(func() {
			for i := range c.st.Targets {
				if r := &c.st.Targets[i]; r.probe == id && sameTarget(r.Target, t) {
					r.Probing = false
					if st.State != rex.StateUnknown {
						r.Status = st
					}
				}
			}
			c.syncMain()
		})
	})
}

// Hosts

// refreshHosts re-reads `rex hosts list` from the home server, once at a
// time; a request meanwhile runs on the next tick.
func (c *Controller) refreshHosts() {
	if c.hostsInflight {
		c.hostsDirty = true
		return
	}
	c.hostsInflight = true
	c.hostsStarted++
	ctx, seq := c.root, c.hostsStarted
	c.ui.Go(func() {
		out, err := c.home.Exec(ctx, cmds.HostsList(), false)
		c.ui.Update(func() {
			c.hostsInflight = false
			c.applyHosts(out, err, seq)
		})
	})
}

// hostsFailed reports a failed hosts call; it returns false for exit 4,
// which means the home server is stopped.
func (c *Controller) hostsFailed(what string, out rex.Outcome, err error) bool {
	var denied *rex.DeniedError
	switch {
	case errors.As(err, &denied):
		c.Popups.Toast("refused: " + denied.Reason)
	case err != nil:
		c.Popups.Toast("error: " + what + ": " + err.Error())
	case out.Kind == rex.ExitUnreachable:
		c.st.Hosts.HomeStopped = true
		return false
	case out.Kind == rex.ExitUsage:
		c.Popups.Toast("invalid command (lazyrex bug or Rex version drift): " + out.Display + ": " + out.Toast)
	case out.Kind != rex.ExitInterrupted:
		c.st.Hosts.Err = render.Label(out.Toast)
		c.Popups.Toast(what + ": " + out.Toast)
	}
	return true
}

// dropHosts hides the discovered hosts of a stopped home server; the active
// one stays listed.
func (c *Controller) dropHosts() {
	c.hosts = nil
	c.rebuildTargets()
	c.syncRexHosts()
}

// applyHosts records a hosts list. A failure keeps the last good list,
// except a stopped home server, whose hosts are dropped.
func (c *Controller) applyHosts(out rex.Outcome, err error, seq uint64) {
	if err != nil || out.Kind != rex.ExitOK {
		if !c.hostsFailed("hosts list", out, err) {
			c.dropHosts()
		}
		c.syncRexHosts()
		c.syncMain()
		return
	}
	v, perr := rex.ParseHosts(out.Stdout)
	if perr != nil {
		h := &c.st.Hosts
		h.HomeStopped, h.Err = false, render.Label(perr.Error())
		c.Popups.Toast("error: parse hosts list: " + perr.Error())
		c.syncRexHosts()
		c.syncMain()
		return
	}
	c.hostsApplied = max(c.hostsApplied, seq)
	h := &c.st.Hosts
	h.Loaded, h.HomeStopped, h.Err, h.Home, h.Warnings = true, false, "", v.Home, nil
	for _, w := range v.Warnings {
		h.Warnings = append(h.Warnings, render.Label(w))
	}
	c.hosts = v.Hosts
	ts := c.clock.Now().Format("15:04:05")
	for _, line := range c.rebuildTargets() {
		c.st.appendLog(ts + " " + render.Label(line))
	}
	if v.Skipped > 0 {
		c.st.appendLog(fmt.Sprintf("%s hosts: skipped %d entries without a label", ts, v.Skipped))
	}
	for _, w := range h.Warnings {
		c.st.appendLog(ts + " hosts: warning: " + w)
	}
	if !c.checkedOnce && slices.ContainsFunc(c.hosts, func(h rex.Host) bool { return !managedOwn(h) }) {
		c.checkedOnce = true
		c.checkHosts()
	}
	c.syncRexHosts()
	c.syncMain()
}

// checkHosts runs `rex hosts check` for labels, or every host, once at a
// time.
func (c *Controller) checkHosts(labels ...string) {
	if c.st.Hosts.Checking {
		return
	}
	c.st.Hosts.Checking = true
	ctx := c.root
	c.ui.Go(func() {
		out, err := c.home.Exec(ctx, cmds.HostsCheck(labels...), false)
		c.ui.Update(func() {
			c.st.Hosts.Checking = false
			c.applyCheck(out, err, len(labels) > 0)
		})
	})
}

func (c *Controller) applyCheck(out rex.Outcome, err error, partial bool) {
	if err != nil || out.Kind != rex.ExitOK {
		if !c.hostsFailed("hosts check", out, err) {
			c.dropHosts()
		}
		c.syncMain()
		return
	}
	rep, perr := rex.ParseHostsCheck(out.Stdout)
	if perr != nil {
		c.Popups.Toast("error: " + perr.Error())
		return
	}
	if partial {
		for _, e := range rep.Entries {
			c.checks = slices.DeleteFunc(c.checks, func(o rex.HostCheck) bool { return strings.EqualFold(o.Label, e.Label) })
			c.checks = append(c.checks, e)
		}
	} else {
		h := &c.st.Hosts
		c.checks = rep.Entries
		h.Skipped, h.Unrecognized, h.Unmatched, h.Unlisted = rep.Skipped, rep.Unrecognized, 0, nil
		for _, e := range rep.Entries {
			if slices.ContainsFunc(c.hosts, func(h rex.Host) bool { return managedOwn(h) && strings.EqualFold(h.Label, e.Label) }) {
				continue
			}
			matched := slices.ContainsFunc(c.st.Targets, func(r TargetRow) bool {
				return r.Target.Kind == rex.TargetNative && strings.EqualFold(r.Target.Server, e.Label)
			})
			switch {
			case matched:
			case e.Unrecognized:
				h.Unlisted = append(h.Unlisted, render.Label(e.Label+": "+string(e.Status)))
			default:
				h.Unmatched++
			}
		}
	}
	c.rebuildTargets()
	c.syncMain()
}
