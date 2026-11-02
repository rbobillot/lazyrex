package gui

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/rbobillot/lazyrex/pkg/render"
	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rexhosts"
)

// RexHostsView is the state of lazyrex-hosts.lua shown in Status. Entries,
// Skipped and Flag are keyed by lowercased target name, the rest by
// lowercased label.
type RexHostsView struct {
	// Ready is set once lazyrex looked at the file; Err then says why the
	// feature is off, Short the same for an error popup.
	Ready      bool
	Err, Short string
	Paths      rexhosts.Paths
	File       rexhosts.State
	Foreign    string
	UpToDate   bool
	Entries    map[string]rex.HostEntry
	Skipped    map[string]string
	// Flag holds the forward-mode targets of --ssh.
	Flag map[string]bool
	// Loaded holds the entries the local server has; LoadedFrom the source
	// of one declared outside lazyrex-hosts.lua.
	Loaded     map[string]bool
	LoadedFrom map[string]string
	// Conflict is where another host with an entry's label comes from.
	Conflict map[string]string
	// Stored are the stored hosts with another label on an entry's socket.
	Stored map[string][]string
	// ByHand are the lines about own-shaped hosts lazyrex does not manage.
	ByHand      []string
	NeedsReload bool
	// AfterReload is set once a hosts list started after a reload landed.
	AfterReload bool
}

// rexHostsLoad is what one look at the file found.
type rexHostsLoad struct {
	err, short string
	paths      rexhosts.Paths
	// dir is the runtime directory, empty when no target needed it.
	dir     string
	snap    rexhosts.Snapshot
	entries []rex.HostEntry
	skipped []rex.SkippedHost
	data    []byte
}

// rexHostsOff says why the feature is off from the start.
func (c *Controller) rexHostsOff() string {
	if c.env == nil || c.homeDir == nil {
		return "it needs lazyrex's environment"
	}
	return ""
}

func forwardConfigTarget(ts []rex.Target) bool {
	for _, t := range ts {
		if t.Kind == rex.TargetSSH && t.Source == rex.SourceConfig && t.SSH.Mode != rex.SSHModeExec {
			return true
		}
	}
	return false
}

func (c *Controller) runtimeDir() (string, error) {
	if c.ssh == nil || c.ssh.Dir == nil {
		return "", errors.New("SSH targets are not available")
	}
	dir, err := c.ssh.Dir()
	if err != nil {
		return "", fmt.Errorf("runtime directory: %v", err)
	}
	return dir, nil
}

// loadRexHosts runs off the UI goroutine: it reads the runtime directory
// only when a forward-mode target needs it.
func (c *Controller) loadRexHosts(ts []rex.Target) rexHostsLoad {
	fail := func(l rexHostsLoad, err, short string) rexHostsLoad {
		l.err, l.short = err, short
		return l
	}
	if c.env("REX_SERVER") != "" {
		return fail(rexHostsLoad{}, "REX_SERVER is set, so lazyrex cannot tell which server reads the file.", "REX_SERVER is set")
	}
	p, err := rexhosts.Locate(c.env, c.homeDir)
	if err != nil {
		return fail(rexHostsLoad{}, err.Error(), err.Error())
	}
	l := rexHostsLoad{paths: p}
	if forwardConfigTarget(ts) {
		if l.dir, err = c.runtimeDir(); err != nil {
			return fail(l, err.Error(), err.Error())
		}
	}
	l.entries, l.skipped = rex.HostsFileEntries(l.dir, ts)
	if l.data, err = rex.RenderHostsFile(l.entries); err != nil {
		return fail(l, err.Error(), err.Error())
	}
	if l.snap, err = rexhosts.Read(p); err != nil {
		return fail(l, err.Error(), err.Error())
	}
	return l
}

// loadThen looks at the file off the UI goroutine, records what it found
// unless a later look landed first, then runs done.
func (c *Controller) loadThen(done func()) {
	ts := c.baseTargets()
	c.rhSeq++
	seq := c.rhSeq
	c.ui.Go(func() {
		l := c.loadRexHosts(ts)
		c.ui.Update(func() {
			if seq > c.rhApplied {
				c.rhApplied, c.rh, c.rhLoaded = seq, l, true
				c.syncRexHosts()
				c.syncMain()
			}
			done()
		})
	})
}

