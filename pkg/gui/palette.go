package gui

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

// targetCache keeps completion data of one server instance.
type targetCache struct {
	key           uint64
	methods       []string
	hasMethods    bool
	actions       []string
	hasActions    bool
	completeField string
}

// targetGen returns the target generation; it is safe from any goroutine.
func (c *Controller) targetGen() uint64 { return c.gen.Load() }

// setTarget records the target and its server instance; a change starts a
// generation.
func (c *Controller) setTarget(name, inst string) {
	if name == c.st.TargetName && inst == c.instance {
		return
	}
	c.st.TargetName, c.instance = name, inst
	c.gen.Add(1)
}

// targetCache returns the cache of the current target generation.
func (c *Controller) targetCache() *targetCache {
	if key := c.targetGen(); c.cache.key != key {
		c.cache = targetCache{key: key}
	}
	return &c.cache
}

const paletteTitle = "Command palette: api call [-s SESSION] METHOD [JSON] | do ACTION [KEY=VALUE...|--args JSON] (tab completes)"

func (c *Controller) palette() {
	gen := c.targetGen()
	c.Popups.PromptWithCompletion(paletteTitle, "", c.completePalette, func(line string) { c.runPalette(gen, line) })
}

func (c *Controller) runPalette(gen uint64, line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	cmd, err := rex.ParsePalette(line)
	if err != nil {
		c.refuse(rex.Cmd{Err: err}, "palette: "+err.Error())
		return
	}
	if gen != c.targetGen() {
		c.refuse(cmd, errTargetChanged.Error())
		return
	}
	if cmd.Args[0] == "do" {
		c.runServerDo(snapshot{key: gen}, "Palette", cmd)
		return
	}
	c.runOn(gen, "Palette", cmd, nil, c.logOutput)
}

// Completion

// completion is one way to complete the word under the cursor: from the
// cache when cached reports a hit, else from fetch off the UI goroutine,
// whose result store keeps for the same target only.
type completion struct {
	cached func() ([]string, bool)
	fetch  func() ([]string, error)
	store  func([]string)
	pick   func([]string) []string
}

func withPrefix(list []string, prefix string) []string {
	var out []string
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

func (c *Controller) completePalette(p *Popup, input string) {
	words, trailing, err := rex.SplitLine(input)
	if err != nil {
		c.Popups.Toast("no completion")
		return
	}
	slot, session, prefix := rex.PaletteSlot(words, trailing)
	if !strings.HasSuffix(input, prefix) {
		c.Popups.Toast("no completion")
		return
	}
	static := func(list ...string) completion {
		return completion{cached: func() ([]string, bool) { return list, true }, pick: func(l []string) []string { return withPrefix(l, prefix) }}
	}
	var comp completion
	switch slot {
	case rex.SlotVerb:
		comp = static("api", "do")
	case rex.SlotCall:
		comp = static("call")
	case rex.SlotMethod:
		comp = c.methodCompletion(prefix)
	case rex.SlotAction:
		comp = c.actionCompletion(prefix)
	case rex.SlotTarget:
		comp = c.targetCompletion(session, prefix)
	default:
		c.Popups.Toast("no completion")
		return
	}
	c.runCompletion(p, input, prefix, comp)
}

func (c *Controller) runCompletion(p *Popup, input, prefix string, comp completion) {
	if comp.cached != nil {
		if list, ok := comp.cached(); ok {
			c.offer(p, p.inputRev, input, prefix, comp.pick(list))
			return
		}
	}
	key, rev := c.targetGen(), p.inputRev
	c.ui.Go(func() {
		list, err := comp.fetch()
		c.ui.Update(func() {
			if key != c.targetGen() {
				return
			}
			if err != nil {
				if c.Popups.Top() == p {
					c.Popups.Toast("no completion: " + err.Error())
				}
				return
			}
			if comp.store != nil {
				comp.store(list)
			}
			c.offer(p, rev, input, prefix, comp.pick(list))
		})
	})
}

// offer applies candidates to prompt p when its text is still input: one
// replaces the word, several open a menu.
func (c *Controller) offer(p *Popup, rev int, input, prefix string, cands []string) {
	if c.Popups.Top() != p || p.inputRev != rev || c.Popups.Input(p) != input {
		return
	}
	cands = slices.DeleteFunc(cands, func(s string) bool { return !paletteWord(s) })
	base := input[:len(input)-len(prefix)]
	switch len(cands) {
	case 0:
		c.Popups.Toast("no completion")
	case 1:
		c.Popups.SetInputIf(p, rev, base+cands[0])
	default:
		const maxItems = 50
		items := make([]MenuItem, 0, min(len(cands), maxItems))
		for _, cand := range cands[:min(len(cands), maxItems)] {
			items = append(items, MenuItem{Label: cand, OnSelect: func() { c.Popups.SetInputIf(p, rev, base+cand) }})
		}
		c.Popups.Menu("Complete "+prefix, items)
	}
}

// paletteWord reports a candidate that stays one literal word of the line.
func paletteWord(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`'"\`, r)
	})
}

func (c *Controller) methodCompletion(prefix string) completion {
	tc := c.targetCache()
	return completion{
		cached: func() ([]string, bool) { return tc.methods, tc.hasMethods },
		fetch: func() ([]string, error) {
			out, err := c.read(cmds.APIList())
			if !succeeded(out, err) {
				return nil, fmt.Errorf("api list: %s", errText(out, err))
			}
			return rex.ParseAPIList(out.Stdout), nil
		},
		store: func(l []string) { tc := c.targetCache(); tc.methods, tc.hasMethods = l, true },
		pick: func(l []string) []string {
			return slices.DeleteFunc(withPrefix(l, prefix), func(m string) bool {
				return c.active().Decide(cmds.APICall("", m, "")).Verdict == rex.Deny
			})
		},
	}
}

func (c *Controller) actionCompletion(prefix string) completion {
	tc := c.targetCache()
	return completion{
		cached: func() ([]string, bool) { return tc.actions, tc.hasActions },
		fetch: func() ([]string, error) {
			acts, err := parseActionsOutcome(c.read(cmds.Actions()))
			if err != nil {
				return nil, fmt.Errorf("actions: %s", err)
			}
			var names []string
			for _, a := range acts {
				names = append(names, a.Name)
			}
			return names, nil
		},
		store: func(l []string) { tc := c.targetCache(); tc.actions, tc.hasActions = l, true },
		pick: func(l []string) []string {
			return slices.DeleteFunc(withPrefix(l, prefix), func(n string) bool {
				return c.active().Decide(cmds.Do(n)).Verdict == rex.Deny
			})
		},
	}
}

// targetCompletion asks the server's target.complete, whose prefix argument
// is read once per instance from its schema.
func (c *Controller) targetCompletion(session rex.SessionID, prefix string) completion {
	field := c.targetCache().completeField
	return completion{
		fetch: func() ([]string, error) {
			if field == "" {
				out, err := c.read(cmds.APIDescribe("target.complete"))
				if !succeeded(out, err) {
					return nil, fmt.Errorf("api describe target.complete: %s", errText(out, err))
				}
				if field, err = rex.CompleteField(out.Stdout); err != nil {
					return nil, err
				}
			}
			out, err := c.read(cmds.APICall(session, "target.complete", rex.BuildArgs(map[string]any{field: prefix})))
			if !succeeded(out, err) {
				return nil, fmt.Errorf("target.complete: %s", errText(out, err))
			}
			return rex.ParseCompletions(out.Stdout)
		},
		store: func([]string) { c.targetCache().completeField = field },
		pick:  func(l []string) []string { return l },
	}
}
