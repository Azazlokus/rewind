package game

import (
	rand "math/rand/v2"
	"testing"

	"arena/internal/protocol"
)

const dt = 1.0 / 30

// wsShift — сдвиг поля выбора оружия в Buttons (зеркалит protocol.weaponSelectShift,
// который неэкспортируемый; здесь достаточно литерала для конструирования вводов).
const wsShift = 5

// switchWeapon — ввод «сменить оружие» без выстрела (старшие биты Buttons).
func switchWeapon(seq uint32, wk weaponKind) protocol.Input {
	return protocol.Input{Seq: seq, Buttons: uint8(wk) << wsShift}
}

// fireWeapon — ввод «сменить оружие И выстрелить» под углом rad. Смена оружия в Step
// обрабатывается до выстрела, поэтому один такой ввод стреляет уже новым оружием.
func fireWeapon(seq uint32, rad float64, wk weaponKind) protocol.Input {
	return protocol.Input{
		Seq:     seq,
		Buttons: protocol.BtnFire | uint8(wk)<<wsShift,
		Aim:     protocol.AimFromRadians(rad),
	}
}

// TestWeaponSelectSwitches: валидный выбор меняет оружие и взводит weaponsDirty; ввод
// без выбора его не трогает.
func TestWeaponSelectSwitches(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	if p.weapon != weaponPistol {
		t.Fatalf("start weapon=%d, want pistol(%d)", p.weapon, weaponPistol)
	}
	w.EnqueueInput(p.ID, switchWeapon(1, weaponSniper))
	w.Step(dt)
	if p.weapon != weaponSniper {
		t.Fatalf("after switch weapon=%d, want sniper(%d)", p.weapon, weaponSniper)
	}
	if !w.WeaponsDirty() {
		t.Fatal("WeaponsDirty should be set after a weapon switch")
	}
	// Следующий тик без смены — dirty сброшен.
	w.EnqueueInput(p.ID, protocol.Input{Seq: 2})
	w.Step(dt)
	if w.WeaponsDirty() {
		t.Fatal("WeaponsDirty should reset when nothing changes")
	}
}

// TestWeaponSelectIgnoresInvalid: выбор вне диапазона оружия (0 или >count) не меняет
// оружие.
func TestWeaponSelectIgnoresInvalid(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	// sel 0 — «не менять».
	w.EnqueueInput(p.ID, protocol.Input{Seq: 1})
	w.Step(dt)
	// sel 5,6,7 — вне диапазона (weaponKindCount == 4).
	for _, bad := range []uint8{5, 6, 7} {
		w.EnqueueInput(p.ID, protocol.Input{Seq: uint32(bad) + 1, Buttons: bad << wsShift})
		w.Step(dt)
	}
	if p.weapon != weaponPistol {
		t.Fatalf("weapon changed to %d on invalid selects, want pistol", p.weapon)
	}
}

// TestWeaponPelletCounts: один выстрел даёт столько снарядов, сколько дробин у оружия.
func TestWeaponPelletCounts(t *testing.T) {
	cases := []struct {
		wk   weaponKind
		want int
	}{
		{weaponPistol, 1},
		{weaponShotgun, 5},
		{weaponSniper, 1},
		{weaponRocket, 1},
	}
	for _, c := range cases {
		w := NewWorld(1)
		p, _ := w.AddPlayer("p")
		place(p, 1000, 1000)
		w.EnqueueInput(p.ID, fireWeapon(1, 0, c.wk))
		w.Step(dt)
		if len(w.projectiles) != c.want {
			t.Fatalf("weapon %d: %d projectiles, want %d", c.wk, len(w.projectiles), c.want)
		}
	}
}