// refreshRexHosts looks at the file again, once at a time.
func (c *Controller) refreshRexHosts() {
	if c.rexHostsOff() != "" {
		return
	}
	if c.rhInflight {
		c.rhDirty = true
		return
	}
	c.rhInflight = true
	c.loadThen(func() {
		c.rhInflight = false
		if c.rhDirty {
			c.rhDirty = false
			c.refreshRexHosts()
		}
	})
}

type hostsClass struct {
	loaded           map[string]bool
	loadedFrom       map[string]string
	conflict         map[string]string
	stored           map[string][]string
	byHand           []string
	staleManaged     bool
	managedOwnLabels map[string]bool
}

func reaches(h rex.Host, path string) bool {
	for _, ep := range h.Endpoints {
		if p, err := rex.NormalizeEndpoint(string(ep)); err == nil && p == path {
			return true
		}
	}
	return false
}

// classifyHosts compares the local hosts list with the entries.
func classifyHosts(hosts []rex.Host, es []rex.HostEntry) hostsClass {
	k := hostsClass{loaded: map[string]bool{}, loadedFrom: map[string]string{}, conflict: map[string]string{}, stored: map[string][]string{}}
	labels := map[string]bool{}
	for _, e := range es {
		key := strings.ToLower(e.Label)
		labels[key] = true
		path, err := rex.NormalizeEndpoint(e.Endpoint)
		if err != nil {
			continue
		}
		for _, h := range hosts {
			if !strings.EqualFold(h.Label, e.Label) {
				if !rex.IsDeclared(h) && reaches(h, path) {
					k.stored[key] = append(k.stored[key], h.Label)
				}
				continue
			}
			own := rex.IsOwnHost(h)
			switch {
			case own && reaches(h, path):
				k.loaded[key] = true
				if !rex.IsManaged(h) {
					k.loadedFrom[key] = string(h.Source)
				}
			case own && rex.IsManaged(h):
			case rex.IsDeclared(h):
				k.conflict[key] = "declared at " + string(h.Source)
			default:
				k.conflict[key] = "stored through the API"
			}
		}
	}
	for _, h := range hosts {
		if !rex.IsOwnHost(h) || labels[strings.ToLower(h.Label)] {
			continue
		}
		if rex.IsManaged(h) {
			k.staleManaged = true
			continue
		}
		k.byHand = append(k.byHand, fmt.Sprintf("The host %s is declared in %s, which lazyrex does not manage: remove that line by hand.",
			render.Label(h.Label), render.Label(string(h.Source))))
	}
	return k
}

// managedOwn reports whether h is a host lazyrex-hosts.lua declares, which
// Targets hides.
func managedOwn(h rex.Host) bool { return rex.IsManaged(h) && rex.IsOwnHost(h) }

// syncRexHosts recomputes the view from the last look and hosts list.
func (c *Controller) syncRexHosts() {
	if !c.rhLoaded {
		c.st.RexHosts = RexHostsView{}
		return
	}
	l := c.rh
	v := RexHostsView{Ready: true, Paths: l.paths}
	if l.err != "" {
		v.Err, v.Short = l.err, l.short
		c.st.RexHosts = v
		return
	}
	v.File, v.Foreign = l.snap.State, l.snap.Why
	switch l.snap.State {
	case rexhosts.Absent:
		v.UpToDate = len(l.entries) == 0
	case rexhosts.Ours:
		v.UpToDate = sha256.Sum256(l.data) == l.snap.Sum
	}
	v.Entries, v.Skipped, v.Flag = map[string]rex.HostEntry{}, map[string]string{}, map[string]bool{}
	for _, e := range l.entries {
		v.Entries[strings.ToLower(e.Target)] = e
	}
	for _, s := range l.skipped {
		v.Skipped[strings.ToLower(s.Target)] = s.Why
	}
	for _, t := range c.baseTargets() {
		if t.Kind == rex.TargetSSH && t.Source == rex.SourceFlag && t.SSH.Mode != rex.SSHModeExec {
			v.Flag[strings.ToLower(t.Name)] = true
		}
	}
	k := classifyHosts(c.hosts, l.entries)
	v.Loaded, v.LoadedFrom, v.Conflict, v.Stored, v.ByHand = k.loaded, k.loadedFrom, k.conflict, k.stored, k.byHand
	unloaded := false
	for _, e := range l.entries {
		key := strings.ToLower(e.Label)
		if v.Conflict[key] == "" && !v.Loaded[key] {
			unloaded = true
		}
	}
	v.NeedsReload = v.UpToDate && !c.st.Hosts.HomeStopped && (unloaded || k.staleManaged)
	v.AfterReload = c.reloaded && c.hostsApplied > c.reloadAfter
	c.st.RexHosts = v
}

