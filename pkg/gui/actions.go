package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rbobillot/lazyrex/pkg/render"
	"github.com/rbobillot/lazyrex/pkg/rex"
)

// refuse logs and shows a command the policy or a check refused.
func (c *Controller) refuse(cmd rex.Cmd, reason string) {
	c.active().Refuse(cmd, reason)
	c.Popups.Error("refused: " + reason)
}

func parseActionsOutcome(out rex.Outcome, err error) ([]rex.Action, error) {
	if !succeeded(out, err) {
		return nil, errors.New(errText(out, err))
	}
	return rex.ParseActions(out.Stdout)
}

var verdictTags = map[rex.Verdict]string{rex.Allow: "allowed", rex.Confirm: "confirm", rex.Deny: "denied"}

// actionLabel is "name [tag]  description", tagged with the policy's verdict
// on probe, the action without arguments.
func (c *Controller) actionLabel(a rex.Action, probe rex.Cmd) string {
	l := render.Label(a.Name) + " [" + verdictTags[c.active().Decide(probe).Verdict] + "]"
	if d := render.Label(a.Description); d != "" {
		l += "  " + d
	}
	return l
}

// actionMenu lists actions; picking one refuses it when probe(name) is
// denied, before any form, and otherwise calls pick.
func (c *Controller) actionMenu(title string, acts []rex.Action, probe func(string) rex.Cmd, pick func(rex.Action)) {
	if len(acts) == 0 {
		c.Popups.Toast("no actions")
		return
	}
	items := make([]MenuItem, 0, len(acts))
	for _, a := range acts {
		items = append(items, MenuItem{Label: c.actionLabel(a, probe(a.Name)), OnSelect: func() {
			cmd := probe(a.Name)
			if d := c.active().Decide(cmd); d.Verdict == rex.Deny {
				c.refuse(cmd, d.Reason)
				return
			}
			pick(a)
		}})
	}
	c.Popups.Menu(title, items)
}

// menu is the x menu: the panel's own bindings, then its extra actions. The
// main view and the command log show the menu of the side panel behind them.
func (c *Controller) menu(p Panel) {
	if !p.isSide() {
		p = c.st.Side
	}
	type entry struct {
		keys         []string
		action, desc string
	}
	var entries []*entry
	byAction := map[string]*entry{}
	for _, b := range c.Bindings(p) {
		if b.Global {
			continue
		}
		e := byAction[b.Action]
		if e == nil {
			e = &entry{action: b.Action, desc: b.Desc}
			byAction[b.Action] = e
			entries = append(entries, e)
		}
		e.keys = append(e.keys, b.Key)
	}
	var items []MenuItem
	for _, e := range entries {
		items = append(items, MenuItem{Label: strings.Join(e.keys, "/") + "  " + e.desc, OnSelect: func() {
			if errors.Is(c.Dispatch(p, e.action), ErrQuit) {
				c.quitting = true
			}
		}})
	}
	switch p {
	case PanelTargets:
		items = append(items, MenuItem{Label: "run a server action on " + render.Label(c.st.TargetName) + "…", OnSelect: c.runServerAction})
	case PanelBlocks:
		items = append(items, MenuItem{Label: "resize pane…", OnSelect: c.resizePane})
	}
	c.Popups.Menu("Actions: "+p.String(), items)
}

// Forms

// snapshot is the target and selection when an action starts. Work that
// reads Rex before asking anything uses it instead of the selection of the
// moment its result arrives, drops its result if the target changed, and a
// command built from it is refused once the target changed.
type snapshot struct {
	key     uint64
	prefill map[string]string
}

func (c *Controller) snapshot() snapshot {
	m := map[string]string{}
	if e, ok := c.st.SelectedSession(); ok {
		m["session_id"] = string(e.SessionID)
	}
	if w, ok := c.st.SelectedWindow(); ok {
		m["session_id"], m["window_id"] = string(w.Session), string(w.Window.WindowID)
	}
	if b, ok := c.st.SelectedBlock(); ok {
		m["session_id"] = string(b.Session)
		for _, k := range []string{"block_id", "pane_id", "block", "pane"} {
			m[k] = string(b.Info.BlockID)
		}
	}
	return snapshot{key: c.targetGen(), prefill: m}
}

