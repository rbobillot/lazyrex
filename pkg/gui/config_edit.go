package gui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rbobillot/lazyrex/pkg/config"
	"github.com/rbobillot/lazyrex/pkg/render"
	"github.com/rbobillot/lazyrex/pkg/rex"
)

func checkTargets(cfg config.Config) error {
	_, err := ConfigTargets(cfg)
	return err
}

// nameTaken refuses name for a config target unless it is free, or is
// keep's (the entry being edited).
func (c *Controller) nameTaken(name, keep string) bool {
	if err := rex.CheckTargetName(name); err != nil {
		c.Popups.Error(fmt.Sprintf("target name %q: %v", render.Label(name), err))
		return true
	}
	if strings.EqualFold(name, rex.LocalName) {
		c.Popups.Error("local is the built-in local target")
		return true
	}
	if r, ok := c.row(name); ok && !strings.EqualFold(name, keep) && (r.Target.Source == rex.SourceConfig || r.Target.Kind == rex.TargetLocal) {
		c.Popups.Error(fmt.Sprintf("a target named %s already exists", render.Label(r.Target.Name)))
		return true
	}
	return false
}

// serverRefused refuses server, or a name held by a target that is not in
// the config file with another server.
func (c *Controller) serverRefused(name, server, keep string) bool {
	if err := rex.CheckServer(server); err != nil {
		c.Popups.Error(fmt.Sprintf("server %q: %v", render.Label(server), err))
		return true
	}
	r, ok := c.row(name)
	if ok && !strings.EqualFold(name, keep) && r.Target.Source != rex.SourceConfig &&
		(r.Target.Kind != rex.TargetNative || !strings.EqualFold(r.Target.Server, server)) {
		c.Popups.Error(fmt.Sprintf("the name %s is taken by a target reaching %s", render.Label(r.Target.Name), reach(r.Target)))
		return true
	}
	return false
}

// sshRefused is serverRefused for an ssh destination.
func (c *Controller) sshRefused(name, host, keep string) bool {
	if err := rex.CheckSSHHost(host); err != nil {
		c.Popups.Error(fmt.Sprintf("ssh destination %q: %v", render.Label(host), err))
		return true
	}
	r, ok := c.row(name)
	if ok && !strings.EqualFold(name, keep) && r.Target.Source != rex.SourceConfig &&
		(r.Target.Kind != rex.TargetSSH || r.Target.SSH.Host != host) {
		c.Popups.Error(fmt.Sprintf("the name %s is taken by a target reaching %s", render.Label(r.Target.Name), reach(r.Target)))
		return true
	}
	return false
}

func reach(t rex.Target) string {
	if t.Kind == rex.TargetSSH {
		return "ssh " + render.Label(t.SSH.Host)
	}
	return render.Label(t.Server)
}

func (c *Controller) row(name string) (TargetRow, bool) {
	for _, r := range c.st.Targets {
		if strings.EqualFold(r.Target.Name, name) {
			return r, true
		}
	}
	return TargetRow{}, false
}

// addTarget adds a native or SSH target to the config file, prefilled from
// a discovered, --server or --ssh row.
func (c *Controller) addTarget() {
	if r, ok := c.st.SelectedTarget(); ok && r.Target.Source != rex.SourceConfig {
		switch r.Target.Kind {
		case rex.TargetNative:
			c.addNativeTarget(r.Target.Name, r.Target.Server)
			return
		case rex.TargetSSH:
			c.addSSHTarget(r.Target.SSH.Host, r.Target.Name)
			return
		}
	}
	items := []MenuItem{{Label: "native: rex -S SERVER…", OnSelect: func() { c.addNativeTarget("", "") }}}
	for _, h := range c.unusedSSHHosts() {
		items = append(items, MenuItem{Label: "ssh: " + render.Label(h) + " (~/.ssh/config)", OnSelect: func() { c.sshTargetName(h, h) }})
	}
	items = append(items, MenuItem{Label: "ssh: another destination…", OnSelect: func() { c.addSSHTarget("", "") }})
	c.Popups.Menu("Add a config target", items)
}

