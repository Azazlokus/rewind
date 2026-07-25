package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"arena/internal/protocol"
	"arena/internal/transport"
)

var (
	// ErrRoomFull возвращается из Join, когда комната заполнена.
	ErrRoomFull = errors.New("game: room is full")
	// ErrRoomClosed возвращается из Join, когда цикл комнаты остановлен.
	ErrRoomClosed = errors.New("game: room is closed")
)

// Config настраивает комнату. Нулевое значение пригодно; у каждого поля есть
// значение по умолчанию.
type Config struct {
	// TickRate — частота симуляции в Гц. По умолчанию 30.
	TickRate int
	// SnapshotRate — как часто уходят снапшоты, в Гц; может быть любым от 1 до
	// TickRate, необязательно делителем (делитель частоты по Брезенхэму
	// раскидывает снапшоты равномерно). По умолчанию равно TickRate. Итерация 2
	// ставит 20 Гц при тикрейте 30 и даёт клиентской интерполяции скрыть разрыв.
	SnapshotRate int
	// MaxPlayers ограничивает комнату. По умолчанию 64.
	MaxPlayers int
	// InboxSize — глубина очереди событий комнаты. По умолчанию 1024.
	InboxSize int
	// SessionQueue — глубина исходящей очереди одного клиента. Намеренно мала:
	// снапшоты в очереди — устаревшие снапшоты. По умолчанию 8.
	SessionQueue int
	// MaxBacklog — сколько подряд снапшотов клиент может пропустить до
	// отключения. По умолчанию 30, т.е. около секунды на 30 Гц.
	MaxBacklog int
	// Seed кормит генератор мира. Равные seed и равные вводы дают равные миры.
	Seed int64
	// Clock по умолчанию RealClock. Тесты передают ManualClock.
	Clock Clock
	// Metrics по умолчанию NopRecorder.
	Metrics Recorder
	// Logger по умолчанию — отбрасывающий логгер.
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.TickRate <= 0 {
		c.TickRate = 30
	}
	if c.SnapshotRate <= 0 || c.SnapshotRate > c.TickRate {
		c.SnapshotRate = c.TickRate
	}
	if c.MaxPlayers <= 0 {
		c.MaxPlayers = 64
	}
	if c.InboxSize <= 0 {
		c.InboxSize = 1024
	}
	if c.SessionQueue <= 0 {
		c.SessionQueue = 8
	}
	if c.MaxBacklog <= 0 {
		c.MaxBacklog = 30
	}
	if c.Clock == nil {
		c.Clock = RealClock{}
	}
	if c.Metrics == nil {
		c.Metrics = NopRecorder{}
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// TickInterval — настенное время между двумя шагами симуляции.
func (c Config) TickInterval() time.Duration {
	return time.Second / time.Duration(c.TickRate)
}

// rateDivider — делитель частоты по Брезенхэму: выдаёт num тиков-«да» на каждые
// den тиков, раскидывая их равномерно. Так снапшоты 20 Гц получаются из тиков
// 30 Гц (2 из каждых 3), хотя 20 не делит 30 нацело.
type rateDivider struct {
	accum int // накопитель, увеличивается на num каждый тик
	num   int // SnapshotRate
	den   int // TickRate
}

// tick продвигает делитель на один тик и сообщает, пора ли слать снапшот.
func (d *rateDivider) tick() bool {
	d.accum += d.num
	if d.accum >= d.den {
		d.accum -= d.den
		return true
	}
	return false
}

// Room — один игровой экземпляр: мир, цикл с фиксированной частотой и набор
// сессий.
//
// Всё, что трогает мир, происходит на горутине цикла. Внешний мир общается с
// комнатой исключительно через события inbox — поэтому игровое состояние без
// мьютексов. Любой код, тянущийся во World из другой горутины, — это баг, а не
// оптимизация.
type Room struct {
	id  string
	cfg Config
	log *slog.Logger

	inbox chan event
	done  chan struct{}
	ready chan struct{}

	// Принадлежит горутине цикла.
	world    *World
	sessions map[PlayerID]*Session
	snap     rateDivider       // делитель частоты снапшотов
	entities []protocol.Entity // переиспользуемый черновик снапшота
	kicked   []PlayerID        // переиспользуемый пер-тик список сессий на удаление

	// Наблюдаемо из других горутин.
	players    atomic.Int32
	inputDrops atomic.Uint64
	started    atomic.Bool
}

// NewRoom создаёт комнату. Цикл не запускает — зовите Run.
func NewRoom(id string, cfg Config) *Room {
	cfg = cfg.withDefaults()
	return &Room{
		id:       id,
		cfg:      cfg,
		log:      cfg.Logger.With("room", id),
		inbox:    make(chan event, cfg.InboxSize),
		done:     make(chan struct{}),
		ready:    make(chan struct{}),
		world:    NewWorld(cfg.Seed),
		sessions: make(map[PlayerID]*Session),
		snap:     rateDivider{num: cfg.SnapshotRate, den: cfg.TickRate},
		entities: make([]protocol.Entity, 0, cfg.MaxPlayers),
	}
}

// ID — идентификатор комнаты.
func (r *Room) ID() string { return r.id }

// Config возвращает эффективную конфигурацию комнаты, с применёнными значениями
// по умолчанию. Это read-only состояние, зафиксированное при создании, поэтому
// его безопасно читать из любой горутины; тесты используют его, чтобы добраться
// до внедрённого Clock.
func (r *Room) Config() Config { return r.cfg }

// Players — текущее число игроков. Это мгновенное значение для hub и метрик,
// никогда не основа игровой логики.
func (r *Room) Players() int { return int(r.players.Load()) }

// Done закрывается, когда цикл остановлен и все сессии освобождены.
func (r *Room) Done() <-chan struct{} { return r.done }

// Ready закрывается, когда цикл запущен и тикер зарегистрирован в Clock. Тесты
// ждут его, прежде чем гнать ManualClock — иначе ранние Advance теряются, пока
// тикер ещё не создан. В проде (RealClock) не используется.
func (r *Room) Ready() <-chan struct{} { return r.ready }

// DroppedInputs считает клиентские команды, отброшенные из-за переполнения inbox.
func (r *Room) DroppedInputs() uint64 { return r.inputDrops.Load() }

// Run крутит комнату, пока ctx не отменится. Он владеет миром всё своё время
// жизни и возвращается только после того, как текущий тик завершён и все сессии
// закрыты.
func (r *Room) Run(ctx context.Context) {
	if !r.started.Swap(true) {
		defer close(r.done)
	} else {
		panic("game: room " + r.id + " started twice")
	}

	interval := r.cfg.TickInterval()
	dt := float32(interval.Seconds())
	ticker := r.cfg.Clock.NewTicker(interval)
	defer ticker.Stop()
	close(r.ready) // тикер зарегистрирован — можно гнать ManualClock

	r.log.Info("room started", "tick_rate", r.cfg.TickRate, "snapshot_rate", r.cfg.SnapshotRate)
	for {
		select {
		case <-ctx.Done():
			// Текущий тик, если он был, уже вернулся: shutdown никогда не
			// прерывает полупросчитанный мир.
			r.shutdown()
			r.log.Info("room stopped", "tick", r.world.Tick)
			return
		case <-ticker.C():
			r.tick(dt)
		}
	}
}

// Join регистрирует клиента и возвращает его сессию. Вызывающий владеет
// возвращённой сессией и обязан вызвать на ней Run.
func (r *Room) Join(ctx context.Context, conn transport.Conn, name string) (*Session, error) {
	req := &joinReq{conn: conn, name: name, reply: make(chan joinResult, 1)}
	if err := r.post(ctx, event{kind: evJoin, join: req}); err != nil {
		return nil, err
	}
	select {
	case res := <-req.reply:
		if res.err != nil {
			return nil, res.err
		}
		return res.sess, nil
	case <-r.done:
		return nil, ErrRoomClosed
	case <-ctx.Done():
		return nil, fmt.Errorf("game: join %s: %w", r.id, ctx.Err())
	}
}

// State возвращает копию текущего состояния мира. Отвечает горутина цикла,
// поэтому это одновременно и без гонок, и точка синхронизации: когда State
// вернулся, каждое событие, посланное до него, уже применено.
func (r *Room) State(ctx context.Context) (protocol.Snapshot, error) {
	reply := make(chan protocol.Snapshot, 1)
	if err := r.post(ctx, event{kind: evState, state: reply}); err != nil {
		return protocol.Snapshot{}, err
	}
	select {
	case s := <-reply:
		return s, nil
	case <-r.done:
		return protocol.Snapshot{}, ErrRoomClosed
	case <-ctx.Done():
		return protocol.Snapshot{}, fmt.Errorf("game: state %s: %w", r.id, ctx.Err())
	}
}

// leave просит комнату освободить игрока. Идемпотентно.
func (r *Room) leave(ctx context.Context, id PlayerID) {
	// Неудачный post означает, что комната уже ушла или выключается, и в этом
	// случае она сама освобождает свои сессии.
	_ = r.post(ctx, event{kind: evLeave, id: id})
}

// input передаёт клиентскую команду. Команды по своей природе теряемы: если
// комната настолько отстала, что её inbox полон, дропнуть эту команду лучше, чем
// блокировать read pump — через ~16 мс придёт новая.
func (r *Room) input(_ context.Context, id PlayerID, in protocol.Input) {
	select {
	case r.inbox <- event{kind: evInput, id: id, input: in}:
	default:
		r.inputDrops.Add(1)
	}
}

// post ставит событие в очередь, сдаваясь, если вызывающий или комната уходят.
func (r *Room) post(ctx context.Context, ev event) error {
	select {
	case r.inbox <- ev:
		return nil
	case <-r.done:
		return ErrRoomClosed
	case <-ctx.Done():
		return fmt.Errorf("game: post to %s: %w", r.id, ctx.Err())
	}
}

// tick — один шаг симуляции: применить всё пришедшее, продвинуть мир, затем
// поговорить с клиентами.
func (r *Room) tick(dt float32) {
	start := r.cfg.Clock.Now()

	r.drainInbox()
	r.world.Step(dt)
	r.dispatchEvents()
	if r.snap.tick() {
		r.broadcast()
	}
	r.dropLaggards()

	r.cfg.Metrics.TickDuration(r.cfg.Clock.Now().Sub(start))
	r.cfg.Metrics.InboxDepth(len(r.inbox))
}

// drainInbox применяет каждое событие из очереди. Ограничение гарантирует, что
// цикл дойдёт до шага симуляции, даже пока клиенты продолжают слать.
func (r *Room) drainInbox() {
	for range cap(r.inbox) {
		select {
		case ev := <-r.inbox:
			r.handle(ev)
		default:
			return
		}
	}
}

func (r *Room) handle(ev event) {
	switch ev.kind {
	case evJoin:
		r.handleJoin(ev.join)
	case evLeave:
		r.removeSession(ev.id, "left")
	case evInput:
		r.world.EnqueueInput(ev.id, ev.input)
	case evState:
		snap := protocol.Snapshot{Tick: r.world.Tick}
		snap.Entities = r.world.AppendEntities(nil)
		ev.state <- snap
	}
}

func (r *Room) handleJoin(req *joinReq) {
	if len(r.sessions) >= r.cfg.MaxPlayers {
		req.reply <- joinResult{err: ErrRoomFull}
		return
	}
	p, err := r.world.AddPlayer(req.name)
	if err != nil {
		req.reply <- joinResult{err: fmt.Errorf("game: join %s: %w", r.id, err)}
		return
	}
	s := newSession(r, p.ID, req.name, req.conn)

	ack, err := protocol.AppendJoinAck(nil, protocol.JoinAck{YourID: uint16(p.ID), Tick: r.world.Tick})
	if err != nil {
		r.world.RemovePlayer(p.ID)
		req.reply <- joinResult{err: fmt.Errorf("game: encode join ack: %w", err)}
		return
	}

	r.sessions[p.ID] = s
	r.setPlayerCount()
	s.enqueueReliable(ack)

	r.log.Info("player joined", "player", p.ID, "name", req.name, "addr", req.conn.RemoteAddr())
	req.reply <- joinResult{sess: s}
}

// broadcast шлёт полный мир каждому клиенту. Итерация 6 заменит это на interest
// management по вьюпорту и дельта-кодирование.
func (r *Room) broadcast() {
	r.entities = r.world.AppendEntities(r.entities[:0])
	if len(r.entities) > protocol.MaxEntities {
		// Невозможно, пока MaxPlayers ниже лимита провода; страховка не даёт
		// будущему типу сущности молча испортить формат.
		r.entities = r.entities[:protocol.MaxEntities]
	}
	snap := protocol.Snapshot{Tick: r.world.Tick, Entities: r.entities}

	r.world.Each(func(p *Player) {
		s := r.sessions[p.ID]
		if s == nil {
			return
		}
		// Номер подтверждённого ввода свой для каждого получателя, поэтому
		// каждый клиент получает своё кодирование тех же сущностей.
		snap.LastProcessedSeq = p.LastProcessedSeq
		// Кодек уже zero-alloc, но буфер здесь всё равно новый на клиента: он
		// отдаётся горутине-писателю, поэтому переиспользовать его тут нельзя.
		// Пул этих буферов — в итерации 6 (нагрузка), где на 200 игроках эта
		// аллокация начинает стоить; на текущих масштабах она незаметна.
		buf, err := protocol.AppendSnapshot(nil, &snap)
		if err != nil {
			r.log.Error("encode snapshot", "player", p.ID, "err", err)
			return
		}
		if s.enqueueSnapshot(buf) {
			r.cfg.Metrics.SnapshotBytes(len(buf))
		}
	})
}

// dispatchEvents разбирает reliable-события боя, накопленные Step, в
// protocol-сообщения и рассылает их: Hit — участникам, Death/Spawn — всем. Эти
// сообщения недропаемы (очередь reliable): клиент, который их не принимает,
// помечается на удаление в enqueueReliable, а не теряет событие.
func (r *Room) dispatchEvents() {
	for _, ev := range r.world.Events() {
		switch ev.Kind {
		case EventHit:
			buf, err := protocol.AppendHit(nil, protocol.Hit{
				Attacker: uint16(ev.Attacker), Victim: uint16(ev.Target),
				Damage: ev.Damage, VictimHP: ev.HP,
			})
			if err != nil {
				r.log.Error("encode hit", "err", err)
				continue
			}
			r.reliableTo(ev.Attacker, buf)
			if ev.Target != ev.Attacker {
				r.reliableTo(ev.Target, buf)
			}
		case EventDeath:
			buf, err := protocol.AppendDeath(nil, protocol.Death{
				Victim: uint16(ev.Target), Killer: uint16(ev.Attacker),
			})
			if err != nil {
				r.log.Error("encode death", "err", err)
				continue
			}
			r.reliableAll(buf)
		case EventSpawn:
			buf, err := protocol.AppendSpawn(nil, protocol.Spawn{
				ID: uint16(ev.Target), X: ev.X, Y: ev.Y,
			})
			if err != nil {
				r.log.Error("encode spawn", "err", err)
				continue
			}
			r.reliableAll(buf)
		}
	}
}

// reliableTo ставит reliable-сообщение в очередь одной сессии, если она есть.
func (r *Room) reliableTo(id PlayerID, buf []byte) {
	if s := r.sessions[id]; s != nil {
		s.enqueueReliable(buf)
	}
}

// reliableAll рассылает reliable-сообщение всем сессиям. Один и тот же буфер
// читается писателями каждой сессии и не мутируется — делить его безопасно. Обход
// map r.sessions неупорядочен намеренно: это сеть, а не симуляция — порядок
// доставки между разными сокетами не наблюдаем и на Checksum/реплеи не влияет
// (порядок сообщений одному получателю задаёт упорядоченный w.Events()).
func (r *Room) reliableAll(buf []byte) {
	for _, s := range r.sessions {
		s.enqueueReliable(buf)
	}
}

// dropLaggards отключает клиентов, отставших слишком сильно. Сам цикл их никогда
// не ждёт: он лишь решает, что их больше нет.
func (r *Room) dropLaggards() {
	r.kicked = r.kicked[:0]
	for id, s := range r.sessions {
		if s.lagging(r.cfg.MaxBacklog) {
			r.kicked = append(r.kicked, id)
		}
	}
	for _, id := range r.kicked {
		r.removeSession(id, "too slow")
	}
}

// removeSession освобождает игрока и закрывает его исходящие очереди. Комната —
// единственный отправитель в эти каналы, поэтому она же — единственная, кому
// можно их закрыть, и это завершает write pump сессии.
func (r *Room) removeSession(id PlayerID, reason string) {
	s, ok := r.sessions[id]
	if !ok {
		return
	}
	delete(r.sessions, id)
	r.world.RemovePlayer(id)
	// Комната — единственный отправитель в обе очереди, поэтому она же их и
	// закрывает; это завершает write pump сессии.
	close(s.reliable)
	close(s.snapshots)
	r.setPlayerCount()
	r.log.Info("player left", "player", id, "name", s.name, "reason", reason, "dropped_snapshots", s.dropped)
}

// shutdown освобождает каждую сессию. Сессии замечают это по закрытой очереди,
// останавливают свои pump'ы и закрывают соединения.
func (r *Room) shutdown() {
	for id := range r.sessions {
		r.removeSession(id, "server shutdown")
	}
}

func (r *Room) setPlayerCount() {
	n := len(r.sessions)
	r.players.Store(int32(n))
	r.cfg.Metrics.ConnectedPlayers(n)
}

// event — всё, что может дойти до комнаты снаружи. Это помеченная структура, а не
// интерфейс, чтобы отправка ввода ничего не аллоцировала.
type event struct {
	kind  eventKind
	id    PlayerID
	input protocol.Input
	join  *joinReq
	state chan protocol.Snapshot
}

type eventKind uint8

const (
	evJoin eventKind = iota + 1
	evLeave
	evInput
	evState
)

type joinReq struct {
	conn  transport.Conn
	name  string
	reply chan joinResult
}

type joinResult struct {
	sess *Session
	err  error
}
