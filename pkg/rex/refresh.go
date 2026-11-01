package rex

import (
	"sync"
	"time"
)

// InspectDone must be called once when an inspect requested by the Refresher
// finishes. It returns whether the result may be applied: false when the
// revision is older than one already applied, or the request predates Reset.
type InspectDone func(rev int64, ok bool) bool

// RefreshHandler performs the refreshes the Refresher decides on. Its methods
// are called without the Refresher's lock held, from any goroutine.
type RefreshHandler interface {
	RefreshSessions()
	InspectSession(id SessionID, done InspectDone)
	BlockEvent(s SessionID, ev BlockEvent)
}

// Refresher maps events to refreshes: it filters client noise, checks
// revisions, debounces per session and keeps one inspect in flight per session.
type Refresher struct {
	mu       sync.Mutex
	clock    Clock
	debounce time.Duration
	h        RefreshHandler
	gen      uint64
	timers   map[SessionID]Timer
	inflight map[SessionID]bool
	dirty    map[SessionID]bool
	revs     map[SessionID]int64
}

// sessionsKey debounces session-list refreshes.
const sessionsKey SessionID = ""

func NewRefresher(c Clock, debounce time.Duration, h RefreshHandler) *Refresher {
	r := &Refresher{clock: c, debounce: debounce, h: h}
	r.clear()
	return r
}

func (r *Refresher) clear() {
	r.timers = map[SessionID]Timer{}
	r.inflight = map[SessionID]bool{}
	r.dirty = map[SessionID]bool{}
	r.revs = map[SessionID]int64{}
}

// Handle routes one event.
func (r *Refresher) Handle(ev Event) {
	if IsClientNoise(ev) {
		return
	}
	switch ev.Type {
	case "session_created", "session_destroyed":
		r.schedule(sessionsKey)
		return
	case "session_event":
	default:
		return
	}
	if !ev.SessionID.Valid() {
		return
	}
	switch ev.Inner {
	case "session_label_changed":
		r.schedule(sessionsKey)
	case "session_view_changed":
		if ev.HasRevision {
			r.mu.Lock()
			last, seen := r.revs[ev.SessionID]
			r.mu.Unlock()
			if seen && ev.Revision <= last {
				return
			}
		}
		r.schedule(ev.SessionID)
	case "window_created", "window_closed", "window_label_changed", "active_window_changed",
		"block_created", "block_closed", "block_owner_changed", "creator_changed":
		r.schedule(ev.SessionID)
	case "block_event":
		if ev.Block != nil {
			r.h.BlockEvent(ev.SessionID, *ev.Block)
		}
	}
}

func (r *Refresher) schedule(key SessionID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, pending := r.timers[key]; pending {
		return
	}
	gen := r.gen
	r.timers[key] = r.clock.AfterFunc(r.debounce, func() { r.fire(key, gen) })
}

func (r *Refresher) fire(key SessionID, gen uint64) {
	r.mu.Lock()
	if gen != r.gen {
		r.mu.Unlock()
		return
	}
	delete(r.timers, key)
	if key == sessionsKey {
		r.mu.Unlock()
		r.h.RefreshSessions()
		return
	}
	if r.inflight[key] {
		r.dirty[key] = true
		r.mu.Unlock()
		return
	}
	r.inflight[key] = true
	r.mu.Unlock()
	r.h.InspectSession(key, r.doneFunc(key, gen))
}

func (r *Refresher) doneFunc(key SessionID, gen uint64) InspectDone {
	var once sync.Once
	return func(rev int64, ok bool) bool {
		apply := false
		once.Do(func() {
			r.mu.Lock()
			if gen != r.gen {
				r.mu.Unlock()
				return
			}
			r.inflight[key] = false
			if ok {
				apply = r.acceptLocked(key, rev)
			}
			rerun := r.dirty[key]
			if rerun {
				r.dirty[key] = false
				r.inflight[key] = true
			}
			r.mu.Unlock()
			if rerun {
				r.h.InspectSession(key, r.doneFunc(key, gen))
			}
		})
		return apply
	}
}

func (r *Refresher) acceptLocked(s SessionID, rev int64) bool {
	if last, seen := r.revs[s]; seen && rev < last {
		return false
	}
	r.revs[s] = rev
	return true
}

// Accept records a session revision fetched outside the Refresher and reports
// whether it is not older than the last applied one.
func (r *Refresher) Accept(s SessionID, rev int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acceptLocked(s, rev)
}

// Reset drops pending work and makes in-flight results stale.
func (r *Refresher) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen++
	for _, t := range r.timers {
		t.Stop()
	}
	r.clear()
}