// TestWeaponCooldowns: кулдаун выстрела берётся из спека оружия.
func TestWeaponCooldowns(t *testing.T) {
	cases := []struct {
		wk   weaponKind
		want uint32
	}{
		{weaponPistol, fireCooldownTicks},
		{weaponShotgun, 18},
		{weaponSniper, 33},
		{weaponRocket, 39},
	}
	for _, c := range cases {
		w := NewWorld(1)
		p, _ := w.AddPlayer("p")
		place(p, 1000, 1000)
		w.EnqueueInput(p.ID, fireWeapon(1, 0, c.wk))
		w.Step(dt) // tryFire на тике 0 ставит nextFireTick = 0 + cooldown
		if p.nextFireTick != c.want {
			t.Fatalf("weapon %d: nextFireTick=%d, want %d", c.wk, p.nextFireTick, c.want)
		}
	}
}

// TestSniperDamage: попадание снайперки снимает урон из её спека, а не пистолетный.
func TestSniperDamage(t *testing.T) {
	w := NewWorld(1)
	shooter, _ := w.AddPlayer("s")
	target, _ := w.AddPlayer("t")
	place(shooter, 1000, 1000)
	place(target, 1100, 1000)

	w.EnqueueInput(shooter.ID, fireWeapon(1, 0, weaponSniper))
	w.Step(dt)
	for range 20 {
		if target.HP != 100 {
			break
		}
		w.Step(dt)
	}
	if want := uint8(100 - weaponSpecs[weaponSniper].damage); target.HP != want {
		t.Fatalf("sniper hit: target HP=%d, want %d", target.HP, want)
	}
}

// TestRocketSplashDamagesArea: ракета детонирует на прямом попадании, задевает
// соседей по площади (с фоллофом), но НЕ владельца.
func TestRocketSplashDamagesArea(t *testing.T) {
	w := NewWorld(1)
	shooter, _ := w.AddPlayer("s")
	target, _ := w.AddPlayer("t")
	bystander, _ := w.AddPlayer("b")
	place(shooter, 1000, 1000)
	place(target, 1100, 1000)    // прямо по курсу — прямое попадание
	place(bystander, 1100, 1080) // рядом с эпицентром, вне прямой траектории

	w.EnqueueInput(shooter.ID, fireWeapon(1, 0, weaponRocket))
	w.Step(dt)
	for range 20 {
		if len(w.projectiles) == 0 {
			break
		}
		w.Step(dt)
	}
	if target.HP == 100 {
		t.Fatal("rocket direct target took no damage")
	}
	if bystander.HP == 100 {
		t.Fatal("rocket splash bystander took no damage")
	}
	if shooter.HP != 100 {
		t.Fatalf("rocket owner self-damaged: HP=%d, want 100", shooter.HP)
	}
	// Фоллоф: цель у эпицентра теряет больше, чем сосед на краю.
	if 100-target.HP <= 100-bystander.HP {
		t.Fatalf("expected falloff: target dmg %d should exceed bystander dmg %d",
			100-target.HP, 100-bystander.HP)
	}
}

// TestRocketSplashRespectsTeamAndInvuln: сплэш пропускает союзника (командный режим)
// и неуязвимого игрока.
func TestRocketSplashRespectsTeamAndInvuln(t *testing.T) {
	w := NewWorld(1)
	w.SetTeamMode(true)
	shooter, _ := w.AddPlayer("s") // team 0
	target, _ := w.AddPlayer("t")  // team 1
	ally, _ := w.AddPlayer("a")    // team 0 (как shooter)
	invuln, _ := w.AddPlayer("i")  // team 1, но под щитом
	if shooter.team != ally.team {
		t.Fatalf("expected shooter(%d) and ally(%d) on same team", shooter.team, ally.team)
	}
	place(shooter, 1000, 1000)
	place(target, 1100, 1000)
	place(ally, 1100, 1070)
	place(invuln, 1100, 930)
	invuln.invulnUntil = w.Tick + 500

	w.EnqueueInput(shooter.ID, fireWeapon(1, 0, weaponRocket))
	w.Step(dt)
	for range 20 {
		if len(w.projectiles) == 0 {
			break
		}
		w.Step(dt)
	}
	if target.HP == 100 {
		t.Fatal("enemy target should have taken splash")
	}
	if ally.HP != 100 {
		t.Fatalf("teammate should not take splash: HP=%d", ally.HP)
	}
	if invuln.HP != 100 {
		t.Fatalf("invulnerable player should not take splash: HP=%d", invuln.HP)
	}
}

