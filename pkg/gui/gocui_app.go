package gui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jesseduffield/gocui"

	"github.com/rbobillot/lazyrex/pkg/config"
	"github.com/rbobillot/lazyrex/pkg/rex"
)

// gocuiUI applies updates on the gocui goroutine in the order they were
// queued; gocui's own Update does not keep that order.
type gocuiUI struct {
	g     *gocui.Gui
	mu    sync.Mutex
	queue []func()
}

func (u *gocuiUI) Update(f func()) {
	u.mu.Lock()
	u.queue = append(u.queue, f)
	first := len(u.queue) == 1
	u.mu.Unlock()
	if first {
		u.g.Update(u.drain)
	}
}

func (u *gocuiUI) drain(*gocui.Gui) error {
	for {
		u.mu.Lock()
		if len(u.queue) == 0 {
			u.mu.Unlock()
			return nil
		}
		f := u.queue[0]
		u.queue = u.queue[1:]
		u.mu.Unlock()
		f()
	}
}

func (u *gocuiUI) Go(f func()) { go f() }

type appOptions struct {
	Headless      bool
	Width, Height int
}

type app struct {
	g           *gocui.Gui
	ctrl        *Controller
	shown       *Popup
	shownRev    int
	mainVersion int
	logSeq      int
}

var panelTitles = map[Panel]string{
	PanelTargets: "Targets", PanelSessions: "Sessions", PanelWindows: "Windows",
	PanelBlocks: "Blocks", PanelClients: "Clients",
}

func newApp(ctrl *Controller, o appOptions) (*app, error) {
	g, err := gocui.NewGui(gocui.NewGuiOpts{OutputMode: gocui.OutputTrue, Headless: o.Headless, Width: o.Width, Height: o.Height})
	if err != nil {
		return nil, err
	}
	g.Highlight = true
	g.SelFgColor = gocui.ColorGreen
	g.SelFrameColor = gocui.ColorGreen
	a := &app{g: g, ctrl: ctrl, mainVersion: -1, logSeq: -1}
	ctrl.Popups.CurrentInput = a.popupInput
	g.SetManagerFunc(a.layout)
	if err := a.bindKeys(); err != nil {
		g.Close()
		return nil, err
	}
	return a, nil
}

func (a *app) close() { a.g.Close() }

func (a *app) redraw() error { return a.g.ForceLayoutAndRedraw() }

func (a *app) handler(f func() error) func(*gocui.Gui, *gocui.View) error {
	return func(*gocui.Gui, *gocui.View) error {
		if err := f(); err != nil {
			return err
		}
		if a.ctrl.Quitting() {
			return gocui.ErrQuit
		}
		return nil
	}
}

// boundBindings are every binding of p, available or not: availability
// changes with the selected target, and Dispatch explains a refusal.
func (a *app) boundBindings(p Panel) []Binding { return BindingsFor(a.ctrl.bindings, p) }

func (a *app) bindKeys() error {
	for _, p := range []Panel{PanelTargets, PanelSessions, PanelWindows, PanelBlocks, PanelClients, PanelMain, PanelLog} {
		for _, b := range a.boundBindings(p) {
			key, err := gocuiKey(b.Key)
			if err != nil {
				return err
			}
			action := b.Action
			h := a.handler(func() error {
				err := a.ctrl.Dispatch(p, action)
				switch {
				case errors.Is(err, ErrQuit):
					return gocui.ErrQuit
				case err != nil:
					a.ctrl.Popups.Error(err.Error())
				}
				return nil
			})
			if err := a.g.SetKeybinding(viewName(p), key, gocui.ModNone, h); err != nil {
				return err
			}
		}
	}
	for _, name := range popupKeyNames {
		key, err := gocuiKey(name)
		if err != nil {
			return err
		}
		h := func(*gocui.Gui, *gocui.View) error { return a.popupKey(name) }
		if err := a.g.SetKeybinding("popup", key, gocui.ModNone, h); err != nil {
			return err
		}
	}
	return nil
}

// popupKey handles a key on the popup view. Keys a text prompt does not use
// are left to its editor; elsewhere they are swallowed, because gocui ends
// its main loop on an unhandled key that no editor takes.
func (a *app) popupKey(name string) error {
	pp := a.ctrl.Popups
	top := pp.Top()
	if top == nil {
		return nil
	}
	switch popupKeyAction(top.Kind, name) {
	case popupQuit:
		return gocui.ErrQuit
	case popupAccept:
		input := ""
		if top.Kind == PopupPrompt {
			if v, err := a.g.View("popup"); err == nil {
				input = v.TextArea.GetContent()
			}
		}
		pp.Accept(input)
	case popupCancel:
		pp.Cancel()
	case popupDown:
		pp.MoveSelection(1)
	case popupUp:
		pp.MoveSelection(-1)
	case popupComplete:
		if !pp.Complete(pp.Input(top)) {
			return gocui.ErrKeybindingNotHandled
		}
	default:
		if top.Kind == PopupPrompt {
			return gocui.ErrKeybindingNotHandled
		}
		return nil
	}
	if a.ctrl.Quitting() {
		return gocui.ErrQuit
	}
	return nil
}

