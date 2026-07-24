//go:build integration

// End-to-end тест: настоящий HTTP/WebSocket-сервер на случайном порту,
// управляемый настоящими ботами по сети. Он прогоняет весь путь — upgrade,
// рукопожатие, inbox, tick, broadcast, — который headless-тесты намеренно
// обходят.
//
// Запуск: go test -tags=integration ./cmd/server
package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arena/internal/bot"
	"arena/internal/game"
	"arena/internal/hub"
	"arena/internal/metrics"
	"arena/internal/protocol"
)

func startServer(t *testing.T) (url string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	h := hub.New(ctx, hub.Config{
		MaxRooms: 4,
		Room: game.Config{
			TickRate:     30,
			SnapshotRate: 20, // итерация 2: снапшоты реже тикрейта
			MaxPlayers:   16,
			Seed:         1,
			Metrics:      metrics.New(),
		},
	})
	gw := newGateway(h, slog.New(slog.DiscardHandler), serverConfig{
		JoinTimeout:    2 * time.Second,
		AllowAllOrigin: true,
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", gw)
	srv := httptest.NewServer(mux)

	t.Cleanup(func() {
		srv.Close()
		cancel()
		h.Wait()
	})
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

// TestE2EMovementVisibleToPeer присоединяет двух клиентов, двигает одного и
// проверяет, что другой видит движение — критерий приёмки итерации 1 («два
// браузера видят движение друг друга»), проверенный кодом.
func TestE2EMovementVisibleToPeer(t *testing.T) {
	url := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mover, err := bot.Dial(ctx, url, "mover")
	if err != nil {
		t.Fatalf("dial mover: %v", err)
	}
	defer mover.Close()

	watcher, err := bot.Dial(ctx, url, "watcher")
	if err != nil {
		t.Fatalf("dial watcher: %v", err)
	}
	defer watcher.Close()

	// Запоминаем стартовый X «mover», как его видит «watcher».
	startX, ok := waitForEntity(ctx, t, watcher, mover.ID())
	if !ok {
		t.Fatal("watcher never saw the mover spawn")
	}

	// Гоним «mover» вправо, пока «watcher» не увидит явное смещение.
	moveCtx, stopMoving := context.WithCancel(ctx)
	defer stopMoving()
	go func() {
		ticker := time.NewTicker(time.Second / 60)
		defer ticker.Stop()
		for {
			select {
			case <-moveCtx.Done():
				return
			case <-ticker.C:
				_ = mover.SendInput(moveCtx, protocol.BtnRight, 0)
			}
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := watcher.ReadSnapshot(ctx)
		if err != nil {
			t.Fatalf("watcher read: %v", err)
		}
		if e, ok := findEntity(snap, mover.ID()); ok && e.X > startX+50 {
			return // успех: движение прошло через сеть
		}
	}
	t.Fatal("watcher never observed the mover move right")
}

func waitForEntity(ctx context.Context, t *testing.T, c *bot.Client, id uint16) (float32, bool) {
	t.Helper()
	for range 300 {
		snap, err := c.ReadSnapshot(ctx)
		if err != nil {
			t.Fatalf("read snapshot: %v", err)
		}
		if e, ok := findEntity(snap, id); ok {
			return e.X, true
		}
	}
	return 0, false
}

func findEntity(s protocol.Snapshot, id uint16) (protocol.Entity, bool) {
	for _, e := range s.Entities {
		if e.ID == id {
			return e, true
		}
	}
	return protocol.Entity{}, false
}