// writeRexHosts is w on Targets: it looks at the file again, then writes it
// or reloads the local server, each after a confirmation.
func (c *Controller) writeRexHosts() {
	switch {
	case c.rexHostsOff() != "":
		c.Popups.Error("the Rex app hosts file is not available: " + c.rexHostsOff())
	case c.rhBusy:
		c.Popups.Error("the Rex app hosts file is still being written")
	case c.configBusy:
		c.Popups.Error("another config edit is still running")
	default:
		c.rhBusy = true
		c.loadThen(func() {
			c.rhBusy = false
			c.afterLook()
		})
	}
}

func (c *Controller) afterLook() {
	v, l := c.st.RexHosts, c.rh
	path := render.Label(v.Paths.Hosts)
	switch {
	case v.Err != "":
		c.Popups.Error("the Rex app hosts file is not available: " + v.Short)
	case v.File == rexhosts.Foreign:
		c.Popups.Error(fmt.Sprintf("%s is not a file lazyrex wrote (%s): lazyrex leaves it alone", path, render.Label(v.Foreign)))
	case !v.UpToDate:
		c.checkRexHosts(l)
	case c.st.Hosts.HomeStopped:
		c.Popups.Toast(c.stoppedText())
	case v.NeedsReload:
		c.reloadLocal()
	default:
		for _, e := range l.entries {
			if src := v.Conflict[strings.ToLower(e.Label)]; src != "" {
				c.Popups.Error(fmt.Sprintf("%s is taken by another host (%s): remove or rename it in the Rex app", e.Label, render.Label(src)))
				return
			}
		}
		switch {
		case len(l.entries) == 0 && len(l.skipped) > 0:
			c.Popups.Error(l.skipped[0].Why)
		case len(l.entries) == 0:
			c.Popups.Toast("no forward-mode SSH target to declare")
		default:
			c.Popups.Toast(path + " is up to date and loaded")
		}
	}
}

func (c *Controller) stoppedText() string {
	return "the local Rex server is not running: it reads " + render.Label(c.st.RexHosts.Paths.Init) + " when it starts"
}

// checkRexHosts has this machine's rex check the file lazyrex would write,
// then asks to write it.
func (c *Controller) checkRexHosts(l rexHostsLoad) {
	c.rhBusy = true
	ctx, pid := c.root, 0
	if c.ssh != nil {
		pid = c.ssh.PID
	}
	c.ui.Go(func() {
		dir, err := l.dir, error(nil)
		if dir == "" {
			dir, err = c.runtimeDir()
		}
		if err == nil {
			err = rex.CheckHostsFile(ctx, c.home, dir, pid, l.data)
		}
		c.ui.Update(func() {
			c.rhBusy = false
			if err != nil {
				c.Popups.Error(render.Label(l.paths.Hosts) + " not written: rex config check: " + err.Error())
				return
			}
			c.confirmWrite(l)
		})
	})
}

func (c *Controller) confirmWrite(l rexHostsLoad) {
	var lines []string
	for _, e := range l.entries {
		if line, err := rex.HostLine(e); err == nil {
			lines = append(lines, render.Label(line))
		}
	}
	if len(lines) == 0 {
		lines = []string{"(no host: only the header)"}
	}
	body := fmt.Sprintf("Write %s (rex config check passed). Rex runs it once %s loads it:\n",
		render.Label(l.paths.Hosts), render.Label(l.paths.Init)) + strings.Join(lines, "\n")
	c.Popups.Confirm("Write the Rex app hosts file", body, func() { c.commitRexHosts(l) })
}

