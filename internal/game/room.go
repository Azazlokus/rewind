package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
	// AOIRadius — половина стороны коробки interest management в мировых юнитах:
	// клиенту уходят только сущности в пределах ±AOIRadius по обеим осям от его
	// игрока. 0 (по умолчанию) — interest management выключен, комната шлёт полный
	// мир каждому (поведение итераций 1–5; на нём держатся существующие тесты).
	// Итерация 6.
	AOIRadius float32
	// Seed кормит генератор мира. Равные seed и равные вводы дают равные миры.
	Seed int64
	// TeamMode включает командный режим (итер. 23): 2 команды, дружественный огонь
	// выключен, счёт по командам. По умолчанию выключено (FFA). Фиксируется при
	// создании мира; реплей воспроизводит его через лог (v2).
	TeamMode bool
	// HillMode включает режим King of the Hill (итер. 29): захват центральной зоны,
	// победитель по очкам контроля. Совместим с TeamMode (контроль по командам) и с FFA.
	// Фиксируется при создании мира; реплей воспроизводит его через лог (v4).
	HillMode bool
	// DomMode включает режим доминации (итер. 30): захват нескольких контрольных точек,
	// победитель по сумме очков контроля. Совместим с TeamMode и с FFA. Фиксируется при
	// создании мира; реплей воспроизводит его через лог (v5).
	DomMode bool
	// CtfMode включает режим Capture the Flag (итер. 31): флаги на базах команд,
	// победитель по сумме захватов. ПОДРАЗУМЕВАЕТ TeamMode (комната включает оба вместе).
	// Фиксируется при создании мира; реплей воспроизводит его через лог (v6).
	CtfMode bool
	// RecordReplay включает запись лога реплея (seed + события со штампом тика).
	// По умолчанию выключено (без накладных расходов). Лог забирается через
	// Room.ReplayLog() после остановки комнаты. Итерация 7.
	RecordReplay bool
	// PersistSink — куда комната шлёт события для персиста: смерти (статистика) и
	// итоги матчей (история). Читает канал persister вне игры (internal/persist).
	// nil (по умолчанию) — персист выключен: тесты, loadtest и реплеи ничего не
	// шлют, симуляция от этого не меняется. Отправка неблокирующая (Room.sendPersist):
	// переполнение роняет сообщение, но никогда не тормозит тик. Итерация 14B.
	PersistSink chan<- PersistMsg
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

	// Наблюдатели (итер. 22): сессии без Player в мире. Живут отдельно от sessions —
	// не входят в счётчик игроков, не участвуют в симуляции/бое, получают полный мир
	// и reliable-события. Ключ — room-local счётчик nextSpecID (НЕ id сущности: в World
	// не попадает). specView — переиспользуемый отсортированный полный набор для них.
	spectators map[PlayerID]*Session
	nextSpecID PlayerID
	specView   []protocol.Entity

	// Interest management (итерация 6): сетка и переиспользуемые буферы AOI.
	// Всё принадлежит горутине цикла — как и world.
	grid aoiGrid
	cand []int32           // кандидаты из сетки на одного зрителя
	view []protocol.Entity // отфильтрованное AOI-подмножество на одного зрителя

	// Дельта-кодирование (итерация 6B): переиспользуемые буферы разницы на одного
	// зрителя. Тоже горутина цикла.
	changed []protocol.Entity // изменённые/новые сущности дельты
	masks   []uint8           // маски присутствующих полей, параллельно changed (итерация 9)
	removed []uint16          // id ушедших сущностей дельты

	// Матч (итерация 14): бродкаст табло событийный, не поллинг. matchDirty
	// взводится, когда табло реально изменилось (смена фазы, смерть, вход/выход
	// игрока); таймер клиент отсчитывает локально между обновлениями. Всё — горутина
	// цикла.
	matchScores    []MatchScore        // черновик табло из world.MatchState
	pmatch         protocol.MatchState // переиспользуемое proto-сообщение (Scores растёт по месту)
	lastMatchPhase matchPhase          // фаза на прошлом тике: смена → бродкаст
	matchDirty     bool                // табло изменилось с прошлого бродкаста

	// Пикапы (итерация 19): бродкаст тоже событийный. pickupsDirty взводится, когда
	// мир сообщил об изменении (World.PickupsDirty) или вошёл новый игрок; pickupBuf/
	// ppickups — переиспользуемые буферы кодирования. Всё — горутина цикла.
	pickupsDirty bool
	pickupBuf    []protocol.Pickup    // черновик активных пикапов из world.AppendPickups
	ppickups     protocol.PickupState // переиспользуемое proto-сообщение

	// Оружие (итер. 26): бродкаст тоже событийный. weaponsDirty взводится, когда мир
	// сообщил о смене оружия (World.WeaponsDirty) или вошёл новый игрок. weaponBuf/
	// pweapons — переиспользуемые буферы кодирования. Всё — горутина цикла.
	weaponsDirty bool
	weaponBuf    []protocol.WeaponInfo
	pweapons     protocol.WeaponState

	// Флаги CTF (итер. 31): бродкаст тоже событийный. flagsDirty взводится, когда мир
	// сообщил об изменении (World.FlagsDirty) или вошёл новый игрок. flagBuf/pflags —
	// переиспользуемые буферы кодирования. Всё — горутина цикла.
	flagsDirty bool
	flagBuf    []protocol.FlagInfo
	pflags     protocol.FlagState

	// Персист (итерация 14B), поля горутины цикла: sendPersist зовётся только из tick.
	persistDrops  uint64 // сколько persist-сообщений отброшено переполнением канала
	persistWarned bool   // Warn о дропах уже залогирован (не спамим на каждый дроп)

	// snapPool переиспользует буферы закодированных снапшотов (итерация 6C). Буфер
	// берётся в sendSnapshotTo (горутина цикла) и возвращается write pump'ом сессии
	// после записи. Безопасность переиспользования держится на контракте
	// transport.Conn.Write: он не удерживает срез после возврата (Pipe копирует, WS
	// пишет синхронно). Транспорт с асинхронной буферизацией записи (напр. будущий
	// WebRTC, отдающий управление до поглощения среза) потребует ревизии этого пула.
	// sync.Pool потокобезопасен для конкурентных Get/Put.
	snapPool sync.Pool

	// Наблюдаемо из других горутин.
	players    atomic.Int32
	specCount  atomic.Int32 // текущее число наблюдателей (итер. 22), для метрик/хаба
	inputDrops atomic.Uint64
	started    atomic.Bool
}

