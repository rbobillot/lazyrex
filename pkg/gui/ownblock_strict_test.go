package gui

import (
	"encoding/json"
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

type jsonObj = map[string]any

func editFixture(t *testing.T, name string, edit func(v jsonObj)) string {
	t.Helper()
	var v jsonObj
	if err := json.Unmarshal([]byte(rextest.FixtureString(name)), &v); err != nil {
		t.Fatal(err)
	}
	edit(v)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func windowAt(v jsonObj, i int) jsonObj { return v["windows"].([]any)[i].(jsonObj) }

func layerAt(v jsonObj, w, l int) jsonObj { return windowAt(v, w)["layers"].([]any)[l].(jsonObj) }

func blockAt(v jsonObj, w, l, b int) jsonObj { return layerAt(v, w, l)["blocks"].([]any)[b].(jsonObj) }

func layoutOf(v jsonObj, w, l int) jsonObj { return layerAt(v, w, l)["layout"].(jsonObj) }

// Window 0 of session_inspect.json is window:01w1, the one closeWindow targets.
var incompleteInspects = []struct {
	name string
	edit func(v jsonObj)
	raw  string
}{
	{name: "empty object", raw: "{}"},
	{name: "wrong session id", edit: func(v jsonObj) { v["session_id"] = "session:01zzzz" }},
	{name: "session id missing", edit: func(v jsonObj) { delete(v, "session_id") }},
	{name: "session id null", edit: func(v jsonObj) { v["session_id"] = nil }},
	{name: "session id not full", edit: func(v jsonObj) { v["session_id"] = "01aaaa" }},
	{name: "windows missing", edit: func(v jsonObj) { delete(v, "windows") }},
	{name: "windows null", edit: func(v jsonObj) { v["windows"] = nil }},
	{name: "null window", edit: func(v jsonObj) { v["windows"] = append(v["windows"].([]any), nil) }},
	{name: "window id missing", edit: func(v jsonObj) { delete(windowAt(v, 1), "window_id") }},
	{name: "layers missing", edit: func(v jsonObj) { delete(windowAt(v, 0), "layers") }},
	{name: "layers null", edit: func(v jsonObj) { windowAt(v, 0)["layers"] = nil }},
	{name: "null layer", edit: func(v jsonObj) { w := windowAt(v, 0); w["layers"] = append(w["layers"].([]any), nil) }},
	{name: "blocks missing", edit: func(v jsonObj) { delete(layerAt(v, 0, 0), "blocks") }},
	{name: "blocks null", edit: func(v jsonObj) { layerAt(v, 0, 1)["blocks"] = nil }},
	{name: "null block", edit: func(v jsonObj) { layerAt(v, 0, 0)["blocks"] = append(layerAt(v, 0, 0)["blocks"].([]any), nil) }},
	{name: "block id missing", edit: func(v jsonObj) { delete(blockAt(v, 0, 0, 1), "block_id") }},
	{name: "block id malformed", edit: func(v jsonObj) { blockAt(v, 0, 0, 0)["block_id"] = "01b1" }},
	{name: "block id empty", edit: func(v jsonObj) { blockAt(v, 0, 1, 0)["block_id"] = "" }},
	{name: "block id prefix only", edit: func(v jsonObj) { blockAt(v, 0, 0, 1)["block_id"] = "block:" }},
	{name: "block id not a string", edit: func(v jsonObj) { blockAt(v, 0, 0, 0)["block_id"] = 42 }},
}

func TestContainerDestroyRefusesIncompleteInspect(t *testing.T) {
	for _, c := range containerDestroys {
		for _, tc := range incompleteInspects {
			t.Run(c.action+"/"+tc.name, func(t *testing.T) {
				h := newHarness(t, withSelf(ownBlock))
				fresh := tc.raw
				if tc.edit != nil {
					fresh = editFixture(t, "session_inspect.json", tc.edit)
				}
				h.fr.On("session", "inspect", "session:01aaaa").Return(fresh, "", 0)
				confirmDestroy(t, h, c)
				expectRefused(t, h, c, "own block")
			})
		}
	}
}

func TestCloseWindowRefusesWindowMissingFromInspect(t *testing.T) {
	h := newHarness(t, withSelf(ownBlock))
	fresh := editFixture(t, "session_inspect.json", func(v jsonObj) { v["windows"] = v["windows"].([]any)[1:] })
	h.fr.On("session", "inspect", "session:01aaaa").Return(fresh, "", 0)
	c := containerDestroys[1]
	confirmDestroy(t, h, c)
	expectRefused(t, h, c, "own block")
}

func TestKillSessionRefusesIncompleteBlockLs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		edit func(v jsonObj)
	}{
		{name: "empty object", raw: "{}"},
		{name: "blocks null", raw: `{"blocks": null}`},
		{name: "blocks not an array", raw: `{"blocks": {}}`},
		{name: "null block", edit: func(v jsonObj) { v["blocks"] = append(v["blocks"].([]any), nil) }},
		{name: "block id missing", edit: func(v jsonObj) { delete(v["blocks"].([]any)[4].(jsonObj), "block_id") }},
		{name: "block id malformed", edit: func(v jsonObj) { v["blocks"].([]any)[4].(jsonObj)["block_id"] = "01b5" }},
		{name: "block id null", edit: func(v jsonObj) { v["blocks"].([]any)[0].(jsonObj)["block_id"] = nil }},
	}
	c := containerDestroys[0]
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withSelf(ownBlock))
			fresh := tc.raw
			if tc.edit != nil {
				fresh = editFixture(t, "block_ls.json", tc.edit)
			}
			h.fr.On("block", "ls", "-s", "session:01aaaa").Return(fresh, "", 0)
			confirmDestroy(t, h, c)
			expectRefused(t, h, c, "own block")
		})
	}
}