// commitRexHosts writes what was checked, if the targets still give it.
func (c *Controller) commitRexHosts(l rexHostsLoad) {
	if c.rhBusy {
		c.Popups.Error("the Rex app hosts file is still being written")
		return
	}
	es, _ := rex.HostsFileEntries(l.dir, c.baseTargets())
	if data, err := rex.RenderHostsFile(es); err != nil || !bytes.Equal(data, l.data) {
		c.Popups.Error("the SSH targets changed since the check; press w again")
		return
	}
	c.rhBusy = true
	c.ui.Go(func() {
		err := c.writeHosts(l.paths, l.data, l.snap)
		c.ui.Update(func() {
			c.rhBusy = false
			path := render.Label(l.paths.Hosts)
			var foreign *rexhosts.ForeignError
			switch {
			case errors.Is(err, rexhosts.ErrChanged):
				c.Popups.Error(path + " changed on disk since lazyrex read it; press w again")
			case errors.Is(err, rexhosts.ErrBusy):
				c.Popups.Error("another lazyrex is writing " + path + "; press w again")
			case errors.Is(err, rexhosts.ErrNotSynced):
				c.Popups.Error(path + " " + err.Error() + "; press w again")
			case errors.As(err, &foreign):
				c.Popups.Error(fmt.Sprintf("%s is not a file lazyrex wrote (%s): lazyrex leaves it alone", path, render.Label(foreign.Why)))
			case err != nil:
				c.Popups.Error(path + " not written: " + err.Error())
			}
			if err != nil {
				c.refreshRexHosts()
				return
			}
			c.LogLine("wrote " + l.paths.Hosts)
			c.Popups.Toast("wrote " + path)
			l.snap = rexhosts.Snapshot{State: rexhosts.Ours, Sum: sha256.Sum256(l.data)}
			c.rhSeq++
			c.rhApplied, c.rh, c.rhLoaded, c.reloaded = c.rhSeq, l, true, false
			c.syncRexHosts()
			c.syncMain()
			v := c.st.RexHosts
			free := false
			for _, e := range l.entries {
				free = free || v.Conflict[strings.ToLower(e.Label)] == ""
			}
			switch {
			case c.st.Hosts.HomeStopped:
				c.Popups.Toast(c.stoppedText())
			case v.NeedsReload || free:
				c.reloadLocal()
			}
		})
	})
}

// reloadLocal asks to reload this machine's server, whatever target is
// active.
func (c *Controller) reloadLocal() {
	p, err := c.home.Prepare(cmds.ConfigReload())
	var denied *rex.DeniedError
	if errors.As(err, &denied) {
		c.home.Refuse(p.Cmd, denied.Reason)
		c.Popups.Error("refused: " + denied.Reason)
		return
	}
	c.Popups.Confirm("Reload the local Rex server", p.Display, func() {
		if c.rhBusy {
			c.Popups.Error("the Rex app hosts file is still being written")
			return
		}
		c.rhBusy = true
		ctx := c.root
		c.ui.Go(func() {
			out, err := c.home.RunPrepared(ctx, p, true, nil)
			c.ui.Update(func() {
				c.rhBusy = false
				c.reloadDone(out, err)
			})
		})
	})
}

func (c *Controller) reloadDone(out rex.Outcome, err error) {
	if err != nil {
		c.Popups.Error(err.Error())
		return
	}
	o, perr := rex.ParseConfigReload(out.Stdout)
	ds, derr := rex.ParseConfigDiagnostics(out.Stdout)
	if derr == nil {
		for _, d := range ds {
			c.LogLine("config reload: " + d.Severity + " " + d.Text())
		}
	}
	switch out.Kind {
	case rex.ExitOK:
		msg := "the local Rex server read its config again"
		d, complained := rex.FirstComplaint(o.Diagnostics)
		switch {
		case perr != nil:
			msg += " (output unreadable)"
		case complained:
			noun := "complaints"
			if len(o.Diagnostics) == 1 {
				noun = "complaint"
			}
			msg += fmt.Sprintf(", with %d %s: %s", len(o.Diagnostics), noun, d.Text())
		}
		if perr == nil {
			c.reloaded, c.reloadAfter = true, c.hostsStarted
		}
		c.Popups.Toast(msg)
		c.syncRexHosts()
		c.refreshHosts()
	case rex.ExitUnreachable:
		c.Popups.Toast(c.stoppedText())
		c.st.Hosts.HomeStopped = true
		c.dropHosts()
	case rex.ExitInterrupted:
	default:
		msg := out.Toast
		if d, ok := rex.FirstComplaint(ds); derr == nil && ok {
			msg = d.Text()
		}
		c.Popups.Error("rex config reload: " + msg)
	}
	c.syncMain()
}