// NewRoom создаёт комнату. Цикл не запускает — зовите Run.
func NewRoom(id string, cfg Config) *Room {
	cfg = cfg.withDefaults()
	r := &Room{
		id:         id,
		cfg:        cfg,
		log:        cfg.Logger.With("room", id),
		inbox:      make(chan event, cfg.InboxSize),
		done:       make(chan struct{}),
		ready:      make(chan struct{}),
		world:      NewWorld(cfg.Seed),
		sessions:   make(map[PlayerID]*Session),
		spectators: make(map[PlayerID]*Session),
		nextSpecID: 1,
		snap:       rateDivider{num: cfg.SnapshotRate, den: cfg.TickRate},
		entities:   make([]protocol.Entity, 0, cfg.MaxPlayers),
	}
	// Пул хранит *[]byte, а не []byte: указатель кладётся в sync.Pool без боксинга
	// (Put([]byte) аллоцировал бы заголовок среза каждый раз). Стартовая ёмкость — с
	// запасом на заголовок + десятки сущностей вида.
	r.snapPool.New = func() any { b := make([]byte, 0, 512); return &b }
	if cfg.TeamMode {
		r.world.SetTeamMode(true) // до первого join и до записи реплея (итер. 23)
	}
	if cfg.HillMode {
		r.world.SetHillMode(true) // King of the Hill: до первого join и записи реплея (итер. 29)
	}
	if cfg.DomMode {
		r.world.SetDomMode(true) // доминация: до первого join и записи реплея (итер. 30)
	}
	if cfg.CtfMode {
		// CTF подразумевает командный режим — включаем оба до первого join (итер. 31).
		r.world.SetTeamMode(true)
		r.world.SetCtfMode(true)
	}
	if cfg.RecordReplay {
		r.world.EnableReplayRecording()
	}
	return r
}

