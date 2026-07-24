// Package hub manages the set of rooms and assigns players to them.
//
// It owns every room's goroutine: it starts them, hands them out to joining
// players, and on shutdown cancels them all and waits for each to drain its
// final tick. Like the rest of the server it never touches a room's world; it
// only ever holds a *game.Room handle and calls its channel-based API.
package hub

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"arena/internal/game"
)

// Config controls the hub and the rooms it creates.
type Config struct {
	// Room is the template applied to every room the hub spins up. Its Clock,
	// Metrics and Logger are reused across rooms.
	Room game.Config
	// MaxRooms caps how many rooms may exist at once. Default 16.
	MaxRooms int
	// Logger defaults to a discarding logger.
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.MaxRooms <= 0 {
		c.MaxRooms = 16
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// Hub is the collection of live rooms.
type Hub struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger

	mu     sync.Mutex
	rooms  []*game.Room
	nextID int
	closed bool

	wg sync.WaitGroup
}

// New creates a hub bound to ctx. When ctx is cancelled every room stops; wait
// for Wait to return before exiting.
func New(ctx context.Context, cfg Config) *Hub {
	cfg = cfg.withDefaults()
	hctx, cancel := context.WithCancel(ctx)
	return &Hub{
		cfg:    cfg,
		ctx:    hctx,
		cancel: cancel,
		log:    cfg.Logger,
	}
}

// Assign returns a room with room for one more player, creating one on demand.
// The returned room is already running.
func (h *Hub) Assign() (*game.Room, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, game.ErrRoomClosed
	}

	for _, r := range h.rooms {
		if r.Players() < h.cfg.Room.MaxPlayers {
			return r, nil
		}
	}
	if len(h.rooms) >= h.cfg.MaxRooms {
		return nil, fmt.Errorf("hub: all %d rooms full", h.cfg.MaxRooms)
	}
	return h.startRoomLocked(), nil
}

// startRoomLocked creates and launches a room. h.mu must be held.
func (h *Hub) startRoomLocked() *game.Room {
	h.nextID++
	id := fmt.Sprintf("room-%d", h.nextID)
	r := game.NewRoom(id, h.cfg.Room)
	h.rooms = append(h.rooms, r)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		r.Run(h.ctx)
	}()
	h.log.Info("room created", "room", id, "total", len(h.rooms))
	return r
}

// Rooms returns a snapshot of the current rooms, for metrics and status pages.
func (h *Hub) Rooms() []*game.Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*game.Room(nil), h.rooms...)
}

// Shutdown stops every room and waits for them to finish their final tick. It is
// idempotent.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		h.cancel()
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// Wait blocks until every room has stopped. Callers that cancel the parent
// context use it to know shutdown is complete.
func (h *Hub) Wait() { h.wg.Wait() }