// TestRocketSplashOnWall: ракета взрывается о стену и задевает игрока рядом с точкой
// удара (не на линии выстрела — прямого попадания нет).
func TestRocketSplashOnWall(t *testing.T) {
	w := NewWorld(1)
	shooter, _ := w.AddPlayer("s")
	bystander, _ := w.AddPlayer("b")
	// Стена {1500,1500,1620,1900}. Стреляем в неё вдоль y=1600; сосед в стороне.
	place(shooter, 1350, 1600)
	place(bystander, 1490, 1680)

	w.EnqueueInput(shooter.ID, fireWeapon(1, 0, weaponRocket))
	w.Step(dt)
	for range 25 {
		if len(w.projectiles) == 0 {
			break
		}
		w.Step(dt)
	}
	if len(w.projectiles) != 0 {
		t.Fatalf("rocket should have detonated on the wall, %d remain", len(w.projectiles))
	}
	if bystander.HP == 100 {
		t.Fatal("bystander next to wall impact took no splash")
	}
}

// TestWeaponPersistsAcrossRespawn: выбранное оружие переживает смерть/респаун (буфы
// пикапов при этом чистятся — оружие нет).
func TestWeaponPersistsAcrossRespawn(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	w.EnqueueInput(p.ID, switchWeapon(1, weaponSniper))
	w.Step(dt)
	// Форсируем смерть с уже наступившим сроком респауна.
	p.dead = true
	p.respawnAt = w.Tick
	w.Step(dt)
	if p.dead {
		t.Fatal("player should have respawned")
	}
	if p.weapon != weaponSniper {
		t.Fatalf("weapon after respawn=%d, want sniper(%d)", p.weapon, weaponSniper)
	}
}

// TestWeaponInChecksum: смена оружия меняет Checksum (оружие — будущее боевое состояние).
func TestWeaponInChecksum(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	w.Step(dt)
	before := w.Checksum()
	p.weapon = weaponRocket
	if after := w.Checksum(); after == before {
		t.Fatal("changing Player.weapon must change Checksum")
	}
}

// TestWeaponDeterminism: два мира с одним seed и одной лентой вводов (движение, смена
// оружия, стрельба ракетами со сплэшем) дают равный Checksum на каждом тике.
func TestWeaponDeterminism(t *testing.T) {
	build := func() (*World, []*Player) {
		w := NewWorld(7)
		ps := make([]*Player, 3)
		for i := range ps {
			p, err := w.AddPlayer("p")
			if err != nil {
				t.Fatal(err)
			}
			ps[i] = p
		}
		return w, ps
	}
	wa, _ := build()
	wb, _ := build()
	if wa.Checksum() != wb.Checksum() {
		t.Fatal("worlds diverged before stepping")
	}

	// Псевдослучайная, но фиксированная лента вводов — одинаковая для обоих миров.
	src := rand.New(rand.NewPCG(42, 42))
	ids := wa.order // те же id в обоих мирах
	for tick := range 400 {
		for _, id := range ids {
			b := uint8(src.UintN(16)) // WASD + fire в младших 5 битах
			wk := weaponKind(1 + src.UintN(weaponKindCount))
			b |= uint8(wk) << wsShift
			in := protocol.Input{
				Seq:      uint32(tick + 1),
				Buttons:  b,
				Aim:      uint16(src.UintN(65536)),
				ViewTick: uint32(tick),
			}
			wa.EnqueueInput(id, in)
			wb.EnqueueInput(id, in)
		}
		wa.Step(dt)
		wb.Step(dt)
		if wa.Checksum() != wb.Checksum() {
			t.Fatalf("worlds diverged at tick %d", tick)
		}
	}
}