// ReplayLog возвращает лог реплея комнаты или nil, если запись не включена
// (Config.RecordReplay). Читает состояние мира, поэтому безопасно звать ТОЛЬКО
// после остановки комнаты (<-Done()): до этого миром владеет горутина цикла.
func (r *Room) ReplayLog() *ReplayLog {
	return r.world.ReplayLog(r.cfg.TickRate)
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

// Spectators — текущее число наблюдателей (итер. 22). Мгновенное значение для
// метрик; наблюдатели не считаются игроками и в игровую логику не входят.
func (r *Room) Spectators() int { return int(r.specCount.Load()) }

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
// возвращённой сессией и обязан вызвать на ней Run. accountID (0 — гость)
// привязывает игрока к аккаунту для персиста статистики; шлюз добывает его из
// проверенного токена Join (итерация 14B).
func (r *Room) Join(ctx context.Context, conn transport.Conn, name string, accountID int64, spectator bool) (*Session, error) {
	req := &joinReq{conn: conn, name: name, accountID: accountID, spectator: spectator, reply: make(chan joinResult, 1)}
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

// leaveSpectator просит комнату освободить наблюдателя (итер. 22). Идемпотентно.
func (r *Room) leaveSpectator(ctx context.Context, id PlayerID) {
	_ = r.post(ctx, event{kind: evLeaveSpectator, id: id})
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
	r.broadcastMatchState()
	r.broadcastPickups()
	r.broadcastWeapons()
	r.broadcastFlags()
	if r.snap.tick() {
		r.broadcast()
	}
	r.dropLaggards()

	r.cfg.Metrics.TickDuration(r.cfg.Clock.Now().Sub(start))
	r.cfg.Metrics.InboxDepth(len(r.inbox))
	r.reportAntiCheat()
}

// reportAntiCheat сливает античит-счётчики мира за тик в метрики (итер. 25). Чтение и
// обнуление — на горутине комнаты (как весь mutating-доступ к World), поэтому без
// синхронизации; при тишине (все нули) в метрики ничего не идёт.
func (r *Room) reportAntiCheat() {
	counts := r.world.DrainAntiCheat()
	for kind, n := range counts {
		if n > 0 {
			r.cfg.Metrics.AntiCheat(AntiCheatKind(kind).String(), int(n))
		}
	}
	r.persistAntiCheat()
}

// persistAntiCheat шлёт persister привязанные к аккаунтам античит-события за тик (итер.
// 40): бэкенд копит их по игроку и при пороге автобанит. Как persistKill — неблокирующе
// (sendPersist), вне БД на горутине цикла; события редки (кулдаун-гейт), поэтому шлём по
// сообщению на событие без агрегации (проще и без обхода map). Гости уже отсеяны recordAC.
func (r *Room) persistAntiCheat() {
	// Сливаем ВСЕГДА (иначе буфер рос бы без sink); при nil-канале просто отбрасываем.
	events := r.world.DrainAntiCheatEvents()
	if r.cfg.PersistSink == nil {
		return
	}
	for _, e := range events {
		r.sendPersist(PersistMsg{
			Kind:             PersistAntiCheat,
			AntiCheatAccount: e.accountID,
			AntiCheatKind:    e.kind.String(),
			AntiCheatCount:   1,
		})
	}
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
	case evLeaveSpectator:
		r.removeSpectator(ev.id, "left")
	case evInput:
		r.world.EnqueueInput(ev.id, ev.input)
		// Клиент подтверждает последний реконструированный снапшот — против него
		// пойдёт дельта. Кламп по world.Tick — анти-чит: подтвердить тик из будущего
		// нельзя (клиент не мог реконструировать то, чего сервер ещё не досчитал), а
		// без клампа фейковый «будущий» ack завис бы, гоняя клиенту полные снапшоты.
		// Монотонно: устаревшее (переупорядоченное) подтверждение не откатывает базу.
		if s := r.sessions[ev.id]; s != nil {
			ack := ev.input.AckTick
			if ack > r.world.Tick {
				ack = r.world.Tick
			}
			if ack > s.ackTick {
				s.ackTick = ack
			}
		}
	case evState:
		snap := protocol.Snapshot{Tick: r.world.Tick}
		snap.Entities = r.world.AppendEntities(nil)
		ev.state <- snap
	}
}

func (r *Room) handleJoin(req *joinReq) {
	if req.spectator {
		r.handleSpectatorJoin(req)
		return
	}
	if len(r.sessions) >= r.cfg.MaxPlayers {
		req.reply <- joinResult{err: ErrRoomFull}
		return
	}
	p, err := r.world.AddPlayer(req.name)
	if err != nil {
		req.reply <- joinResult{err: fmt.Errorf("game: join %s: %w", r.id, err)}
		return
	}
	// Привязка к аккаунту — идентити-метаданные, не симуляция: ставим на игроке
	// после конструирования (в Checksum/реплей не входит). 0 оставляет гостя.
	p.AccountID = req.accountID
	s := newSession(r, p.ID, req.name, req.conn, false)

	ack, err := protocol.AppendJoinAck(nil, protocol.JoinAck{YourID: uint16(p.ID), Tick: r.world.Tick})
	if err != nil {
		r.world.RemovePlayer(p.ID)
		req.reply <- joinResult{err: fmt.Errorf("game: encode join ack: %w", err)}
		return
	}

	r.sessions[p.ID] = s
	r.setPlayerCount()
	s.enqueueReliable(ack)
	// Новый игрок = новая строка табло: взводим dirty, и broadcastMatchState в этом же
	// тике разошлёт актуальное состояние всем, включая новичка.
	r.matchDirty = true
	// Ему же нужно текущее состояние пикапов, оружия и флагов — разошлём в этом тике всем.
	r.pickupsDirty = true
	r.weaponsDirty = true
	r.flagsDirty = true

	r.log.Info("player joined", "player", p.ID, "name", req.name, "addr", req.conn.RemoteAddr())
	req.reply <- joinResult{sess: s}
}

// handleSpectatorJoin регистрирует наблюдателя (итер. 22): сессию без Player в мире.
// Ключ — room-local nextSpecID (не id сущности, в World не попадает); JoinAck несёт
// YourID == 0 — сигнал клиенту «своей сущности нет, ты наблюдаешь». Наблюдатель не
// участвует в симуляции/бое, только получает снапшоты (полный мир) и reliable-события.
func (r *Room) handleSpectatorJoin(req *joinReq) {
	if len(r.spectators) >= r.cfg.MaxPlayers {
		req.reply <- joinResult{err: ErrRoomFull}
		return
	}
	// Свободный ненулевой ключ: наблюдателей < MaxPlayers, поэтому поиск конечен.
	for r.spectators[r.nextSpecID] != nil || r.nextSpecID == 0 {
		r.nextSpecID++
	}
	id := r.nextSpecID
	r.nextSpecID++

	s := newSession(r, id, req.name, req.conn, true)
	ack, err := protocol.AppendJoinAck(nil, protocol.JoinAck{YourID: 0, Tick: r.world.Tick})
	if err != nil {
		req.reply <- joinResult{err: fmt.Errorf("game: encode join ack: %w", err)}
		return
	}
	r.spectators[id] = s
	r.specCount.Store(int32(len(r.spectators)))
	s.enqueueReliable(ack)
	// Новичку нужно текущее состояние матча, пикапов и оружия — разошлём всем.
	r.matchDirty = true
	r.pickupsDirty = true
	r.weaponsDirty = true

	r.log.Info("spectator joined", "spectator", id, "name", req.name, "addr", req.conn.RemoteAddr())
	req.reply <- joinResult{sess: s}
}

// broadcast рассылает состояние мира клиентам. При выключенном interest
// management (AOIRadius == 0) каждый получает полный мир; иначе — только сущности
// в своей окрестности. Поверх этого каждый снапшот кодируется дельтой против
// подтверждённой клиентом базы (итерация 6B) — см. sendSnapshotTo.
func (r *Room) broadcast() {
	r.entities = r.world.AppendEntities(r.entities[:0])
	if r.cfg.AOIRadius <= 0 {
		// Полный бродкаст: сортируем по entityKey (дельта-диффу нужен согласованный
		// порядок и у базы, и у вида) и режем до лимита провода (игроки идут раньше
		// снарядов, поэтому при клипе выпадает хвост снарядов, а не игроки; сейчас
		// MaxPlayers ниже лимита, и клип не срабатывает).
		sortEntitiesByKey(r.entities)
		if len(r.entities) > protocol.MaxEntities {
			r.entities = r.entities[:protocol.MaxEntities]
		}
		r.broadcastFull()
	} else {
		// AOI: лимит применяется пер-вью (в broadcastAOI), общий список НЕ режем — иначе
		// хвост сверх лимита (снаряды) выпал бы из сетки и стал бы невидим даже близким
		// зрителям. Сетка строится по полному набору.
		r.broadcastAOI()
	}
	// Наблюдатели (итер. 22) видят весь мир, вне AOI игроков.
	r.broadcastSpectators()
}

// broadcastSpectators шлёт наблюдателям (итер. 22) весь мир: у них нет позиции для
// AOI, поэтому набор — полный, отсортированный и подрезанный до лимита провода.
// Наблюдатель ack не шлёт (вводы игнорируются), поэтому его дельта-база пуста и
// снапшот всегда полный — для редких наблюдателей это приемлемо. Пропускаем всю
// работу, если наблюдателей нет.
func (r *Room) broadcastSpectators() {
	if len(r.spectators) == 0 {
		return
	}
	r.specView = append(r.specView[:0], r.entities...)
	sortEntitiesByKey(r.specView)
	if len(r.specView) > protocol.MaxEntities {
		r.specView = r.specView[:protocol.MaxEntities]
	}
	for _, s := range r.spectators {
		r.sendSnapshotTo(s, 0, r.specView)
	}
}

// broadcastFull шлёт полный список сущностей каждому клиенту (interest management
// выключен). Все видят один набор, но кодирование своё: и LastProcessedSeq, и
// дельта-база у каждого свои.
func (r *Room) broadcastFull() {
	r.world.Each(func(p *Player) {
		if s := r.sessions[p.ID]; s != nil {
			r.sendSnapshotTo(s, p.LastProcessedSeq, r.entities)
		}
	})
}

// broadcastAOI шлёт каждому клиенту только сущности в его окрестности (interest
// management). Так исходящий трафик перестаёт расти линейно с числом игроков в
// комнате: клиент платит за то, что видит, а не за всю комнату.
//
// AOI — networking, а не симуляция: сетка и подмножества строятся из уже
// просчитанных позиций и в Checksum/реплеи не влияют. Всё идёт на горутине цикла,
// поэтому переиспользуемые буферы (grid/cand/view) не требуют синхронизации.
func (r *Room) broadcastAOI() {
	r.grid.build(r.entities)
	rad := r.cfg.AOIRadius
	r.world.Each(func(p *Player) {
		s := r.sessions[p.ID]
		if s == nil {
			return
		}
		// Центр интереса — позиция игрока (для мёртвого это точка смерти: death-cam
		// показывает окрестность до респауна). Себя игрок видит всегда — он в центре.
		r.cand = r.grid.queryBox(p.X, p.Y, rad, r.cand[:0])
		r.view = r.view[:0]
		for _, idx := range r.cand {
			e := r.entities[idx]
			if abs32(e.X-p.X) <= rad && abs32(e.Y-p.Y) <= rad {
				r.view = append(r.view, e)
			}
		}
		// Стабильный порядок (игроки раньше снарядов, внутри по id) держит клиентское
		// кодирование детерминированным и базы дельт согласованными, а при упоре в
		// лимит провода срежется хвост снарядов, а не игроки.
		sortEntitiesByKey(r.view)
		if len(r.view) > protocol.MaxEntities {
			r.view = r.view[:protocol.MaxEntities]
		}
		r.sendSnapshotTo(s, p.LastProcessedSeq, r.view)
	})
}

// sendSnapshotTo кодирует набор view для сессии s и ставит его в её очередь. view
// уже отсортирован по entityKey. Кодирование — дельтой против подтверждённой
// клиентом базы (Session.ackTick), либо полным, если базы нет в кольце (клиент
// ещё ничего не подтвердил / база протухла) или дельта не короче полного. Успешно
// поставленный набор становится новой базой этого клиента.
//
// Буфер снапшота новый на клиента: он отдаётся горутине-писателю, переиспользовать
// нельзя (пул — в итерации 6C). Переиспользуемые changed/removed и кольцо баз
// принадлежат горутине цикла и наружу не утекают — AppendSnapshot копирует данные
// в свежий буфер.
func (r *Room) sendSnapshotTo(s *Session, lastSeq uint32, view []protocol.Entity) {
	snap := protocol.Snapshot{Tick: r.world.Tick, LastProcessedSeq: lastSeq}
	if base := s.baseline.get(s.ackTick); base == nil {
		snap.Entities = view // полный снапшот
	} else {
		r.changed = r.changed[:0]
		r.masks = r.masks[:0]
		r.removed = r.removed[:0]
		r.changed, r.masks, r.removed = diffEntities(base, view, r.changed, r.masks, r.removed)
		// Дельта оправдана, только если она короче полного. Смена почти всего вида
		// (респаун/телепорт: много ушедших + много новых) даёт дельту крупнее
		// полного — тогда шлём полный. Оценка по числу записей: изменённая запись
		// field-level дельты обычно < 12B полной (шлём лишь изменившиеся поля).
		// Исключение — НОВАЯ сущность (FieldAll = 13B, на 1B больше полной), но всплеск
		// новых почти всегда сопровождается всплеском removed старой базы, и тогда порог
		// по числу записей сам выбирает полный снапшот.
		if len(r.changed)+len(r.removed) < len(view) {
			snap.BaseTick = s.ackTick
			snap.Entities = r.changed
			snap.Masks = r.masks
			snap.Removed = r.removed
		} else {
			snap.Entities = view
		}
	}
	bp := r.snapPool.Get().(*[]byte)
	buf, err := protocol.AppendSnapshot((*bp)[:0], &snap)
	*bp = buf // AppendSnapshot мог перерастить срез — сохраняем выросший буфер в пуле
	if err != nil {
		r.log.Error("encode snapshot", "session", s.id, "err", err)
		r.snapPool.Put(bp)
		return
	}
	if s.enqueueSnapshot(bp) {
		r.cfg.Metrics.SnapshotBytes(len(buf))
		// Метрика меряет размер ВИДА (сколько сущностей клиент отслеживает — эффект
		// AOI), а не размер дельты; экономию дельты видно по SnapshotBytes.
		r.cfg.Metrics.EntitiesPerSnapshot(len(view))
		// Новая база — то, что клиент теперь видит целиком (view). Сохраняем только на
		// успешной постановке: дропнутый снапшот клиент не получит и не подтвердит.
		s.baseline.put(r.world.Tick, view)
	} else {
		// Не поставлен в очередь — наружу не ушёл, возвращаем буфер в пул.
		r.snapPool.Put(bp)
	}
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
			// Смерть сдвинула счёт (фраг + смерть) — табло устарело.
			r.matchDirty = true
			// Статистика убийства копится вживую (переживает дисконнект до конца матча).
			r.persistKill(ev.Attacker, ev.Target)
		case EventSpawn:
			buf, err := protocol.AppendSpawn(nil, protocol.Spawn{
				ID: uint16(ev.Target), X: ev.X, Y: ev.Y,
			})
			if err != nil {
				r.log.Error("encode spawn", "err", err)
				continue
			}
			r.reliableAll(buf)
		case EventKillstreak:
			// Веха серии убийств (итерация 20): объявление всем — фид + щит-визуал.
			buf, err := protocol.AppendKillstreak(nil, protocol.Killstreak{
				ID: uint16(ev.Target), Streak: ev.Streak,
			})
			if err != nil {
				r.log.Error("encode killstreak", "err", err)
				continue
			}
			r.reliableAll(buf)
		case EventCapture:
			// Захват флага (итер. 31): объявление всем — баннер/звук. Команду берём с
			// игрока в мире (он только что захватил — жив, ещё в мире на этом тике).
			var team uint8
			if p := r.world.Player(ev.Target); p != nil {
				team = p.team
			}
			buf, err := protocol.AppendCapture(nil, protocol.Capture{
				Player: uint16(ev.Target), Team: team,
			})
			if err != nil {
				r.log.Error("encode capture", "err", err)
				continue
			}
			r.reliableAll(buf)
			// Захват сдвинул счёт (Captures) — табло устарело.
			r.matchDirty = true
		}
	}
}

