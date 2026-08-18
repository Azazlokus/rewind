// Команда server запускает игровой сервер arena: HTTP/WebSocket-gateway перед
// hub комнат, плюс эндпоинты метрик и pprof.
//
// Конфигурация полностью берётся из окружения (см. config.go). Shutdown мягкий:
// SIGINT/SIGTERM отменяет базовый контекст, комнаты доигрывают текущий тик и
// закрывают соединения, затем HTTP-сервер сливается.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"arena/internal/account"
	"arena/internal/api"
	"arena/internal/botfill"
	"arena/internal/game"
	"arena/internal/hub"
	"arena/internal/metrics"
	"arena/internal/persist"
	"arena/internal/ratelimit"
	"arena/internal/store"
	"arena/internal/tracing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// buildVersion — версия сборки для атрибута service.version в трейсах; goreleaser
// может переопределить через ldflags.
var buildVersion = "dev"

// persistBuffer — глубина канала комната → persister. Итоги матчей редки, смерти
// чаще; буфер переживает всплеск, переполнение роняет статистику (не тик).
const persistBuffer = 1024

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

	// OpenTelemetry-трассировка (итер. 34): ставит глобальный провайдер+пропагатор.
	// Выключена по умолчанию (no-op). Слив буферизованных спанов — при выходе.
	tracingShutdown, err := tracing.Setup(ctx, tracing.Config{
		Enabled:     cfg.TracingEnabled,
		Endpoint:    cfg.TracingEndpoint,
		Insecure:    cfg.TracingInsecure,
		Stdout:      cfg.TracingStdout,
		ServiceName: cfg.TracingService,
		Version:     buildVersion,
		SampleRatio: cfg.TracingSampleRatio,
	})
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	defer func() {
		fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer fcancel()
		if err := tracingShutdown(fctx); err != nil {
			log.Warn("tracing shutdown failed", "err", err)
		}
	}()
	if cfg.TracingEnabled || cfg.TracingStdout {
		log.Info("tracing enabled", "endpoint", cfg.TracingEndpoint, "stdout", cfg.TracingStdout, "sample_ratio", cfg.TracingSampleRatio)
	}

	// Бэкенд: хранилище (аккаунты/стата/матчи) и сервис идентичности. Игровое ядро
	// от них не зависит — они обслуживают только HTTP-API (и, с итерации 14,
	// персист результатов матчей).
	st, err := store.Open(ctx, cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if len(cfg.AuthSecret) == 0 {
		cfg.AuthSecret = make([]byte, 32)
		if _, err := rand.Read(cfg.AuthSecret); err != nil {
			return fmt.Errorf("generate auth secret: %w", err)
		}
		log.Warn("ARENA_AUTH_SECRET not set — using an ephemeral secret; tokens won't survive restart")
	}
	// Почта верификации/сброса (итер. 37): без внешнего SMTP — LogMailer печатает токен
	// в лог (dev). Прод подключает свою реализацию Mailer здесь.
	accounts := account.NewService(st, cfg.AuthSecret, cfg.AccessTTL, cfg.RefreshTTL,
		account.WithMailer(account.LogMailer{Log: log}),
		account.WithTokenTTLs(cfg.VerifyTTL, cfg.ResetTTL))

	// Бутстрап первого админа (итер. 39): чтобы модерация не была chicken-egg, при старте
	// повышаем указанный аккаунт до admin (если он уже зарегистрирован).
	if cfg.AdminUsername != "" {
		if err := ensureAdmin(ctx, st, cfg.AdminUsername, log); err != nil {
			return err
		}
	}
	apiHandler := api.NewHandler(accounts, st, log, cfg.AuthRate)

	// Persister: комнаты шлют сюда смерти и итоги матчей, он пишет их в store вне
	// горутин комнат. Один канал fan-in на все комнаты; закрывается при shutdown,
	// когда все комнаты уже остановлены (см. shutdown) — отправителей больше нет.
	persistCh := make(chan game.PersistMsg, persistBuffer)
	persistDone := make(chan struct{})
	persister := persist.New(st, log, persist.Config{
		AntiCheatBanThreshold: int64(cfg.AntiCheatBan),
		AntiCheatBanDuration:  cfg.AntiCheatDur,
	})
	go func() {
		defer close(persistDone)
		persister.Run(persistCh)
	}()

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
			TeamMode:     cfg.TeamMode,
			HillMode:     cfg.HillMode,
			DomMode:      cfg.DomMode,
			CtfMode:      cfg.CtfMode,
			PersistSink:  persistCh,
			Metrics:      mtr,
			Logger:       log,
		},
	})

	// Наполнитель комнат ботами (итерация 17): пока в комнате есть человек, держит в
	// ней ARENA_BOT_FILL игроков. Боты — обычные клиенты через Pipe, мир не трогают.
	// При Target<=0 (по умолчанию) Run сразу возвращается — ни одной горутины.
	filler := botfill.New(h, botfill.Config{
		Target:     cfg.BotFill,
		MaxPlayers: cfg.MaxPlayers,
		Seed:       cfg.Seed,
		Logger:     log,
		Metrics:    mtr,
	})
	fillerDone := make(chan struct{})
	go func() {
		defer close(fillerDone)
		filler.Run(ctx)
	}()

	gw := newGateway(h, accounts, log, cfg)
	rtcGw := newRTCGateway(gw, log, cfg)

	// Рейт-лимит на игровой вход (итер. 33). Лимитеры ОБЩИЕ для /ws и /rtc, чтобы
	// соединения одного IP по обоим транспортам считались вместе; nil-примитив —
	// соответствующая проверка выключена (ARENA_JOIN_*=0).
	joinRate := ratelimit.NewLimiter(cfg.JoinRateBurst, cfg.JoinRateWindow)
	joinConns := ratelimit.NewConnLimiter(cfg.JoinMaxPerIP)

	mux := http.NewServeMux()
	mux.Handle("/ws", newJoinGate(gw, joinRate, joinConns, cfg.JoinRateHeader, log, mtr))
	mux.Handle("/rtc", newJoinGate(rtcGw, joinRate, joinConns, cfg.JoinRateHeader, log, mtr)) // WebRTC-сигналинг (итерация 11); игровой транспорт — DataChannel
	// REST-бэкенд (итерация 13) под otelhttp-инструментовкой (итер. 34): серверный спан
	// на каждый запрос, ctx с трейсом течёт в хендлеры и SQL (otelsql). Только /api —
	// /ws,/rtc блокируются на сессию (там ограниченный join-спан), /metrics,/healthz не
	// трассируем (шум).
	mux.Handle("/api/", otelhttp.NewHandler(apiHandler.Routes(), "api",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	))
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

	return shutdown(srv, pprofSrv, h, filler, fillerDone, persistCh, persistDone, cfg.ShutdownGrace, log)
}

