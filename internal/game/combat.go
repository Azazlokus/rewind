package game

import (
	"math"

	"arena/internal/protocol"
)

// Боевые константы. ProjectileSpeed/Radius — мировые величины; кулдаун, время
// жизни снаряда и задержка респауна заданы в тиках (30 Гц).
const (
	// ProjectileSpeed — скорость снаряда в юнитах/с.
	ProjectileSpeed float32 = 700
	// ProjectileRadius — радиус снаряда для коллизии.
	ProjectileRadius float32 = 4
	// ProjectileDamage — урон одного попадания.
	ProjectileDamage uint8 = 25

	// fireCooldownTicks — минимум тиков между выстрелами игрока (~0.3 с при 30 Гц).
	fireCooldownTicks = 9
	// projectileLifeTicks — сколько тиков живёт снаряд, если ни во что не попал (~2 с).
	projectileLifeTicks = 60
	// respawnDelayTicks — задержка от смерти до респауна (~2 с).
	respawnDelayTicks = 60
	// maxProjectiles — глобальный потолок числа снарядов (граница памяти/CPU;
	// снапшот и так режется до protocol.MaxEntities, а игроки идут в нём первыми).
	maxProjectiles = 128

	// historyLen — длина кольца истории позиций в тиках. Степень двойки: индекс
	// берётся маской, а не остатком. ~1.07 с при 30 Гц — с запасом над окном
	// перемотки.
	historyLen = 32
	histMask   = historyLen - 1
	// maxRewindTicks — потолок перемотки для lag compensation (~333 мс при 30 Гц).
	// Кламп окна — это анти-чит: клиент не может отмотать цель произвольно далеко в
	// прошлое. Окна хватает на типичную интерп-задержку клиента (~100 мс) + RTT/2.
	maxRewindTicks = 10
)

// histPos — снимок позиции игрока на одном тике. Кольцо таких снимков (Player.
// posHist) позволяет перематывать цели к тому, что видел стрелок.
type histPos struct{ x, y float32 }

// projectile — снаряд в полёте. Живёт на сервере; клиенту виден как сущность
// KindProjectile в снапшоте (интерполируется, не предсказывается).
type projectile struct {
	id     PlayerID // id в общем пространстве сущностей
	owner  PlayerID // кто выстрелил — по нему пропускаем самопопадание
	x, y   float32
	vx, vy float32
	life   int32 // тиков до самоуничтожения
	rewind int32 // на сколько тиков перематывать цели (lag comp); 0 — по настоящему
	team   uint8 // команда стрелка на момент выстрела (итер. 23) — дружественный огонь off
}

// EventKind помечает reliable-событие боя, накопленное за тик.
type EventKind uint8

const (
	EventHit EventKind = iota + 1
	EventDeath
	EventSpawn
	// EventKillstreak — игрок достиг вехи серии убийств (итерация 20). Идёт всем.
	EventKillstreak
)

// Event — reliable-событие боя. Комната переводит его в protocol-сообщение и
// маршрутизирует: Hit — участникам, Death/Spawn/Killstreak — всем.
type Event struct {
	Kind     EventKind
	Attacker PlayerID // Hit/Death: кто нанёс урон/убил
	Target   PlayerID // Hit/Death: жертва; Spawn: кто (пере)родился; Killstreak: кто на серии
	Damage   uint8    // Hit
	HP       uint8    // Hit: HP жертвы после урона
	X, Y     float32  // Spawn: точка появления
	Streak   uint16   // Killstreak: длина серии на момент вехи
}

// tryFire стреляет, если кулдаун игрока истёк и есть слот под снаряд. Кулдаун
// короче под бафом ускорения, а под бафом веера один выстрел даёт spreadCount
// снарядов симметричным веером (итерация 19). Направление — из угла прицела ввода,
// чистая детерминированная функция.
func (w *World) tryFire(p *Player, in protocol.Input) {
	if w.Tick < p.nextFireTick || len(w.projectiles) >= maxProjectiles {
		return
	}
	// Кулдаун: короче, пока активен баф ускорения (итерация 19).
	cd := uint32(fireCooldownTicks)
	if w.Tick < p.rapidUntil {
		cd = rapidFireCooldownTicks
	}
	p.nextFireTick = w.Tick + cd
	// Выстрел снимает окно неуязвимости (итерация 20): нельзя бить из-под щита.
	p.invulnUntil = 0
	// Перемотка целей (lag compensation): стрелок из-за интерполяции и RTT видит
	// цели в прошлом, поэтому фиксируем на снаряде постоянный сдвиг назад — тик,
	// который клиент видел (in.ViewTick), зажатый в окно. ViewTick==0 значит «клиент
	// ещё не получал снапшотов» — тогда бьём по настоящему (rewind 0). Один сдвиг на
	// весь выстрел, включая веер.
	rewind := int32(0)
	if in.ViewTick != 0 {
		rewind = clampRewind(int32(w.Tick) - int32(in.ViewTick))
	}
	ang := float64(in.AimRadians())
	// Баф веера: spreadCount снарядов симметрично вокруг угла прицела (итерация 19).
	if w.Tick < p.spreadUntil {
		start := ang - float64(spreadStepRad)*float64(spreadCount-1)/2
		for k := 0; k < spreadCount; k++ {
			w.spawnProjectile(p, start+float64(spreadStepRad)*float64(k), rewind)
		}
		return
	}
	w.spawnProjectile(p, ang, rewind)
}