// stale reports, with a toast, that the target changed since s was taken.
func (c *Controller) stale(s snapshot) bool {
	if s.key == c.targetGen() {
		return false
	}
	c.Popups.Toast("the target changed; action cancelled")
	return true
}

// recheck turns a check into a pre-run hook of runChecked, which calls it on
// the worker right before the command, after any confirmation.
func recheck(problem func() string) func() ([]rex.BlockID, error) {
	return func() ([]rex.BlockID, error) {
		if p := problem(); p != "" {
			return nil, errors.New(p)
		}
		return nil, nil
	}
}

func fieldTitle(name string, f rex.Field) string {
	typ := f.Type
	if typ == "" {
		typ = "any"
	}
	if f.Required {
		typ += ", required"
	}
	t := fmt.Sprintf("%s: %s (%s)", name, f.Name, typ)
	if f.Description != "" {
		t += ": " + f.Description
	}
	return t
}

// form asks for each argument of a JSON Schema, an enum from a menu, and
// calls done with the arguments as one JSON object, "" when there are none.
func (c *Controller) form(name string, schema json.RawMessage, prefill map[string]string, done func(args string)) {
	fields, free, err := rex.SchemaFields(schema)
	if err != nil {
		c.Popups.Error("unusable schema: " + err.Error())
		return
	}
	if len(fields) == 0 {
		if !free {
			done("")
			return
		}
		c.Popups.Prompt(name+": arguments (a JSON object, empty for none)", "", func(s string) {
			s = strings.TrimSpace(s)
			var obj map[string]json.RawMessage
			if s != "" && (json.Unmarshal([]byte(s), &obj) != nil || obj == nil) {
				c.Popups.Error("arguments must be a JSON object")
				return
			}
			done(s)
		})
		return
	}
	values := map[string]any{}
	var ask func(i int)
	ask = func(i int) {
		if i == len(fields) {
			done(rex.BuildArgs(values))
			return
		}
		f := fields[i]
		set := func(in string) {
			v, skip, err := rex.FieldValue(f, in)
			if err != nil {
				c.Popups.Error(err.Error())
				return
			}
			if !skip {
				values[f.Name] = v
			}
			ask(i + 1)
		}
		if len(f.Enum) == 0 {
			c.Popups.Prompt(fieldTitle(name, f), prefill[f.Name], set)
			return
		}
		var items []MenuItem
		for _, e := range f.Enum {
			items = append(items, MenuItem{Label: e, OnSelect: func() { set(e) }})
		}
		if !f.Required {
			items = append(items, MenuItem{Label: "(skip)", OnSelect: func() { set("") }})
		}
		c.Popups.Menu(fieldTitle(name, f), items)
	}
	ask(0)
}

// Server actions

func (c *Controller) runServerAction() {
	snap := c.snapshot()
	c.ui.Go(func() {
		acts, err := parseActionsOutcome(c.read(cmds.Actions()))
		c.ui.Update(func() {
			if c.stale(snap) {
				return
			}
			if err != nil {
				c.Popups.Error("cannot read the server actions: " + err.Error())
				return
			}
			c.actionMenu("Server actions", acts, func(n string) rex.Cmd { return cmds.Do(n) }, func(a rex.Action) {
				c.form(a.Name, a.Args, snap.prefill, func(args string) {
					cmd := cmds.Do(a.Name)
					if args != "" {
						cmd = cmds.DoJSON(a.Name, args)
					}
					c.runServerDo(snap, "Run "+a.Name, cmd)
				})
			})
		})
	})
}

// runServerDo runs a server `do` through the policy. It checks against
// fresh lists that the server offers the action and that no client would
// get it instead: on the worker before asking, so a refusal comes first, and
// again right before the call, since the lists can change while a
// confirmation waits.
func (c *Controller) runServerDo(snap snapshot, title string, cmd rex.Cmd) {
	if d := c.active().Decide(cmd); d.Verdict == rex.Deny {
		c.refuse(cmd, d.Reason)
		return
	}
	name := cmd.Name
	c.ui.Go(func() {
		reason := c.serverActionProblem(name)
		c.ui.Update(func() {
			if c.stale(snap) {
				return
			}
			if reason != "" {
				c.refuse(cmd, reason)
				return
			}
			c.runOn(snap.key, title, cmd, recheck(func() string { return c.serverActionProblem(name) }), func(o rex.Outcome) {
				c.logOutput(o)
				c.refreshSessions()
			})
		})
	})
}