// broadcastMatchState рассылает состояние матча всем сессиям, когда табло реально
// изменилось: сменилась фаза (детект по world.matchPhase), либо взведён matchDirty
// (смерть/вход/выход игрока). Событийно, без поллинга: сообщение редкое и небольшое,
// таймер клиент отсчитывает локально, поэтому в тишине провод молчит, а тесты с
// пейсингом 1 снапшот/тик не сбиваются. Reliable — дельта-путь снапшотов не касается.
func (r *Room) broadcastMatchState() {
	if r.world.matchPhase != r.lastMatchPhase {
		prev := r.lastMatchPhase
		r.lastMatchPhase = r.world.matchPhase
		r.matchDirty = true
		// Матч завершился (бой → антракт): winner и счёт зафиксированы этим тиком,
		// startMatch их ещё не обнулил — снимаем итог для персиста именно здесь.
		if prev == matchActive && r.world.matchPhase == matchIntermission {
			r.persistMatchResult()
		}
	}
	if !r.matchDirty {
		return
	}
	r.matchDirty = false
	if buf := r.encodeMatchState(); buf != nil {
		r.reliableAll(buf)
	}
}

// encodeMatchState собирает текущее табло матча и кодирует его в свежий буфер.
// Возвращает nil при ошибке кодирования (залогирована). Буфер новый (AppendMatchState
// с nil dst), поэтому его безопасно раздать писателям всех сессий через reliableAll —
// после кодирования он read-only. Переиспользуемые matchScores/pmatch принадлежат
// горутине цикла и наружу не утекают — кодек копирует и скаляры, и байты имён.
func (r *Room) encodeMatchState() []byte {
	snap := r.world.MatchState(r.matchScores)
	r.matchScores = snap.Scores // сохраняем выросший буфер для переиспользования
	r.pmatch.Phase = uint8(snap.Phase)
	r.pmatch.Remaining = snap.Remaining
	r.pmatch.Winner = uint16(snap.Winner)
	r.pmatch.TeamMode = snap.TeamMode // командный режим (итер. 23)
	r.pmatch.HillMode = snap.HillMode // King of the Hill (итер. 29)
	r.pmatch.DomMode = snap.DomMode   // доминация (итер. 30)
	r.pmatch.CtfMode = snap.CtfMode   // Capture the Flag (итер. 31)
	// s.HillScore здесь — слот очков объектива: MatchState уже положил в него DomScore
	// в domMode (захваты в ctfMode, иначе очки холма), поэтому проводу отдаём его как есть.
	r.pmatch.Scores = r.pmatch.Scores[:0]
	for _, s := range snap.Scores {
		r.pmatch.Scores = append(r.pmatch.Scores, protocol.MatchScore{
			ID: uint16(s.ID), Name: s.Name, Kills: s.Kills, Deaths: s.Deaths, Team: s.Team, HillScore: s.HillScore,
		})
	}
	buf, err := protocol.AppendMatchState(nil, r.pmatch)
	if err != nil {
		r.log.Error("encode matchstate", "err", err)
		return nil
	}
	return buf
}

