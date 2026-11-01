package gui

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

// expectTargetRefusal checks that the top popup refuses a command because
// the target changed, and that the refusal was logged.
func (h *harness) expectTargetRefusal() {
	h.t.Helper()
	h.expectRefusal("target changed")
	if !strings.Contains(h.logText(), "target changed") {
		h.t.Fatalf("target refusal not logged: %q", h.st.Log)
	}
}

func (h *harness) expectConfirm() {
	h.t.Helper()
	if p := h.top(); p.Kind != PopupConfirm {
		h.t.Fatalf("popup %+v, want a confirmation", p)
	}
}

// bounce makes the server stop, then come back with the same instance.
func (h *harness) bounce() {
	h.t.Helper()
	h.fr.On("whoami", "--json").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	h.dispatch(PanelTargets, "checkTarget")
	if h.st.Status.State == rex.StateOnline {
		h.t.Fatalf("still online after the stop")
	}
	h.fr.On("whoami", "--json").Return(rextest.FixtureString("whoami.json"), "", 0)
	h.dispatch(PanelTargets, "checkTarget")
	if h.st.Status.State != rex.StateOnline || h.st.Status.Whoami == nil || string(h.st.Status.Whoami.InstanceID) != "inst:01hz" {
		h.t.Fatalf("status after the bounce: %+v", h.st.Status)
	}
}

// sameInstance reads the status again, finding the same server instance.
func (h *harness) sameInstance() { h.dispatch(PanelTargets, "checkTarget") }

type targetChange struct {
	name string
	do   func(h *harness)
}

var targetChanges = []targetChange{
	{"new instance", func(h *harness) { h.newInstance("inst:02hz") }},
	{"A-B-A", func(h *harness) { h.newInstance("inst:02hz"); h.newInstance("inst:01hz") }},
	{"bounce", (*harness).bounce},
	{"other target", func(h *harness) { h.ctrl.setTarget("other", "inst:01hz") }},
	{"activate another target", func(h *harness) { h.addConfigTarget("studio", "studio"); h.activateTarget("studio") }},
}

// argvHook is rex.Local calling hook on every Argv, Display's included.
type argvHook struct {
	rex.Local
	hook func(argv []string)
}

func (a *argvHook) Argv(binary string, globals, args []string) []string {
	argv := a.Local.Argv(binary, globals, args)
	if a.hook != nil {
		a.hook(argv)
	}
	return argv
}

// onceArgv makes the next Argv containing tokens call f, once.
func (a *argvHook) onceArgv(f func(), tokens ...string) *bool {
	fired := new(bool)
	a.hook = func(argv []string) {
		if !*fired && rextest.Contains(argv, tokens...) {
			*fired = true
			f()
		}
	}
	return fired
}

// waitSurface is an action left waiting on a form or a confirmation.
type waitSurface struct {
	name   string
	open   func(h *harness)
	finish func(h *harness)
	ran    func(h *harness) int
}

func accept(h *harness) { h.ctrl.Popups.Accept("") }

func apiCalls(h *harness) int { return h.countContaining("api", "call") }

func confirmOf(panel Panel, action string) func(h *harness) {
	return func(h *harness) {
		h.focus(panel)
		h.dispatch(panel, action)
		h.expectConfirm()
	}
}