// popupInput returns the text of prompt p as typed in its view, which holds
// p only once drawn.
func (a *app) popupInput(p *Popup) string {
	if p != a.shown {
		return p.Initial
	}
	v, err := a.g.View("popup")
	if err != nil {
		return p.Initial
	}
	return v.TextArea.GetContent()
}

func (a *app) setView(name string, r Rect, frame bool) (*gocui.View, bool, error) {
	x0, y0, x1, y1 := r.X0, r.Y0, r.X1, r.Y1
	if !frame {
		x0, y0, x1, y1 = x0-1, y0-1, x1+1, y1+1
	}
	v, err := a.g.SetView(name, x0, y0, x1, y1, 0)
	created := isUnknownView(err)
	if err != nil && !created {
		return nil, false, err
	}
	if created {
		v.Frame = frame
	}
	return v, created, nil
}

func (a *app) layout(g *gocui.Gui) error {
	st := a.ctrl.st
	w, h := g.Size()
	rects := Arrange(w, h, st.Focus)
	for _, p := range sidePanels {
		if err := a.drawList(p, rects[viewName(p)]); err != nil {
			return err
		}
	}
	if err := a.drawMain(rects["main"]); err != nil {
		return err
	}
	if err := a.drawLog(rects["log"]); err != nil {
		return err
	}
	if err := a.drawFooter(rects["footer"]); err != nil {
		return err
	}
	return a.drawPopup(w, h)
}

func (a *app) drawList(p Panel, r Rect) error {
	st := a.ctrl.st
	v, created, err := a.setView(viewName(p), r, true)
	if err != nil {
		return err
	}
	if created {
		v.Highlight = true
		v.SelBgColor = gocui.ColorBlue
		v.InactiveViewSelBgColor = gocui.AttrBold
	}
	v.Title = fmt.Sprintf("[%d] %s", int(p)+1, panelTitles[p])
	v.HighlightInactive = st.Focus != p
	var sub []string
	if p == PanelBlocks {
		sub = append(sub, [...]string{"window", "session", "all sessions"}[st.Scope])
	}
	if p == PanelTargets {
		if s := TargetsSubtitle(st); s != "" {
			sub = append(sub, s)
		}
	}
	if p == PanelWindows && st.WindowsAll {
		sub = append(sub, "all sessions")
	}
	if f := st.Lists[p].Filter; f != "" {
		sub = append(sub, "/"+f)
	}
	v.Subtitle = strings.Join(sub, " ")
	all := st.Rows(p)
	vis := st.Visible(p)
	rows := make([]string, 0, len(vis))
	for _, i := range vis {
		rows = append(rows, all[i])
	}
	v.Clear()
	fmt.Fprint(v, strings.Join(rows, "\n"))
	if len(rows) > 0 {
		v.FocusPoint(0, min(max(st.Lists[p].Selected, 0), len(rows)-1), true)
	}
	if p == PanelClients {
		st.ClientsVisible = r.Y1-r.Y0-1 >= 1
	}
	return nil
}

func (a *app) drawMain(r Rect) error {
	st := a.ctrl.st
	v, created, err := a.setView("main", r, true)
	if err != nil {
		return err
	}
	v.Tabs = Tabs(st.Side)
	v.TabIndex = st.Tab[st.Side]
	v.Subtitle = ""
	if m := st.Main; m.Query != "" {
		v.Subtitle = fmt.Sprintf("/%s %d of %d", m.Query, min(m.Match+1, len(m.Matches)), len(m.Matches))
	}
	if created || a.mainVersion != st.Main.Version {
		v.Clear()
		fmt.Fprint(v, st.Main.Content)
		a.mainVersion = st.Main.Version
	}
	height := max(v.InnerHeight(), 1)
	st.Main.Height = height
	oy := st.Main.Origin
	if st.Main.Follow {
		oy = max(st.mainLines()-height, 0)
	}
	v.SetOrigin(0, oy)
	return nil
}

func (a *app) drawLog(r Rect) error {
	st := a.ctrl.st
	v, created, err := a.setView("log", r, true)
	if err != nil {
		return err
	}
	if created {
		v.Wrap = true
	}
	v.Title = "Command log"
	if created || a.logSeq != st.LogSeq {
		v.Clear()
		fmt.Fprint(v, strings.Join(st.Log, "\n"))
		a.logSeq = st.LogSeq
	}
	st.LogHeight = max(v.InnerHeight(), 1)
	v.Autoscroll = st.LogFollow
	if !st.LogFollow {
		v.SetOrigin(0, st.LogOrigin)
	}
	return nil
}

