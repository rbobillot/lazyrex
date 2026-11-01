package gui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rbobillot/lazyrex/pkg/config"
)

// Binding maps a key to an action, in one panel or in every panel (Global).
type Binding struct {
	Panel  Panel
	Global bool
	Action string
	Key    string
	Desc   string
	Footer bool
}

var namedKeys = map[string]bool{
	"<tab>": true, "<backtab>": true, "<enter>": true, "<esc>": true, "<space>": true,
	"<pgup>": true, "<pgdown>": true, "<up>": true, "<down>": true, "<left>": true,
	"<right>": true, "<home>": true, "<end>": true,
}

// ValidKeyName accepts one printable character, a named key, or <c-a> to
// <c-z> except <c-h>, <c-i> and <c-m>, which terminals send as backspace,
// tab and enter.
func ValidKeyName(k string) bool {
	if namedKeys[k] {
		return true
	}
	if len(k) == 5 && strings.HasPrefix(k, "<c-") && k[4] == '>' {
		c := k[3]
		return c >= 'a' && c <= 'z' && c != 'h' && c != 'i' && c != 'm'
	}
	r, n := utf8.DecodeRuneInString(k)
	return n == len(k) && r != utf8.RuneError && unicode.IsPrint(r) && !unicode.IsSpace(r)
}

func DefaultBindings() []Binding {
	var bs []Binding
	g := func(action, desc string, footer bool, keys ...string) {
		for _, k := range keys {
			bs = append(bs, Binding{Global: true, Action: action, Key: k, Desc: desc, Footer: footer && k == keys[0]})
		}
	}
	p := func(panel Panel, action, desc string, footer bool, keys ...string) {
		for _, k := range keys {
			bs = append(bs, Binding{Panel: panel, Action: action, Key: k, Desc: desc, Footer: footer && k == keys[0]})
		}
	}
	g("quit", "quit", true, "q", "<c-c>")
	g("back", "close popup, clear filter, go back", false, "<esc>")
	g("help", "keybindings", true, "?")
	g("menu", "action menu", true, "x")
	g("palette", "command palette", false, ":")
	g("filter", "filter list or search main view", true, "/")
	g("commandLog", "focus the command log", false, "@")
	for i := 1; i <= 5; i++ {
		g(fmt.Sprintf("jump%d", i), "jump to "+sidePanels[i-1].String(), false, fmt.Sprint(i))
	}
	g("jumpMain", "focus the main view", false, "0")
	g("nextPanel", "next panel", false, "<tab>", "l", "<right>")
	g("prevPanel", "previous panel", false, "<backtab>", "h", "<left>")
	g("down", "move down", false, "j", "<down>")
	g("up", "move up", false, "k", "<up>")
	g("top", "go to top", false, "<")
	g("bottom", "go to bottom", false, ">")
	g("pageUp", "page up", false, ",")
	g("pageDown", "page down", false, ".")
	g("prevTab", "previous main tab", false, "[")
	g("nextTab", "next main tab", false, "]")
	g("scrollPageUp", "scroll main view a page up", false, "<pgup>", "<c-u>")
	g("scrollPageDown", "scroll main view a page down", false, "<pgdown>", "<c-d>")
	g("scrollUp", "scroll main view up", false, "K")
	g("scrollDown", "scroll main view down", false, "J")
	g("refresh", "refresh now", false, "R")

	p(PanelTargets, "activate", "activate target", false, "<enter>", "<space>")
	p(PanelTargets, "startServer", "start server", true, "s")
	p(PanelTargets, "stopServer", "stop server", true, "S")
	p(PanelTargets, "checkTarget", "check reachability", true, "c")
	p(PanelTargets, "addTarget", "add a config target", true, "a")
	p(PanelTargets, "editTarget", "edit a config target", false, "e")
	p(PanelTargets, "removeTarget", "remove a config target", false, "d")

	p(PanelSessions, "scope", "scope windows and blocks to it", true, "<enter>")
	p(PanelSessions, "newSession", "new session", true, "n")
	p(PanelSessions, "renameSession", "rename session", true, "r")
	p(PanelSessions, "killSession", "kill session", true, "d")
	p(PanelSessions, "showInApp", "show in a Rex app", true, "o")

	p(PanelWindows, "scope", "scope blocks to it", true, "<enter>")
	p(PanelWindows, "toggleAll", "all sessions", false, "A")
	p(PanelWindows, "focusWindow", "focus window", true, "f")
	p(PanelWindows, "newWindow", "new window", true, "n")
	p(PanelWindows, "renameWindow", "rename window", false, "r")
	p(PanelWindows, "closeWindow", "close window", true, "d")

	p(PanelBlocks, "showCapture", "show capture", false, "<enter>")
	p(PanelBlocks, "toggleAll", "all sessions", false, "A")
	p(PanelBlocks, "focusBlock", "focus block", true, "f")
	p(PanelBlocks, "toggleZoom", "zoom toggle", false, "z")
	p(PanelBlocks, "newPane", "new pane", true, "n")
	p(PanelBlocks, "splitRight", "split right", false, "|")
	p(PanelBlocks, "splitBelow", "split below", false, "-")
	p(PanelBlocks, "sendText", "send text", true, "i")
	p(PanelBlocks, "sendTextEnter", "send text then enter", false, "I")
	p(PanelBlocks, "pressKey", "press a key", true, "p")
	p(PanelBlocks, "renameBlock", "rename block", false, "r")
	p(PanelBlocks, "closeBlock", "close block", true, "d")

	p(PanelClients, "showInspect", "show inspect", true, "<enter>")
	p(PanelClients, "toggleCLI", "show CLI clients", true, "A")
	p(PanelClients, "runClientAction", "run a client action", true, "a")

	p(PanelMain, "nextMatch", "next match", true, "n")
	p(PanelMain, "prevMatch", "previous match", true, "N")
	return bs
}

