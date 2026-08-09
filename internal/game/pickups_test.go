package game

import (
	"testing"

	"arena/internal/protocol"
)

const testDt = 1.0 / 30

// activatePickup ставит в точке i активный пикап типа k в обход розыгрыша — чтобы
// тест эффекта не зависел от rng. Поля пакета доступны из теста.
func activatePickup(w *World, i int, k pickupKind) {
	w.pickups[i] = pickupState{active: true, kind: k}
}

// TestPickupsSpawnAtStart: первый Step активирует все точки, розыгрывая валидный
// тип, и взводит pickupsDirty; AppendPickups отдаёт их по порядку индекса.
func TestPickupsSpawnAtStart(t *testing.T) {
	w := NewWorld(1)
	w.Step(testDt) // тик 0 → активация всех точек
	for i := range w.pickups {
		if !w.pickups[i].active {
			t.Fatalf("spot %d inactive after first step", i)
		}
		if k := w.pickups[i].kind; k < pickupMedkit || k > pickupSpread {
			t.Fatalf("spot %d has invalid kind %d", i, k)
		}
	}
	if !w.PickupsDirty() {
		t.Fatal("PickupsDirty must be set after spawning pickups")
	}
	got := w.AppendPickups(nil)
	if len(got) != len(pickupSpots) {
		t.Fatalf("AppendPickups: got %d active, want %d", len(got), len(pickupSpots))
	}
	for i, pk := range got {
		if int(pk.Spot) != i {
			t.Fatalf("AppendPickups[%d]: spot %d, want %d", i, pk.Spot, i)
		}
	}
}

// TestPickupMedkitHeals: аптечка лечит на medkitHeal с клампом по 100.
func TestPickupMedkitHeals(t *testing.T) {
	cases := []struct{ before, after uint8 }{
		{40, 40 + medkitHeal}, // обычный хил
		{80, 100},             // кламп: 80+50 > 100
	}
	for _, c := range cases {
		w := NewWorld(1)
		p, _ := w.AddPlayer("p")
		place(p, pickupSpots[0].x, pickupSpots[0].y)
		p.HP = c.before
		activatePickup(w, 0, pickupMedkit)
		w.Step(testDt) // подбор в этом же тике (точка уже активна)
		if p.HP != c.after {
			t.Fatalf("medkit heal from %d: got HP %d, want %d", c.before, p.HP, c.after)
		}
		if w.pickups[0].active {
			t.Fatal("spot must be empty after pickup")
		}
		if w.pickups[0].readyAt != w.Tick-1+pickupRespawnTicks {
			t.Fatalf("respawn timer: got %d, want %d", w.pickups[0].readyAt, w.Tick-1+pickupRespawnTicks)
		}
	}
}

// TestPickupRapidShortensCooldown: под бафом ускорения игрок стреляет чаще, чем
// позволяет обычный кулдаун fireCooldownTicks.
func TestPickupRapidShortensCooldown(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	activatePickup(w, 0, pickupRapid)
	place(p, pickupSpots[0].x, pickupSpots[0].y)
	w.Step(testDt) // подбор бафа ускорения
	if p.rapidUntil == 0 {
		t.Fatal("rapid buff not applied")
	}
	place(p, 1000, 1000) // подальше от точек, чтобы не хватать другие

	// Стреляем каждый тик; под rapidFireCooldownTicks=3 за 12 тиков успеем ~4 раза,
	// тогда как обычный кулдаун 9 дал бы не больше 2.
	seq := uint32(10)
	for range 12 {
		seq++
		w.EnqueueInput(p.ID, fireInput(seq, 0))
		w.Step(testDt)
	}
	if len(w.projectiles) < 3 {
		t.Fatalf("rapid fire: got %d projectiles in 12 ticks, want >=3 (cooldown %d)",
			len(w.projectiles), rapidFireCooldownTicks)
	}
}

// TestPickupSpreadFiresFan: под бафом веера один выстрел даёт spreadCount снарядов.
func TestPickupSpreadFiresFan(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, pickupSpots[0].x, pickupSpots[0].y)
	activatePickup(w, 0, pickupSpread)
	w.Step(testDt) // подбор бафа веера
	place(p, 1000, 1000)

	w.EnqueueInput(p.ID, fireInput(1, 0))
	w.Step(testDt)
	if len(w.projectiles) != spreadCount {
		t.Fatalf("spread fire: got %d projectiles, want %d", len(w.projectiles), spreadCount)
	}
}