// broadcastPickups рассылает состояние пикапов всем сессиям, когда оно изменилось:
// мир сообщил об изменении за последний Step (World.PickupsDirty — спавн/подбор)
// либо вошёл новый игрок (pickupsDirty взведён в handleJoin). Событийно, без
// поллинга — как broadcastMatchState. Reliable, дельта-путь снапшотов не касается.
func (r *Room) broadcastPickups() {
	if r.world.PickupsDirty() {
		r.pickupsDirty = true
	}
	if !r.pickupsDirty {
		return
	}
	r.pickupsDirty = false
	if buf := r.encodePickups(); buf != nil {
		r.reliableAll(buf)
	}
}

// encodePickups собирает активные пикапы мира и кодирует их в свежий буфер.
// Возвращает nil при ошибке кодирования (залогирована). Буфер новый (AppendPickupState
// с nil dst), поэтому его безопасно раздать писателям всех сессий через reliableAll.
// Переиспользуемые pickupBuf/ppickups принадлежат горутине цикла и наружу не утекают —
// кодек копирует байты в буфер.
func (r *Room) encodePickups() []byte {
	r.pickupBuf = r.world.AppendPickups(r.pickupBuf[:0])
	r.ppickups.Active = r.pickupBuf
	buf, err := protocol.AppendPickupState(nil, r.ppickups)
	if err != nil {
		r.log.Error("encode pickupstate", "err", err)
		return nil
	}
	return buf
}

