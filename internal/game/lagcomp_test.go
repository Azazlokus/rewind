package game

import (
	"testing"

	"arena/internal/protocol"
)

// stepDt — длительность тика симуляции (30 Гц) для тестов lag compensation.
const stepDt = float32(1.0) / 30

func holdInput(seq uint32) protocol.Input { return protocol.Input{Seq: seq} }

func moveInput(seq uint32, btn uint8) protocol.Input {
	return protocol.Input{Seq: seq, Buttons: btn}
}

// fireView — выстрел вправо (aim=0) с номером seq и меткой viewTick, к которой
// сервер перемотает цели.
func fireView(seq uint32, viewTick uint32) protocol.Input {
	return protocol.Input{Seq: seq, Buttons: protocol.BtnFire, Aim: protocol.AimFromRadians(0), ViewTick: viewTick}
}

// buildMovingTarget строит мир, где стрелок стоит в (1000,1000), а цель сначала
// стоит на линии огня (1030,1000) один тик (фиксируется в истории под меткой
// seen), затем уезжает вверх upTicks тиков, сходя с линии. Возвращает мир,
// стрелка, цель и метку тика, на которой цель ещё была на линии.
func buildMovingTarget(t *testing.T, upTicks int) (w *World, shooter, target *Player, seen uint32) {
	t.Helper()
	w = NewWorld(1)
	var err error
	if shooter, err = w.AddPlayer("s"); err != nil {
		t.Fatal(err)
	}
	if target, err = w.AddPlayer("t"); err != nil {
		t.Fatal(err)
	}
	place(shooter, 1000, 1000)
	place(target, 1030, 1000)

	// Тик 1: оба стоят — записываем позицию цели на линии огня в историю.
	w.EnqueueInput(shooter.ID, holdInput(1))
	w.EnqueueInput(target.ID, holdInput(1))
	w.Step(stepDt)
	seen = w.Tick // 1

	// Цель уезжает вверх (BtnUp = -Y), уходя с линии огня; стрелок стоит.
	for i := 0; i < upTicks; i++ {
		seq := uint32(2 + i)
		w.EnqueueInput(shooter.ID, holdInput(seq))
		w.EnqueueInput(target.ID, moveInput(seq, protocol.BtnUp))
		w.Step(stepDt)
	}
	return w, shooter, target, seen
}

// fireAndResolve делает один выстрел вправо с заданным viewTick и прогоняет
// несколько тиков, держа цель на месте, чтобы снаряд успел разрешиться.
func fireAndResolve(w *World, shooterID, targetID PlayerID, viewTick uint32) {
	seq := uint32(100) // выше всех seq из build — не отсеется дедупом
	w.EnqueueInput(shooterID, fireView(seq, viewTick))
	w.EnqueueInput(targetID, holdInput(seq))
	w.Step(stepDt)
	for i := 0; i < 6; i++ {
		seq++
		w.EnqueueInput(shooterID, holdInput(seq))
		w.EnqueueInput(targetID, holdInput(seq))
		w.Step(stepDt)
	}
}

// TestLagCompensationHitsRewoundTarget: сервер перематывает цель к тому, что видел
// стрелок. Выстрел с корректным ViewTick попадает по прошлой позиции цели; тот же
// выстрел «по настоящему» — мимо, ведь цель уже ушла с линии.
func TestLagCompensationHitsRewoundTarget(t *testing.T) {
	// С компенсацией: rewind = 6 (в пределах окна) до позиции на линии — попадание.
	w, shooter, target, seen := buildMovingTarget(t, 6)
	fireAndResolve(w, shooter.ID, target.ID, seen)
	if target.HP == 100 {
		t.Fatal("lag-comp shot missed the rewound target: HP still 100")
	}

	// Без компенсации (ViewTick == настоящее -> rewind 0): цель уже уехала — мимо.
	w2, shooter2, target2, _ := buildMovingTarget(t, 6)
	fireAndResolve(w2, shooter2.ID, target2.ID, w2.Tick)
	if target2.HP != 100 {
		t.Fatalf("uncompensated shot hit a target that already moved away: HP=%d", target2.HP)
	}
}

// TestLagCompRewindClampedToWindow: перемотка зажата окном (анти-чит). Цель была
// на линии слишком давно (> maxRewindTicks назад); древний ViewTick не дотягивается
// до той позиции — в пределах окна цель уже ушла с линии, и выстрел мимо.
func TestLagCompRewindClampedToWindow(t *testing.T) {
	// seen=1, после 15 подъёмов now=16; желаемый rewind=15 > maxRewindTicks(10),
	// значит перемотка упрётся в history[now-10], где цель уже далеко от линии.
	w, shooter, target, seen := buildMovingTarget(t, 15)
	fireAndResolve(w, shooter.ID, target.ID, seen)
	if target.HP != 100 {
		t.Fatalf("clamp failed: ancient ViewTick reached an out-of-window position, HP=%d", target.HP)
	}
}

// TestLagCompDeterminism: два мира с одной лентой (движущаяся цель + выстрелы с
// перемоткой) идут к байт-в-байт равному Checksum. Покрывает детерминизм записи
// истории и её учёт в Checksum на нетривиальной (движущейся) истории.
func TestLagCompDeterminism(t *testing.T) {
	build := func() (*World, PlayerID, PlayerID) {
		w := NewWorld(7)
		s, _ := w.AddPlayer("s")
		tg, _ := w.AddPlayer("t")
		place(s, 900, 1000)
		place(tg, 1000, 1000)
		return w, s.ID, tg.ID
	}
	wa, sa, ta := build()
	wb, sb, tb := build()

	hits := 0
	for tick := 0; tick < 200; tick++ {
		seq := uint32(tick + 1)
		// Стрелок бьёт вправо с перемоткой на ~4 тика (в пределах окна).
		var vt uint32
		if tick > 4 {
			vt = uint32(tick - 4)
		}
		fire := fireView(seq, vt)
		// Цель колеблется поперёк линии огня: 5 тиков вверх, 5 вниз.
		btn := uint8(protocol.BtnUp)
		if (tick/5)%2 == 1 {
			btn = protocol.BtnDown
		}
		mv := moveInput(seq, btn)

		wa.EnqueueInput(sa, fire)
		wa.EnqueueInput(ta, mv)
		wa.Step(stepDt)
		wb.EnqueueInput(sb, fire)
		wb.EnqueueInput(tb, mv)
		wb.Step(stepDt)

		for _, e := range wa.Events() {
			if e.Kind == EventHit {
				hits++
			}
		}
		if wa.Checksum() != wb.Checksum() {
			t.Fatalf("lag-comp divergence at tick %d", tick)
		}
	}
	// Лента обязана реально упражнять перемотку: без попаданий тест ослеп бы к
	// lag-comp коду при сдвиге геометрии.
	if hits == 0 {
		t.Fatal("lag-comp tape trivial: no hits recorded")
	}
}
