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

// TestAssignReusesRoom проверяет, что два назначения при наличии места попадают в
// одну и ту же комнату.
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
	// Реально никто не присоединился, вместимость не тронута — hub должен вернуть
	// ту же комнату.
	if r1 != r2 {
		t.Fatalf("expected the same room, got %s and %s", r1.ID(), r2.ID())
	}
	if got := len(h.Rooms()); got != 1 {
		t.Fatalf("expected 1 room, got %d", got)
	}
}

// TestAssignRespectsMaxRooms проверяет, что hub отказывается превышать MaxRooms,
// когда единственная комната заполнена.
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

// TestNewRoomWhenFull проверяет, что hub поднимает вторую комнату, когда первая
// заполнена, а вместимость позволяет.
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

// TestShutdownStopsRooms проверяет, что Shutdown останавливает каждую комнату.
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

// fillRoom присоединяет игроков, пока комната не сообщит о заполнении, шагая её
// ручными часами, чтобы события join обрабатывались.
func fillRoom(t *testing.T, r *game.Room) {
	t.Helper()
	clock, ok := r.Config().Clock.(*game.ManualClock)
	if !ok {
		t.Fatal("expected a ManualClock in the room config")
	}
	<-r.Ready() // ждём регистрации тикера комнаты, иначе первые Advance потеряются
	interval := r.Config().TickInterval()
	ctx := context.Background()
	target := r.Config().MaxPlayers

	for r.Players() < target {
		server, _ := transport.Pipe(8)
		joinDone := make(chan error, 1)
		go func() {
			_, err := r.Join(ctx, server, "filler", 0)
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
