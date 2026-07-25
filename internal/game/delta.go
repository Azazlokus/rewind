package game

import (
	"slices"

	"arena/internal/protocol"
)

// Дельта-кодирование снапшотов (итерация 6B).
//
// Каждому клиенту снапшот уходит либо полным, либо дельтой против недавнего
// снапшота, который клиент подтвердил (прислал его метку в Input.AckTick). Сервер
// держит на сессию кольцо последних отправленных наборов сущностей как возможных
// баз. Реконструкция полного набора из базы и дельты — на стороне клиента/бота;
// здесь только считается разница и хранятся базы.

// baselineRingLen — сколько последних отправленных клиенту снапшотов держим как
// базы. ~0.8 с при 20 Гц — с запасом на ack round-trip (интерп-задержка клиента
// ~100 мс + RTT). Если подтверждённая база выпала из кольца (клиент сильно отстал
// или большой RTT), сервер шлёт полный снапшот. Степень двойки — индекс маской.
const baselineRingLen = 16

// sentSnap — набор сущностей, отправленный клиенту на одном тике. Копия: комнатный
// view переиспользуется и живёт лишь в пределах broadcast. Это база для дельты.
type sentSnap struct {
	tick uint32
	ents []protocol.Entity
	used bool
}

// baselineRing — кольцо последних отправленных клиенту снапшотов, ключ — метка
// тика по модулю длины. Владелец — горутина комнаты (как и прочее состояние
// рассылки сессии). Слоты и их срезы переиспользуются: put не аллоцирует после
// прогрева.
type baselineRing struct {
	slots [baselineRingLen]sentSnap
}

// put сохраняет набор ents под меткой tick, переиспользуя слот кольца.
func (b *baselineRing) put(tick uint32, ents []protocol.Entity) {
	s := &b.slots[tick%baselineRingLen]
	s.tick = tick
	s.ents = append(s.ents[:0], ents...)
	s.used = true
}

// get возвращает сохранённый набор для tick или nil, если его нет в кольце —
// клиент его никогда не получал или тик вытеснен более новым с той же меткой по
// модулю. Тик 0 всегда даёт nil (реальные снапшоты идут с меткой ≥ 1), что и
// означает «клиент ещё ничего не подтвердил → полный снапшот».
func (b *baselineRing) get(tick uint32) []protocol.Entity {
	s := &b.slots[tick%baselineRingLen]
	if s.used && s.tick == tick {
		return s.ents
	}
	return nil
}

// entityKey — ключ полного порядка (Kind, id): игроки раньше снарядов, внутри — по
// возрастанию id. И база, и текущий вид сортируются им, поэтому дельту считаем
// слиянием двух указателей без аллокаций и без map.
func entityKey(e protocol.Entity) uint32 {
	return uint32(e.Kind)<<16 | uint32(e.ID)
}

// sortEntitiesByKey сортирует срез по entityKey. Стабильный детерминированный
// порядок нужен и дельта-диффу (обе стороны отсортированы одинаково), и клиенту.
func sortEntitiesByKey(ents []protocol.Entity) {
	slices.SortFunc(ents, func(a, b protocol.Entity) int {
		return int(entityKey(a)) - int(entityKey(b))
	})
}

// diffEntities сравнивает базу base с текущим видом view (оба отсортированы по
// entityKey) и дописывает в changed новые/изменённые сущности, в removed — id
// ушедших, возвращая расширенные срезы. Слияние двух указателей: за один проход,
// без аллокаций (буферы передаются переиспользуемыми). Изменение определяется по
// проводному представлению (protocol.EntityWireEqual), чтобы не слать байт-в-байт
// те же записи.
func diffEntities(base, view, changed []protocol.Entity, removed []uint16) ([]protocol.Entity, []uint16) {
	i, j := 0, 0
	for i < len(base) && j < len(view) {
		ki, kj := entityKey(base[i]), entityKey(view[j])
		switch {
		case ki < kj: // сущность базы отсутствует в виде — ушла
			removed = append(removed, base[i].ID)
			i++
		case ki > kj: // сущность вида отсутствует в базе — новая
			changed = append(changed, view[j])
			j++
		default: // та же сущность — шлём, только если проводные байты изменились
			if !protocol.EntityWireEqual(base[i], view[j]) {
				changed = append(changed, view[j])
			}
			i++
			j++
		}
	}
	for ; i < len(base); i++ {
		removed = append(removed, base[i].ID)
	}
	for ; j < len(view); j++ {
		changed = append(changed, view[j])
	}
	return changed, removed
}
