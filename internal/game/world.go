package game

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	rand "math/rand/v2"
	"slices"

	"arena/internal/protocol"
)

// ErrWorldFull возвращается, когда нет свободного id сущности.
var ErrWorldFull = errors.New("game: world is full")

// PlayerID идентифицирует игрока внутри одного мира. Он же — id сущности на
// проводе, поэтому это uint16.
type PlayerID uint16

// Player — один подключённый участник.
type Player struct {
	ID   PlayerID
	Name string
	// AccountID — привязка к зарегистрированному аккаунту (итер. 14B); 0 — гость.
	// Идентити-метаданные, как Name: НЕ входит в Checksum и в лог реплея (симуляцию
	// не трогает — только атрибуция статистики в persister). Проставляется комнатой
	// после AddPlayer из проверенного токена, а не самой симуляцией.
	AccountID int64
	MoveState
	HP uint8

	// LastProcessedSeq — номер последнего ввода, применённого симуляцией. Клиент
	// использует его при реконсиляции, чтобы отбрасывать подтверждённые вводы.
	LastProcessedSeq uint32

	// inputs — очередь ещё не просчитанных вводов игрока (FIFO). Клиент шлёт 60 Гц,
	// сервер тикает 30 Гц, поэтому за тик приходит ~2 ввода: Step применяет их все
	// по одному шагом inputDt. Так серверная симуляция потребляет ровно тот же
	// поток вводов, что переигрывает клиент при реконсиляции, — предсказание
	// сходится точно, а не «в среднем». Срез переиспользуется (осушается в [:0]).
	inputs        []protocol.Input
	lastQueuedSeq uint32 // seq последнего поставленного в очередь — отсев повторов/реордера
	hasQueued     bool   // был ли хоть один ввод (чтобы принять seq 0 как первый)

	// Боевое состояние (итерация 5).
	dead         bool   // мёртв: не двигается, не стреляет, не в снапшоте, ждёт респауна
	respawnAt    uint32 // тик, на котором игрок возродится (если dead)
	nextFireTick uint32 // тик, с которого снова можно стрелять (кулдаун)

	// Буфы от пикапов (итерация 19): тик, до которого баф активен (0 — нет). Влияют
	// на будущую стрельбу (кулдаун/веер), поэтому входят в Checksum. Чистятся при
	// респауне (свежая жизнь — без буфов).
	rapidUntil  uint32 // до этого тика кулдаун стрельбы укорочен
	spreadUntil uint32 // до этого тика выстрел даёт веер снарядов

	// Счёт текущего матча (итерация 14). Обнуляется на старте матча. В Checksum —
	// влияет на исход матча (победителя) и на будущий сброс.
	Kills  uint16
	Deaths uint16

	// posHist — кольцо позиций за последние historyLen тиков для lag compensation:
	// сервер перематывает цель сюда, к тому, что видел стрелок. Индекс — метка тика
	// маской (см. recordHistory). Фиксированный массив: запись за тик без аллокаций.
	posHist [historyLen]histPos
}

