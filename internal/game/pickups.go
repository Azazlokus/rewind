package game

import "arena/internal/protocol"

// Пикапы/бонусы (итерация 19): предметы, разбросанные по арене, которые игрок
// подбирает, наступив на них. Три типа: аптечка (мгновенный хил), ускорение
// стрельбы и веерная стрельба (оба — временные буфы игрока).
//
// Пикапы — часть симуляции (влияют на HP, кулдаун, число снарядов), поэтому их
// состояние живёт во World и входит в Checksum: спавн детерминирован (тип
// розыгрывается w.rng, тайминг — по w.Tick), подбор — детерминированным обходом
// точек по индексу и игроков по order. Реплей воспроизводит их точь-в-точь.
//
// На проводе пикапы НЕ едут в снапшоте (иначе раздули бы дельту и сломали бы
// пер-тик счётчики сущностей): раскладка точек фиксирована и зеркалится клиентом
// (web/game.js: PICKUP_SPOTS) как и walls, а какие точки сейчас заняты и чем —
// сервер шлёт отдельным reliable-сообщением MsgPickupState событийно (как табло
// матча). Клиент рисует активные пикапы в их точках.

// pickupKind — тип пикапа. Значения зеркалит web/game.js.
type pickupKind uint8

const (
	pickupMedkit pickupKind = iota + 1 // 1 — аптечка: мгновенный хил
	pickupRapid                        // 2 — ускорение стрельбы (таймер)
	pickupSpread                       // 3 — веерная стрельба (таймер)
)

// pickupKindCount — число типов; розыгрыш идёт как 1 + rng.IntN(pickupKindCount).
const pickupKindCount = 3

// pickupSpot — фиксированная точка появления пикапа в мировых координатах.
// Раскладка ФИКСИРОВАНА и зеркалится клиентом (web/game.js: PICKUP_SPOTS) для
// рендера — как walls. Точки выбраны в открытых зонах: подальше от краёв и вне
// стен (те занимают x∈[1500..3100], y∈[1400..2420]).
type pickupSpot struct{ x, y float32 }

var pickupSpots = []pickupSpot{
	{700, 700},   // верхне-левый квадрант
	{3396, 700},  // верхне-правый
	{700, 3396},  // нижне-левый
	{3396, 3396}, // нижне-правый
	{2048, 3300}, // низ по центру (ниже стен)
}

// Константы пикапов заданы в тиках (30 Гц) и мировых величинах.
const (
	// pickupRadius — радиус подбора сверх радиуса игрока: игрок берёт пикап, когда
	// его центр ближе PlayerRadius+pickupRadius к центру точки.
	pickupRadius float32 = 18
	// pickupRespawnTicks — задержка до повторного появления после подбора (~8 с).
	pickupRespawnTicks = 30 * 8
	// medkitHeal — сколько HP восстанавливает аптечка (кламп по 100).
	medkitHeal uint8 = 50
	// rapidDurationTicks — длительность бафа ускорения стрельбы (~6 с).
	rapidDurationTicks = 30 * 6
	// rapidFireCooldownTicks — кулдаун выстрела под бафом (~0.1 с) вместо
	// fireCooldownTicks (~0.3 с).
	rapidFireCooldownTicks = 3
	// spreadDurationTicks — длительность бафа веерной стрельбы (~6 с).
	spreadDurationTicks = 30 * 6
	// spreadCount — сколько снарядов выпускает один выстрел под бафом веера.
	spreadCount = 3
	// spreadStepRad — угловой шаг между соседними снарядами веера (радианы).
	spreadStepRad float32 = 0.14
)

// pickupState — состояние одной точки. Индекс в срезе World.pickups совпадает с
// индексом в pickupSpots (позиция берётся оттуда). Будущее состояние симуляции
// (определяет хил/баф и момент следующего спавна) → входит в Checksum.
type pickupState struct {
	active  bool       // есть ли сейчас пикап в точке
	kind    pickupKind // тип активного пикапа (валиден при active)
	readyAt uint32     // тик появления следующего пикапа (валиден при !active)
}

// initPickups заводит состояние точек: все стартуют пустыми с readyAt 0, поэтому
// первый же stepPickups (тик 0) их активирует, розыгрывая тип через w.rng.
func (w *World) initPickups() {
	w.pickups = make([]pickupState, len(pickupSpots))
}

// stepPickups продвигает пикапы на один тик: активирует созревшие точки и отдаёт
// активные пикапы наступившим на них живым игрокам. Зовётся из World.Step после
// движения/респауна, до инкремента Tick (тайминг согласован с respawn).
//
// Детерминизм: точки обходятся по индексу, игроки — по w.order; тип пикапа
// розыгрывается w.rng в этом фиксированном порядке. Подобравший — игрок с
// МЛАДШИМ id среди накрывших точку (обход order по возрастанию, берём первого).
func (w *World) stepPickups() {
	const grab = PlayerRadius + pickupRadius
	for i := range w.pickups {
		ps := &w.pickups[i]
		if !ps.active {
			if w.Tick >= ps.readyAt {
				ps.active = true
				ps.kind = pickupKind(1 + w.rng.IntN(pickupKindCount))
				w.pickupsDirty = true
			}
			continue
		}
		spot := pickupSpots[i]
		for _, id := range w.order {
			p := w.players[id]
			if p.dead {
				continue
			}
			dx, dy := p.X-spot.x, p.Y-spot.y
			if dx*dx+dy*dy <= grab*grab {
				w.applyPickup(p, ps.kind)
				ps.active = false
				ps.readyAt = w.Tick + pickupRespawnTicks
				w.pickupsDirty = true
				break // точку взял младший id; остальные пусть ищут другие точки
			}
		}
	}
}

// applyPickup применяет эффект пикапа к игроку. Аптечка лечит немедленно (кламп по
// 100); ускорение и веер ставят таймер-баф до тика w.Tick+длительность.
func (w *World) applyPickup(p *Player, k pickupKind) {
	switch k {
	case pickupMedkit:
		if hp := uint32(p.HP) + uint32(medkitHeal); hp > 100 {
			p.HP = 100
		} else {
			p.HP = uint8(hp)
		}
	case pickupRapid:
		p.rapidUntil = w.Tick + rapidDurationTicks
	case pickupSpread:
		p.spreadUntil = w.Tick + spreadDurationTicks
	}
}

// AppendPickups дописывает активные пикапы в dst (для рассылки MsgPickupState) в
// порядке индекса точки и возвращает расширенный срез. Вызывающий передаёт
// переиспользуемый срез, чтобы не аллоцировать. Networking, не Checksum — как
// AppendEntities, метод отдаёт готовый provod-тип (world.go и так знает protocol).
func (w *World) AppendPickups(dst []protocol.Pickup) []protocol.Pickup {
	for i := range w.pickups {
		if w.pickups[i].active {
			dst = append(dst, protocol.Pickup{Spot: uint8(i), Kind: uint8(w.pickups[i].kind)})
		}
	}
	return dst
}

// PickupsDirty сообщает, изменилось ли состояние пикапов последним Step (спавн или
// подбор). Комната по нему решает, слать ли MsgPickupState. Сбрасывается в начале
// каждого Step (как w.events).
func (w *World) PickupsDirty() bool { return w.pickupsDirty }
