package game

import "arena/internal/protocol"

// Capture the Flag (итерация 31): командный режим захвата флага. У каждой команды —
// база с флагом; игрок подбирает флаг ВРАЖЕСКОЙ команды касанием, несёт к своей базе
// и, если СВОЙ флаг дома, совершает захват (canonical CTF): вражеский флаг
// возвращается на базу, а команде-захватчику засчитывается очко. Носитель, погибнув,
// роняет флаг на месте; брошенный флаг враг может подобрать снова, союзник — вернуть
// касанием, а если его никто не тронул за flagReturnTicks, он авто-возвращается.
//
// CTF по своей природе командный (две стороны с базами), поэтому режим подразумевает
// teamMode: комната включает оба флага вместе (см. NewRoom), а stepCTF защитно
// выходит вне ctfMode/teamMode.
//
// Полностью детерминирован: обход w.order и индексов flags/flagBases, без rng/времени/
// map. Всё состояние флагов и Player.Captures ВХОДИТ в Checksum и реплей-безопасно.
// Геометрия баз статична и одинакова во всех мирах — в Checksum НЕ входит (как walls);
// в хэш идёт лишь динамика (статус/носитель/позиция флага) и счёт захватов.
// Раскладку баз зеркалит клиент (web/game.js: SIM.FlagBases) для отрисовки.

// Статусы флага.
const (
	flagAtBase  uint8 = iota // покоится на своей базе
	flagCarried              // несёт игрок (carrier)
	flagDropped              // брошен на земле (после смерти носителя)
)

// flagBases — фиксированные позиции баз: индекс = команда (0/1). Симметрично по краям
// открытой полосы y≈2048, вне стен (те занимают x∈[1500..3100]). Значения обязаны
// совпадать с SIM.FlagBases в web/game.js.
var flagBases = [2]controlPoint{
	{512, 2048},  // база команды 0 (синие) — слева
	{3584, 2048}, // база команды 1 (красные) — справа
}

const (
	// flagGrabRadius — зазор подбора/возврата флага сверх радиуса игрока.
	flagGrabRadius float32 = 28
	// flagBaseRadius — радиус базы: носитель захватывает, подойдя к своей базе ближе
	// flagBaseRadius+PlayerRadius к её центру.
	flagBaseRadius float32 = 64
	// flagReturnTicks — через сколько тиков брошенный флаг сам возвращается на базу
	// (~20 c при 30 Гц), если его никто не тронул.
	flagReturnTicks uint32 = 30 * 20
)

// flagState — динамика одного флага (в Checksum). x/y — позиция для рендера: у
// flagAtBase равна центру базы, у flagCarried отслеживает носителя, у flagDropped —
// точка падения. dropAt — тик авто-возврата (валиден при flagDropped).
type flagState struct {
	status  uint8
	carrier PlayerID
	x, y    float32
	dropAt  uint32
}

// resetFlags возвращает оба флага на их базы (старт мира и старт нового матча).
func (w *World) resetFlags() {
	for i := range w.flags {
		w.flags[i] = flagState{status: flagAtBase, x: flagBases[i].x, y: flagBases[i].y}
	}
}

// resetFlagToBase возвращает один флаг на базу и помечает состояние флагов изменившимся.
func (w *World) resetFlagToBase(i int) {
	w.flags[i] = flagState{status: flagAtBase, x: flagBases[i].x, y: flagBases[i].y}
	w.flagsDirty = true
}