// broadcastWeapons рассылает оружие игроков всем сессиям, когда оно изменилось: мир
// сообщил о смене (World.WeaponsDirty) либо вошёл новый игрок (weaponsDirty взведён в
// handleJoin). Событийно, без поллинга — как broadcastPickups. Reliable, снапшоты не
// касается (итер. 26).
func (r *Room) broadcastWeapons() {
	if r.world.WeaponsDirty() {
		r.weaponsDirty = true
	}
	if !r.weaponsDirty {
		return
	}
	r.weaponsDirty = false
	if buf := r.encodeWeapons(); buf != nil {
		r.reliableAll(buf)
	}
}

// encodeWeapons собирает оружие игроков и кодирует его в свежий буфер. Возвращает nil
// при ошибке (залогирована). Буфер новый, поэтому его безопасно раздать через
// reliableAll; переиспользуемые weaponBuf/pweapons принадлежат горутине цикла.
func (r *Room) encodeWeapons() []byte {
	r.weaponBuf = r.world.AppendWeapons(r.weaponBuf[:0])
	r.pweapons.Weapons = r.weaponBuf
	buf, err := protocol.AppendWeaponState(nil, r.pweapons)
	if err != nil {
		r.log.Error("encode weaponstate", "err", err)
		return nil
	}
	return buf
}

