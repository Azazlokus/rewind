// Пакет botfill наполняет комнаты ИИ-ботами, чтобы игрок, зашедший в одиночку, не
// оказался один: пока в комнате есть хотя бы один живой человек, наполнитель держит
// в ней суммарно Target игроков, добавляя ботов и снимая их, когда приходят люди или
// комната пустеет.
//
// Боты — это ОБЫЧНЫЕ клиенты: наполнитель подключает их через transport.Pipe и
// room.Join (ровно как cmd/loadtest), гоняет автопилот из internal/bot и НИКОГДА не
// трогает мир комнаты. Поэтому пакет — потребитель шва рядом с hub, а не часть
// симуляции: internal/game его не импортирует (стрелка botfill → game/bot/transport).
//
// Владение горутинами: наполнитель владеет одной горутиной reconcile-цикла и, на
// каждого бота, тремя — сессия (серверный конец Pipe), Drain и Autopilot (клиентский
// конец). Все учтены в wg; Run возвращается по отмене ctx, Wait дожидается всех.
package botfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	rand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"arena/internal/bot"
	"arena/internal/game"
	"arena/internal/transport"
)

// pipeDepth — глубина in-process Pipe бота (как в cmd/loadtest).
const pipeDepth = 64

// defaultInterval — как часто наполнитель сверяет население комнат.
const defaultInterval = 500 * time.Millisecond

// RoomProvider отдаёт текущие комнаты. Его реализует *hub.Hub (метод Rooms).
type RoomProvider interface {
	Rooms() []*game.Room
}

// Metrics — необязательный приёмник числа активных ботов (реализует *metrics.Metrics).
type Metrics interface {
	ActiveBots(int)
}

// Config настраивает наполнитель.
type Config struct {
	// Target — желаемое суммарное число игроков (люди + боты) в комнате, где есть
	// хотя бы один человек. 0 — наполнитель выключен.
	Target int
	// MaxPlayers — потолок игроков в комнате: боты никогда не переполняют его и
	// уступают места людям.
	MaxPlayers int
	// Seed делает поведение ботов воспроизводимым между запусками.
	Seed int64
	// Interval — период сверки населения (по умолчанию 500 мс).
	Interval time.Duration
	// Logger по умолчанию — отбрасывающий.
	Logger *slog.Logger
	// Metrics — необязательный гейдж активных ботов.
	Metrics Metrics
}

// Filler держит комнаты населёнными ботами.
type Filler struct {
	provider RoomProvider
	cfg      Config
	log      *slog.Logger

	// Поля ниже принадлежат горутине reconcile-цикла и без синхронизации не
	// читаются/пишутся никем другим.
	bots  map[*game.Room][]*botHandle // боты, созданные наполнителем, по комнатам
	nextN int                         // счётчик уникальных имён и rng-потоков ботов

	active atomic.Int32   // последнее посчитанное число активных ботов (для метрик/тестов)
	wg     sync.WaitGroup // все горутины наполнителя: цикл + по три на бота
}

// botHandle — один бот под управлением наполнителя.
type botHandle struct {
	cancel   context.CancelFunc // отменяет контекст бота; вызывается defer'ом сессии по её завершении
	client   *bot.Client        // клиентский конец Pipe; Close снимает игрока с комнаты
	done     chan struct{}      // закрывается, когда сессия бота завершилась (игрок ушёл)
	stopping bool               // наполнитель уже попросил бота уйти — не считать «удерживаемым»
}

// New собирает наполнитель. Ничего не запускает — зовите Run.
func New(provider RoomProvider, cfg Config) *Filler {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Filler{
		provider: provider,
		cfg:      cfg,
		log:      cfg.Logger,
		bots:     make(map[*game.Room][]*botHandle),
	}
}

// Run крутит reconcile-цикл, пока не отменится ctx. При выключении (Target<=0) сразу
// возвращается — ни одной горутины не заводит. Все боты гасятся отменой ctx; Wait
// дожидается их завершения.
func (f *Filler) Run(ctx context.Context) {
	if f.cfg.Target <= 0 {
		return
	}
	f.log.Info("bot filler started", "target", f.cfg.Target, "interval", f.cfg.Interval)
	tk := time.NewTicker(f.cfg.Interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			// Отмена ctx каскадом гасит все контексты ботов (они дочерние к ctx):
			// Drain/Autopilot и сессии завершаются, комнаты освобождают игроков.
			// Wait дожидается их — здесь ничего закрывать не нужно.
			return
		case <-tk.C:
			f.reconcile(ctx)
		}
	}
}