func TestContainerDestroyAcceptsExplicitlyEmptyCollections(t *testing.T) {
	t.Run("kill with no windows and no blocks", func(t *testing.T) {
		h := newHarness(t, withSelf(ownBlock))
		h.fr.On("session", "inspect", "session:01aaaa").Return(`{"session_id": "session:01aaaa", "windows": []}`, "", 0)
		h.fr.On("block", "ls", "-s", "session:01aaaa").Return(`{"blocks": []}`, "", 0)
		c := containerDestroys[0]
		confirmDestroy(t, h, c)
		if h.count(c.call) != 1 {
			t.Fatalf("%s did not run; calls:\n%s", c.call, h.dumpCalls())
		}
	})
	t.Run("close of a window with empty layers", func(t *testing.T) {
		h := newHarness(t, withSelf(ownBlock))
		fresh := editFixture(t, "session_inspect.json", func(v jsonObj) {
			layerAt(v, 0, 1)["blocks"] = []any{}
			windowAt(v, 1)["layers"] = []any{}
		})
		h.fr.On("session", "inspect", "session:01aaaa").Return(fresh, "", 0)
		c := containerDestroys[1]
		confirmDestroy(t, h, c)
		if h.count(c.call) != 1 {
			t.Fatalf("%s did not run; calls:\n%s", c.call, h.dumpCalls())
		}
	})
}

func expectRan(t *testing.T, h *harness, c destroyCase) {
	t.Helper()
	if n := h.count(c.call); n != 1 {
		t.Fatalf("%s ran %d times; calls:\n%s", c.call, n, h.dumpCalls())
	}
	if p := h.ctrl.Popups.Top(); p != nil && p.Kind == PopupError {
		t.Fatalf("unexpected error popup: %+v", p)
	}
}

func TestContainerDestroyRefusesOwnBlockFoundFresh(t *testing.T) {
	cases := []struct {
		name string
		edit func(v jsonObj)
	}{
		{"floating layer blocks", func(v jsonObj) { blockAt(v, 0, 1, 0)["block_id"] = ownBlock }},
		{"split child of layout only", func(v jsonObj) { layoutOf(v, 0, 0)["children"].([]any)[1].(jsonObj)["block_id"] = ownBlock }},
		{"floating layout leaf only", func(v jsonObj) { layoutOf(v, 0, 1)["block_id"] = ownBlock }},
		{"stack-shaped layout only", func(v jsonObj) {
			layerAt(v, 0, 0)["layout"] = jsonObj{"split": "vertical", "children": []any{
				jsonObj{"block_id": "block:01b1"}, jsonObj{"stack": jsonObj{"block_ids": []any{"block:01b2", ownBlock}}}}}
		}},
	}
	for _, c := range containerDestroys {
		for _, tc := range cases {
			t.Run(c.action+"/"+tc.name, func(t *testing.T) {
				h := newHarness(t, withSelf(ownBlock))
				h.fr.On("session", "inspect", "session:01aaaa").Return(editFixture(t, "session_inspect.json", tc.edit), "", 0)
				confirmDestroy(t, h, c)
				expectRefused(t, h, c, "targets lazyrex's own block")
			})
		}
	}
}

func TestKillSessionRefusesOwnBlockInAnotherWindowsLayout(t *testing.T) {
	h := newHarness(t, withSelf(ownBlock))
	fresh := editFixture(t, "session_inspect.json", func(v jsonObj) { layoutOf(v, 1, 0)["block_id"] = ownBlock })
	h.fr.On("session", "inspect", "session:01aaaa").Return(fresh, "", 0)
	c := containerDestroys[0]
	confirmDestroy(t, h, c)
	expectRefused(t, h, c, "targets lazyrex's own block")
}