var waitSurfaces = []waitSurface{
	{"T1 resize form", func(h *harness) {
		h.xMenu(PanelBlocks)
		h.chooseContaining("resize pane")
		h.acceptPrefilled()
	}, func(h *harness) { h.acceptPrefilled(); h.choose("right"); h.answer("5") }, apiCalls},
	{"T4 server action confirmation", func(h *harness) {
		h.serverActions()
		h.chooseAction("split_right")
		h.choose("right")
		h.acceptPrefilled()
		h.answer("")
		h.expectConfirm()
	}, accept, (*harness).doCalls},
	{"T5 palette do confirmation", func(h *harness) { h.palette("do mystery"); h.expectConfirm() }, accept, (*harness).doCalls},
	{"T6 palette api call confirmation", func(h *harness) {
		h.palette("api call server.config_reload {}")
		h.expectConfirm()
	}, accept, apiCalls},
	{"T7a palette prompt, api call", func(h *harness) { h.ctrl.Popups.Clear(); h.dispatch(PanelSessions, "palette") },
		func(h *harness) { h.answer("api call server.status") }, apiCalls},
	{"T7b palette prompt, do", func(h *harness) { h.ctrl.Popups.Clear(); h.dispatch(PanelSessions, "palette") },
		func(h *harness) { h.answer("do mystery") }, (*harness).doCalls},
	{"T8 client action confirmation", func(h *harness) {
		h.clientActions()
		h.chooseAction("closeWindow")
		h.expectConfirm()
	}, accept, (*harness).doCalls},
	{"T9 client action form", func(h *harness) { h.clientActions(); h.chooseAction("pane.zoom") },
		func(h *harness) { h.acceptPrefilled() }, (*harness).doCalls},
	{"T13 kill session", confirmOf(PanelSessions, "killSession"), accept, func(h *harness) int { return h.countContaining("kill") }},
	{"T14 close block", confirmOf(PanelBlocks, "closeBlock"), accept, func(h *harness) int { return h.countContaining("block", "close") }},
	{"T15 close window", confirmOf(PanelWindows, "closeWindow"), accept, func(h *harness) int { return h.countContaining("window", "close") }},
	{"T16 stop server", confirmOf(PanelTargets, "stopServer"), accept, func(h *harness) int { return h.countContaining("server", "stop") }},
}

// TestTargetChangeWhileWaiting (T1, T4-T9, T13-T19): a target observed to
// change while a form or a confirmation waits refuses the command, which
// does not run.
func TestTargetChangeWhileWaiting(t *testing.T) {
	for _, s := range waitSurfaces {
		for _, ch := range targetChanges {
			t.Run(s.name+"/"+ch.name, func(t *testing.T) {
				h := newHarness(t)
				s.open(h)
				ch.do(h)
				s.finish(h)
				h.expectTargetRefusal()
				if n := s.ran(h); n != 0 {
					t.Fatalf("ran %d times; calls:\n%s", n, h.dumpCalls())
				}
			})
		}
	}
}

// TestTargetUnchangedWhileWaiting (T20, T21): with no change, or a status
// read finding the same instance, every surface runs its command once.
func TestTargetUnchangedWhileWaiting(t *testing.T) {
	changes := map[string]func(h *harness){"none": func(*harness) {}, "same instance": (*harness).sameInstance}
	for _, s := range waitSurfaces {
		for cn, change := range changes {
			t.Run(s.name+"/"+cn, func(t *testing.T) {
				h := newHarness(t)
				s.open(h)
				change(h)
				s.finish(h)
				if p := h.ctrl.Popups.Top(); p != nil && p.Kind == PopupConfirm {
					accept(h)
				}
				if p := h.ctrl.Popups.Top(); p != nil {
					t.Fatalf("popup left open: %+v", p)
				}
				if n := s.ran(h); n != 1 {
					t.Fatalf("ran %d times; calls:\n%s", n, h.dumpCalls())
				}
			})
		}
	}
}

// TestServerFormResultDroppedOnTargetChange (T3): a server action form
// completed after the target changed is dropped by the read that follows.
func TestServerFormResultDroppedOnTargetChange(t *testing.T) {
	h := newHarness(t)
	h.serverActions()
	h.chooseAction("split_right")
	h.choose("right")
	h.newInstance("inst:02hz")
	h.acceptPrefilled()
	h.answer("")
	if p := h.ctrl.Popups.Top(); p != nil {
		t.Fatalf("popup %+v, want none", p)
	}
	if toast := h.ctrl.Popups.ToastText(); !strings.Contains(toast, "target changed") {
		t.Fatalf("toast %q", toast)
	}
	if h.doCalls() != 0 {
		t.Fatalf("do ran; calls:\n%s", h.dumpCalls())
	}
}

// TestTargetChangeBeforeWorker (T2): a change observed after the form
// completed but before its worker runs refuses the command.
func TestTargetChangeBeforeWorker(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui))
	h.xMenu(PanelBlocks)
	h.chooseContaining("resize pane")
	h.acceptPrefilled()
	h.acceptPrefilled()
	h.choose("right")
	ui.hold = true
	h.answer("5")
	if len(ui.queue) != 1 {
		t.Fatalf("%d workers queued, want 1", len(ui.queue))
	}
	ui.hold = false
	h.newInstance("inst:02hz")
	ui.drain()
	h.expectTargetRefusal()
	if n := apiCalls(h); n != 0 {
		t.Fatalf("api call ran; calls:\n%s", h.dumpCalls())
	}
}