// BindingsFor returns the bindings active in panel p: its own, then globals.
func BindingsFor(bs []Binding, p Panel) []Binding {
	var own, global []Binding
	for _, b := range bs {
		switch {
		case b.Global:
			global = append(global, b)
		case b.Panel == p:
			own = append(own, b)
		}
	}
	return append(own, global...)
}

var configPanels = map[string]Panel{
	"targets": PanelTargets, "sessions": PanelSessions, "windows": PanelWindows,
	"blocks": PanelBlocks, "clients": PanelClients, "main": PanelMain,
}

const universal = "universal"

func panelNamesList() string {
	names := []string{universal}
	for n := range configPanels {
		names = append(names, n)
	}
	sort.Strings(names[1:])
	return strings.Join(names, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ResolveBindings applies config overrides to the defaults. An override
// replaces every default key of its action. Unknown panels, actions or keys,
// and a key bound twice in one panel, are errors.
func ResolveBindings(over map[string]map[string]config.KeyList) ([]Binding, error) {
	bs := DefaultBindings()
	inScope := func(b Binding, panelName string) bool {
		if panelName == universal {
			return b.Global
		}
		return !b.Global && b.Panel == configPanels[panelName]
	}
	for _, pn := range sortedKeys(over) {
		if _, ok := configPanels[pn]; !ok && pn != universal {
			return nil, fmt.Errorf("keybindings: unknown panel %q (want one of %s)", pn, panelNamesList())
		}
		for _, action := range sortedKeys(over[pn]) {
			keys := over[pn][action]
			i := slices.IndexFunc(bs, func(b Binding) bool { return b.Action == action && inScope(b, pn) })
			if i < 0 {
				if slices.ContainsFunc(bs, func(b Binding) bool { return b.Action == action }) {
					return nil, fmt.Errorf("keybindings.%s: %s is not a %s action", pn, action, pn)
				}
				return nil, fmt.Errorf("keybindings.%s: unknown action %q", pn, action)
			}
			if len(keys) == 0 {
				return nil, fmt.Errorf("keybindings.%s.%s: empty key list", pn, action)
			}
			for _, k := range keys {
				if !ValidKeyName(k) {
					return nil, fmt.Errorf("keybindings.%s.%s: invalid key %q", pn, action, k)
				}
			}
			proto := bs[i]
			bs = slices.DeleteFunc(bs, func(b Binding) bool { return b.Action == action && inScope(b, pn) })
			for j, k := range keys {
				nb := proto
				nb.Key, nb.Footer = k, proto.Footer && j == 0
				bs = append(bs, nb)
			}
		}
	}
	for _, p := range []Panel{PanelTargets, PanelSessions, PanelWindows, PanelBlocks, PanelClients, PanelMain, PanelLog} {
		seen := map[string]string{}
		for _, b := range BindingsFor(bs, p) {
			if prev, ok := seen[b.Key]; ok {
				return nil, fmt.Errorf("keybindings: key %q is bound to both %s and %s in the %s panel", b.Key, prev, b.Action, p)
			}
			seen[b.Key] = b.Action
		}
	}
	return bs, nil
}

// HelpItems lists the bindings active in p, one "key  description" line each.
func HelpItems(bs []Binding, p Panel) []string {
	active := BindingsFor(bs, p)
	items := make([]string, 0, len(active))
	for _, b := range active {
		items = append(items, fmt.Sprintf("%-10s %s", b.Key, b.Desc))
	}
	return items
}