// TestPickupBuffsClearOnRespawn: свежая жизнь обнуляет буфы пикапов.
func TestPickupBuffsClearOnRespawn(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	p.rapidUntil = 10000
	p.spreadUntil = 10000
	p.dead = true
	p.respawnAt = w.Tick // респаун сработает на ближайшем Step
	w.Step(testDt)
	if p.dead {
		t.Fatal("player should have respawned")
	}
	if p.rapidUntil != 0 || p.spreadUntil != 0 {
		t.Fatalf("buffs survived respawn: rapid=%d spread=%d", p.rapidUntil, p.spreadUntil)
	}
}

// TestPickupRespawnsAfterDelay: подобранная точка снова появляется через
// pickupRespawnTicks.
func TestPickupRespawnsAfterDelay(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, pickupSpots[0].x, pickupSpots[0].y)
	activatePickup(w, 0, pickupMedkit)
	w.Step(testDt) // подбор
	if w.pickups[0].active {
		t.Fatal("spot should be empty right after pickup")
	}
	place(p, 1000, 1000) // уводим игрока, чтобы не схватил сразу после респауна
	readyAt := w.pickups[0].readyAt
	for w.Tick < readyAt {
		w.Step(testDt)
	}
	// На тике readyAt stepPickups активирует точку.
	w.Step(testDt)
	if !w.pickups[0].active {
		t.Fatalf("spot did not respawn by tick %d (readyAt %d)", w.Tick, readyAt)
	}
}

// TestPickupStateInChecksum: буфы игрока и состояние точек входят в Checksum —
// иначе реплеи/предсказание разошлись бы по невидимому состоянию.
func TestPickupStateInChecksum(t *testing.T) {
	build := func() *World {
		w := NewWorld(9)
		_, _ = w.AddPlayer("p")
		return w
	}
	base := build()

	for name, mut := range map[string]func(*World){
		"rapidUntil":     func(w *World) { w.players[w.order[0]].rapidUntil = 5 },
		"spreadUntil":    func(w *World) { w.players[w.order[0]].spreadUntil = 5 },
		"pickup active":  func(w *World) { w.pickups[0].active = !w.pickups[0].active },
		"pickup kind":    func(w *World) { w.pickups[0].active = true; w.pickups[0].kind = pickupSpread },
		"pickup readyAt": func(w *World) { w.pickups[0].readyAt++ },
	} {
		w := build()
		mut(w)
		if w.Checksum() == base.Checksum() {
			t.Fatalf("%s not covered by Checksum", name)
		}
	}
}

// TestPickupDeterminism: два мира с одним seed и одной лентой вводов, гоняющей
// игроков по точкам пикапов и стрельбой, совпадают по Checksum каждый тик через
// полный цикл спавна/подбора/респауна и буфнутой стрельбы.
func TestPickupDeterminism(t *testing.T) {
	tape := func(seq uint32, tick, idx int) protocol.Input {
		var b uint8
		switch (tick/7 + idx) % 4 {
		case 0:
			b = protocol.BtnRight
		case 1:
			b = protocol.BtnDown
		case 2:
			b = protocol.BtnLeft
		default:
			b = protocol.BtnUp
		}
		if (tick+idx)%5 == 0 {
			b |= protocol.BtnFire
		}
		return protocol.Input{Seq: seq, Buttons: b, Aim: uint16((tick*13 + idx*97) & 0xffff)}
	}

	build := func() (*World, []PlayerID) {
		w := NewWorld(123)
		var ids []PlayerID
		for i := range 3 {
			p, _ := w.AddPlayer("p")
			// Стартуем на точках пикапов — гарантирует, что путь подбора и буфов
			// действительно исполняется, а не только спавн.
			place(p, pickupSpots[i].x, pickupSpots[i].y)
			ids = append(ids, p.ID)
		}
		return w, ids
	}

	a, aids := build()
	b, bids := build()

	const ticks = 700 // > pickupRespawnTicks (240): покрывает подбор и повторный спавн
	seq := uint32(0)
	for tick := range ticks {
		seq++
		for i := range aids {
			in := tape(seq, tick, i)
			a.EnqueueInput(aids[i], in)
			b.EnqueueInput(bids[i], in)
		}
		a.Step(testDt)
		b.Step(testDt)
		if a.Checksum() != b.Checksum() {
			t.Fatalf("determinism broke at tick %d", tick)
		}
	}
}