// TestShowInAppTargetChangeBeforeWorker (T10): show in app refuses, before
// its fresh check reads anything, once the target changed.
func TestShowInAppTargetChangeBeforeWorker(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui))
	h.focus(PanelSessions)
	ui.hold = true
	h.dispatch(PanelSessions, "showInApp")
	if len(ui.queue) != 1 {
		t.Fatalf("%d workers queued, want 1", len(ui.queue))
	}
	f := ui.queue[0]
	ui.queue = ui.queue[1:]
	f()
	if len(ui.queue) != 1 {
		t.Fatalf("the client read queued %d workers, want 1", len(ui.queue))
	}
	ui.hold = false
	reads := h.countContaining("-C", "client:02app", "actions", "--json")
	h.newInstance("inst:02hz")
	ui.drain()
	h.expectTargetRefusal()
	if h.countContaining("session.select") != 0 {
		t.Fatalf("session.select ran; calls:\n%s", h.dumpCalls())
	}
	if n := h.countContaining("-C", "client:02app", "actions", "--json"); n != reads {
		t.Fatalf("the app's actions were read after the change; calls:\n%s", h.dumpCalls())
	}
}

// TestShowInAppUnchangedRuns (T20 for T10): with no change, show in app
// runs once through the queued workers.
func TestShowInAppUnchangedRuns(t *testing.T) {
	ui := &queuedUI{}
	h := newHarness(t, withUI(ui))
	h.focus(PanelSessions)
	ui.hold = true
	h.dispatch(PanelSessions, "showInApp")
	f := ui.queue[0]
	ui.queue = ui.queue[1:]
	f()
	ui.hold = false
	ui.drain()
	if h.count("-C client:02app do session.select session_id=session:01aaaa") != 1 {
		t.Fatalf("calls:\n%s", h.dumpCalls())
	}
}

// TestTargetChangeDuringRecheck (T11): a change observed while the pre-run
// check reads Rex refuses the command.
func TestTargetChangeDuringRecheck(t *testing.T) {
	h := newHarness(t)
	h.palette("do mystery")
	h.expectConfirm()
	var once sync.Once
	h.fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, "actions", "--json") {
			once.Do(func() { h.newInstance("inst:02hz") })
		}
	}
	accept(h)
	h.expectTargetRefusal()
	if h.doCalls() != 0 {
		t.Fatalf("do ran; calls:\n%s", h.dumpCalls())
	}
}

// TestTargetChangeInsideExec (T12): a change observed inside the client,
// after every check of the GUI, refuses the command before the runner.
func TestTargetChangeInsideExec(t *testing.T) {
	cases := []struct {
		name   string
		open   func(h *harness)
		tokens []string
		finish func(h *harness)
		ran    func(h *harness) int
	}{
		{"T12a palette do", func(h *harness) { h.palette("do mystery"); h.expectConfirm() },
			[]string{"do", "mystery"}, accept, (*harness).doCalls},
		{"T12b resize", func(h *harness) {
			h.xMenu(PanelBlocks)
			h.chooseContaining("resize pane")
			h.acceptPrefilled()
			h.acceptPrefilled()
			h.choose("right")
		}, []string{"api", "call"}, func(h *harness) { h.answer("5") }, apiCalls},
		{"T12c kill session", confirmOf(PanelSessions, "killSession"), []string{"kill"}, accept,
			func(h *harness) int { return h.countContaining("kill") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &argvHook{}
			h := newHarness(t, withTransport(tr))
			tc.open(h)
			fired := tr.onceArgv(func() { h.newInstance("inst:02hz") }, tc.tokens...)
			tc.finish(h)
			if !*fired {
				t.Fatalf("the change never happened")
			}
			h.expectTargetRefusal()
			if n := tc.ran(h); n != 0 {
				t.Fatalf("ran %d times; calls:\n%s", n, h.dumpCalls())
			}
		})
	}
}

