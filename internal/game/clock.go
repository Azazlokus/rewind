package game

import (
	"sync"
	"time"
)

// Clock is the room's only source of time. Production uses RealClock; tests use
// ManualClock and drive a thousand ticks in microseconds, deterministically and
// without a single time.Sleep.
type Clock interface {
	Now() time.Time
	NewTicker(d time.Duration) Ticker
}

// Ticker is the subset of time.Ticker the room needs.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// RealClock is the wall-clock implementation of Clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) NewTicker(d time.Duration) Ticker {
	return realTicker{t: time.NewTicker(d)}
}

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// ManualClock is a Clock whose time only moves when a test says so.
//
// Advance delivers ticks synchronously: it returns only once the consumer has
// received every tick it fired. A test can therefore advance the clock, then
// read the resulting snapshot, and know the two are ordered. If nothing is
// consuming the ticker, Advance blocks until the test's own timeout fires.
type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*manualTicker
}

// NewManualClock returns a clock started at t. A zero t starts at a fixed,
// arbitrary date so that logs in tests look sane.
func NewManualClock(t time.Time) *ManualClock {
	if t.IsZero() {
		t = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return &ManualClock{now: t}
}

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) NewTicker(d time.Duration) Ticker {
	if d <= 0 {
		panic("game: ManualClock.NewTicker with non-positive period")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTicker{
		c:       make(chan time.Time),
		period:  d,
		next:    c.now.Add(d),
		stopped: make(chan struct{}),
	}
	c.tickers = append(c.tickers, t)
	return t
}

// Advance moves time forward by d, firing every ticker that comes due, in
// chronological order.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	deadline := c.now.Add(d)
	c.mu.Unlock()

	for {
		c.mu.Lock()
		var due *manualTicker
		var at time.Time
		for _, t := range c.tickers {
			if t.isStopped() || t.next.After(deadline) {
				continue
			}
			if due == nil || t.next.Before(at) {
				due, at = t, t.next
			}
		}
		if due == nil {
			c.now = deadline
			c.mu.Unlock()
			return
		}
		c.now = at
		due.next = at.Add(due.period)
		ch := due.c
		c.mu.Unlock()

		// Sent outside the lock: the consumer calls Now() while handling a tick.
		select {
		case ch <- at:
		case <-due.stopped:
		}
	}
}

// AdvanceTicks advances the clock by n periods of d.
func (c *ManualClock) AdvanceTicks(n int, d time.Duration) {
	for range n {
		c.Advance(d)
	}
}

type manualTicker struct {
	c       chan time.Time
	period  time.Duration
	next    time.Time // guarded by ManualClock.mu
	stopped chan struct{}
	once    sync.Once
}

func (t *manualTicker) C() <-chan time.Time { return t.c }

func (t *manualTicker) Stop() { t.once.Do(func() { close(t.stopped) }) }

func (t *manualTicker) isStopped() bool {
	select {
	case <-t.stopped:
		return true
	default:
		return false
	}
}
