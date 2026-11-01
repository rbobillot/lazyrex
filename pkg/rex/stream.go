package rex

import (
	"context"
	"errors"
	"time"

	"github.com/rbobillot/lazyrex/pkg/render"
)

// StreamState reports the event stream going up (first line received) or
// down (process ended), with the exit code that ended it.
type StreamState struct {
	Up     bool
	Code   int
	Kind   ExitKind
	Stderr string
	// Remote reports that the stream reached another machine's server, where
	// exit 4 does not mean stopped.
	Remote bool
}

// Supervisor keeps one `rex events --all --json` running, reconnecting with
// exponential backoff (Min doubling up to Max, reset once a line arrives).
type Supervisor struct {
	Client    *Client
	Clock     Clock
	Min, Max  time.Duration
	OnEvent   func(Event)
	OnBadLine func([]byte, error)
	OnState   func(StreamState)
}

// Run blocks until ctx ends.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.Min
	for ctx.Err() == nil {
		state := s.once(ctx, &backoff)
		if ctx.Err() != nil {
			return
		}
		s.OnState(state)
		select {
		case <-ctx.Done():
			return
		case <-s.Clock.After(backoff):
		}
		backoff = min(backoff*2, s.Max)
	}
}

func (s *Supervisor) once(ctx context.Context, backoff *time.Duration) StreamState {
	ls, err := s.Client.Stream(ctx, Commands{}.Events())
	if err != nil {
		var route *RouteError
		if errors.As(err, &route) {
			// The route (a tunnel) comes back by itself: retry at Min.
			*backoff = s.Min
		}
		return StreamState{Code: -1, Kind: ExitStartError, Stderr: render.LogText(err.Error())}
	}
	first := true
	for line := range ls.Lines() {
		if first {
			first = false
			*backoff = s.Min
			s.OnState(StreamState{Up: true})
		}
		ev, err := ParseEvent(line)
		if err != nil {
			s.OnBadLine(line, err)
			continue
		}
		s.OnEvent(ev)
	}
	res := ls.Wait()
	route := s.Client.Transport
	if r, ok := ls.(interface{ Route() Transport }); ok {
		route = r.Route()
	}
	return StreamState{Code: res.Code, Kind: MapExit(res.Code, route, ctx.Err()), Stderr: render.LogText(string(res.Stderr)), Remote: route.Remote()}
}