// promptedAction is a key action that asks before it runs.
type promptedAction struct {
	name   string
	panel  Panel
	action string
	steps  []func(h *harness)
	ran    []string
}

func answerWith(s string) func(h *harness) { return func(h *harness) { h.answer(s) } }
func chooseItem(s string) func(h *harness) { return func(h *harness) { h.choose(s) } }

var promptedActions = []promptedAction{
	{"T30 new session", PanelSessions, "newSession", []func(*harness){answerWith("s"), answerWith("")}, []string{"new s --json"}},
	{"T31 rename session", PanelSessions, "renameSession", []func(*harness){answerWith("renamed")},
		[]string{"session rename session:01aaaa renamed"}},
	{"T32 new window", PanelWindows, "newWindow", []func(*harness){answerWith("w9")},
		[]string{"window new -s session:01aaaa w9 --focus=false --json"}},
	{"T33 rename window", PanelWindows, "renameWindow", []func(*harness){answerWith("n2")},
		[]string{"window rename -s session:01aaaa window:01w1 n2"}},
	{"T34 new pane", PanelBlocks, "newPane", []func(*harness){chooseItem("right"), answerWith(""), answerWith(""), answerWith("")},
		[]string{"split -s session:01aaaa -b block:01b1 --split=right --focus=false --json"}},
	{"T35 send text", PanelBlocks, "sendText", []func(*harness){answerWith("ls")}, []string{"send -s session:01aaaa -b block:01b1 -- ls"}},
	{"T36 send text enter", PanelBlocks, "sendTextEnter", []func(*harness){answerWith("make")},
		[]string{"send -s session:01aaaa -b block:01b1 -- make", "send-key -s session:01aaaa -b block:01b1 enter"}},
	{"T37 press key menu", PanelBlocks, "pressKey", []func(*harness){chooseItem("enter")},
		[]string{"send-key -s session:01aaaa -b block:01b1 enter"}},
	{"T38 press key other", PanelBlocks, "pressKey", []func(*harness){chooseItem("other…"), answerWith("f5")},
		[]string{"send-key -s session:01aaaa -b block:01b1 f5"}},
	{"T39 rename block", PanelBlocks, "renameBlock", []func(*harness){answerWith("n3")},
		[]string{"block rename -s session:01aaaa block:01b1 n3"}},
}

// runPrompted presses the action's key and takes its steps, calling change
// right before step at.
func runPrompted(h *harness, a promptedAction, at int, change func(h *harness)) {
	h.t.Helper()
	h.focus(a.panel)
	h.dispatch(a.panel, a.action)
	for i, step := range a.steps {
		if i == at {
			change(h)
		}
		step(h)
	}
}

// TestPromptedActionsPinTarget (T30-T39 a, b): a prompted key action
// refuses its command once the target changed since the key press.
func TestPromptedActionsPinTarget(t *testing.T) {
	for _, a := range promptedActions {
		waits := map[string]int{"first wait": 0, "last wait": len(a.steps) - 1}
		for wn, at := range waits {
			for _, ch := range targetChanges {
				t.Run(a.name+"/"+wn+"/"+ch.name, func(t *testing.T) {
					h := newHarness(t)
					runPrompted(h, a, at, ch.do)
					h.expectTargetRefusal()
					for _, w := range a.ran {
						if n := h.count(w); n != 0 {
							t.Fatalf("%q ran %d times; calls:\n%s", w, n, h.dumpCalls())
						}
					}
				})
			}
		}
	}
}

// TestPromptedActionsUnchangedRun (T30-T39 c, T41): with no change, or a
// status read finding the same instance, each runs its commands once.
func TestPromptedActionsUnchangedRun(t *testing.T) {
	changes := map[string]func(h *harness){"none": func(*harness) {}, "same instance": (*harness).sameInstance}
	for _, a := range promptedActions {
		for cn, change := range changes {
			t.Run(a.name+"/"+cn, func(t *testing.T) {
				h := newHarness(t)
				runPrompted(h, a, 0, change)
				if p := h.ctrl.Popups.Top(); p != nil {
					t.Fatalf("popup left open: %+v", p)
				}
				for _, w := range a.ran {
					if n := h.count(w); n != 1 {
						t.Fatalf("%q ran %d times; calls:\n%s", w, n, h.dumpCalls())
					}
				}
			})
		}
	}
}

