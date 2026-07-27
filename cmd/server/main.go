// Команда server запускает игровой сервер arena: HTTP/WebSocket-gateway перед
// hub комнат, плюс эндпоинты метрик и pprof.
//
// Конфигурация полностью берётся из окружения (см. config.go). Shutdown мягкий:
// SIGINT/SIGTERM отменяет базовый контекст, комнаты доигрывают текущий тик и
// закрывают соединения, затем HTTP-сервер сливается.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"arena/internal/game"
	"arena/internal/hub"
	"arena/internal/metrics"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	// Базовый контекст — время жизни всего сервера. Сигнал отменяет его, и отмена
	// растекается по hub, комнатам и каждой сессии.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mtr := metrics.New()
	h := hub.New(ctx, hub.Config{
		MaxRooms: cfg.MaxRooms,
		Logger:   log,
		Room: game.Config{
			TickRate:     cfg.TickRate,
			SnapshotRate: cfg.SnapshotRate,
			MaxPlayers:   cfg.MaxPlayers,
			AOIRadius:    cfg.AOIRadius,
			Seed:         cfg.Seed,
			Metrics:      mtr,
			Logger:       log,
		},
	})

	gw := newGateway(h, log, cfg)
	rtcGw := newRTCGateway(gw, log, cfg)

	mux := http.NewServeMux()
	mux.Handle("/ws", gw)
	mux.Handle("/rtc", rtcGw) // WebRTC-сигналинг (итерация 11); игровой транспорт — DataChannel
	mux.Handle("/metrics", mtr.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", staticHandler(cfg.WebDir))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// Без write timeout: WebSocket-соединение долгоживущее. Отдельные записи
		// ограничивает транспортный слой.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	pprofSrv := startPprof(cfg.PprofAddr, log)

	// Крутим HTTP-сервер, пока он не остановится сам (реальная ошибка) или пока
	// не отменится базовый контекст (сигнал) — что наступит раньше.
	serveErr := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.Addr, "web_dir", cfg.WebDir)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	return shutdown(srv, pprofSrv, h, cfg.ShutdownGrace, log)
}

// shutdown сливает HTTP-серверы и ждёт остановки комнат. Порядок важен: сначала
// перестаём принимать соединения, затем даём hub завершиться — чтобы новый игрок
// не смог зайти в уже сворачивающуюся комнату.
func shutdown(srv, pprofSrv *http.Server, h *hub.Hub, grace time.Duration, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	err := srv.Shutdown(ctx)
	if err != nil {
		// Слив по таймауту всё равно обязан освободить сокеты — закрываем силой.
		log.Warn("graceful shutdown timed out, forcing close", "err", err)
		_ = srv.Close()
	}
	if pprofSrv != nil {
		_ = pprofSrv.Shutdown(ctx)
	}

	// Базовый контекст уже отменён сигналом, так что комнаты на пути к остановке;
	// дожидаемся их последнего тика.
	h.Wait()
	log.Info("shutdown complete")
	return nil
}

// startPprof отдаёт net/http/pprof на localhost-порту. Возвращает nil, когда
// профилирование отключено (пустой адрес).
func startPprof(addr string, log *slog.Logger) *http.Server {
	if addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("pprof listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("pprof server", "err", err)
		}
	}()
	return srv
}