// World держит авторитетное игровое состояние одной комнаты.
//
// Им владеет одна горутина — game loop комнаты, — и он намеренно без блокировок:
// каждая мутация происходит между двумя тиками, в одном месте. World прекрасно
// работает вообще без сети, на что и опираются headless-тесты и инструмент
// реплеев.
type World struct {
	// Tick — число просимулированных шагов с момента создания мира.
	Tick uint32

	players map[PlayerID]*Player
	// order держит id игроков по возрастанию. Любой обход идёт через него:
	// range по Go-map рандомизирован и сделал бы симуляцию недетерминированной.
	order []PlayerID
	rng   *rand.Rand
	// rngSrc — источник rng. Хешируется в Checksum: число розыгрышей зависит от
	// исходов боя (респауны), поэтому курсор ГПСЧ — часть будущего состояния.
	rngSrc *rand.PCG
	nextID PlayerID

	// Снаряды в полёте. Слайс переиспользуется (компактируется на месте); порядок
	// детерминирован (спавн идёт в порядке order). allocID проверяет занятость id
	// сканом этого слайса. Итерация 5.
	projectiles []projectile
	// events — reliable-события боя, накопленные последним Step; переиспользуется.
	events []Event

	// hitGrid — широкофазная сетка коллизии снаряд×игрок (итерация 8). Транзиентный
	// индекс: перестраивается каждый stepProjectiles из позиций/истории игроков, в
	// Checksum НЕ входит — это производное от уже-хешируемого состояния (два равных
	// мира дают равные сетки). hitCand — переиспользуемый буфер кандидатов запроса.
	// Оба живут во World, чтобы не аллоцировать на горячем пути.
	hitGrid hitGrid
	hitCand []PlayerID

	// Матч (итерация 14): FFA deathmatch с таймером. Всё детерминировано по Tick.
	// phase — active/intermission; deadline — тик перехода фазы (конец матча или конец
	// антракта); winner — победитель прошедшего матча (в антракте). Все три — будущее
	// состояние, поэтому в Checksum.
	matchPhase matchPhase
	matchAt    uint32   // тик, на котором текущая фаза заканчивается
	winner     PlayerID // победитель прошлого матча (валиден в intermission)

	// Пикапы (итерация 19): состояние фиксированных точек (см. pickups.go). Спавн и
	// подбор детерминированы (w.rng + w.Tick + позиции), поэтому в Checksum.
	// pickupsDirty взводится, когда состояние изменилось за Step (спавн/подбор), и
	// сбрасывается в начале следующего Step; комната по нему шлёт MsgPickupState.
	pickups      []pickupState
	pickupsDirty bool

	// seed — исходный seed мира (для заголовка лога реплея). Итерация 7.
	seed int64
	// rec — лог реплея; != nil, когда включена запись (EnableReplayRecording).
	// Пишется здесь же, на горутине комнаты — синхронизация не нужна. Итерация 7.
	rec *ReplayLog
}

// NewWorld создаёт пустой мир. Два мира, созданные с одним seed и накормленные
// одними вводами, дают байт-в-байт идентичное состояние; TestWorldDeterminism
// это проверяет, и на этом держатся реплеи.
func NewWorld(seed int64) *World {
	src := rand.NewPCG(uint64(seed), 0x9e3779b97f4a7c15)
	w := &World{
		players: make(map[PlayerID]*Player),
		rng:     rand.New(src),
		rngSrc:  src,
		nextID:  1,
		seed:    seed,
		// Первый матч стартует активным и заканчивается через matchDurationTicks.
		matchPhase: matchActive,
		matchAt:    matchDurationTicks,
	}
	w.initPickups() // точки пикапов стартуют пустыми, первый Step их активирует
	return w
}

// EnableReplayRecording включает запись событий мира в лог реплея. Зовётся до
// первого события (обычно сразу после NewWorld). Запись идёт на той же горутине,
// что мутирует мир, поэтому синхронизации не требует.
func (w *World) EnableReplayRecording() {
	w.rec = &ReplayLog{Seed: w.seed}
}

// ReplayLog возвращает записанный лог (или nil, если запись выключена) с
// проставленными итоговыми Ticks/TickRate. Читает состояние мира — звать на
// горутине комнаты или после её остановки (как Checksum). Возвращается КОПИЯ
// заголовка (события шарятся read-only), поэтому метод не мутирует w.rec: два
// вызова после Done() не устроят гонку по Ticks/TickRate.
func (w *World) ReplayLog(tickRate int) *ReplayLog {
	if w.rec == nil {
		return nil
	}
	out := *w.rec
	out.TickRate = tickRate
	out.Ticks = w.Tick
	return &out
}

// Len сообщает число игроков в мире.
func (w *World) Len() int { return len(w.players) }

// Player возвращает игрока с данным id или nil.
func (w *World) Player(id PlayerID) *Player { return w.players[id] }

// AddPlayer размещает нового игрока в засиженной seed'ом точке спавна.
func (w *World) AddPlayer(name string) (*Player, error) {
	id, err := w.allocID()
	if err != nil {
		return nil, err
	}
	p := &Player{
		ID:        id,
		Name:      name,
		MoveState: w.spawnPoint(),
		HP:        100,
	}
	p.initHistory() // кольцо истории стартует с точки спавна
	w.players[id] = p
	i, _ := slices.BinarySearch(w.order, id)
	w.order = slices.Insert(w.order, i, id)
	if w.rec != nil {
		// Пишем имя: id и точка спавна выводятся детерминированно при реплее.
		w.rec.join(w.Tick, name)
	}
	return p, nil
}