// TestSendEnterAfterTargetChange (T40): the Enter chained after send text
// is refused when the target changed while the text was sent.
func TestSendEnterAfterTargetChange(t *testing.T) {
	h := newHarness(t)
	var once sync.Once
	h.fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, "send") {
			once.Do(func() { h.newInstance("inst:02hz") })
		}
	}
	h.focus(PanelBlocks)
	h.dispatch(PanelBlocks, "sendTextEnter")
	h.answer("make")
	if n := h.countContaining("send"); n != 1 {
		t.Fatalf("send ran %d times; calls:\n%s", n, h.dumpCalls())
	}
	if n := h.countContaining("send-key"); n != 0 {
		t.Fatalf("send-key ran %d times; calls:\n%s", n, h.dumpCalls())
	}
	h.expectTargetRefusal()
}

// TestStartServerWhileStopped (T22): a status read that finds the target
// still stopped does not refuse the start it waits to confirm.
func TestStartServerWhileStopped(t *testing.T) {
	fr := fixtureRunner()
	fr.On("whoami").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	fr.On("server", "status").Return("", rextest.FixtureString("stderr_server_not_running.txt"), 1)
	fr.On("ls").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	fr.On("client", "ls").Return("", rextest.FixtureString("stderr_whoami_stopped.txt"), 4)
	h := newHarness(t, withRunner(fr))
	confirmOf(PanelTargets, "startServer")(h)
	h.sameInstance()
	if h.st.Status.State != rex.StateStopped {
		t.Fatalf("status %v, want stopped", h.st.Status.State)
	}
	accept(h)
	if n := h.countContaining("server", "start"); n != 1 {
		t.Fatalf("server start ran %d times; calls:\n%s", n, h.dumpCalls())
	}
}

// goUI runs Go work on goroutines and queues Update work for settle, which
// runs it on the test goroutine as the UI goroutine would.
type goUI struct {
	mu      sync.Mutex
	updates []func()
	// goes counts the Go calls, running the ones not returned yet.
	goes, running atomic.Int32
	// wait bounds waitWorkers; zero means rextest.Timeout.
	wait time.Duration
}

func (u *goUI) Go(f func()) {
	u.goes.Add(1)
	u.running.Add(1)
	go func() {
		defer u.running.Add(-1)
		f()
	}()
}

// waitWorkers waits for the Go work, without running the queued updates.
func (u *goUI) waitWorkers(t testing.TB) {
	t.Helper()
	wait := u.wait
	if wait == 0 {
		wait = rextest.Timeout
	}
	deadline := time.Now().Add(wait)
	for u.running.Load() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for the Go workers (%d running)", wait, u.running.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func (u *goUI) Update(f func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.updates = append(u.updates, f)
}

// settle waits for the workers and runs the queued updates until none is
// left.
func (u *goUI) settle(t testing.TB) {
	t.Helper()
	for {
		u.waitWorkers(t)
		u.mu.Lock()
		fs := u.updates
		u.updates = nil
		u.mu.Unlock()
		if len(fs) == 0 {
			return
		}
		for _, f := range fs {
			f()
		}
	}
}

// TestTargetChangeRacesWorker (T23): a change observed while the worker
// runs either refuses the command or lets it run, without a data race.
func TestTargetChangeRacesWorker(t *testing.T) {
	ui := &goUI{}
	h := newHarness(t, withUI(ui))
	ui.settle(t)
	h.palette("do mystery")
	ui.settle(t)
	h.expectConfirm()
	started := make(chan struct{})
	var once sync.Once
	h.fr.OnCall = func(argv []string) {
		if rextest.Contains(argv, "actions", "--json") {
			once.Do(func() { close(started) })
		}
	}
	accept(h)
	rextest.Recv(t, started, "the accepted command's actions --json call")
	h.newInstance("inst:02hz")
	ui.settle(t)
	if u := h.fr.Unexpected(); len(u) != 0 {
		t.Fatalf("unexpected calls: %q", u)
	}
	refused := strings.Contains(h.logText(), "target changed")
	if ran := h.doCalls(); ran+btoi(refused) != 1 {
		t.Fatalf("do ran %d times, refused %v; calls:\n%s", ran, refused, h.dumpCalls())
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
