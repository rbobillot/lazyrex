package gui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/config"
)

var allPanels = []Panel{PanelTargets, PanelSessions, PanelWindows, PanelBlocks, PanelClients, PanelMain, PanelLog}

func assertNoDuplicates(t *testing.T, bs []Binding) {
	t.Helper()
	for _, p := range allPanels {
		seen := map[string]string{}
		for _, b := range BindingsFor(bs, p) {
			if prev, ok := seen[b.Key]; ok {
				t.Errorf("panel %v: key %q bound to %s and %s", p, b.Key, prev, b.Action)
			}
			seen[b.Key] = b.Action
		}
	}
}

func TestKeymapDefaultsNoDuplicates(t *testing.T) {
	bs := DefaultBindings()
	assertNoDuplicates(t, bs)
	for _, b := range bs {
		if !ValidKeyName(b.Key) {
			t.Errorf("%s: invalid default key %q", b.Action, b.Key)
		}
		if b.Desc == "" {
			t.Errorf("%s: no description", b.Action)
		}
	}
	want := map[Panel][]string{
		PanelTargets:  {"<enter>", "<space>", "s", "S", "c"},
		PanelSessions: {"<enter>", "n", "r", "d", "o"},
		PanelWindows:  {"<enter>", "A", "f", "n", "r", "d"},
		PanelBlocks:   {"<enter>", "A", "f", "z", "n", "|", "-", "i", "I", "p", "r", "d"},
		PanelClients:  {"<enter>", "A", "a"},
		PanelMain:     {"n", "N"},
	}
	global := []string{"q", "<c-c>", "<esc>", "?", "/", "@", "1", "2", "3", "4", "5", "0", "<tab>", "<backtab>", "h", "l",
		"<left>", "<right>", "j", "k", "<up>", "<down>", "<", ">", ",", ".", "[", "]", "<pgup>", "<pgdown>", "<c-u>", "<c-d>", "K", "J", "R",
		"x", ":"}
	for _, p := range allPanels {
		keys := map[string]bool{}
		for _, b := range BindingsFor(bs, p) {
			keys[b.Key] = true
		}
		for _, k := range append(global, want[p]...) {
			if !keys[k] {
				t.Errorf("panel %v: key %q not bound", p, k)
			}
		}
	}
}

