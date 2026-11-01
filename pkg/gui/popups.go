package gui

import (
	"time"

	"github.com/rbobillot/lazyrex/pkg/render"
)

type PopupKind int

const (
	PopupConfirm PopupKind = iota
	PopupPrompt
	PopupMenu
	PopupError
)

func (k PopupKind) String() string {
	return [...]string{"confirm", "prompt", "menu", "error"}[k]
}

type MenuItem struct {
	Label    string
	OnSelect func()
}

type Popup struct {
	Kind     PopupKind
	Title    string
	Body     string
	Initial  string
	Items    []MenuItem
	Selected int
	onYes    func()
	onDone   func(string)
	complete func(p *Popup, input string)
	// inputRev changes whenever lazyrex replaces the prompt's text.
	inputRev int
}

type popupAction int

const (
	popupPass popupAction = iota
	popupAccept
	popupCancel
	popupQuit
	popupDown
	popupUp
	popupComplete
)

// popupKeyNames are the keys bound on the popup view.
var popupKeyNames = []string{"<enter>", "<esc>", "<c-c>", "<down>", "<up>", "j", "k", "q", "y", "n", "<tab>"}

// popupKeyAction resolves a key in a popup. ctrl+c quits everywhere; text
// prompts pass every other printable key to the editor.
func popupKeyAction(kind PopupKind, key string) popupAction {
	switch key {
	case "<c-c>":
		return popupQuit
	case "<enter>":
		return popupAccept
	case "<esc>":
		return popupCancel
	}
	if kind == PopupPrompt {
		if key == "<tab>" {
			return popupComplete
		}
		return popupPass
	}
	if key == "q" {
		return popupQuit
	}
	switch kind {
	case PopupMenu:
		switch key {
		case "j", "<down>":
			return popupDown
		case "k", "<up>":
			return popupUp
		}
	case PopupConfirm:
		switch key {
		case "y":
			return popupAccept
		case "n":
			return popupCancel
		}
	}
	return popupPass
}

const toastTTL = 4 * time.Second

// Popups is the stack of modal popups and the current toast.
type Popups struct {
	now     func() time.Time
	stack   []*Popup
	toast   string
	expires time.Time
	// Version changes whenever the stack or the toast changes.
	Version int
	// CurrentInput, when set, returns the text typed in prompt p so far.
	CurrentInput func(p *Popup) string
}

func NewPopups(now func() time.Time) *Popups { return &Popups{now: now} }

func (p *Popups) push(pp *Popup) {
	p.stack = append(p.stack, pp)
	p.Version++
}

func (p *Popups) pop() *Popup {
	if len(p.stack) == 0 {
		return nil
	}
	top := p.stack[len(p.stack)-1]
	p.stack = p.stack[:len(p.stack)-1]
	p.Version++
	return top
}

func (p *Popups) Top() *Popup {
	if len(p.stack) == 0 {
		return nil
	}
	return p.stack[len(p.stack)-1]
}

func (p *Popups) Confirm(title, body string, onYes func()) {
	p.push(&Popup{Kind: PopupConfirm, Title: render.Label(title), Body: body, onYes: onYes})
}

func (p *Popups) Prompt(title, initial string, onDone func(string)) {
	p.PromptWithCompletion(title, initial, nil, onDone)
}

// PromptWithCompletion opens a prompt whose tab key calls complete with the
// text typed so far.
func (p *Popups) PromptWithCompletion(title, initial string, complete func(*Popup, string), onDone func(string)) *Popup {
	pp := &Popup{Kind: PopupPrompt, Title: render.Label(title), Initial: initial, onDone: onDone, complete: complete}
	p.push(pp)
	return pp
}

func (p *Popups) Menu(title string, items []MenuItem) {
	clean := make([]MenuItem, len(items))
	for i, it := range items {
		clean[i] = MenuItem{Label: render.Label(it.Label), OnSelect: it.OnSelect}
	}
	p.push(&Popup{Kind: PopupMenu, Title: render.Label(title), Items: clean})
}

// Complete runs the completion of the top prompt on input and reports
// whether it has one.
func (p *Popups) Complete(input string) bool {
	top := p.Top()
	if top == nil || top.Kind != PopupPrompt || top.complete == nil {
		return false
	}
	top.Initial = input
	top.complete(top, input)
	return true
}

// SetInputIf replaces the text of prompt pp when it is still on top and its
// text was not replaced since revision rev.
func (p *Popups) SetInputIf(pp *Popup, rev int, text string) bool {
	if p.Top() != pp || pp.inputRev != rev {
		return false
	}
	pp.Initial = text
	pp.inputRev++
	p.Version++
	return true
}

// Input returns the text typed in prompt pp.
func (p *Popups) Input(pp *Popup) string {
	if p.CurrentInput != nil {
		return p.CurrentInput(pp)
	}
	return pp.Initial
}

func (p *Popups) Error(msg string) {
	p.push(&Popup{Kind: PopupError, Title: "Error", Body: render.LogText(msg)})
}

// Accept closes the top popup and runs its action: confirm, the prompt's
// callback with input, or the selected menu item.
func (p *Popups) Accept(input string) {
	top := p.pop()
	if top == nil {
		return
	}
	switch top.Kind {
	case PopupConfirm:
		if top.onYes != nil {
			top.onYes()
		}
	case PopupPrompt:
		if top.onDone != nil {
			top.onDone(input)
		}
	case PopupMenu:
		if top.Selected >= 0 && top.Selected < len(top.Items) && top.Items[top.Selected].OnSelect != nil {
			top.Items[top.Selected].OnSelect()
		}
	}
}

func (p *Popups) Cancel() { p.pop() }

func (p *Popups) Clear() {
	p.stack = nil
	p.Version++
}

func (p *Popups) MoveSelection(d int) {
	top := p.Top()
	if top == nil || len(top.Items) == 0 {
		return
	}
	top.Selected = min(max(top.Selected+d, 0), len(top.Items)-1)
	p.Version++
}

// Toast shows a one-line message for a few seconds.
func (p *Popups) Toast(msg string) {
	p.toast = render.Label(msg)
	p.expires = p.now().Add(toastTTL)
	p.Version++
}

func (p *Popups) ToastText() string {
	if p.toast == "" || !p.now().Before(p.expires) {
		return ""
	}
	return p.toast
}