// broadcastFlags рассылает состояние флагов CTF всем сессиям, когда оно изменилось:
// мир сообщил об изменении (World.FlagsDirty — подбор/дроп/захват/возврат) либо вошёл
// новый игрок (flagsDirty взведён в handleJoin). Событийно, без поллинга — как
// broadcastPickups. Только в ctfMode: вне режима флаги не рассылаются. Reliable,
// снапшоты не касается (итер. 31).
func (r *Room) broadcastFlags() {
	if !r.world.ctfMode {
		return
	}
	if r.world.FlagsDirty() {
		r.flagsDirty = true
	}
	if !r.flagsDirty {
		return
	}
	r.flagsDirty = false
	if buf := r.encodeFlags(); buf != nil {
		r.reliableAll(buf)
	}
}

// encodeFlags собирает состояние флагов и кодирует его в свежий буфер. Возвращает nil
// при ошибке (залогирована). Буфер новый, поэтому его безопасно раздать через
// reliableAll; переиспользуемые flagBuf/pflags принадлежат горутине цикла.
func (r *Room) encodeFlags() []byte {
	r.flagBuf = r.world.AppendFlags(r.flagBuf[:0])
	r.pflags.Flags = r.flagBuf
	buf, err := protocol.AppendFlagState(nil, r.pflags)
	if err != nil {
		r.log.Error("encode flagstate", "err", err)
		return nil
	}
	return buf
}

// persistKill шлёт persister инкремент статистики за смерть. Аккаунты берутся с
// игроков (0 у гостя, nil-игрок = уже ушедший стрелок → 0). Если оба гостя —
// копить нечего, не шлём. Зовётся из dispatchEvents (горутина цикла).
func (r *Room) persistKill(killer, victim PlayerID) {
	if r.cfg.PersistSink == nil {
		return
	}
	var killerAcc, victimAcc int64
	if p := r.world.Player(killer); p != nil {
		killerAcc = p.AccountID
	}
	if p := r.world.Player(victim); p != nil {
		victimAcc = p.AccountID
	}
	if killerAcc == 0 && victimAcc == 0 {
		return
	}
	r.sendPersist(PersistMsg{Kind: PersistKill, Killer: killerAcc, Victim: victimAcc})
}