// spawnProjectile порождает один снаряд из позиции игрока под углом ang с фиксацией
// перемотки rewind. Проверяет потолок снарядов и наличие свободного id — на пределе
// просто не добавляет (веер может выпустить меньше снарядов). Направление считается
// через math.Cos/Sin: они детерминированы в пределах одного бинарника/арки — этого
// хватает для реплеев на той же машине и для TestCombatDeterminism. Кросс-
// платформенная портируемость реплеев (запись на amd64, проигрыш на arm64)
// потребует убрать libm отсюда (таблица направлений по 16-битному Aim).
func (w *World) spawnProjectile(p *Player, ang float64, rewind int32) {
	if len(w.projectiles) >= maxProjectiles {
		return
	}
	id, err := w.allocID()
	if err != nil {
		return // свободных id нет — просто не стреляем
	}
	dx, dy := float32(math.Cos(ang)), float32(math.Sin(ang))
	w.projectiles = append(w.projectiles, projectile{
		id:     id,
		owner:  p.ID,
		x:      p.X + dx*(PlayerRadius+ProjectileRadius+1),
		y:      p.Y + dy*(PlayerRadius+ProjectileRadius+1),
		vx:     dx * ProjectileSpeed,
		vy:     dy * ProjectileSpeed,
		life:   projectileLifeTicks,
		rewind: rewind,
		team:   p.team, // команда стрелка — для дружественного огня (итер. 23)
	})
}

// clampRewind зажимает сдвиг перемотки в [0, maxRewindTicks]. Нижняя граница
// гасит отрицательный сдвиг (клиент прислал ViewTick из будущего — рассинхрон
// часов или чит); верхняя — анти-чит по окну.
func clampRewind(d int32) int32 {
	if d < 0 {
		return 0
	}
	if d > maxRewindTicks {
		return maxRewindTicks
	}
	return d
}

// hitGrid — детерминированная широкофазная сетка живых игроков для сужения
// коллизии снаряд×игрок (итерация 8). Клетка держит id игроков, чьё окно
// перемотки её пересекает; снаряд запрашивает клетки вокруг своего сегмента и
// уточняет попадание лишь по кандидатам — O(снаряды×кандидаты) вместо
// O(снаряды×игроки). Геометрия клеток общая с AOI-сеткой (cellCoord/aoiCols).
//
// Транзиентный индекс: строится в начале stepProjectiles, в Checksum НЕ входит
// (производное от уже-хешируемых позиций/истории). Клетки — переиспользуемые
// срезы: после прогрева build/query zero-alloc.
type hitGrid struct {
	// cells[cy*aoiCols+cx] держит id игроков, чей rewindAABB пересекает клетку.
	cells [aoiNumCells][]PlayerID
}

// build раскладывает живых игроков по клеткам, ПОКРЫВАЯ окно перемотки: игрок
// вставляется во все клетки, пересекающие AABB его позиций за [0, maxRewindTicks]
// тиков (rewindAABB). Так широкая фаза остаётся точным надмножеством независимо от
// скорости движения — любую позицию, к которой targetPos может перемотать цель,
// накрывает вставленная клетка, поэтому запросу достаточно расшириться лишь на
// радиус коллизии. Мёртвых не кладём: их пропускает и брутфорс.
func (g *hitGrid) build(w *World) {
	for i := range g.cells {
		g.cells[i] = g.cells[i][:0]
	}
	for _, id := range w.order {
		p := w.players[id]
		if p.dead {
			continue
		}
		minX, minY, maxX, maxY := p.rewindAABB(w.Tick)
		x0, x1 := cellCoord(minX), cellCoord(maxX)
		y0, y1 := cellCoord(minY), cellCoord(maxY)
		for cy := y0; cy <= y1; cy++ {
			row := cy * aoiCols
			for cx := x0; cx <= x1; cx++ {
				g.cells[row+cx] = append(g.cells[row+cx], id)
			}
		}
	}
}

