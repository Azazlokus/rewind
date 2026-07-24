package game

import (
	"testing"

	"arena/internal/protocol"
)

// TestWorldDeterminism is the foundation the whole project stands on: two worlds
// with the same seed, fed the same inputs, must reach byte-identical state.
// Replays and client prediction both depend on this holding.
func TestWorldDeterminism(t *testing.T) {
	const players, ticks = 8, 1000
	const dt = 1.0 / 30

	build := func() *World {
		w := NewWorld(1234)
		for i := range players {
			if _, err := w.AddPlayer("p"); err != nil {
				t.Fatalf("add player %d: %v", i, err)
			}
		}
		return w
	}
	a, b := build(), build()

	// A deterministic, per-player input tape.
	tape := func(id PlayerID, tick int) protocol.Input {
		var btn uint8
		switch (int(id) + tick) % 4 {
		case 0:
			btn = protocol.BtnUp
		case 1:
			btn = protocol.BtnRight
		case 2:
			btn = protocol.BtnDown | protocol.BtnLeft
		case 3:
			btn = protocol.BtnUp | protocol.BtnRight
		}
		return protocol.Input{Seq: uint32(tick), Buttons: btn}
	}

	for tick := range ticks {
		for id := PlayerID(1); id <= players; id++ {
			in := tape(id, tick)
			a.SetInput(id, in)
			b.SetInput(id, in)
		}
		a.Step(dt)
		b.Step(dt)
		if ca, cb := a.Checksum(), b.Checksum(); ca != cb {
			t.Fatalf("divergence at tick %d: %x != %x", tick, ca, cb)
		}
	}
}

// TestWorldSeedIndependence checks two different seeds actually differ, so the
// determinism test above is not passing trivially.
func TestWorldSeedIndependence(t *testing.T) {
	a := NewWorld(1)
	b := NewWorld(2)
	if _, err := a.AddPlayer("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddPlayer("x"); err != nil {
		t.Fatal(err)
	}
	if a.Checksum() == b.Checksum() {
		t.Fatal("different seeds produced identical spawn state")
	}
}

// TestMovementClampsToMap checks a player driven into a wall stops at the border
// rather than leaving the map.
func TestMovementClampsToMap(t *testing.T) {
	w := NewWorld(1)
	p, err := w.AddPlayer("wall")
	if err != nil {
		t.Fatal(err)
	}
	w.SetInput(p.ID, protocol.Input{Seq: 1, Buttons: protocol.BtnLeft | protocol.BtnUp})
	for range 1000 {
		w.Step(1.0 / 30)
	}
	if p.X < PlayerRadius || p.Y < PlayerRadius {
		t.Fatalf("player left the map: x=%.2f y=%.2f (min %.0f)", p.X, p.Y, PlayerRadius)
	}
	if p.X != PlayerRadius || p.Y != PlayerRadius {
		t.Fatalf("player did not settle in the top-left corner: x=%.2f y=%.2f", p.X, p.Y)
	}
}

// TestLastProcessedSeqMonotonic checks the acknowledged sequence never walks
// backwards even when an older input arrives late.
func TestLastProcessedSeqMonotonic(t *testing.T) {
	w := NewWorld(1)
	p, err := w.AddPlayer("seq")
	if err != nil {
		t.Fatal(err)
	}
	w.SetInput(p.ID, protocol.Input{Seq: 10})
	w.Step(1.0 / 30)
	if p.LastProcessedSeq != 10 {
		t.Fatalf("LastProcessedSeq=%d, want 10", p.LastProcessedSeq)
	}
	// A stale (re-ordered) input must not lower the acknowledgement.
	w.SetInput(p.ID, protocol.Input{Seq: 5})
	w.Step(1.0 / 30)
	if p.LastProcessedSeq != 10 {
		t.Fatalf("stale input lowered LastProcessedSeq to %d, want 10", p.LastProcessedSeq)
	}
}

// TestRemovePlayerKeepsOrderSorted checks the deterministic id order stays sorted
// across adds and removes, since map iteration order must never leak in.
func TestRemovePlayerKeepsOrderSorted(t *testing.T) {
	w := NewWorld(1)
	for range 5 {
		if _, err := w.AddPlayer("p"); err != nil {
			t.Fatal(err)
		}
	}
	w.RemovePlayer(3)
	w.RemovePlayer(1)
	if _, err := w.AddPlayer("late"); err != nil {
		t.Fatal(err)
	}
	prev := PlayerID(0)
	w.Each(func(p *Player) {
		if p.ID <= prev {
			t.Fatalf("order not ascending: %d after %d", p.ID, prev)
		}
		prev = p.ID
	})
}
