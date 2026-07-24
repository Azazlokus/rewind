// Пакет hub управляет набором комнат и распределяет игроков по ним.
//
// Он владеет горутиной каждой комнаты: запускает их, выдаёт присоединяющимся
// игрокам, а при shutdown отменяет все и ждёт, пока каждая доиграет свой
// последний тик. Как и остальной сервер, он никогда не трогает мир комнаты —
// держит лишь *game.Room и зовёт его канальный API.
package hub

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"arena/internal/game"
)

// Config управляет hub и создаваемыми им комнатами.
type Config struct {
	// Room — шаблон, применяемый к каждой поднимаемой комнате. Её Clock, Metrics
	// и Logger переиспользуются между комнатами.
	Room game.Config
	// MaxRooms ограничивает число одновременно существующих комнат. По умолчанию 16.
	MaxRooms int
	// Logger по умолчанию — отбрасывающий логгер.
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

// Hub — коллекция живых комнат.
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

// New создаёт hub, привязанный к ctx. При отмене ctx все комнаты
// останавливаются; дождитесь возврата Wait, прежде чем выходить.
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

// Assign возвращает комнату, где есть место ещё для одного игрока, создавая её по
// требованию. Возвращённая комната уже запущена.
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

// startRoomLocked создаёт и запускает комнату. h.mu должен быть удержан.
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

// Rooms возвращает снимок текущих комнат — для метрик и страниц статуса.
func (h *Hub) Rooms() []*game.Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*game.Room(nil), h.rooms...)
}

// Shutdown останавливает каждую комнату и ждёт, пока они доиграют последний тик.
// Идемпотентно.
func (h *Hub) Shutdown() {
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		h.cancel()
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// Wait блокируется, пока не остановится каждая комната. Вызывающие, отменившие
// родительский контекст, используют его, чтобы узнать о завершении shutdown.
func (h *Hub) Wait() { h.wg.Wait() }
