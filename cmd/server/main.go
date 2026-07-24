// Command server runs the arena game server: an HTTP/WebSocket gateway in front
// of the room hub, plus metrics and pprof endpoints.
//
// Configuration comes entirely from the environment (see config.go). Shutdown is
// graceful: SIGINT/SIGTERM cancels the base context, rooms finish the tick in
// progress and close their connections, then the HTTP server drains.
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

	// The base context is the lifetime of the whole server. A signal cancels
	// it, and cancellation flows to the hub, the rooms and every session.
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
			Seed:         cfg.Seed,
			Metrics:      mtr,
			Logger:       log,
		},
	})

	gw := newGateway(h, log, cfg)

	mux := http.NewServeMux()
	mux.Handle("/ws", gw)
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
		// No write timeout: a WebSocket connection is long-lived. The transport
		// layer bounds individual writes instead.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	pprofSrv := startPprof(cfg.PprofAddr, log)

	// Run the HTTP server until it stops on its own (a real error) or the base
	// context is cancelled (a signal), whichever comes first.
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

// shutdown drains the HTTP servers and waits for the rooms to stop. Order
// matters: stop accepting connections first, then let the hub finish, so no new
// player can join a room that is already winding down.
func shutdown(srv, pprofSrv *http.Server, h *hub.Hub, grace time.Duration, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	err := srv.Shutdown(ctx)
	if err != nil {
		// A timed-out drain still has to release sockets, so force them closed.
		log.Warn("graceful shutdown timed out, forcing close", "err", err)
		_ = srv.Close()
	}
	if pprofSrv != nil {
		_ = pprofSrv.Shutdown(ctx)
	}

	// The base context is already cancelled by the signal, so the rooms are on
	// their way down; wait for their last tick to land.
	h.Wait()
	log.Info("shutdown complete")
	return nil
}

// startPprof serves net/http/pprof on a localhost-only port. It returns nil when
// profiling is disabled (empty address).
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