// shutdown сливает HTTP-серверы и ждёт остановки комнат. Порядок важен: сначала
// перестаём принимать соединения, затем даём hub завершиться — чтобы новый игрок
// не смог зайти в уже сворачивающуюся комнату.
func shutdown(srv, pprofSrv *http.Server, h *hub.Hub, filler *botfill.Filler, fillerDone <-chan struct{}, persistCh chan game.PersistMsg, persistDone <-chan struct{}, grace time.Duration, log *slog.Logger) error {
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

	// Наполнитель ботов: цикл уже остановлен отменой ctx (ждём fillerDone), затем
	// дожидаемся горутин ботов — их сессии закрылись вместе с комнатами выше.
	<-fillerDone
	filler.Wait()

	// Комнаты остановлены — отправителей в канал персиста больше нет, безопасно
	// закрыть его. Persister дочитывает остаток и выходит; ждём его слив, но не
	// дольше grace (зависший store не должен держать shutdown вечно).
	close(persistCh)
	select {
	case <-persistDone:
	case <-ctx.Done():
		log.Warn("persister drain timed out")
	}
	log.Info("shutdown complete")
	return nil
}

// ensureAdmin повышает аккаунт до admin при старте (бутстрап модерации, итер. 39).
// Отсутствие аккаунта — не ошибка (зарегистрируется позже): просто предупреждаем.
func ensureAdmin(ctx context.Context, st store.Store, username string, log *slog.Logger) error {
	acc, _, err := st.CredentialsByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		log.Warn("ARENA_ADMIN_USERNAME not registered yet — register it, then restart to promote", "username", username)
		return nil
	}
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if acc.Role == account.RoleAdmin {
		return nil
	}
	if err := st.SetRole(ctx, acc.ID, account.RoleAdmin); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	log.Info("promoted account to admin", "username", username, "id", acc.ID)
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