// unusedSSHHosts are the ~/.ssh/config aliases no SSH target reaches yet,
// in file order.
func (c *Controller) unusedSSHHosts() []string {
	var out []string
	for _, h := range c.sshHosts {
		used := false
		for _, r := range c.st.Targets {
			used = used || (r.Target.Kind == rex.TargetSSH && r.Target.Source == rex.SourceConfig && r.Target.SSH.Host == h)
		}
		if !used {
			out = append(out, h)
		}
	}
	return out
}

func (c *Controller) addSSHTarget(dest, name string) {
	c.Popups.Prompt("Add an SSH target: destination (a ~/.ssh/config Host or user@host)", dest, func(d string) {
		d = strings.TrimSpace(d)
		if err := rex.CheckSSHHost(d); err != nil {
			c.Popups.Error(fmt.Sprintf("ssh destination %q: %v", render.Label(d), err))
			return
		}
		n := name
		if n == "" {
			n = d[strings.LastIndex(d, "@")+1:]
		}
		c.sshTargetName(d, n)
	})
}

func (c *Controller) sshTargetName(dest, name string) {
	c.Popups.Prompt("Add an SSH target: name", name, func(n string) {
		if c.nameTaken(n, "") || c.sshRefused(n, dest, "") {
			return
		}
		c.sshMode("Add SSH target "+render.Label(n)+": mode", func(mode string) {
			t := config.Target{Name: n, Kind: string(rex.TargetSSH), SSHHost: dest, Mode: mode}
			c.writeConfig("added "+n, n, "", func(f config.File) (config.Config, config.File, error) {
				return config.Edit(f, config.AddTarget(t), checkTargets)
			})
		})
	})
}

func (c *Controller) sshMode(title string, then func(mode string)) {
	c.Popups.Menu(title, []MenuItem{
		{Label: "forward: forward the remote Rex socket (default)", OnSelect: func() { then(string(rex.SSHModeForward)) }},
		{Label: "exec: run the remote rex through ssh for every call", OnSelect: func() { then(string(rex.SSHModeExec)) }},
	})
}

func (c *Controller) addNativeTarget(name, server string) {
	c.Popups.Prompt("Add a config target: name", name, func(n string) {
		if c.nameTaken(n, "") {
			return
		}
		c.Popups.Prompt("Add a config target: server (rex -S value: a rex hosts label, host:port or URL)", server, func(s string) {
			if c.serverRefused(n, s, "") {
				return
			}
			t := config.Target{Name: n, Kind: string(rex.TargetNative), Server: s}
			c.writeConfig("added "+n, n, "", func(f config.File) (config.Config, config.File, error) {
				return config.Edit(f, config.AddTarget(t), checkTargets)
			})
		})
	})
}

// editable returns the selected row if it is a config target lazyrex may
// change.
func (c *Controller) editable() (TargetRow, bool) {
	r, ok := c.st.SelectedTarget()
	if !ok {
		return r, false
	}
	name := render.Label(r.Target.Name)
	switch {
	case r.Target.Source == rex.SourceHosts:
		c.Popups.Error(name + " is a rex host: lazyrex never edits rex hosts; press a to add a config target")
	case r.Target.Kind == rex.TargetLocal:
		c.Popups.Error(name + " is the local target, built in: rename it with a kind: local entry, by hand")
	case r.Target.Source != rex.SourceConfig:
		c.Popups.Error(name + " comes from --server or --ssh, not from the config file; press a to add it")
	case c.st.isActive(r):
		c.Popups.Error(name + " is the active target: activate another target first")
	default:
		return r, true
	}
	return r, false
}

