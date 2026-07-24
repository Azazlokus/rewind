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
	MoveState
	HP uint8

	// LastProcessedSeq — номер последнего ввода, применённого симуляцией. Клиент
	// использует его, чтобы отбрасывать подтверждённые вводы при реконсиляции
	// (итерация 4).
	LastProcessedSeq uint32

	// input — последняя полученная команда. Клиенты шлют на 60 Гц, а сервер
	// тикает на 30 Гц, поэтому итерация 1 применяет свежайшую команду и
	// отбрасывает остальные; итерация 4 заменит это на полноценную очередь
	// вводов.
	input protocol.Input
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
	order  []PlayerID
	rng    *rand.Rand
	nextID PlayerID
}

// NewWorld создаёт пустой мир. Два мира, созданные с одним seed и накормленные
// одними вводами, дают байт-в-байт идентичное состояние; TestWorldDeterminism
// это проверяет, и на этом держатся реплеи.
func NewWorld(seed int64) *World {
	return &World{
		players: make(map[PlayerID]*Player),
		rng:     rand.New(rand.NewPCG(uint64(seed), 0x9e3779b97f4a7c15)),
		nextID:  1,
	}
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
	w.players[id] = p
	i, _ := slices.BinarySearch(w.order, id)
	w.order = slices.Insert(w.order, i, id)
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
}

// SetInput запоминает свежайшую команду игрока.
func (w *World) SetInput(id PlayerID, in protocol.Input) {
	p := w.players[id]
	if p == nil {
		return
	}
	p.input = in
}

// Step продвигает весь мир на dt секунд.
func (w *World) Step(dt float32) {
	for _, id := range w.order {
		p := w.players[id]
		Step(&p.MoveState, p.input, dt)
		// Номера последовательности только растут: клиент, повторяющий или
		// подделывающий старый номер, не должен откатывать подтверждение назад.
		if p.input.Seq > p.LastProcessedSeq {
			p.LastProcessedSeq = p.input.Seq
		}
	}
	w.Tick++
}

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
	for _, id := range w.order {
		p := w.players[id]
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
	return dst
}

// Checksum — хеш полного состояния симуляции. Это тест равенства, используемый
// тестом детерминизма и проверкой реплеев: одинаковые контрольные суммы означают
// одинаковые миры, вплоть до последнего бита float.
func (w *World) Checksum() uint64 {
	h := fnv.New64a()
	var buf [8]byte
	writeU32 := func(v uint32) {
		binary.LittleEndian.PutUint32(buf[:4], v)
		_, _ = h.Write(buf[:4])
	}
	writeF32 := func(v float32) { writeU32(math.Float32bits(v)) }

	writeU32(w.Tick)
	writeU32(uint32(len(w.order)))
	for _, id := range w.order {
		p := w.players[id]
		writeU32(uint32(p.ID))
		writeU32(p.LastProcessedSeq)
		writeF32(p.X)
		writeF32(p.Y)
		writeF32(p.VX)
		writeF32(p.VY)
		buf[0] = p.HP
		_, _ = h.Write(buf[:1])
		_, _ = h.Write([]byte(p.Name))
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
		if _, taken := w.players[id]; !taken && id != 0 {
			return id, nil
		}
	}
	return 0, fmt.Errorf("%w: %d players", ErrWorldFull, len(w.players))
}

// spawnPoint выбирает позицию из собственного генератора мира. Симуляция никогда
// не должна трогать глобальный rand или настенные часы.
func (w *World) spawnPoint() MoveState {
	const margin = 128
	span := MapSize - 2*margin
	return MoveState{
		X: margin + w.rng.Float32()*span,
		Y: margin + w.rng.Float32()*span,
	}
}
