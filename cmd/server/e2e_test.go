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
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"arena/internal/account"
	"arena/internal/bot"
	"arena/internal/botfill"
	"arena/internal/game"
	"arena/internal/hub"
	"arena/internal/metrics"
	"arena/internal/protocol"
	"arena/internal/ratelimit"
	"arena/internal/store"
	"arena/internal/transport"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func startServer(t *testing.T) (url string) {
	url, _, _ = startServerCore(t)
	return url
}

// startServerCore поднимает настоящий сервер и отдаёт также store и сервис аккаунтов —
// нужны тестам, которым надо готовить аккаунты/баны (итер. 39).
func startServerCore(t *testing.T) (url string, st store.Store, accounts *account.Service) {
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
	log := slog.New(slog.DiscardHandler)
	cfg := serverConfig{
		JoinTimeout:    2 * time.Second,
		AllowAllOrigin: true,
	}
	// Боты подключаются гостями (без токена), но шлюзу нужен сервис аккаунтов для
	// проверки токена; поднимаем поверх in-memory SQLite.
	st, err := store.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	accounts = account.NewService(st, []byte("e2e-secret-0123456789"), time.Hour, 24*time.Hour)
	gw := newGateway(h, accounts, log, cfg)

	mux := http.NewServeMux()
	mux.Handle("/ws", gw)
	mux.Handle("/rtc", newRTCGateway(gw, log, cfg)) // WebRTC-транспорт (итерация 11)
	srv := httptest.NewServer(mux)

	t.Cleanup(func() {
		srv.Close()
		cancel()
		h.Wait()
	})
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws", st, accounts
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

// TestE2EWebRTCMovement прогоняет по сети путь WebRTC (итерация 11): бот
// подключается через /rtc (WS-сигналинг → DataChannel), двигается вправо и
// наблюдает в своих же снапшотах, что его X растёт. Это проверяет весь стек
// нового транспорта — сигналинг, ICE/DTLS/SCTP по host-кандидатам, DataChannel —
// плюс общий session-путь, тот же, что у WebSocket.
func TestE2EWebRTCMovement(t *testing.T) {
	url := startServer(t)
	rtcURL := strings.TrimSuffix(url, "/ws") + "/rtc"
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	mover, err := bot.DialWebRTC(ctx, rtcURL, "rtcmover")
	if err != nil {
		t.Fatalf("dial webrtc: %v", err)
	}
	defer mover.Close()

	startX, ok := waitForEntity(ctx, t, mover, mover.ID())
	if !ok {
		t.Fatal("mover never saw itself spawn over the data channel")
	}

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

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := mover.ReadSnapshot(ctx)
		if err != nil {
			t.Fatalf("read snapshot: %v", err)
		}
		if e, ok := findEntity(snap, mover.ID()); ok && e.X > startX+50 {
			return // движение прошло сквозь WebRTC DataChannel
		}
	}
	t.Fatal("mover never observed itself move right over the data channel")
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

// TestE2ECombatKillsAndRespawns прогоняет сценарий приёмки итерации 5 по сети:
// join -> move -> shoot -> death -> respawn. Стрелок наводится на неподвижную
// жертву, при необходимости подходит в радиус и стреляет; мы наблюдаем через его
// снапшоты, как HP жертвы падает, затем она исчезает (смерть) и снова появляется
// (респаун).
func TestE2ECombatKillsAndRespawns(t *testing.T) {
	url := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	shooter, err := bot.Dial(ctx, url, "shooter")
	if err != nil {
		t.Fatalf("dial shooter: %v", err)
	}
	defer shooter.Close()
	victim, err := bot.Dial(ctx, url, "victim")
	if err != nil {
		t.Fatalf("dial victim: %v", err)
	}
	defer victim.Close()

	// Жертва стоит на месте, но её очереди надо вычерпывать, иначе комната сочтёт
	// её отставшей и отключит.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if _, err := victim.ReadSnapshot(ctx); err != nil {
				return
			}
		}
	}()

	// Стрелок шлёт ввод на 60 Гц из фоновой горутины (как настоящий клиент); цель
	// наводки/движения обновляет основной цикл по снапшотам (20 Гц). Read и Write
	// на одном соединении идут в разных горутинах — websocket это допускает. Поля
	// bot.Client у чтения и записи пересекаются только по ack (итерация 6B), и то
	// через atomic; остальные (seq, reconstructor) — раздельны по горутинам.
	var mu sync.Mutex
	var curBtn uint8
	var curAim uint16
	setCmd := func(b uint8, a uint16) { mu.Lock(); curBtn, curAim = b, a; mu.Unlock() }
	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(time.Second / 60)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				mu.Lock()
				b, a := curBtn, curAim
				mu.Unlock()
				if err := shooter.SendInput(ctx, b, a); err != nil {
					return
				}
			}
		}
	}()
	// Обе фоновые горутины завершаются по отмене ctx или закрытию conn; дожидаемся
	// их перед выходом из теста, чтобы у каждой был явный join (правило 2).
	defer func() { cancel(); wg.Wait() }()

	const inRange = 600
	var sawDamage, sawDeath bool
	deadline := time.Now().Add(36 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := shooter.ReadSnapshot(ctx)
		if err != nil {
			t.Fatalf("shooter read: %v", err)
		}
		me, meOK := findEntity(snap, shooter.ID())
		vic, vicOK := findEntity(snap, victim.ID())

		if vicOK && vic.HP < 100 {
			sawDamage = true
		}
		if !vicOK && sawDamage {
			sawDeath = true // получила урон, затем исчезла из снапшота
		}
		if vicOK && sawDeath {
			return // снова в снапшоте после смерти — респаун; сценарий пройден
		}

		switch {
		case !meOK || !vicOK:
			setCmd(0, 0) // жертва мертва/не видна — ждём
		default:
			dx, dy := vic.X-me.X, vic.Y-me.Y
			aim := protocol.AimFromRadians(math.Atan2(float64(dy), float64(dx)))
			if math.Hypot(float64(dx), float64(dy)) > inRange {
				setCmd(moveButtons(dx, dy), aim) // подходим на дистанцию
			} else {
				setCmd(protocol.BtnFire, aim) // в радиусе — стреляем
			}
		}
	}
	t.Fatalf("combat scenario incomplete: sawDamage=%v sawDeath=%v", sawDamage, sawDeath)
}

