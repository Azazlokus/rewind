package hub

import (
	"context"
	"testing"
	"time"

	"arena/internal/game"
	"arena/internal/transport"
)

func testHub(t *testing.T, cfg Config) *Hub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfg.Room.Clock = game.NewManualClock(time.Time{})
	h := New(ctx, cfg)
	t.Cleanup(func() {
		cancel()
		h.Wait()
	})
	return h
}

// TestAssignReusesRoom checks two assignments with spare capacity land in the
// same room.
func TestAssignReusesRoom(t *testing.T) {
	h := testHub(t, Config{Room: game.Config{MaxPlayers: 4}})

	r1, err := h.Assign()
	if err != nil {
		t.Fatal(err)
	}
	r2, err := h.Assign()
	if err != nil {
		t.Fatal(err)
	}
	// No players have actually joined, so capacity is untouched and the hub
	// should hand back the same room.
	if r1 != r2 {
		t.Fatalf("expected the same room, got %s and %s", r1.ID(), r2.ID())
	}
	if got := len(h.Rooms()); got != 1 {
		t.Fatalf("expected 1 room, got %d", got)
	}
}

// TestAssignRespectsMaxRooms checks the hub refuses to exceed MaxRooms once the
// only room is full.
func TestAssignRespectsMaxRooms(t *testing.T) {
	h := testHub(t, Config{MaxRooms: 1, Room: game.Config{MaxPlayers: 2}})
	r, err := h.Assign()
	if err != nil {
		t.Fatal(err)
	}
	fillRoom(t, r)

	if _, err := h.Assign(); err == nil {
		t.Fatal("expected Assign to fail when the only room is full and MaxRooms=1")
	}
}

// TestNewRoomWhenFull checks the hub spins up a second room when the first is
// full and capacity allows.
func TestNewRoomWhenFull(t *testing.T) {
	h := testHub(t, Config{MaxRooms: 4, Room: game.Config{MaxPlayers: 2}})
	r1, err := h.Assign()
	if err != nil {
		t.Fatal(err)
	}
	fillRoom(t, r1)

	r2, err := h.Assign()
	if err != nil {
		t.Fatal(err)
	}
	if r1 == r2 {
		t.Fatal("expected a new room once the first was full")
	}
	if got := len(h.Rooms()); got != 2 {
		t.Fatalf("expected 2 rooms, got %d", got)
	}
}

// TestShutdownStopsRooms checks Shutdown stops every room.
func TestShutdownStopsRooms(t *testing.T) {
	h := testHub(t, Config{MaxRooms: 4, Room: game.Config{MaxPlayers: 2}})
	r1, err := h.Assign()
	if err != nil {
		t.Fatal(err)
	}
	fillRoom(t, r1)
	if _, err := h.Assign(); err != nil {
		t.Fatal(err)
	}
	rooms := h.Rooms()
	if len(rooms) != 2 {
		t.Fatalf("expected 2 rooms, got %d", len(rooms))
	}

	h.Shutdown()
	for _, r := range rooms {
		select {
		case <-r.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("room %s did not stop", r.ID())
		}
	}
}

// fillRoom joins players until the room reports full, driving its manual clock so
// the join events are processed.
func fillRoom(t *testing.T, r *game.Room) {
	t.Helper()
	clock, ok := r.Config().Clock.(*game.ManualClock)
	if !ok {
		t.Fatal("expected a ManualClock in the room config")
	}
	interval := r.Config().TickInterval()
	ctx := context.Background()
	target := r.Config().MaxPlayers

	for r.Players() < target {
		server, _ := transport.Pipe(8)
		joinDone := make(chan error, 1)
		go func() {
			_, err := r.Join(ctx, server, "filler")
			joinDone <- err
		}()
	wait:
		for range 10000 {
			select {
			case err := <-joinDone:
				if err != nil {
					t.Fatalf("join: %v", err)
				}
				break wait
			default:
				clock.Advance(interval)
			}
		}
	}
}