// splitLayout is a layout of Rex's real shape, a split whose before and after
// nodes are leaves holding a and b.
func splitLayout(a, b any) jsonObj {
	rect := func(x int) jsonObj { return jsonObj{"x": x, "y": 0, "width": 100, "height": 50} }
	return jsonObj{"rect": jsonObj{"x": 0, "y": 0, "width": 200, "height": 50}, "block_id": nil, "split": jsonObj{
		"split_id": "split:01s1", "direction": "horizontal", "ratio": 0.5,
		"before": jsonObj{"rect": rect(0), "block_id": a, "split": nil},
		"after":  jsonObj{"rect": rect(100), "block_id": b, "split": nil},
	}}
}

func TestContainerDestroyIgnoresLayoutNoise(t *testing.T) {
	cases := []struct {
		name string
		edit func(v jsonObj)
	}{
		{"own block in another window's layout", func(v jsonObj) { layoutOf(v, 1, 0)["block_id"] = ownBlock }},
		{"malformed layout in another window", func(v jsonObj) { layerAt(v, 1, 0)["layout"] = 42 }},
		{"split layout", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = splitLayout("block:01b1", "block:01b2") }},
		{"leaf block id null", func(v jsonObj) { layoutOf(v, 0, 1)["block_id"] = nil }},
		{"empty layer with null layout", func(v jsonObj) { layerAt(v, 0, 1)["blocks"] = []any{}; layerAt(v, 0, 1)["layout"] = nil }},
		{"empty layer without layout", func(v jsonObj) { layerAt(v, 0, 1)["blocks"] = []any{}; delete(layerAt(v, 0, 1), "layout") }},
		{"own block under unrelated keys", func(v jsonObj) {
			layoutOf(v, 0, 0)["children"].([]any)[0].(jsonObj)["focused"] = ownBlock
			layoutOf(v, 0, 1)["previous_block"] = ownBlock
		}},
	}
	c := containerDestroys[1]
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withSelf(ownBlock))
			h.fr.On("session", "inspect", "session:01aaaa").Return(editFixture(t, "session_inspect.json", tc.edit), "", 0)
			confirmDestroy(t, h, c)
			expectRan(t, h, c)
		})
	}
}

func TestContainerDestroyRefusesMalformedLayout(t *testing.T) {
	cases := []struct {
		name string
		edit func(v jsonObj)
	}{
		{"layout missing on a layer with blocks", func(v jsonObj) { delete(layerAt(v, 0, 0), "layout") }},
		{"layout null on a layer with blocks", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = nil }},
		{"layout a number", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = 42 }},
		{"layout a string", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = "block:01b1" }},
		{"layout an array", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = []any{} }},
		{"layout a bool on an empty layer", func(v jsonObj) { layerAt(v, 0, 1)["blocks"] = []any{}; layerAt(v, 0, 1)["layout"] = false }},
		{"leaf block id not a string", func(v jsonObj) { layoutOf(v, 0, 1)["block_id"] = 42 }},
		{"leaf block id prefix only", func(v jsonObj) { layoutOf(v, 0, 1)["block_id"] = "block:" }},
		{"leaf block id not full", func(v jsonObj) { layoutOf(v, 0, 1)["block_id"] = "01b9" }},
		{"split leaf block id not a string", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = splitLayout("block:01b1", 42) }},
		{"split leaf block id not full", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = splitLayout("01b1", "block:01b2") }},
		{"split leaf block id an object", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = splitLayout("block:01b1", jsonObj{}) }},
		{"block ids not an array", func(v jsonObj) { layoutOf(v, 0, 1)["block_ids"] = "block:01b3" }},
		{"block ids entry not full", func(v jsonObj) { layoutOf(v, 0, 1)["block_ids"] = []any{"block:01b3", "01b9"} }},
		{"block ids entry null", func(v jsonObj) { layoutOf(v, 0, 1)["block_ids"] = []any{nil} }},
	}
	for _, c := range containerDestroys {
		for _, tc := range cases {
			t.Run(c.action+"/"+tc.name, func(t *testing.T) {
				h := newHarness(t, withSelf(ownBlock))
				h.fr.On("session", "inspect", "session:01aaaa").Return(editFixture(t, "session_inspect.json", tc.edit), "", 0)
				confirmDestroy(t, h, c)
				expectRefused(t, h, c, "cannot check")
			})
		}
	}
}

func TestContainerDestroyRefusesOwnBlockInSplitLeaf(t *testing.T) {
	for _, c := range containerDestroys {
		t.Run(c.action, func(t *testing.T) {
			h := newHarness(t, withSelf(ownBlock))
			fresh := editFixture(t, "session_inspect.json", func(v jsonObj) { layerAt(v, 0, 0)["layout"] = splitLayout("block:01b1", ownBlock) })
			h.fr.On("session", "inspect", "session:01aaaa").Return(fresh, "", 0)
			confirmDestroy(t, h, c)
			expectRefused(t, h, c, "targets lazyrex's own block")
		})
	}
}