// stepCTF продвигает состояние флагов за тик (итер. 31). Зовётся из World.Step в
// режиме ctfMode во время активного матча, по актуальным (после движения/респауна)
// позициям. No-op вне ctfMode/teamMode или активного матча.
func (w *World) stepCTF() {
	if !w.ctfMode || !w.teamMode || w.matchPhase != matchActive {
		return
	}

	// 0. Валидность носителей и авто-возврат брошенных.
	for i := range w.flags {
		f := &w.flags[i]
		switch f.status {
		case flagCarried:
			p := w.players[f.carrier]
			switch {
			case p == nil:
				// Носитель ушёл из мира (дисконнект) — флаг домой.
				w.resetFlagToBase(i)
			case p.dead:
				// Носитель погиб — роняем флаг на месте смерти.
				f.status = flagDropped
				f.carrier = 0
				f.x, f.y = p.X, p.Y
				f.dropAt = w.Tick + flagReturnTicks
				w.flagsDirty = true
			default:
				// Несомый флаг следует за носителем (позиция — производное от p.X/Y).
				f.x, f.y = p.X, p.Y
			}
		case flagDropped:
			if w.Tick >= f.dropAt {
				w.resetFlagToBase(i)
			}
		}
	}

	const grab2 = (PlayerRadius + flagGrabRadius) * (PlayerRadius + flagGrabRadius)

	// 1. Взаимодействия: касание флага. Обход w.order — детерминированно; при равном
	// касании флаг достаётся игроку с меньшим id (первый в order подберёт, статус
	// станет carried, следующие пропустят carried-флаг).
	for _, id := range w.order {
		p := w.players[id]
		if p.dead {
			continue
		}
		for i := range w.flags {
			f := &w.flags[i]
			if f.status == flagCarried {
				continue // несомый флаг не трогаем
			}
			// Позиция флага для касания: на базе — центр базы, брошен — точка падения.
			fx, fy := f.x, f.y
			if f.status == flagAtBase {
				fx, fy = flagBases[i].x, flagBases[i].y
			}
			dx, dy := p.X-fx, p.Y-fy
			if dx*dx+dy*dy > grab2 {
				continue
			}
			if p.team&1 == uint8(i) {
				// Свой флаг: касанием возвращаем брошенный на базу.
				if f.status == flagDropped {
					w.resetFlagToBase(i)
				}
			} else {
				// Вражеский флаг: подбираем (с базы или с земли).
				f.status = flagCarried
				f.carrier = id
				f.x, f.y = p.X, p.Y
				w.flagsDirty = true
			}
		}
	}

	// 2. Захваты: носитель у своей базы при своём флаге дома. Обход w.order.
	const cap2 = (PlayerRadius + flagBaseRadius) * (PlayerRadius + flagBaseRadius)
	for _, id := range w.order {
		p := w.players[id]
		if p.dead {
			continue
		}
		t := p.team & 1
		enemy := &w.flags[1-t] // вражеский флаг, который несёт игрок команды t
		if enemy.status != flagCarried || enemy.carrier != id {
			continue
		}
		if w.flags[t].status != flagAtBase {
			continue // свой флаг не дома — захват не засчитывается (canonical CTF)
		}
		bx, by := flagBases[t].x, flagBases[t].y
		dx, dy := p.X-bx, p.Y-by
		if dx*dx+dy*dy > cap2 {
			continue
		}
		// Захват: вражеский флаг домой, очко команде (носителю), событие.
		w.resetFlagToBase(int(1 - t))
		p.Captures++
		w.events = append(w.events, Event{Kind: EventCapture, Target: id})
	}
}

// AppendFlags дописывает состояние обоих флагов в dst (для рассылки MsgFlagState) и
// возвращает расширенный срез. Индекс = команда (совпадает с team в записи). Вызывающий
// передаёт переиспользуемый срез, чтобы не аллоцировать. Networking, не Checksum — как
// AppendPickups/AppendWeapons.
func (w *World) AppendFlags(dst []protocol.FlagInfo) []protocol.FlagInfo {
	for i := range w.flags {
		f := &w.flags[i]
		dst = append(dst, protocol.FlagInfo{
			Team: uint8(i), Status: f.status, Carrier: uint16(f.carrier), X: f.x, Y: f.y,
		})
	}
	return dst
}

// ctfWinningTeam — команда с большей суммой захватов (итер. 31). При равенстве —
// команда 0. Детерминированный обход w.order.
func (w *World) ctfWinningTeam() uint8 {
	var c [2]int
	for _, id := range w.order {
		p := w.players[id]
		c[p.team&1] += int(p.Captures)
	}
	if c[1] > c[0] {
		return 1
	}
	return 0
}
