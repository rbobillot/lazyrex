package gui

import (
	"strings"
	"testing"
)

func TestHeadlessLayout(t *testing.T) {
	h := newHarness(t)
	a, err := newApp(h.ctrl, appOptions{Headless: true, Width: 120, Height: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	if err := a.redraw(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"targets", "sessions", "windows", "blocks", "clients", "main", "log", "footer"} {
		if _, err := a.g.View(name); err != nil {
			t.Errorf("view %s missing: %v", name, err)
		}
	}
	snap := a.g.Snapshot()
	if !containsAll(snap, "[1] Targets", "[2] Sessions", "[3] Windows", "[4] Blocks", "[5] Clients", "work", "local", "online", "Status") {
		t.Fatalf("snapshot:\n%s", snap)
	}

	h.ctrl.Popups.Confirm("Kill session", "rex kill session:01aaaa", func() {})
	if err := a.redraw(); err != nil {
		t.Fatal(err)
	}
	if cur := a.g.CurrentView(); cur == nil || cur.Name() != "popup" {
		t.Fatalf("popup not focused: %v", cur)
	}
	if !strings.Contains(a.g.Snapshot(), "rex kill session:01aaaa") {
		t.Fatalf("popup text not drawn")
	}
}