// TestE2EBotFillGivesLoneHumanCompany — headline-сценарий итерации 17: одинокий
// человек заходит по настоящему WS, наполнитель подсаживает к нему ботов до Target, а
// когда человек уходит — осушает комнату. Проверяет весь провод (hub → filler →
// room.Join через Pipe) на реальном сетевом пути человека.
func TestE2EBotFillGivesLoneHumanCompany(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mtr := metrics.New()
	h := hub.New(ctx, hub.Config{
		MaxRooms: 2,
		Room: game.Config{
			TickRate: 30, SnapshotRate: 20, MaxPlayers: 8, Seed: 1, Metrics: mtr,
		},
	})
	log := slog.New(slog.DiscardHandler)
	cfg := serverConfig{JoinTimeout: 2 * time.Second, AllowAllOrigin: true}
	st, err := store.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	accounts := account.NewService(st, []byte("e2e-secret-0123456789"), time.Hour, 24*time.Hour)
	gw := newGateway(h, accounts, log, cfg)

	filler := botfill.New(h, botfill.Config{
		Target: 4, MaxPlayers: 8, Seed: 1, Interval: 20 * time.Millisecond, Metrics: mtr,
	})
	go filler.Run(ctx)

	mux := http.NewServeMux()
	mux.Handle("/ws", gw)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		cancel()
		h.Wait()
		filler.Wait()
	})
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	// Одинокий человек заходит — комната должна дорасти до Target (человек + 3 бота).
	human, err := bot.Dial(ctx, url, "human")
	if err != nil {
		t.Fatalf("dial human: %v", err)
	}
	go bot.Drain(ctx, human) // вычёрпываем снапшоты, чтобы не отставать

	roomFilled := func() bool {
		rooms := h.Rooms()
		return len(rooms) == 1 && rooms[0].Players() == 4 && filler.ActiveBots() == 3
	}
	if !eventuallyTrue(5*time.Second, roomFilled) {
		t.Fatalf("filler did not fill room to target; rooms=%d", len(h.Rooms()))
	}

	// Человек уходит — наполнитель осушает комнату (пустую ботами не оживляем).
	_ = human.Close()
	roomDrained := func() bool {
		rooms := h.Rooms()
		return len(rooms) == 1 && rooms[0].Players() == 0 && filler.ActiveBots() == 0
	}
	if !eventuallyTrue(5*time.Second, roomDrained) {
		t.Fatalf("filler did not drain room after human left; bots=%d", filler.ActiveBots())
	}
}