// RemovePlayer убирает игрока. Удаление неизвестного id — no-op.
func (w *World) RemovePlayer(id PlayerID) {
	if _, ok := w.players[id]; !ok {
		return
	}
	delete(w.players, id)
	if i, ok := slices.BinarySearch(w.order, id); ok {
		w.order = slices.Delete(w.order, i, i+1)
	}
	if w.rec != nil {
		w.rec.leave(w.Tick, id)
	}
}

// EnqueueInput ставит ввод игрока в очередь на обработку. Вводы с seq не больше
// уже поставленного отбрасываются (повтор/реордер), поэтому подтверждение не может
// уехать назад. Очередь осушается ближайшим Step.
//
// Сравнение seq плоское: считаем, что за сессию uint32 не переполнится (при 60 Гц
// это ~2.3 года). Клиент фильтрует с учётом заворачивания (seqLE) — расхождение
// правил безвредно на таких горизонтах.
func (w *World) EnqueueInput(id PlayerID, in protocol.Input) {
	p := w.players[id]
	if p == nil {
		return
	}
	if p.hasQueued && in.Seq <= p.lastQueuedSeq {
		return
	}
	p.inputs = append(p.inputs, in)
	p.lastQueuedSeq = in.Seq
	p.hasQueued = true
	if w.rec != nil {
		// Пишем только принятые вводы: дропнутый дедупом повтор lastQueuedSeq не
		// меняет, поэтому в логе не нужен.
		w.rec.input(w.Tick, id, in)
	}
}

// Step продвигает весь мир на один тик:
//  1. движение игроков — интегрирует очередь вводов каждого шагом inputDt (то же
//     авторитетное потребление потока, что переигрывает клиент), а зажатый BtnFire
//     порождает снаряд с учётом кулдауна;
//  2. снаряды — летят на длительность тика dt и проверяются на попадания;
//  3. респаун — возрождает мёртвых, чей срок пришёл.
//
// Reliable-события боя (Hit/Death/Spawn) копятся в w.events; комната разбирает их
// сразу после Step через Events().
func (w *World) Step(dt float32) {
	w.events = w.events[:0]
	w.pickupsDirty = false // взведётся заново в stepPickups, если что-то изменится

	// 1. Игроки: движение по очереди вводов + стрельба. Мёртвые пропускают тик.
	for _, id := range w.order {
		p := w.players[id]
		if p.dead {
			p.inputs = p.inputs[:0]
			continue
		}
		for i := range p.inputs {
			in := p.inputs[i]
			Step(&p.MoveState, in, inputDt)
			// Номера последовательности только растут: подтверждение не откатывается.
			if in.Seq > p.LastProcessedSeq {
				p.LastProcessedSeq = in.Seq
			}
			if in.Pressed(protocol.BtnFire) {
				w.tryFire(p, in)
			}
		}
		p.inputs = p.inputs[:0] // очередь осушена; срез переиспользуется
	}

	// 2. Снаряды: полёт на длительность тика + коллизии.
	w.stepProjectiles(dt)

	// 3. Респаун тех, чей срок пришёл (смерть в этом же тике не воскресает: срок в
	// будущем).
	for _, id := range w.order {
		p := w.players[id]
		if p.dead && w.Tick >= p.respawnAt {
			w.respawn(p)
		}
	}

	// 4. Пикапы: активация созревших точек и подбор наступившими игроками (итер. 19).
	// После движения/респауна — по актуальным позициям; до Tick++ — тайминг как у
	// respawn.
	w.stepPickups()

	w.Tick++
	// Матч: таймер/переходы фаз по уже актуальному тику (сброс счёта, респаун на
	// старте нового матча эмитит Spawn — их разберёт dispatchEvents).
	w.stepMatch()
	// Записываем позиции под новой меткой тика — той же, что уйдёт в снапшот этого
	// тика; на этом кольце стоит перемотка целей (lag compensation).
	w.recordHistory()
}

