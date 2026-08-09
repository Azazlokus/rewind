package game

import "arena/internal/protocol"

// Система оружия (итерация 26): у игрока есть выбранный тип оружия, определяющий
// картину выстрела — число дробин, разброс, урон, кулдаун, скорость снаряда и
// (для ракеты) сплэш по площади. Игрок переключает оружие клавишами; выбор едет
// в старших битах Input.Buttons (WeaponSelect), поэтому формат провода ввода не
// меняется (см. protocol.go).
//
// Что где живёт:
//   - Player.weapon — ВЫБРАННОЕ оружие. Влияет на будущий бой (что породит выстрел),
//     поэтому ВХОДИТ в Checksum. Ставится обработкой ввода в Step, переживает респаун.
//   - projectile.weapon — оружие, которым СТРЕЛЯЛИ. Снаряд несёт его с собой (стрелок
//     мог сменить оружие, пока снаряд летит), урон/сплэш при попадании берутся из
//     спека этого оружия. Тоже в Checksum.
//   - weaponSpecs — фиксированная таблица характеристик. Как walls/pickupSpots она
//     одинакова во всех мирах, поэтому в Checksum НЕ входит; в хэш идут уже её
//     следствия (vx/vy снаряда, изменения HP). Меняя таблицу, помнить: реплеи,
//     записанные на старой таблице, воспроизведутся иначе (это баланс, не провод).
//
// Взаимодействие с бафами пикапов (итерация 19): баф ускорения (rapidUntil) режет
// кулдаун любого оружия; баф веера (spreadUntil) превращает ОДНОДРОБинное оружие в
// веер (для дробовика, у которого дробин и так много, добавки нет) — так прежнее
// поведение «пистолет + веер = 3 снаряда» сохраняется точь-в-точь.

// weaponKind — тип оружия. Значения зеркалит web/game.js (WEAPONS). 0 не используется
// (невалиден: WeaponSelect==0 значит «не менять»).
type weaponKind uint8

const (
	weaponPistol  weaponKind = iota + 1 // 1 — пистолет: одиночный, базовый
	weaponShotgun                       // 2 — дробовик: веер дробин, урон в упор
	weaponSniper                        // 3 — снайперка: одна быстрая пуля, большой урон
	weaponRocket                        // 4 — ракета: сплэш по площади при попадании
)

// weaponKindCount — число типов оружия; валидный WeaponSelect ∈ [1, weaponKindCount].
const weaponKindCount = 4

// weaponSpec — характеристики одного оружия. Урон/скорость/сплэш — мировые
// величины; кулдаун — в тиках (30 Гц).
type weaponSpec struct {
	pellets      int     // сколько снарядов даёт один выстрел (веер симметричный)
	spreadRad    float32 // угловой шаг между соседними дробинами (радианы), при pellets>1
	damage       uint8   // прямой урон одного снаряда (для сплэш-оружия 0 — весь урон в сплэше)
	cooldown     uint32  // минимум тиков между выстрелами
	speed        float32 // скорость снаряда, юнитов/с
	splashRadius float32 // радиус сплэша (0 — без сплэша: обычный прямой урон)
	splashDamage uint8   // урон в эпицентре сплэша, линейно спадает к краю радиуса
}

// weaponSpecs — таблица характеристик, индексируется weaponKind. Индекс 0 — заглушка
// (совпадает с пистолетом), чтобы невалидный weaponKind не выходил за границы и вёл
// себя как пистолет. Пистолет намеренно повторяет исторические боевые константы
// (ProjectileDamage/fireCooldownTicks/ProjectileSpeed) — прежние бои/тесты без смены
// оружия ведут себя ровно как раньше.
var weaponSpecs = [weaponKindCount + 1]weaponSpec{
	weaponPistol:  {pellets: 1, damage: ProjectileDamage, cooldown: fireCooldownTicks, speed: ProjectileSpeed},
	0:             {pellets: 1, damage: ProjectileDamage, cooldown: fireCooldownTicks, speed: ProjectileSpeed},
	weaponShotgun: {pellets: 5, spreadRad: 0.16, damage: 11, cooldown: 18, speed: 620},
	weaponSniper:  {pellets: 1, damage: 80, cooldown: 33, speed: 1500},
	weaponRocket:  {pellets: 1, damage: 0, cooldown: 39, speed: 480, splashRadius: 130, splashDamage: 65},
}

// weaponSpecOf возвращает спек оружия, зажимая невалидный тип к пистолету (индекс 0).
func weaponSpecOf(k weaponKind) weaponSpec {
	if k > weaponKindCount {
		return weaponSpecs[0]
	}
	return weaponSpecs[k]
}

// WeaponsDirty сообщает, сменил ли кто-то оружие за последний Step (итер. 26). Комната
// по нему решает, слать ли MsgWeaponState. Networking, не Checksum.
func (w *World) WeaponsDirty() bool { return w.weaponsDirty }

// AppendWeapons дописывает оружие каждого игрока в dst (для рассылки MsgWeaponState) в
// порядке order и возвращает расширенный срез. Вызывающий передаёт переиспользуемый
// срез, чтобы не аллоцировать. Networking, не Checksum — как AppendPickups.
func (w *World) AppendWeapons(dst []protocol.WeaponInfo) []protocol.WeaponInfo {
	for _, id := range w.order {
		p := w.players[id]
		dst = append(dst, protocol.WeaponInfo{ID: uint16(p.ID), Weapon: uint8(p.weapon)})
	}
	return dst
}