// persistMatchResult снимает итог завершившегося матча и шлёт его persister. Времена
// проставляются от Clock комнаты: EndedAt — сейчас, StartedAt выводится из
// длительности матча (matchDurationTicks на тикрейте). Зовётся из broadcastMatchState
// на переходе бой → антракт (горутина цикла).
func (r *Room) persistMatchResult() {
	if r.cfg.PersistSink == nil {
		return
	}
	res := r.world.MatchResult()
	res.Mode = "ffa"
	if r.cfg.TeamMode {
		res.Mode = "tdm" // team deathmatch (итер. 23) — в историю матчей
	}
	res.Seed = r.cfg.Seed
	res.EndedAt = r.cfg.Clock.Now()
	res.StartedAt = res.EndedAt.Add(-time.Duration(matchDurationTicks) * r.cfg.TickInterval())
	r.sendPersist(PersistMsg{Kind: PersistMatch, Match: res})
}

// sendPersist кладёт сообщение в канал персиста неблокирующе: если persister отстаёт
// и канал полон, сообщение роняется (статистика — не критичный путь), но тик НИКОГДА
// не блокируется на внешнем I/O. Зовётся только горутиной цикла, поэтому счётчик
// дропов и флаг warn-once без синхронизации.
func (r *Room) sendPersist(msg PersistMsg) {
	select {
	case r.cfg.PersistSink <- msg:
	default:
		r.persistDrops++
		if !r.persistWarned {
			r.persistWarned = true
			r.log.Warn("persist sink full, dropping stats events", "dropped", r.persistDrops)
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
	// Наблюдатели тоже получают reliable-события (итер. 22): Death/Spawn/MatchState/
	// PickupState/Killstreak — им есть что показать. Hit (reliableTo участникам) их
	// не касается.
	for _, s := range r.spectators {
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
	// Наблюдатели (итер. 22) — тем же критерием отставания: у них есть reliable-очередь,
	// переполнение которой иначе копило бы потерянные события до самого дисконнекта.
	// Отдельный проход: их id живут в другой карте и снимаются removeSpectator.
	r.kicked = r.kicked[:0]
	for id, s := range r.spectators {
		if s.lagging(r.cfg.MaxBacklog) {
			r.kicked = append(r.kicked, id)
		}
	}
	for _, id := range r.kicked {
		r.removeSpectator(id, "too slow")
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
	// Игрок ушёл — строка исчезла из табло: следующий тик разошлёт обновление.
	r.matchDirty = true
	// Комната — единственный отправитель в обе очереди, поэтому она же их и
	// закрывает; это завершает write pump сессии.
	close(s.reliable)
	close(s.snapshots)
	r.setPlayerCount()
	r.log.Info("player left", "player", id, "name", s.name, "reason", reason, "dropped_snapshots", s.dropped)
}

// removeSpectator освобождает наблюдателя (итер. 22): удаляет из карты и закрывает
// его очереди (комната — единственный отправитель, поэтому она же закрывает).
// В отличие от removeSession, World не трогает — наблюдателя там нет.
func (r *Room) removeSpectator(id PlayerID, reason string) {
	s, ok := r.spectators[id]
	if !ok {
		return
	}
	delete(r.spectators, id)
	r.specCount.Store(int32(len(r.spectators)))
	close(s.reliable)
	close(s.snapshots)
	r.log.Info("spectator left", "spectator", id, "name", s.name, "reason", reason, "dropped_snapshots", s.dropped)
}

// shutdown освобождает каждую сессию. Сессии замечают это по закрытой очереди,
// останавливают свои pump'ы и закрывают соединения.
//
// Обход map недетерминирован, поэтому хвостовые leave попадают в лог реплея в
// случайном порядке. Это безопасно: RemovePlayer коммутативен на финальном
// (пустом) состоянии и не трогает rng, а Replay применяет хвост без Step —
// гарантия даётся на Checksum, не на побайтность лога (см. TestRoomReplayMatchesLive).
// Если RemovePlayer когда-нибудь начнёт розыгрыш rng или порядок ухода станет
// значимым — обходить здесь в порядке world.order.
func (r *Room) shutdown() {
	for id := range r.sessions {
		r.removeSession(id, "server shutdown")
	}
	for id := range r.spectators {
		r.removeSpectator(id, "server shutdown")
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
	evLeaveSpectator // уход наблюдателя (итер. 22)
	evInput
	evState
)

type joinReq struct {
	conn      transport.Conn
	name      string
	accountID int64
	spectator bool // наблюдатель без спавна (итер. 22)
	reply     chan joinResult
}

type joinResult struct {
	sess *Session
	err  error
}
