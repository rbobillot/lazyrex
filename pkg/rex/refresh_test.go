package rex_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rbobillot/lazyrex/pkg/rex"
	"github.com/rbobillot/lazyrex/pkg/rex/rextest"
)

type recHandler struct {
	mu       sync.Mutex
	sessions int
	inspects []rex.SessionID
	pending  []rex.InspectDone
	blocks   []rex.BlockEvent
}

func (h *recHandler) RefreshSessions() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions++
}

func (h *recHandler) InspectSession(id rex.SessionID, done rex.InspectDone) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inspects = append(h.inspects, id)
	h.pending = append(h.pending, done)
}

func (h *recHandler) BlockEvent(_ rex.SessionID, ev rex.BlockEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.blocks = append(h.blocks, ev)
}

func (h *recHandler) counts() (int, int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions, len(h.inspects), len(h.blocks)
}

func (h *recHandler) finish(i int, rev int64, ok bool) bool {
	h.mu.Lock()
	done := h.pending[i]
	h.mu.Unlock()
	return done(rev, ok)
}

func mustEvent(t *testing.T, line string) rex.Event {
	t.Helper()
	ev, err := rex.ParseEvent([]byte(line))
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return ev
}

func inner(s rex.SessionID, body string) string {
	return fmt.Sprintf(`{"type":"session_event","payload":{"session_id":%q,"event":%s}}`, s, body)
}

func newRefresher() (*rex.Refresher, *recHandler, *rextest.FakeClock) {
	clk := rextest.NewFakeClock(time.Unix(0, 0))
	h := &recHandler{}
	return rex.NewRefresher(clk, 200*time.Millisecond, h), h, clk
}

func TestRefresherSelfNoise(t *testing.T) {
	r, h, clk := newRefresher()
	for i := 0; i < 50; i++ {
		switch i % 5 {
		case 0:
			r.Handle(mustEvent(t, `{"type":"client_joined","payload":{"client_id":"client:x"}}`))
		case 1:
			r.Handle(mustEvent(t, `{"type":"client_left"}`))
		case 2:
			r.Handle(mustEvent(t, inner(S, `{"type":"client_connected"}`)))
		case 3:
			r.Handle(mustEvent(t, inner(S, `{"type":"client_disconnected"}`)))
		default:
			r.Handle(mustEvent(t, `{"type":"client_changed"}`))
		}
	}
	clk.Advance(time.Second)
	if s, i, b := h.counts(); s+i+b != 0 {
		t.Fatalf("noise triggered %d session refreshes, %d inspects, %d block updates", s, i, b)
	}
}

func TestRefresherRevisionNotNewer(t *testing.T) {
	r, h, clk := newRefresher()
	if !r.Accept(S, 7) {
		t.Fatalf("first revision refused")
	}
	r.Handle(mustEvent(t, inner(S, `{"type":"session_view_changed","revision":7}`)))
	r.Handle(mustEvent(t, inner(S, `{"type":"session_view_changed","revision":5}`)))
	clk.Advance(time.Second)
	if _, i, _ := h.counts(); i != 0 {
		t.Fatalf("stale revisions triggered %d inspects", i)
	}
}

func TestRefresherNewerOnceAfterDebounce(t *testing.T) {
	r, h, clk := newRefresher()
	r.Accept(S, 7)
	for i := 0; i < 3; i++ {
		r.Handle(mustEvent(t, inner(S, `{"type":"session_view_changed","revision":8}`)))
	}
	r.Handle(mustEvent(t, inner(S, `{"type":"window_created"}`)))
	clk.Advance(199 * time.Millisecond)
	if _, i, _ := h.counts(); i != 0 {
		t.Fatalf("inspect before the debounce elapsed")
	}
	clk.Advance(time.Millisecond)
	if _, i, _ := h.counts(); i != 1 || h.inspects[0] != S {
		t.Fatalf("inspects = %v", h.inspects)
	}
	clk.Advance(time.Second)
	if _, i, _ := h.counts(); i != 1 {
		t.Fatalf("extra inspects: %v", h.inspects)
	}
}

func TestRefresherInflightAndStale(t *testing.T) {
	r, h, clk := newRefresher()
	r.Handle(mustEvent(t, inner(S, `{"type":"block_created"}`)))
	clk.Advance(200 * time.Millisecond)
	if _, i, _ := h.counts(); i != 1 {
		t.Fatalf("inspects = %d", i)
	}
	r.Handle(mustEvent(t, inner(S, `{"type":"block_closed"}`)))
	clk.Advance(time.Second)
	if _, i, _ := h.counts(); i != 1 {
		t.Fatalf("second inspect while one is in flight")
	}
	if !h.finish(0, 7, true) {
		t.Fatalf("fresh result refused")
	}
	if _, i, _ := h.counts(); i != 2 {
		t.Fatalf("dirty session not re-inspected after the in-flight one finished")
	}
	if h.finish(1, 6, true) {
		t.Fatalf("lower revision accepted")
	}
	r.Handle(mustEvent(t, inner(S, `{"type":"window_closed"}`)))
	clk.Advance(200 * time.Millisecond)
	if _, i, _ := h.counts(); i != 3 {
		t.Fatalf("inspects = %d", i)
	}
	r.Reset()
	if h.finish(2, 9, true) {
		t.Fatalf("result from before Reset accepted")
	}
	r.Handle(mustEvent(t, inner(S, `{"type":"window_closed"}`)))
	clk.Advance(200 * time.Millisecond)
	if _, i, _ := h.counts(); i != 4 {
		t.Fatalf("session not inspectable after Reset: %d", i)
	}
}

func TestRefresherMapping(t *testing.T) {
	r, h, clk := newRefresher()
	r.Handle(mustEvent(t, `{"type":"session_created","payload":{"session_id":"session:03cccc"}}`))
	r.Handle(mustEvent(t, `{"type":"session_destroyed","payload":{"session_id":"session:02bbbb"}}`))
	r.Handle(mustEvent(t, inner(S, `{"type":"session_label_changed"}`)))
	clk.Advance(200 * time.Millisecond)
	if s, i, _ := h.counts(); s != 1 || i != 0 {
		t.Fatalf("session events: %d refreshes, %d inspects", s, i)
	}
	for _, typ := range []string{"window_created", "window_closed", "window_label_changed", "active_window_changed",
		"block_created", "block_closed", "block_owner_changed", "creator_changed"} {
		r2, h2, clk2 := newRefresher()
		r2.Handle(mustEvent(t, inner(S, fmt.Sprintf(`{"type":%q}`, typ))))
		clk2.Advance(200 * time.Millisecond)
		if _, i, _ := h2.counts(); i != 1 {
			t.Errorf("%s: %d inspects", typ, i)
		}
	}
	r.Handle(mustEvent(t, inner(S, `{"type":"block_event","payload":{"block_id":"block:01b1","name":"child_exited","payload":{"exit_code":1}}}`)))
	if _, _, b := h.counts(); b != 1 || h.blocks[0].BlockID != "block:01b1" {
		t.Fatalf("block event not delivered at once: %v", h.blocks)
	}
	r.Handle(mustEvent(t, `{"type":"keymap_changed"}`))
	r.Handle(mustEvent(t, `{"type":"hosts_changed"}`))
	r.Handle(mustEvent(t, `{"type":"brand_new"}`))
	r.Handle(mustEvent(t, inner(S, `{"type":"brand_new_inner"}`)))
	clk.Advance(time.Second)
	if s, i, b := h.counts(); s != 1 || i != 0 || b != 1 {
		t.Fatalf("ignored events triggered work: %d %d %d", s, i, b)
	}
}