func (c *Controller) editTarget() {
	r, ok := c.editable()
	if !ok {
		return
	}
	old := r.Target.Name
	c.Popups.Prompt("Edit config target "+render.Label(old)+": name", old, func(n string) {
		if c.nameTaken(n, old) {
			return
		}
		if r.Target.Kind == rex.TargetSSH {
			c.editSSHTarget(r.Target, n)
			return
		}
		c.Popups.Prompt("Edit config target "+render.Label(old)+": server", r.Target.Server, func(s string) {
			if c.serverRefused(n, s, old) {
				return
			}
			t := config.Target{Name: n, Server: s}
			c.writeConfig("updated "+n, n, old, func(f config.File) (config.Config, config.File, error) {
				return config.Edit(f, config.UpdateTarget(old, t), checkTargets)
			})
		})
	})
}

// editSSHTarget asks the destination and mode of SSH target t, renamed n;
// rex_path and comments are kept.
func (c *Controller) editSSHTarget(t rex.Target, n string) {
	old := t.Name
	c.Popups.Prompt("Edit config target "+render.Label(old)+": ssh destination", t.SSH.Host, func(d string) {
		d = strings.TrimSpace(d)
		if c.sshRefused(n, d, old) {
			return
		}
		c.sshMode("Edit config target "+render.Label(old)+": mode", func(mode string) {
			ct := config.Target{Name: n, SSHHost: d, Mode: mode}
			c.writeConfig("updated "+n, n, old, func(f config.File) (config.Config, config.File, error) {
				return config.Edit(f, config.UpdateTarget(old, ct), checkTargets)
			})
		})
	})
}

func (c *Controller) removeTarget() {
	r, ok := c.editable()
	if !ok {
		return
	}
	name := r.Target.Name
	body := fmt.Sprintf("Remove %s (rex -S %s) from %s? rex hosts are not changed.",
		render.Label(name), render.Label(r.Target.Server), render.Label(c.file.Path))
	if r.Target.Kind == rex.TargetSSH {
		body = fmt.Sprintf("Remove %s (ssh %s) from %s? ~/.ssh/config is not changed.",
			render.Label(name), render.Label(r.Target.SSH.Host), render.Label(c.file.Path))
	}
	c.Popups.Confirm("Remove config target", body, func() {
		c.writeConfig("removed "+name, "", name, func(f config.File) (config.Config, config.File, error) {
			return config.Edit(f, config.RemoveTarget(name), checkTargets)
		})
	})
}

// writeConfig runs one config edit at a time off the UI goroutine, then
// lists the new targets and selects sel. edits names the entry it changes,
// which cannot be the active target, nor become it meanwhile.
func (c *Controller) writeConfig(done, sel, edits string, edit func(config.File) (config.Config, config.File, error)) {
	if c.configBusy {
		c.Popups.Error("another config edit is still running")
		return
	}
	if edits != "" && strings.EqualFold(edits, c.st.TargetName) {
		c.Popups.Error(render.Label(edits) + " is the active target: activate another target first")
		return
	}
	c.configBusy, c.configEdits = true, edits
	f := c.file
	c.ui.Go(func() {
		cfg, nf, err := edit(f)
		var ts []rex.Target
		if err == nil || errors.Is(err, config.ErrNotSynced) {
			var terr error
			if ts, terr = ConfigTargets(cfg); terr != nil {
				err = terr
			}
		}
		c.ui.Update(func() {
			c.configBusy, c.configEdits = false, ""
			if err != nil && !errors.Is(err, config.ErrNotSynced) {
				c.Popups.Error("config not changed: " + render.Label(err.Error()))
				return
			}
			c.file, c.cfg.Targets, c.cfgTargets = nf, cfg.Targets, ts
			c.rebuildTargets()
			if sel != "" {
				c.st.selectID(PanelTargets, sel)
			}
			if err != nil {
				c.Popups.Error(render.Label(err.Error()))
			} else {
				c.Popups.Toast(render.Label(done) + " in " + render.Label(nf.Path))
			}
			c.syncMain()
			c.refreshRexHosts()
		})
	})
}
