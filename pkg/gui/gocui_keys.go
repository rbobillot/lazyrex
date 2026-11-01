package gui

import (
	"fmt"
	"unicode/utf8"

	"github.com/jesseduffield/gocui"
)

var gocuiNamedKeys = map[string]gocui.Key{
	"<tab>": gocui.KeyTab, "<backtab>": gocui.KeyBacktab, "<enter>": gocui.KeyEnter,
	"<esc>": gocui.KeyEsc, "<space>": gocui.KeySpace, "<pgup>": gocui.KeyPgup,
	"<pgdown>": gocui.KeyPgdn, "<up>": gocui.KeyArrowUp, "<down>": gocui.KeyArrowDown,
	"<left>": gocui.KeyArrowLeft, "<right>": gocui.KeyArrowRight, "<home>": gocui.KeyHome,
	"<end>": gocui.KeyEnd,
}

// gocuiKey converts a key name accepted by ValidKeyName to a gocui key or rune.
func gocuiKey(name string) (any, error) {
	if !ValidKeyName(name) {
		return nil, fmt.Errorf("invalid key %q", name)
	}
	if k, ok := gocuiNamedKeys[name]; ok {
		return k, nil
	}
	if len(name) == 5 && name[:3] == "<c-" {
		return gocui.KeyCtrlA + gocui.Key(name[3]-'a'), nil
	}
	r, _ := utf8.DecodeRuneInString(name)
	return r, nil
}

// isUnknownView reports gocui's "view created" signal from SetView, whose
// error is wrapped without errors.Is support.
func isUnknownView(err error) bool {
	return err != nil && err.Error() == gocui.ErrUnknownView.Error()
}