func (c *Controller) serverActionProblem(name string) string {
	acts, err := parseActionsOutcome(c.read(cmds.Actions()))
	if err != nil {
		return "cannot check that " + name + " is a server action: actions: " + err.Error()
	}
	if !slices.ContainsFunc(acts, func(a rex.Action) bool { return a.Name == name }) {
		return "not a server action: " + name + " is not listed by rex actions (client actions run from the Clients panel, a)"
	}
	return c.clientPrefixProblem(name)
}

// clientPrefixProblem guards against rex do sending a dotted name to a
// client, as it does when the prefix is a client's: it refuses a dotted name
// whose first segment starts a dotted client action, and refuses when any
// client or its actions cannot be read. CLI clients offer no actions.
func (c *Controller) clientPrefixProblem(name string) string {
	unknown := func(why string) string {
		return "cannot check that no client would receive " + name + ": " + why
	}
	out, err := c.read(cmds.ClientLs())
	if !succeeded(out, err) {
		return unknown("client ls: " + errText(out, err))
	}
	list, err := rex.ParseClientLs(out.Stdout)
	if err != nil {
		return unknown(err.Error())
	}
	for _, cl := range list.Clients {
		if strings.EqualFold(cl.Info.Kind, "cli") || strings.EqualFold(string(cl.ConnectionState), "disconnected") {
			continue
		}
		acts, err := parseActionsOutcome(c.read(cmds.ClientActions(cl.ClientID)))
		if err != nil {
			return unknown(fmt.Sprintf("actions of %s: %s", cl.ClientID, err))
		}
		for _, a := range acts {
			if overlapsClientAction(name, a.Name) {
				return fmt.Sprintf("%s overlaps %s, an action of client %s (%s), so rex do could send it there: run client actions from the Clients panel (a)",
					name, render.Label(a.Name), label(cl.Info.Name, "(no name)"), cl.ClientID)
			}
		}
	}
	return ""
}

func overlapsClientAction(name, clientAction string) bool {
	prefix, _, dotted := strings.Cut(name, ".")
	cprefix, _, cdotted := strings.Cut(clientAction, ".")
	return dotted && cdotted && strings.EqualFold(prefix, cprefix)
}

// logOutput writes a command's output to the command log.
func (c *Controller) logOutput(o rex.Outcome) {
	const maxLines = 200
	if text := strings.TrimRight(prettyJSON(o.Stdout), "\n"); text != "" {
		ls := strings.Split(text, "\n")
		if n := len(ls) - maxLines; n > 0 {
			ls = append(ls[:maxLines], fmt.Sprintf("… %d more lines", n))
		}
		for i := range ls {
			ls[i] = "    " + ls[i]
		}
		c.st.appendLog(ls...)
	}
	c.Popups.Toast("done (output in the command log)")
}

// Client actions

func (c *Controller) runClientAction() {
	cl, ok := c.st.SelectedClient()
	if !ok {
		c.Popups.Toast("no client selected")
		return
	}
	id, name := cl.ClientID, label(cl.Info.Name, string(cl.ClientID))
	snap := c.snapshot()
	c.ui.Go(func() {
		acts, err := parseActionsOutcome(c.read(cmds.ClientActions(id)))
		c.ui.Update(func() {
			if c.stale(snap) {
				return
			}
			if err != nil {
				c.Popups.Error("cannot read the actions of " + name + ": " + err.Error())
				return
			}
			c.actionMenu("Actions of "+name, acts, func(n string) rex.Cmd { return cmds.ClientDo(id, n) }, func(a rex.Action) {
				c.form(a.Name, a.Args, snap.prefill, func(args string) {
					cmd := cmds.ClientDo(id, a.Name)
					if args != "" {
						cmd = cmds.ClientDoJSON(id, a.Name, args)
					}
					c.runOn(snap.key, "Run "+a.Name+" on "+name, cmd, recheck(func() string { return c.clientActionProblem(id, name, a.Name) }), c.logOutput)
				})
			})
		})
	})
}

