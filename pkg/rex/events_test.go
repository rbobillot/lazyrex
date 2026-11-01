package rex_test

import (
	"testing"

	"github.com/rbobillot/lazyrex/pkg/rex"
)

func TestParseEventServer(t *testing.T) {
	ev, err := rex.ParseEvent([]byte(`{"type":"session_created","payload":{"session_id":"session:03cccc"},"zz":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "session_created" || ev.Inner != "" || ev.Block != nil {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseEventSessionWrapper(t *testing.T) {
	ev, err := rex.ParseEvent([]byte(`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"window_created","window_id":"window:01w3"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "session_event" || ev.SessionID != "session:01aaaa" || ev.Inner != "window_created" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestParseEventRevision(t *testing.T) {
	for _, line := range []string{
		`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"session_view_changed","revision":8}}}`,
		`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"session_view_changed","payload":{"revision":8}}}}`,
	} {
		ev, err := rex.ParseEvent([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if ev.Inner != "session_view_changed" || !ev.HasRevision || ev.Revision != 8 {
			t.Fatalf("event = %+v", ev)
		}
	}
	ev, _ := rex.ParseEvent([]byte(`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"session_view_changed"}}}`))
	if ev.HasRevision {
		t.Fatalf("revision invented: %+v", ev)
	}
}

func TestParseEventBlockEvent(t *testing.T) {
	ev, err := rex.ParseEvent([]byte(`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"block_event","payload":{"block_id":"block:01b1","creator_name":"terminal","name":"child_exited","payload":{"exit_code":3,"error":"boom"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Block == nil || ev.Block.BlockID != "block:01b1" || ev.Block.Name != "child_exited" || ev.Block.CreatorName != "terminal" ||
		ev.Block.ExitCode == nil || *ev.Block.ExitCode != 3 || ev.Block.Error != "boom" {
		t.Fatalf("block event = %+v", ev.Block)
	}
	ev, err = rex.ParseEvent([]byte(`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"block_event","payload":{"block_id":"block:01b2","name":"program_status_changed","payload":{"id":"p1","record":{"state":"working"},"reason":"started"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Block == nil || ev.Block.Status != "working" || ev.Block.Reason != "started" {
		t.Fatalf("program status = %+v", ev.Block)
	}
	ev, _ = rex.ParseEvent([]byte(`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"block_event","payload":{"block_id":"block:01b2","name":"title_changed","payload":{"title":"vim main.go"}}}}}`))
	if ev.Block == nil || ev.Block.Title != "vim main.go" {
		t.Fatalf("title = %+v", ev.Block)
	}
}

func TestParseEventUnknownIgnored(t *testing.T) {
	ev, err := rex.ParseEvent([]byte(`{"type":"brand_new","payload":{"x":[1,2]},"zz":{"a":1}}`))
	if err != nil || ev.Type != "brand_new" || ev.Inner != "" || ev.Block != nil {
		t.Fatalf("unknown outer = %+v, %v", ev, err)
	}
	ev, err = rex.ParseEvent([]byte(`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"brand_new_inner","zz":1}}}`))
	if err != nil || ev.Inner != "brand_new_inner" || ev.Block != nil {
		t.Fatalf("unknown inner = %+v, %v", ev, err)
	}
	ev, err = rex.ParseEvent([]byte(`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"block_event","payload":{"block_id":"block:01b1","name":"brand_new","payload":"weird"}}}}`))
	if err != nil || ev.Block == nil || ev.Block.Name != "brand_new" {
		t.Fatalf("unknown block event = %+v, %v", ev, err)
	}
}

func TestParseEventMalformed(t *testing.T) {
	for _, line := range []string{"not json", `{"payload":{}}`, `[]`, ``} {
		if _, err := rex.ParseEvent([]byte(line)); err == nil {
			t.Errorf("ParseEvent(%q) succeeded", line)
		}
	}
}

func TestIsClientNoise(t *testing.T) {
	noise := []string{
		`{"type":"client_joined"}`, `{"type":"client_left"}`, `{"type":"client_changed"}`,
		`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"client_connected"}}}`,
		`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"client_disconnected"}}}`,
		`{"type":"session_event","payload":{"session_id":"session:01aaaa","event":{"type":"client_connection_changed"}}}`,
	}
	for _, line := range noise {
		ev, err := rex.ParseEvent([]byte(line))
		if err != nil || !rex.IsClientNoise(ev) {
			t.Errorf("%s not noise (%v)", line, err)
		}
	}
	ev, _ := rex.ParseEvent([]byte(`{"type":"session_created"}`))
	if rex.IsClientNoise(ev) {
		t.Errorf("session_created is noise")
	}
}