// rexHost renders the Rex app host block of the active target name.
func (l *lines) rexHost(s *State, name string) {
	v := s.RexHosts
	if !v.Ready {
		return
	}
	key := strings.ToLower(name)
	n := render.Label(name)
	e, entry := v.Entries[key]
	switch {
	case v.Err != "":
		l.add("Rex app host none: %s", render.Label(v.Err))
	case v.Flag[key]:
		l.add("Rex app host none: %s comes from --ssh, not the config file. Add it with a (Targets), then w writes the Rex app hosts file.", n)
	case v.Skipped[key] != "":
		l.add("Rex app host none: %s", render.Label(v.Skipped[key]))
	case v.File == rexhosts.Foreign:
		l.add("Rex app host none: %s is not a file lazyrex wrote (%s), so lazyrex leaves it alone. Move it away, then w writes it.",
			render.Label(v.Paths.Hosts), render.Label(v.Foreign))
	case !entry:
	case v.Conflict[strings.ToLower(e.Label)] != "":
		l.add("Rex app host %s: the label is taken by another host (%s), so Rex leaves lazyrex's declaration out. "+
			"Remove or rename that host in the Rex app; lazyrex never changes hosts.", render.Label(e.Label), render.Label(v.Conflict[strings.ToLower(e.Label)]))
	default:
		l.rexHostState(s, n, e)
	}
	for _, b := range v.ByHand {
		l.add("%s", b)
	}
}

func (l *lines) rexHostState(s *State, n string, e rex.HostEntry) {
	v := s.RexHosts
	key := strings.ToLower(e.Label)
	head := func(state string) {
		l.add("Rex app host %s in %s: %s", render.Label(e.Label), render.Label(v.Paths.Hosts), state)
	}
	init := render.Label(v.Paths.Init)
	include, _ := rex.IncludeLine(v.Paths.Hosts)
	include = render.Label(include)
	switch {
	case !v.UpToDate:
		if v.File == rexhosts.Absent {
			head("not written yet")
		} else {
			head("out of date")
		}
		l.add("w (Targets) writes it, then reloads the local Rex server, each after a confirmation.")
	case v.Loaded[key]:
		head("up to date, loaded by the local Rex server")
		if src := v.LoadedFrom[key]; src != "" {
			l.add("declared in %s, not in %s", render.Label(src), rex.HostsFileName)
		}
	case s.Hosts.HomeStopped:
		head("up to date; the local Rex server is not running")
		l.add("It reads the file when it starts, once %s loads it:", init)
		l.add("%s", include)
	case v.AfterReload:
		head("up to date, still not loaded after the reload")
		l.add("Check that %s has this line, and the reload output in the command log:", init)
		l.add("%s", include)
		l.add("If your Rex ignores hosts declared in a loaded file, copy this line into %s instead:", init)
		line, _ := rex.HostLine(e)
		l.add("%s", render.Label(line))
	default:
		head("up to date, not loaded yet")
		l.add("If %s does not load it yet, add this line there (lazyrex never creates or edits %s):", init, init)
		l.add("%s", include)
		l.add("Then w (Targets) reloads the local Rex server, after a confirmation.")
	}
	for _, other := range v.Stored[key] {
		l.add("The host %s also reaches this socket and is stored through the API (rex hosts add), so it syncs to your other devices, "+
			"where it reaches nothing. Remove it in the Rex app; lazyrex never removes hosts.", render.Label(other))
	}
	l.add("It works while lazyrex runs with %s active. Rex has no transport auth: only your user can open this socket.", n)
}