func TestKeymapSampleOverride(t *testing.T) {
	bs, err := ResolveBindings(map[string]map[string]config.KeyList{
		"blocks":    {"sendText": {"t"}},
		"universal": {"refresh": {"<c-r>"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoDuplicates(t, bs)
	keyOf := func(p Panel, action string) []string {
		var out []string
		for _, b := range BindingsFor(bs, p) {
			if b.Action == action {
				out = append(out, b.Key)
			}
		}
		return out
	}
	if k := keyOf(PanelBlocks, "sendText"); len(k) != 1 || k[0] != "t" {
		t.Fatalf("sendText keys = %v", k)
	}
	if k := keyOf(PanelSessions, "refresh"); len(k) != 1 || k[0] != "<c-r>" {
		t.Fatalf("refresh keys = %v", k)
	}
	for _, b := range BindingsFor(bs, PanelBlocks) {
		if b.Key == "i" || b.Key == "R" {
			t.Fatalf("old key %q still bound to %s", b.Key, b.Action)
		}
	}
}

func TestKeymapPhase2Bindings(t *testing.T) {
	actionOf := func(bs []Binding, p Panel, key string) string {
		for _, b := range BindingsFor(bs, p) {
			if b.Key == key {
				return b.Action
			}
		}
		return ""
	}
	bs := DefaultBindings()
	for _, p := range allPanels {
		if actionOf(bs, p, "x") != "menu" || actionOf(bs, p, ":") != "palette" {
			t.Errorf("panel %v: x = %q, : = %q", p, actionOf(bs, p, "x"), actionOf(bs, p, ":"))
		}
	}
	if actionOf(bs, PanelSessions, "o") != "showInApp" || actionOf(bs, PanelClients, "a") != "runClientAction" {
		t.Errorf("o = %q, a = %q", actionOf(bs, PanelSessions, "o"), actionOf(bs, PanelClients, "a"))
	}
	over, err := ResolveBindings(map[string]map[string]config.KeyList{
		"sessions":  {"showInApp": {"O"}},
		"universal": {"palette": {";"}},
		"clients":   {"runClientAction": {"<c-a>"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertNoDuplicates(t, over)
	if actionOf(over, PanelSessions, "O") != "showInApp" || actionOf(over, PanelSessions, "o") != "" {
		t.Errorf("showInApp override not applied")
	}
	if actionOf(over, PanelBlocks, ";") != "palette" || actionOf(over, PanelBlocks, ":") != "" {
		t.Errorf("palette override not applied")
	}
	if actionOf(over, PanelClients, "<c-a>") != "runClientAction" {
		t.Errorf("runClientAction override not applied")
	}
}

// TestWriteRexHostsBinding (F11): w stays bound whatever
// experimental.rex_hosts_file says, so an override of it stays valid.
func TestWriteRexHostsBinding(t *testing.T) {
	keysOf := func(bs []Binding) []string {
		var out []string
		for _, b := range BindingsFor(bs, PanelTargets) {
			if b.Action == "writeRexHosts" {
				out = append(out, b.Key)
			}
		}
		return out
	}
	if k := keysOf(DefaultBindings()); !slices.Equal(k, []string{"w"}) {
		t.Fatalf("writeRexHosts keys = %v", k)
	}
	over, err := ResolveBindings(map[string]map[string]config.KeyList{"targets": {"writeRexHosts": {"W"}}})
	if err != nil {
		t.Fatal(err)
	}
	if k := keysOf(over); !slices.Equal(k, []string{"W"}) {
		t.Fatalf("overridden keys = %v", k)
	}
	if help := HelpItems(DefaultBindings(), PanelTargets); !slices.Contains(help, "w          write the Rex app hosts file (experimental, off by default)") {
		t.Fatalf("help %q", help)
	}
}

func TestKeymapOverrideConflict(t *testing.T) {
	cases := map[string]struct {
		over  map[string]map[string]config.KeyList
		parts []string
	}{
		"panel conflict":   {map[string]map[string]config.KeyList{"blocks": {"sendText": {"d"}}}, []string{`"d"`, "sendText", "closeBlock"}},
		"global conflict":  {map[string]map[string]config.KeyList{"blocks": {"sendText": {"q"}}}, []string{`"q"`, "sendText", "quit"}},
		"universal clash":  {map[string]map[string]config.KeyList{"universal": {"refresh": {"n"}}}, []string{`"n"`, "refresh"}},
		"unknown action":   {map[string]map[string]config.KeyList{"blocks": {"nope": {"t"}}}, []string{"nope"}},
		"unknown panel":    {map[string]map[string]config.KeyList{"nowhere": {"sendText": {"t"}}}, []string{"nowhere"}},
		"invalid key":      {map[string]map[string]config.KeyList{"blocks": {"sendText": {"<nope>"}}}, []string{"<nope>"}},
		"empty key list":   {map[string]map[string]config.KeyList{"blocks": {"sendText": {}}}, []string{"sendText"}},
		"wrong panel name": {map[string]map[string]config.KeyList{"sessions": {"sendText": {"t"}}}, []string{"sendText"}},
		"client action A":  {map[string]map[string]config.KeyList{"clients": {"runClientAction": {"A"}}}, []string{`"A"`, "runClientAction", "toggleCLI"}},
		"menu on n":        {map[string]map[string]config.KeyList{"universal": {"menu": {"n"}}}, []string{`"n"`, "menu"}},
		"palette on o":     {map[string]map[string]config.KeyList{"universal": {"palette": {"o"}}}, []string{`"o"`, "palette", "showInApp"}},
		"show in app on d": {map[string]map[string]config.KeyList{"sessions": {"showInApp": {"d"}}}, []string{`"d"`, "showInApp", "killSession"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveBindings(c.over)
			if err == nil {
				t.Fatalf("accepted %v", c.over)
			}
			for _, p := range c.parts {
				if !strings.Contains(err.Error(), p) {
					t.Errorf("error %q does not mention %s", err, p)
				}
			}
		})
	}
}

func TestEveryBindingDispatches(t *testing.T) {
	h := newHarness(t)
	for _, b := range DefaultBindings() {
		panels := []Panel{b.Panel}
		if b.Global {
			panels = allPanels
		}
		for _, p := range panels {
			h.ctrl.Popups.Clear()
			h.ctrl.Focus(p)
			err := h.ctrl.Dispatch(p, b.Action)
			if err != nil && !errors.Is(err, ErrQuit) {
				t.Errorf("panel %v: %s (%s): %v", p, b.Action, b.Key, err)
			}
			found := false
			for _, item := range HelpItems(h.ctrl.Bindings(p), p) {
				if strings.HasPrefix(item, b.Key+" ") || strings.Contains(item, " "+b.Key+" ") {
					found = true
				}
			}
			if !found {
				t.Errorf("panel %v: %s (%s) missing from help", p, b.Action, b.Key)
			}
		}
	}
}