// query дописывает в dst id игроков всех клеток, пересекающих коробку
// [minX,maxX]×[minY,maxY], и возвращает расширенный срез. dst передаётся
// переиспользуемым (обычно dst[:0]). Один игрок может попасть в несколько клеток
// (окно перемотки шире клетки) — дубли безвредны: findHit идемпотентен по id.
func (g *hitGrid) query(minX, minY, maxX, maxY float32, dst []PlayerID) []PlayerID {
	x0, x1 := cellCoord(minX), cellCoord(maxX)
	y0, y1 := cellCoord(minY), cellCoord(maxY)
	for cy := y0; cy <= y1; cy++ {
		row := cy * aoiCols
		for cx := x0; cx <= x1; cx++ {
			dst = append(dst, g.cells[row+cx]...)
		}
	}
	return dst
}

// rewindAABB возвращает AABB всех позиций, к которым lag comp может перемотать
// игрока в этот тик: текущая позиция плюс кольцо истории за [1, maxRewindTicks]
// тиков. Именно это множество читает targetPos (rewind∈[0,maxRewindTicks] после
// clampRewind), поэтому широкая фаза, накрыв его клетками, не теряет ни одного
// возможного попадания. Индексация истории — та же (w.Tick-r)&histMask, что в
// targetPos, поэтому AABB согласован с ней даже на ранних тиках (кольцо заполнено
// initHistory).
func (p *Player) rewindAABB(tick uint32) (minX, minY, maxX, maxY float32) {
	minX, minY, maxX, maxY = p.X, p.Y, p.X, p.Y
	for r := uint32(1); r <= maxRewindTicks; r++ {
		h := p.posHist[(tick-r)&histMask]
		minX, maxX = min(minX, h.x), max(maxX, h.x)
		minY, maxY = min(minY, h.y), max(maxY, h.y)
	}
	return
}

// findHit возвращает жертву снаряда на отрезке (pr.x,pr.y)→(nx,ny): игрока с
// МЛАДШИМ id среди всех геометрически попавших. Это тот же исход, что даёт
// брутфорс (первый хит при обходе по возрастанию id), но по кандидатам широкой
// фазы. Кандидаты не отсортированы, поэтому победитель выбирается явным минимумом
// id, а не порядком обхода — надмножество и дубли безвредны. Эквивалентность
// брутфорсу стережёт TestBroadPhaseAgreesWithBruteforce.
func (w *World) findHit(pr *projectile, nx, ny float32) *Player {
	// Коробка сегмента, расширенная только на радиус коллизии: окно перемотки уже
	// покрыто при вставке игроков (rewindAABB), здесь запас — лишь радиус попадания.
	const r = PlayerRadius + ProjectileRadius
	minX, maxX := min(pr.x, nx)-r, max(pr.x, nx)+r
	minY, maxY := min(pr.y, ny)-r, max(pr.y, ny)+r
	w.hitCand = w.hitGrid.query(minX, minY, maxX, maxY, w.hitCand[:0])

	var best *Player
	for _, id := range w.hitCand {
		if id == pr.owner {
			continue
		}
		if best != nil && id >= best.ID {
			continue // младший id уже найден — этот не может его побить (и дубли тоже)
		}
		tgt := w.players[id]
		if tgt.dead || tgt.invulnerable(w.Tick) {
			continue // мёртв или под окном неуязвимости (итер. 20) — снаряд проходит насквозь
		}
		if w.teamMode && tgt.team == pr.team {
			continue // дружественный огонь выключен (итер. 23) — снаряд проходит сквозь союзника
		}
		// Цель перематывается к тому, что видел стрелок (lag comp). Живость не
		// перематываем — сейчас-мёртвых пропускаем: respawnDelayTicks намного больше
		// окна перемотки, поэтому статус жив/мёртв в пределах окна почти не меняется.
		tx, ty := w.targetPos(tgt, pr.rewind)
		if segmentCircleHit(pr.x, pr.y, nx, ny, tx, ty, r) {
			best = tgt
		}
	}
	return best
}

// stepProjectiles продвигает снаряды на dt (длительность тика), ловит попадания
// свит-проверкой (чтобы быстрый снаряд не проскочил сквозь игрока за тик) и
// компактирует слайс на месте. Цели ищутся через широкофазную сетку (findHit);
// сетка строится один раз на тик из живых игроков.
func (w *World) stepProjectiles(dt float32) {
	w.hitGrid.build(w)

	j := 0
	for i := range w.projectiles {
		pr := w.projectiles[i]
		pr.life--
		nx, ny := pr.x+pr.vx*dt, pr.y+pr.vy*dt

		// Стена на пути гасит снаряд (итерация 10). Сегмент проверки попадания
		// подрезаем до точки входа в стену: цель ПЕРЕД стеной урон получает, ЗА
		// стеной — нет.
		wallHit, ex, ey := segmentWallHit(pr.x, pr.y, nx, ny)
		if !wallHit {
			ex, ey = nx, ny
		}
		victim := w.findHit(&pr, ex, ey)
		if victim != nil {
			w.applyDamage(victim, pr.owner, ProjectileDamage)
		}
		if victim != nil || wallHit || pr.life <= 0 || outOfBounds(nx, ny) {
			continue // не переносим в компактированный слайс (попал/врезался/истёк/за картой)
		}
		pr.x, pr.y = nx, ny
		w.projectiles[j] = pr
		j++
	}
	w.projectiles = w.projectiles[:j]
}