// eventuallyTrue опрашивает cond до timeout.
func eventuallyTrue(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// moveButtons выбирает WASD-биты, ведущие к смещению (dx, dy). Мёртвая зона
// гасит дрожание у самой цели.
func moveButtons(dx, dy float32) uint8 {
	var b uint8
	if dx > 8 {
		b |= protocol.BtnRight
	} else if dx < -8 {
		b |= protocol.BtnLeft
	}
	if dy > 8 {
		b |= protocol.BtnDown
	} else if dy < -8 {
		b |= protocol.BtnUp
	}
	return b
}

// TestE2EBannedAccountRefused: забаненный аккаунт получает отказ на join (шлюз закрывает
// соединение до комнаты), а гость без токена по-прежнему заходит (итер. 39).
func TestE2EBannedAccountRefused(t *testing.T) {
	url, st, accounts := startServerCore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	id, toks, err := accounts.Register(ctx, "cheater", "password12", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := st.BanAccount(ctx, store.Ban{
		AccountID: id.AccountID, Reason: "aimbot", CreatedBy: id.AccountID, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("ban: %v", err)
	}

	// Забаненный: сырое соединение + Join с токеном → сервер закрывает без JoinAck.
	conn, err := transport.Dial(ctx, url, transport.WSOptions{WriteKind: transport.KindBinary})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	join, err := protocol.AppendJoin(nil, protocol.Join{Name: "cheater", Token: toks.Access})
	if err != nil {
		t.Fatalf("encode join: %v", err)
	}
	if err := conn.Write(ctx, join); err != nil {
		t.Fatalf("write join: %v", err)
	}
	readCtx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	defer rcancel()
	if _, err := conn.Read(readCtx); err == nil {
		t.Fatalf("banned account should be refused, but got a message")
	}
	_ = conn.Close("done")

	// Контроль: гость без токена по-прежнему заходит.
	guest, err := bot.Dial(ctx, url, "guest")
	if err != nil {
		t.Fatalf("guest join should still work: %v", err)
	}
	_ = guest.Close()
}

// TestE2EJoinConcurrencyCap: кап живых соединений на IP (итер. 33) отклоняет второе
// одновременное соединение того же клиента ещё на апгрейде; после ухода первого слот
// освобождается и новое соединение проходит. Настоящий WS-путь через joinGate.
func TestE2EJoinConcurrencyCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := hub.New(ctx, hub.Config{
		MaxRooms: 2,
		Room:     game.Config{TickRate: 30, SnapshotRate: 20, MaxPlayers: 8, Seed: 1, Metrics: metrics.New()},
	})
	log := slog.New(slog.DiscardHandler)
	cfg := serverConfig{JoinTimeout: 2 * time.Second, AllowAllOrigin: true}
	st, err := store.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	accounts := account.NewService(st, []byte("e2e-secret-0123456789"), time.Hour, 24*time.Hour)
	gw := newGateway(h, accounts, log, cfg)

	// Кап = 1 живое соединение на IP; рейт выключен (nil) — проверяем именно кап.
	gate := newJoinGate(gw, nil, ratelimit.NewConnLimiter(1), "", log, nil)
	mux := http.NewServeMux()
	mux.Handle("/ws", gate)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.Close()
		cancel()
		h.Wait()
	})
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"

	// Первый клиент занимает единственный слот и держит соединение живым.
	first, err := bot.Dial(ctx, url, "first")
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	go bot.Drain(ctx, first)

	// Второй клиент того же IP — кап исчерпан, апгрейд отклоняется (dial падает).
	if second, err := bot.Dial(ctx, url, "second"); err == nil {
		_ = second.Close()
		t.Fatal("second concurrent connection should have been refused by the cap")
	}

	// Первый уходит — слот освобождается (асинхронно), новое соединение снова проходит.
	_ = first.Close()
	reconnected := func() bool {
		c, err := bot.Dial(ctx, url, "third")
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}
	if !eventuallyTrue(5*time.Second, reconnected) {
		t.Fatal("connection should succeed after the first one releases its slot")
	}
}