func (a *app) drawFooter(r Rect) error {
	st := a.ctrl.st
	v, _, err := a.setView("footer", r, false)
	if err != nil {
		return err
	}
	v.Clear()
	if t := a.ctrl.Popups.ToastText(); t != "" {
		fmt.Fprint(v, "\x1b[33m"+t+"\x1b[0m")
	} else {
		fmt.Fprint(v, Footer(st, a.ctrl.Bindings(st.Focus)))
	}
	return nil
}

func wrapText(s string, width int) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		for utf8.RuneCountInString(line) > width {
			rs := []rune(line)
			out = append(out, string(rs[:width]))
			line = string(rs[width:])
		}
		out = append(out, line)
	}
	return out
}

func popupLines(p *Popup, inner int) []string {
	switch p.Kind {
	case PopupConfirm:
		return append(wrapText(p.Body, inner), "", "enter/y: confirm   esc/n: cancel")
	case PopupError:
		return append(wrapText(p.Body, inner), "", "enter/esc: close")
	case PopupMenu:
		lines := make([]string, len(p.Items))
		for i, it := range p.Items {
			lines[i] = it.Label
		}
		return lines
	}
	return []string{""}
}

func (a *app) drawPopup(w, h int) error {
	st := a.ctrl.st
	top := a.ctrl.Popups.Top()
	if top == nil {
		if a.shown != nil {
			a.shown = nil
			a.g.Cursor = false
			if err := a.g.DeleteView("popup"); err != nil && !isUnknownView(err) {
				return err
			}
		}
		if _, err := a.g.SetCurrentView(viewName(st.Focus)); err != nil && !isUnknownView(err) {
			return err
		}
		return nil
	}
	maxInner := max(w-6, 10)
	inner := min(max(60, utf8.RuneCountInString(top.Title)+4), maxInner)
	lines := popupLines(top, inner)
	if top.Kind == PopupMenu {
		for _, l := range lines {
			inner = max(inner, min(utf8.RuneCountInString(l)+2, maxInner))
		}
	}
	height := min(len(lines), max(h-6, 1))
	x0 := (w - inner - 2) / 2
	y0 := max((h-height-2)/2, 0)
	v, created, err := a.setView("popup", Rect{x0, y0, x0 + inner + 1, y0 + height + 1}, true)
	if err != nil {
		return err
	}
	v.Title = top.Title
	if created || top != a.shown || top.inputRev != a.shownRev {
		a.shown, a.shownRev = top, top.inputRev
		v.Clear()
		v.Editable = top.Kind == PopupPrompt
		v.Highlight = top.Kind == PopupMenu
		v.SelBgColor = gocui.ColorBlue
		v.SetOrigin(0, 0)
		a.g.Cursor = v.Editable
		if v.Editable {
			v.Editor = gocui.DefaultEditor
			v.TextArea.Clear()
			v.TextArea.TypeString(top.Initial)
			v.RenderTextArea()
		}
	}
	if top.Kind != PopupPrompt {
		v.Clear()
		fmt.Fprint(v, strings.Join(lines, "\n"))
		if top.Kind == PopupMenu {
			v.FocusPoint(0, top.Selected, true)
		}
	}
	if _, err := a.g.SetViewOnTop("popup"); err != nil {
		return err
	}
	_, err = a.g.SetCurrentView("popup")
	return err
}

// RunOptions configures Run.
type RunOptions struct {
	Client   *rex.Client
	Config   config.Config
	Bindings []Binding
	Warnings []string
	// Targets, Initial, Flag and File are as in Options.
	Targets []rex.Target
	Initial string
	Flag    *rex.Target
	File    config.File
	// SSH and SSHConfigHosts are as in Options.
	SSH            *rex.SSHEnv
	SSHConfigHosts []string
}

// Run starts the TUI on the initial target and blocks until quit.
func Run(ctx context.Context, o RunOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	clock := rex.RealClock()
	ui := &gocuiUI{}
	ctrl := NewController(NewState(), o.Client, ui, Options{Clock: clock, Config: o.Config, Bindings: o.Bindings, Warnings: o.Warnings,
		Targets: o.Targets, Initial: o.Initial, Flag: o.Flag, File: o.File, SSH: o.SSH, SSHConfigHosts: o.SSHConfigHosts})
	a, err := newApp(ctrl, appOptions{})
	if err != nil {
		return err
	}
	defer a.close()
	ui.g = a.g

	ctrl.Start(ctx)
	go func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				a.g.Update(func(*gocui.Gui) error { return gocui.ErrQuit })
				return
			case <-t.C:
				ui.Update(ctrl.Tick)
			}
		}
	}()

	err = a.g.MainLoop()
	cancel()
	ctrl.Close()
	if errors.Is(err, gocui.ErrQuit) {
		return nil
	}
	return err
}