// applyDamage наносит урон, эмитит Hit и — если HP дошёл до нуля — помечает
// смерть, назначает время респауна и эмитит Death.
func (w *World) applyDamage(victim *Player, attacker PlayerID, dmg uint8) {
	if victim.HP == 0 {
		return
	}
	if dmg >= victim.HP {
		victim.HP = 0
	} else {
		victim.HP -= dmg
	}
	w.events = append(w.events, Event{
		Kind: EventHit, Attacker: attacker, Target: victim.ID, Damage: dmg, HP: victim.HP,
	})
	if victim.HP == 0 {
		victim.dead = true
		victim.respawnAt = w.Tick + respawnDelayTicks
		victim.VX, victim.VY = 0, 0
		w.events = append(w.events, Event{Kind: EventDeath, Attacker: attacker, Target: victim.ID})
		// Счёт матча (итерация 14): фраг атакующему (кроме суицида), смерть жертве.
		victim.Deaths++
		victim.streak = 0 // смерть обрывает серию убийств (итерация 20)
		if attacker != victim.ID {
			if a := w.players[attacker]; a != nil {
				a.Kills++
				w.recordKill(a) // серия + награда на вехе (итерация 20)
			}
		}
	}
}

// respawn возрождает игрока в новой точке спавна с полным HP и эмитит Spawn.
func (w *World) respawn(p *Player) {
	p.MoveState = w.spawnPoint()
	p.HP = 100
	p.dead = false
	p.nextFireTick = 0
	p.rapidUntil = 0 // свежая жизнь — без буфов пикапов (итерация 19)
	p.spreadUntil = 0
	p.invulnUntil = w.Tick + spawnInvulnTicks // окно неуязвимости после респауна (итерация 20)
	p.initHistory()                           // новая точка — чистим историю, чтобы не отмотать к позиции до смерти
	w.events = append(w.events, Event{Kind: EventSpawn, Target: p.ID, X: p.X, Y: p.Y})
}

// targetPos возвращает позицию цели для проверки попадания. rewind==0 — настоящее
// (компенсация выключена); rewind>0 — позиция из кольца истории на rewind тиков
// назад, то есть там, где стрелок видел цель в момент выстрела. Индекс берётся
// маской по метке (w.Tick - rewind) — той же, под которой позиция записана.
func (w *World) targetPos(tgt *Player, rewind int32) (float32, float32) {
	if rewind <= 0 {
		return tgt.X, tgt.Y
	}
	h := tgt.posHist[(w.Tick-uint32(rewind))&histMask]
	return h.x, h.y
}

// recordHistory сохраняет позицию каждого игрока в кольцо под меткой текущего
// w.Tick (после инкремента в Step) — той же меткой, что несёт снапшот этого тика.
// На этом кольце стоит перемотка целей. Zero-alloc: posHist — фиксированный
// массив внутри Player, обход — по отсортированному order.
func (w *World) recordHistory() {
	slot := w.Tick & histMask
	for _, id := range w.order {
		p := w.players[id]
		p.posHist[slot] = histPos{p.X, p.Y}
	}
}

// initHistory заполняет всё кольцо истории текущей позицией игрока. Зовётся при
// входе в мир и при респауне, чтобы перемотка не читала устаревшую позицию (до
// смерти) или мусор до накопления истории.
func (p *Player) initHistory() {
	h := histPos{p.X, p.Y}
	for i := range p.posHist {
		p.posHist[i] = h
	}
}

// segmentCircleHit сообщает, подходит ли отрезок AB ближе r к точке C. Это
// свит-проверка коллизии снаряда (путь за тик) с кругом игрока.
func segmentCircleHit(ax, ay, bx, by, cx, cy, r float32) bool {
	dx, dy := bx-ax, by-ay
	seg2 := dx*dx + dy*dy
	var t float32 // параметр ближайшей к C точки отрезка, зажатый в [0,1]
	if seg2 > 0 {
		t = clamp(((cx-ax)*dx+(cy-ay)*dy)/seg2, 0, 1)
	}
	px, py := ax+t*dx, ay+t*dy
	ddx, ddy := px-cx, py-cy
	return ddx*ddx+ddy*ddy <= r*r
}

// outOfBounds сообщает, вышла ли точка за пределы карты.
func outOfBounds(x, y float32) bool {
	return x < 0 || y < 0 || x > MapSize || y > MapSize
}
