package gui

import (
	"errors"
	"testing"

	"github.com/jesseduffield/gocui"
)

func TestPopupKeyResolution(t *testing.T) {
	want := map[PopupKind]map[string]popupAction{
		PopupConfirm: {"<c-c>": popupQuit, "q": popupQuit, "<esc>": popupCancel, "n": popupCancel, "<enter>": popupAccept, "y": popupAccept,
			"j": popupPass, "k": popupPass, "<down>": popupPass, "<up>": popupPass, "<tab>": popupPass},
		PopupMenu: {"<c-c>": popupQuit, "q": popupQuit, "<esc>": popupCancel, "<enter>": popupAccept,
			"j": popupDown, "<down>": popupDown, "k": popupUp, "<up>": popupUp, "y": popupPass, "n": popupPass, "<tab>": popupPass},
		PopupError: {"<c-c>": popupQuit, "q": popupQuit, "<esc>": popupCancel, "<enter>": popupAccept,
			"y": popupPass, "n": popupPass, "j": popupPass, "k": popupPass, "<down>": popupPass, "<up>": popupPass, "<tab>": popupPass},
		PopupPrompt: {"<c-c>": popupQuit, "<esc>": popupCancel, "<enter>": popupAccept,
			"q": popupPass, "y": popupPass, "n": popupPass, "j": popupPass, "k": popupPass, "<down>": popupPass, "<up>": popupPass,
			"<tab>": popupComplete},
	}
	for kind, keys := range want {
		for _, key := range popupKeyNames {
			w, ok := keys[key]
			if !ok {
				t.Fatalf("%v: no expectation for %q", kind, key)
			}
			if got := popupKeyAction(kind, key); got != w {
				t.Errorf("%v %q = %v, want %v", kind, key, got, w)
			}
		}
	}
}

func TestPopupKeysHeadless(t *testing.T) {
	h := newHarness(t)
	a, err := newApp(h.ctrl, appOptions{Headless: true, Width: 120, Height: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	redraw := func() {
		t.Helper()
		if err := a.redraw(); err != nil {
			t.Fatal(err)
		}
	}

	ran := false
	h.ctrl.Popups.Confirm("Kill session", "rex kill session:01aaaa", func() { ran = true })
	redraw()
	for _, key := range []string{"<c-c>", "q"} {
		if err := a.popupKey(key); !errors.Is(err, gocui.ErrQuit) {
			t.Fatalf("confirm %s returned %v, want quit", key, err)
		}
	}
	if ran {
		t.Fatalf("quitting from a confirmation ran its command")
	}
	if err := a.popupKey("<esc>"); err != nil || h.ctrl.Popups.Top() != nil {
		t.Fatalf("esc did not close the confirmation: %v", err)
	}

	var typed string
	h.ctrl.Popups.Prompt("Rename", "", func(s string) { typed = s })
	redraw()
	if err := a.popupKey("q"); !errors.Is(err, gocui.ErrKeybindingNotHandled) || h.ctrl.Popups.Top() == nil {
		t.Fatalf("q in a prompt was not left to the editor: %v", err)
	}
	if err := a.popupKey("<c-c>"); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("ctrl+c in a prompt returned %v, want quit", err)
	}
	if err := a.popupKey("<esc>"); err != nil || h.ctrl.Popups.Top() != nil || typed != "" {
		t.Fatalf("esc did not cancel the prompt: %v", err)
	}

	h.dispatch(PanelBlocks, "pressKey")
	redraw()
	if err := a.popupKey("j"); err != nil || h.top().Selected != 1 {
		t.Fatalf("j in a menu: %v, selected %d", err, h.top().Selected)
	}
	if err := a.popupKey("y"); err != nil || h.ctrl.Popups.Top() == nil {
		t.Fatalf("an unused key in a menu must be swallowed, not end the main loop: %v", err)
	}
	if err := a.popupKey("q"); !errors.Is(err, gocui.ErrQuit) {
		t.Fatalf("q in a menu returned %v, want quit", err)
	}
}
