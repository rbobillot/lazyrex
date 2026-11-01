package rex

import "time"

// Timer is a stoppable pending call.
type Timer interface{ Stop() bool }

// Clock abstracts time for debouncing, backoff and polling.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

// RealClock returns the wall clock.
func RealClock() Clock { return realClock{} }

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }
func (realClock) After(d time.Duration) <-chan time.Time    { return time.After(d) }
