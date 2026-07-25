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
)

// projectile — снаряд в полёте. Живёт на сервере; клиенту виден как сущность
// KindProjectile в снапшоте (интерполируется, не предсказывается).
type projectile struct {
	id     PlayerID // id в общем пространстве сущностей
	owner  PlayerID // кто выстрелил — по нему пропускаем самопопадание
	x, y   float32
	vx, vy float32
	life   int32 // тиков до самоуничтожения
}

// EventKind помечает reliable-событие боя, накопленное за тик.
type EventKind uint8

const (
	EventHit EventKind = iota + 1
	EventDeath
	EventSpawn
)

// Event — reliable-событие боя. Комната переводит его в protocol-сообщение и
// маршрутизирует: Hit — участникам, Death/Spawn — всем.
type Event struct {
	Kind     EventKind
	Attacker PlayerID // Hit/Death: кто нанёс урон/убил
	Target   PlayerID // Hit/Death: жертва; Spawn: кто (пере)родился
	Damage   uint8    // Hit
	HP       uint8    // Hit: HP жертвы после урона
	X, Y     float32  // Spawn: точка появления
}

// tryFire порождает снаряд, если кулдаун игрока истёк и есть свободный id и слот.
// Направление берётся из угла прицела ввода — чистая детерминированная функция.
func (w *World) tryFire(p *Player, in protocol.Input) {
	if w.Tick < p.nextFireTick || len(w.projectiles) >= maxProjectiles {
		return
	}
	id, err := w.allocID()
	if err != nil {
		return // свободных id нет — просто не стреляем
	}
	p.nextFireTick = w.Tick + fireCooldownTicks
	// Направление из угла прицела. math.Cos/Sin детерминированы в пределах одного
	// бинарника/арки — этого хватает для реплеев на той же машине и для
	// TestCombatDeterminism. Кросс-платформенная портируемость реплеев (запись на
	// amd64, проигрыш на arm64) потребует убрать libm отсюда (таблица направлений
	// по 16-битному Aim) — задел на итерацию 6.
	ang := float64(in.AimRadians())
	dx, dy := float32(math.Cos(ang)), float32(math.Sin(ang))
	w.projectiles = append(w.projectiles, projectile{
		id:    id,
		owner: p.ID,
		x:     p.X + dx*(PlayerRadius+ProjectileRadius+1),
		y:     p.Y + dy*(PlayerRadius+ProjectileRadius+1),
		vx:    dx * ProjectileSpeed,
		vy:    dy * ProjectileSpeed,
		life:  projectileLifeTicks,
	})
}

// stepProjectiles продвигает снаряды на dt (длительность тика), ловит попадания
// свит-проверкой (чтобы быстрый снаряд не проскочил сквозь игрока за тик) и
// компактирует слайс на месте. Обход игроков — по отсортированному order.
func (w *World) stepProjectiles(dt float32) {
	j := 0
	for i := range w.projectiles {
		pr := w.projectiles[i]
		pr.life--
		nx, ny := pr.x+pr.vx*dt, pr.y+pr.vy*dt

		hit := false
		for _, id := range w.order {
			tgt := w.players[id]
			if tgt.dead || tgt.ID == pr.owner {
				continue
			}
			if segmentCircleHit(pr.x, pr.y, nx, ny, tgt.X, tgt.Y, PlayerRadius+ProjectileRadius) {
				w.applyDamage(tgt, pr.owner, ProjectileDamage)
				hit = true
				break
			}
		}
		if hit || pr.life <= 0 || outOfBounds(nx, ny) {
			continue // не переносим в компактированный слайс
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
	}
}

// respawn возрождает игрока в новой точке спавна с полным HP и эмитит Spawn.
func (w *World) respawn(p *Player) {
	p.MoveState = w.spawnPoint()
	p.HP = 100
	p.dead = false
	p.nextFireTick = 0
	w.events = append(w.events, Event{Kind: EventSpawn, Target: p.ID, X: p.X, Y: p.Y})
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
