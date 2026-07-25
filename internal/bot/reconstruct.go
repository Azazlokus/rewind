package bot

import (
	"slices"

	"arena/internal/protocol"
)

// reconKeep — сколько недавних реконструированных наборов бот держит как возможные
// базы. Должно быть не меньше кольца баз сервера (baselineRingLen), иначе клиент
// вытеснит базу, против которой сервер ещё кодирует. С запасом.
const reconKeep = 32

// reconstructor восстанавливает полный набор сущностей из потока снапшотов —
// полных (BaseTick==0) и дельт (BaseTick!=0, против недавней подтверждённой базы).
// Зеркалит клиентскую логику web/game.js. Принадлежит одной горутине чтения бота.
//
// Инвариант, на котором держится модель: клиент подтверждает (ackTick) только тик,
// который он реконструировал и ещё хранит; сервер кодирует дельту именно против
// него — значит база всегда есть в store. Если сервер прислал дельту с неизвестной
// базой (клиент отстал и вытеснил её), реконструкция невозможна — снапшот
// пропускается, а сервер, не увидев подтверждения новее, рано или поздно пришлёт
// полный.
type reconstructor struct {
	store map[uint32]map[uint16]protocol.Entity // tick -> (id -> сущность)
	order []uint32                              // ticks в порядке добавления, для вытеснения
	ents  []protocol.Entity                     // переиспользуемый буфер результата
}

func newReconstructor() *reconstructor {
	return &reconstructor{store: make(map[uint32]map[uint16]protocol.Entity)}
}

// apply реконструирует полный набор из снапшота s (полного или дельты), сохраняет
// его как возможную базу и возвращает сущности по возрастанию entityKey. Второй
// результат false — базу дельты найти не удалось, снапшот следует пропустить.
func (rc *reconstructor) apply(s *protocol.Snapshot) ([]protocol.Entity, bool) {
	var next map[uint16]protocol.Entity
	if s.BaseTick == 0 {
		next = make(map[uint16]protocol.Entity, len(s.Entities))
	} else {
		base, ok := rc.store[s.BaseTick]
		if !ok {
			return nil, false
		}
		// Копируем базу: хранимый набор мутировать нельзя (сервер может кодировать
		// ещё одну дельту против того же тика).
		next = make(map[uint16]protocol.Entity, len(base))
		for id, e := range base {
			next[id] = e
		}
	}
	for _, id := range s.Removed {
		delete(next, id)
	}
	for _, e := range s.Entities {
		next[e.ID] = e
	}
	rc.remember(s.Tick, next)

	rc.ents = rc.ents[:0]
	for _, e := range next {
		rc.ents = append(rc.ents, e)
	}
	slices.SortFunc(rc.ents, func(a, b protocol.Entity) int {
		ka := uint32(a.Kind)<<16 | uint32(a.ID)
		kb := uint32(b.Kind)<<16 | uint32(b.ID)
		return int(ka) - int(kb)
	})
	return rc.ents, true
}

// remember сохраняет набор под меткой tick, вытесняя старейший при переполнении.
func (rc *reconstructor) remember(tick uint32, ents map[uint16]protocol.Entity) {
	if _, seen := rc.store[tick]; !seen {
		rc.order = append(rc.order, tick)
	}
	rc.store[tick] = ents
	for len(rc.order) > reconKeep {
		delete(rc.store, rc.order[0])
		rc.order = rc.order[1:]
	}
}