// Wait дожидается завершения всех горутин ботов. Зовите ПОСЛЕ возврата Run (в проводке —
// через отдельный fillerDone): тогда reconcile-цикл больше не вызывает wg.Add, и гонки
// Add-во-время-Wait нет. Отмена ctx каскадом гасит контексты ботов, поэтому горутины и
// завершатся.
func (f *Filler) Wait() { f.wg.Wait() }

// ActiveBots — последнее посчитанное число активных ботов (для метрик и тестов).
func (f *Filler) ActiveBots() int { return int(f.active.Load()) }

// reconcile сверяет население каждой комнаты с целью и добавляет/снимает ботов.
// Зовётся только горутиной цикла, поэтому f.bots/f.nextN без синхронизации.
func (f *Filler) reconcile(ctx context.Context) {
	present := make(map[*game.Room]struct{})
	active := 0
	for _, room := range f.provider.Rooms() {
		present[room] = struct{}{}
		active += f.reconcileRoom(ctx, room)
	}
	// Комнаты, исчезнувшие из провайдера (остановлены), осушаем целиком.
	for room := range f.bots {
		if _, ok := present[room]; !ok {
			f.drainRoom(room)
		}
	}

	f.active.Store(int32(active))
	if f.cfg.Metrics != nil {
		f.cfg.Metrics.ActiveBots(active)
	}
}

// reconcileRoom приводит число ботов одной живой комнаты к цели и возвращает, сколько
// ботов в ней удерживается (не помечены на снятие).
func (f *Filler) reconcileRoom(ctx context.Context, room *game.Room) int {
	// Разделяем боты на живые (сессия работает) и завершённые (done закрыт).
	handles := f.bots[room]
	var done, live []*botHandle
	for _, h := range handles {
		select {
		case <-h.done:
			done = append(done, h)
		default:
			live = append(live, h)
		}
	}
	// Барьер против «фантомных людей»: сессия постит leave ДО закрытия done (мы уже
	// увидели done), поэтому State — точка синхронизации — гарантирует, что leave
	// завершённых ботов обработаны комнатой прежде, чем мы прочитаем Players() и
	// выбросим их из списка. Иначе на один тик они бы выглядели людьми, и наполнитель
	// вечно добавлял бы им замену.
	if len(done) > 0 {
		_, _ = room.State(ctx)
	}
	if len(live) == 0 {
		delete(f.bots, room)
	} else {
		f.bots[room] = live
	}

	// Люди = Players() − наши живые боты (комната их всех считает). Ботов держим
	// ровно до Target, уступая людям и не переполняя комнату.
	want := targetBots(room.Players(), len(live), f.cfg.Target, f.cfg.MaxPlayers)
	keeping := 0
	for _, h := range live {
		if !h.stopping {
			keeping++
		}
	}
	for keeping < want {
		h := f.addBot(ctx, room)
		if h == nil {
			break // комната полна/закрыта — не долбим в этот тик, попробуем позже
		}
		f.bots[room] = append(f.bots[room], h)
		keeping++
	}
	for keeping > want {
		if !f.stopNewest(room) {
			break
		}
		keeping--
	}
	return keeping
}

// drainRoom снимает всех ботов остановленной комнаты и подчищает её из карты. Барьер
// не нужен: остановленная комната уже закрыла свои сессии, Players() не читаем.
// Оборонительная ветка: сейчас hub комнаты не удаляет, поэтому она срабатывает лишь на
// shutdown; корректна и на случай, если появится реапинг пустых комнат.
func (f *Filler) drainRoom(room *game.Room) {
	var live []*botHandle
	for _, h := range f.bots[room] {
		select {
		case <-h.done:
			// сессия завершилась — забываем бота
		default:
			if !h.stopping {
				h.stopping = true
				h.stop()
			}
			live = append(live, h)
		}
	}
	if len(live) == 0 {
		delete(f.bots, room)
	} else {
		f.bots[room] = live
	}
}