// Events возвращает reliable-события боя, накопленные последним Step. Срез
// переиспользуется следующим Step — комната обязана разобрать его сразу.
func (w *World) Events() []Event { return w.events }

// Each зовёт f для каждого игрока в детерминированном порядке id.
func (w *World) Each(f func(*Player)) {
	for _, id := range w.order {
		f(w.players[id])
	}
}

// AppendEntities дописывает каждую сущность мира в dst в порядке id и возвращает
// расширенный срез. Вызывающие передают переиспользуемый срез, чтобы оставаться
// без аллокаций на горячем пути.
func (w *World) AppendEntities(dst []protocol.Entity) []protocol.Entity {
	// Игроки идут первыми: если снапшот упрётся в protocol.MaxEntities, комната
	// срежет хвост (снаряды), а не живых игроков. Мёртвых в снапшоте нет.
	for _, id := range w.order {
		p := w.players[id]
		if p.dead {
			continue
		}
		dst = append(dst, protocol.Entity{
			ID:   uint16(p.ID),
			Kind: protocol.KindPlayer,
			X:    p.X,
			Y:    p.Y,
			VX:   p.VX,
			VY:   p.VY,
			HP:   p.HP,
		})
	}
	for i := range w.projectiles {
		pr := &w.projectiles[i]
		dst = append(dst, protocol.Entity{
			ID:   uint16(pr.id),
			Kind: protocol.KindProjectile,
			X:    pr.x,
			Y:    pr.y,
			VX:   pr.vx,
			VY:   pr.vy,
			HP:   1,
		})
	}
	return dst
}

// Checksum — хеш полного состояния симуляции. Это тест равенства, используемый
// тестом детерминизма и проверкой реплеев: одинаковые контрольные суммы означают
// одинаковые миры, вплоть до последнего бита float.
//
// Валиден только на границе тика (после Step): к этому моменту очередь вводов
// каждого игрока осушена, поэтому в хэш она не входит. Снятый между EnqueueInput
// и Step, хэш не учёл бы накопленные вводы — два мира с равным хэшем разошлись бы
// на следующем Step.
func (w *World) Checksum() uint64 {
	h := fnv.New64a()
	var buf [8]byte
	writeU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(buf[:4], v)
		_, _ = h.Write(buf[:4])
	}
	writeF32 := func(v float32) { writeU32(math.Float32bits(v)) }

	writeBool := func(b bool) {
		if b {
			buf[0] = 1
		} else {
			buf[0] = 0
		}
		_, _ = h.Write(buf[:1])
	}

	writeU32(w.Tick)
	// Состояние матча (итерация 14) — будущее состояние симуляции.
	buf[0] = byte(w.matchPhase)
	_, _ = h.Write(buf[:1])
	writeU32(w.matchAt)
	writeU32(uint32(w.winner))
	writeU32(uint32(len(w.order)))
	for _, id := range w.order {
		p := w.players[id]
		writeU32(uint32(p.ID))
		writeU32(p.LastProcessedSeq)
		// Гейт дедупа вводов — тоже будущее состояние: от него зависит, какие вводы
		// примутся дальше. Реплей воспроизводит его точно (пишет принятые вводы),
		// поэтому включаем в Checksum (закрыто слепое пятно итерации 4). Итерация 7.
		writeU32(p.lastQueuedSeq)
		writeBool(p.hasQueued)
		writeF32(p.X)
		writeF32(p.Y)
		writeF32(p.VX)
		writeF32(p.VY)
		buf[0] = p.HP
		_, _ = h.Write(buf[:1])
		// Боевое состояние влияет на будущее — значит в хэш.
		writeBool(p.dead)
		writeU32(p.respawnAt)
		writeU32(p.nextFireTick)
		// Буфы пикапов (итерация 19) — влияют на будущую стрельбу (кулдаун/веер).
		writeU32(p.rapidUntil)
		writeU32(p.spreadUntil)
		// Счёт матча — от него зависит победитель и будущий сброс.
		writeU32(uint32(p.Kills))
		writeU32(uint32(p.Deaths))
		// Кольцо истории позиций — тоже будущее состояние: снаряд в полёте прочитает
		// его при перемотке цели, поэтому равные во всём остальном миры с разной
		// историей обязаны различаться (иначе разойдутся на следующем hit-тесте).
		// Хешируем всё кольцо — порядок фиксирован, Checksum не на горячем пути.
		for i := range p.posHist {
			writeF32(p.posHist[i].x)
			writeF32(p.posHist[i].y)
		}
		_, _ = h.Write([]byte(p.Name))
	}
	// Снаряды — в порядке слайса (детерминированном: спавн идёт в порядке order).
	writeU32(uint32(len(w.projectiles)))
	for i := range w.projectiles {
		pr := &w.projectiles[i]
		writeU32(uint32(pr.id))
		writeU32(uint32(pr.owner))
		writeF32(pr.x)
		writeF32(pr.y)
		writeF32(pr.vx)
		writeF32(pr.vy)
		writeU32(uint32(pr.life))
		writeU32(uint32(pr.rewind)) // сдвиг перемотки влияет на будущий hit-тест
	}
	// Пикапы (итерация 19) — будущее состояние: занятость точки и её тип определяют
	// хил/баф при подборе, readyAt — момент следующего спавна (розыгрыш rng). Порядок
	// фиксирован (индекс точки). Длину НЕ префиксуем (в отличие от снарядов): len(pickups)
	// — инвариант (== len(pickupSpots), задаётся в initPickups и не меняется), а запись
	// фиксированной ширины, так что неоднозначности нет. Позиции точек в хэш не идут —
	// они статичны и равны во всех мирах (как walls); хешируется только динамика.
	for i := range w.pickups {
		ps := &w.pickups[i]
		writeBool(ps.active)
		buf[0] = byte(ps.kind)
		_, _ = h.Write(buf[:1])
		writeU32(ps.readyAt)
	}
	// Курсор ГПСЧ — часть будущего состояния: два мира с равными полями, но разным
	// числом розыгрышей (разные исходы боя) обязаны различаться, иначе разойдутся
	// на следующем спавне. MarshalBinary у PCG детерминирован и не аллоцирует лишку.
	if b, err := w.rngSrc.MarshalBinary(); err == nil {
		_, _ = h.Write(b)
	}
	return h.Sum64()
}

