package game

import (
	"testing"
	"time"
)

// TestManualClockAdvance checks that advancing the clock delivers exactly the
// ticks that come due, in order, and moves Now to the deadline.
func TestManualClockAdvance(t *testing.T) {
	c := NewManualClock(time.Time{})
	start := c.Now()
	tk := c.NewTicker(10 * time.Millisecond)

	got := make([]time.Time, 0, 3)
	done := make(chan struct{})
	go func() {
		for range 3 {
			got = append(got, <-tk.C())
		}
		close(done)
	}()

	c.Advance(35 * time.Millisecond)
	<-done
	tk.Stop()

	if len(got) != 3 {
		t.Fatalf("expected 3 ticks, got %d", len(got))
	}
	for i, at := range got {
		want := start.Add(time.Duration(i+1) * 10 * time.Millisecond)
		if !at.Equal(want) {
			t.Errorf("tick %d at %v, want %v", i, at, want)
		}
	}
	if elapsed := c.Now().Sub(start); elapsed != 35*time.Millisecond {
		t.Errorf("Now advanced by %v, want 35ms", elapsed)
	}
}

// TestManualClockStop checks a stopped ticker stops firing.
func TestManualClockStop(t *testing.T) {
	c := NewManualClock(time.Time{})
	tk := c.NewTicker(10 * time.Millisecond)
	tk.Stop()

	fired := make(chan struct{}, 1)
	go func() {
		select {
		case <-tk.C():
			fired <- struct{}{}
		case <-time.After(50 * time.Millisecond):
		}
	}()

	c.Advance(100 * time.Millisecond)
	select {
	case <-fired:
		t.Fatal("stopped ticker fired")
	case <-time.After(20 * time.Millisecond):
	}
}