// stopNewest помечает и гасит самого свежего не-снимаемого бота комнаты. Свежий, а не
// старый, чтобы уступать место людям без дёрганья давно игравших ботов. Возвращает
// false, если снимать некого.
func (f *Filler) stopNewest(room *game.Room) bool {
	handles := f.bots[room]
	for i := len(handles) - 1; i >= 0; i-- {
		if h := handles[i]; !h.stopping {
			h.stopping = true
			h.stop()
			return true
		}
	}
	return false
}

// addBot подключает одного бота к комнате через Pipe: серверный конец идёт в
// room.Join (комната присоединяет игрока и ставит JoinAck), клиентский — в bot.Attach.
// Возвращает nil, если комната полна или закрыта. Зовётся только горутиной цикла.
func (f *Filler) addBot(ctx context.Context, room *game.Room) *botHandle {
	f.nextN++
	n := f.nextN
	name := fmt.Sprintf("AI-%d", n)

	server, client := transport.Pipe(pipeDepth)
	sess, err := room.Join(ctx, server, name, 0, false) // боты — гости (accountID 0), не наблюдатели
	if err != nil {
		_ = server.Close("join failed")
		_ = client.Close("join failed")
		f.log.Debug("bot join failed", "room", room.ID(), "err", err)
		return nil
	}

	botCtx, cancel := context.WithCancel(ctx)
	h := &botHandle{cancel: cancel, done: make(chan struct{})}

	// Горутина сессии (серверный конец). Внутри Run комната получает leave, пока botCtx
	// ещё жив (cancel — в defer, ПОСЛЕ возврата Run), поэтому снятие бота всегда реально
	// удаляет игрока. done закрывается по завершении — по нему reconcile понимает, что
	// игрок ушёл. Ошибку, кроме обычного дисконнекта и отмены, показываем.
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		defer close(h.done)
		defer h.cancel()
		if err := sess.Run(botCtx); err != nil && !errors.Is(err, transport.ErrClosed) && botCtx.Err() == nil {
			f.log.Debug("bot session ended", "room", room.ID(), "name", name, "err", err)
		}
	}()

	// Клиентский конец: ждём JoinAck. Сессия уже запущена, так что подтверждение придёт.
	c, err := bot.Attach(botCtx, client)
	if err != nil {
		_ = client.Close("attach failed") // завершит сессию (её defer погасит botCtx)
		f.log.Debug("bot attach failed", "room", room.ID(), "err", err)
		return nil
	}
	h.client = c

	// Автопилот + вычёрпывание снапшотов — клиентские горутины бота.
	rng := rand.New(rand.NewPCG(uint64(f.cfg.Seed), uint64(n)))
	f.wg.Add(2)
	go func() { defer f.wg.Done(); bot.Drain(botCtx, c) }()
	go func() { defer f.wg.Done(); bot.Autopilot(botCtx, c, rng) }()

	f.log.Debug("bot added", "room", room.ID(), "name", name)
	return h
}

// stop снимает бота: закрывает клиентский конец Pipe. Серверная сессия получает
// ErrClosed и внутри Run — пока её контекст ещё жив — постит leave, поэтому комната
// надёжно удаляет игрока; затем сессия возвращается, и её defer гасит контекст
// (Drain/Autopilot тоже завершаются). Контекст здесь НЕ отменяем: отмена до постинга
// leave гонялась бы с ним и могла оставить игрока в комнате.
func (h *botHandle) stop() {
	if h.client != nil {
		_ = h.client.Close()
	}
}

// targetBots — сколько ботов держать в комнате. Чистая функция (легко тестируется):
//   - players — текущее население (люди + боты),
//   - currentBots — сколько из них наши боты,
//   - target — желаемое суммарное население, maxPlayers — потолок комнаты.
//
// Люди = players − currentBots. Ботов держим ровно столько, чтобы дотянуть до target,
// но не переполняя комнату; если людей нет или наполнитель выключен — ноль (пустую
// комнату ботами не оживляем).
func targetBots(players, currentBots, target, maxPlayers int) int {
	humans := players - currentBots
	if humans <= 0 || target <= 0 {
		return 0
	}
	want := target - humans
	if want < 0 {
		want = 0
	}
	if humans+want > maxPlayers {
		want = maxPlayers - humans
	}
	if want < 0 {
		want = 0
	}
	return want
}