// allocID ищет свободный id сущности, сканируя вперёд от последнего выданного,
// чтобы id не переиспользовались сразу после ухода игрока.
func (w *World) allocID() (PlayerID, error) {
	for range math.MaxUint16 {
		id := w.nextID
		w.nextID++
		if w.nextID == 0 {
			w.nextID = 1
		}
		if id != 0 && !w.idTaken(id) {
			return id, nil
		}
	}
	return 0, fmt.Errorf("%w: %d players", ErrWorldFull, len(w.players))
}

// idTaken сообщает, занят ли id живым игроком или снарядом в полёте. Скан снарядов
// дёшев: их не больше maxProjectiles.
func (w *World) idTaken(id PlayerID) bool {
	if _, ok := w.players[id]; ok {
		return true
	}
	for i := range w.projectiles {
		if w.projectiles[i].id == id {
			return true
		}
	}
	return false
}

// spawnPoint выбирает позицию из собственного генератора мира. Симуляция никогда
// не должна трогать глобальный rand или настенные часы.
//
// Точка перебрасывается до spawnTries раз, пока не окажется вне стен (с зазором
// spawnClearance): свежий игрок не должен появиться внутри препятствия (итерация
// 10). Розыгрыши идут через w.rng — детерминированно, поэтому реплей воспроизводит
// те же точки. Если за отведённые попытки чистой точки нет (крайне маловероятно
// при разреженных стенах), последнюю выталкиваем из стен, чтобы не застрять.
func (w *World) spawnPoint() MoveState {
	const margin = 128
	span := MapSize - 2*margin
	var x, y float32
	for range spawnTries {
		x = margin + w.rng.Float32()*span
		y = margin + w.rng.Float32()*span
		if !insideAnyWall(x, y, PlayerRadius+spawnClearance) {
			return MoveState{X: x, Y: y}
		}
	}
	x, y = resolveWalls(x, y, PlayerRadius)
	return MoveState{X: x, Y: y}
}