// TestE2ETracingJoinSpan: с установленным SDK-провайдером join-рукопожатие
// зарегистрированного аккаунта порождает спан "game.join", а проверка бана под ним
// — дочерний SQL-спан (otelsql) в ТОМ ЖЕ трейсе. Проверяет control-plane-инструментовку
// итер. 34 на реальном WS: провод/симуляция/тик не трассируются.
func TestE2ETracingJoinSpan(t *testing.T) {
	// Ставим SDK-провайдер с in-memory экспортёром ДО открытия store (otelsql берёт
	// провайдер при Open). По завершении восстанавливаем прежний.
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	})

	url, _, accounts := startServerCore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Регистрируем аккаунт: join с его токеном заставит шлюз сходить в БД (IsBanned).
	_, toks, err := accounts.Register(ctx, "alice", "password12", "")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	conn, err := transport.Dial(ctx, url, transport.WSOptions{WriteKind: transport.KindBinary})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	join, err := protocol.AppendJoin(nil, protocol.Join{Name: "alice", Token: toks.Access})
	if err != nil {
		t.Fatalf("encode join: %v", err)
	}
	if err := conn.Write(ctx, join); err != nil {
		t.Fatalf("write join: %v", err)
	}
	// Дожидаемся первого сообщения (join прошёл), затем закрываем.
	readCtx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	defer rcancel()
	if _, err := conn.Read(readCtx); err != nil {
		t.Fatalf("expected a message after join: %v", err)
	}
	_ = conn.Close("done")

	// Спаны экспортируются синхронно, но serve завершает join-спан асинхронно —
	// опрашиваем: должен появиться "game.join" и дочерний SQL-спан в его трейсе.
	var joinTrace oteltrace.TraceID
	haveJoinAndDBChild := func() bool {
		spans := exp.GetSpans()
		joinTrace = oteltrace.TraceID{}
		for _, s := range spans {
			if s.Name == "game.join" {
				joinTrace = s.SpanContext.TraceID()
			}
		}
		if !joinTrace.IsValid() {
			return false
		}
		for _, s := range spans {
			if s.SpanContext.TraceID() != joinTrace {
				continue
			}
			for _, a := range s.Attributes {
				if a.Key == "db.system" {
					return true // SQL-спан под тем же трейсом, что и join
				}
			}
		}
		return false
	}
	if !eventuallyTrue(5*time.Second, haveJoinAndDBChild) {
		t.Fatalf("expected a 'game.join' span with a child db span in the same trace; got %d spans", len(exp.GetSpans()))
	}
}