// clientActionProblem reports, from a fresh read, that client id no longer
// offers action, or that its actions cannot be read.
func (c *Controller) clientActionProblem(id rex.ClientID, name, action string) string {
	acts, err := parseActionsOutcome(c.read(cmds.ClientActions(id)))
	switch {
	case err != nil:
		return "cannot check that " + name + " offers " + action + ": " + err.Error()
	case !slices.ContainsFunc(acts, func(a rex.Action) bool { return a.Name == action }):
		return name + " does not offer " + action
	}
	return ""
}

// Show in app

func (c *Controller) showInApp() {
	e, ok := c.st.SelectedSession()
	if !ok {
		return
	}
	s, snap := e.SessionID, c.snapshot()
	c.ui.Go(func() {
		out, err := c.read(cmds.ClientLs())
		var apps []rex.ClientEntry
		problem := ""
		if !succeeded(out, err) {
			problem = "cannot list clients: " + errText(out, err)
		} else if list, perr := rex.ParseClientLs(out.Stdout); perr != nil {
			problem = "cannot list clients: " + perr.Error()
		} else {
			for _, cl := range list.Clients {
				if strings.EqualFold(cl.Info.Kind, "app") && cl.ClientID.Valid() && !strings.EqualFold(string(cl.ConnectionState), "disconnected") {
					apps = append(apps, cl)
				}
			}
		}
		c.ui.Update(func() {
			if c.stale(snap) {
				return
			}
			switch {
			case problem != "":
				c.Popups.Error(problem)
			case len(apps) == 0:
				c.Popups.Error("no Rex app is connected")
			case len(apps) == 1:
				c.showOn(apps[0], s, snap)
			default:
				var items []MenuItem
				for _, a := range apps {
					items = append(items, MenuItem{
						Label:    fmt.Sprintf("%s  %s  %s", label(a.Info.Name, "(no name)"), a.ClientID, render.Label(a.Info.Platform)),
						OnSelect: func() { c.showOn(a, s, snap) },
					})
				}
				c.Popups.Menu("Show "+c.st.sessionLabel(s)+" in", items)
			}
		})
	})
}

// showOn runs session.select on app, checking in the same worker step that
// the app offers it.
func (c *Controller) showOn(app rex.ClientEntry, s rex.SessionID, snap snapshot) {
	if c.stale(snap) {
		return
	}
	id, name := app.ClientID, label(app.Info.Name, string(app.ClientID))
	c.runOn(snap.key, "Show in "+name, cmds.ClientDo(id, "session.select", "session_id="+string(s)),
		recheck(func() string { return c.clientActionProblem(id, name, "session.select") }),
		func(rex.Outcome) { c.Popups.Toast("shown in " + name) })
}

// Resize

const resizeMethod = "session.resize_pane"

// resizePane builds its form from `api describe`, as Rex has no resize command.
func (c *Controller) resizePane() {
	b, ok := c.st.SelectedBlock()
	if !ok {
		c.Popups.Toast("no block selected")
		return
	}
	snap := c.snapshot()
	c.ui.Go(func() {
		out, err := c.read(cmds.APIDescribe(resizeMethod))
		var schema json.RawMessage
		problem := ""
		if !succeeded(out, err) {
			problem = errText(out, err)
		} else if schema, err = rex.MethodSchema(out.Stdout); err != nil {
			problem = err.Error()
		}
		c.ui.Update(func() {
			if c.stale(snap) {
				return
			}
			if problem != "" {
				c.Popups.Error("cannot read the schema of " + resizeMethod + ": " + problem)
				return
			}
			if fields, _, err := rex.SchemaFields(schema); err == nil && len(fields) == 0 {
				c.Popups.Error("unknown schema; use the palette: api call -s " + string(b.Session) + " " + resizeMethod + " {...}")
				return
			}
			c.form(resizeMethod, schema, snap.prefill, func(args string) {
				c.runOn(snap.key, "Resize pane", cmds.APICall(b.Session, resizeMethod, args), nil, func(rex.Outcome) { c.refreshSession(b.Session) })
			})
		})
	})
}
