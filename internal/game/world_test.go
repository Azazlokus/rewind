package game

import (
	"testing"

	"arena/internal/protocol"
)

// TestWorldDeterminism — фундамент, на котором стоит весь проект: два мира с
// одним seed, накормленные одними вводами, обязаны прийти к байт-в-байт
// одинаковому состоянию. На этом держатся и реплеи, и клиентское предсказание.
func TestWorldDeterminism(t *testing.T) {
	const players, ticks = 8, 1000
	const dt = 1.0 / 30

	build := func() *World {
		w := NewWorld(1234)
		for i := range players {
			if _, err := w.AddPlayer("p"); err != nil {
				t.Fatalf("add player %d: %v", i, err)
			}
		}
		return w
	}
	a, b := build(), build()

	// Детерминированная лента кнопок на игрока и шаг ввода.
	buttons := func(id PlayerID, step int) uint8 {
		switch (int(id) + step) % 4 {
		case 0:
			return protocol.BtnUp
		case 1:
			return protocol.BtnRight
		case 2:
			return protocol.BtnDown | protocol.BtnLeft
		default:
			return protocol.BtnUp | protocol.BtnRight
		}
	}

	// Кормим ~2 ввода на тик (клиент 60 Гц / тик 30 Гц): так проверяется именно
	// пакетное осушение очереди — суть итерации 4, а не одно-вводная модель.
	for tick := range ticks {
		for id := PlayerID(1); id <= players; id++ {
			s1, s2 := 2*uint32(tick)+1, 2*uint32(tick)+2
			b1, b2 := buttons(id, 2*tick), buttons(id, 2*tick+1)
			a.EnqueueInput(id, protocol.Input{Seq: s1, Buttons: b1})
			b.EnqueueInput(id, protocol.Input{Seq: s1, Buttons: b1})
			a.EnqueueInput(id, protocol.Input{Seq: s2, Buttons: b2})
			b.EnqueueInput(id, protocol.Input{Seq: s2, Buttons: b2})
		}
		a.Step(dt)
		b.Step(dt)
		if ca, cb := a.Checksum(), b.Checksum(); ca != cb {
			t.Fatalf("divergence at tick %d: %x != %x", tick, ca, cb)
		}
	}
}

// TestWorldSeedIndependence проверяет, что разные seed действительно отличаются —
// значит, тест детерминизма выше не проходит тривиально.
func TestWorldSeedIndependence(t *testing.T) {
	a := NewWorld(1)
	b := NewWorld(2)
	if _, err := a.AddPlayer("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddPlayer("x"); err != nil {
		t.Fatal(err)
	}
	if a.Checksum() == b.Checksum() {
		t.Fatal("different seeds produced identical spawn state")
	}
}

// TestMovementClampsToMap проверяет, что игрок, вжатый в стену, останавливается
// на границе, а не покидает карту.
func TestMovementClampsToMap(t *testing.T) {
	w := NewWorld(1)
	p, err := w.AddPlayer("wall")
	if err != nil {
		t.Fatal(err)
	}
	// Держим влево-вверх постоянно (как реальный клиент — по вводу каждый кадр),
	// пока не упрёмся в угол. Диагональный шаг ~3.5 юнита/ось, до угла хватает.
	for i := range 3000 {
		w.EnqueueInput(p.ID, protocol.Input{Seq: uint32(i + 1), Buttons: protocol.BtnLeft | protocol.BtnUp})
		w.Step(1.0 / 30)
	}
	if p.X != PlayerRadius || p.Y != PlayerRadius {
		t.Fatalf("player did not settle in the top-left corner: x=%.2f y=%.2f", p.X, p.Y)
	}
}

// TestLastProcessedSeqMonotonic проверяет, что подтверждённый номер никогда не
// идёт назад, даже если старый ввод приходит с опозданием.
func TestLastProcessedSeqMonotonic(t *testing.T) {
	w := NewWorld(1)
	p, err := w.AddPlayer("seq")
	if err != nil {
		t.Fatal(err)
	}
	w.EnqueueInput(p.ID, protocol.Input{Seq: 10})
	w.Step(1.0 / 30)
	if p.LastProcessedSeq != 10 {
		t.Fatalf("LastProcessedSeq=%d, want 10", p.LastProcessedSeq)
	}
	// Устаревший (переупорядоченный) ввод не должен опускать подтверждение.
	w.EnqueueInput(p.ID, protocol.Input{Seq: 5})
	w.Step(1.0 / 30)
	if p.LastProcessedSeq != 10 {
		t.Fatalf("stale input lowered LastProcessedSeq to %d, want 10", p.LastProcessedSeq)
	}
}

// TestChecksumTracksQueueCursor стережёт закрытие слепого пятна итерации 4:
// lastQueuedSeq и hasQueued входят в Checksum. Два мира с идентичным ВИДИМЫМ
// состоянием (позиция, LastProcessedSeq), но разным курсором очереди обязаны
// давать разные хэши — иначе desync по гейту дедупа прошёл бы незамеченным (как
// раньше и было). Курсор расходится без боя: поставленный, но не отработанный
// Step-ом ввод двигает lastQueuedSeq, не трогая позицию.
func TestChecksumTracksQueueCursor(t *testing.T) {
	// lastQueuedSeq: B получает лишний ввод после Step и не шагает — позиция и
	// LastProcessedSeq совпадают с A, а lastQueuedSeq расходится.
	buildMoved := func(extraSeq uint32) *World {
		w := NewWorld(1)
		p, err := w.AddPlayer("q")
		if err != nil {
			t.Fatal(err)
		}
		w.EnqueueInput(p.ID, protocol.Input{Seq: 5, Buttons: protocol.BtnRight})
		w.Step(1.0 / 30)
		if extraSeq != 0 {
			w.EnqueueInput(p.ID, protocol.Input{Seq: extraSeq}) // в очередь, но не в Step
		}
		return w
	}
	a, b := buildMoved(0), buildMoved(9)
	pa, pb := a.players[1], b.players[1]
	if pa.X != pb.X || pa.Y != pb.Y || pa.LastProcessedSeq != pb.LastProcessedSeq {
		t.Fatalf("visible state diverged: A(%v,%v seq %d) B(%v,%v seq %d)",
			pa.X, pa.Y, pa.LastProcessedSeq, pb.X, pb.Y, pb.LastProcessedSeq)
	}
	if pa.lastQueuedSeq == pb.lastQueuedSeq {
		t.Fatalf("test setup broken: lastQueuedSeq did not diverge (%d)", pa.lastQueuedSeq)
	}
	if a.Checksum() == b.Checksum() {
		t.Fatal("Checksum ignores lastQueuedSeq — dedup-gate desync would go unnoticed")
	}

	// hasQueued: C не получал вводов вовсе (примет seq 0 как первый), D получил
	// seq 0 (примет только seq ≥ 1). Видимое состояние идентично, различает
	// только hasQueued.
	c := NewWorld(1)
	if _, err := c.AddPlayer("h"); err != nil {
		t.Fatal(err)
	}
	d := NewWorld(1)
	dp, err := d.AddPlayer("h")
	if err != nil {
		t.Fatal(err)
	}
	d.EnqueueInput(dp.ID, protocol.Input{Seq: 0}) // в очередь, не в Step
	if c.players[1].lastQueuedSeq != d.players[1].lastQueuedSeq {
		t.Fatalf("test setup broken: lastQueuedSeq differs (%d vs %d)",
			c.players[1].lastQueuedSeq, d.players[1].lastQueuedSeq)
	}
	if c.Checksum() == d.Checksum() {
		t.Fatal("Checksum ignores hasQueued — first-input gate desync would go unnoticed")
	}
}

// TestRemovePlayerKeepsOrderSorted проверяет, что детерминированный порядок id
// остаётся отсортированным при добавлениях и удалениях — порядок обхода map
// никогда не должен просачиваться внутрь.
func TestRemovePlayerKeepsOrderSorted(t *testing.T) {
	w := NewWorld(1)
	for range 5 {
		if _, err := w.AddPlayer("p"); err != nil {
			t.Fatal(err)
		}
	}
	w.RemovePlayer(3)
	w.RemovePlayer(1)
	if _, err := w.AddPlayer("late"); err != nil {
		t.Fatal(err)
	}
	prev := PlayerID(0)
	w.Each(func(p *Player) {
		if p.ID <= prev {
			t.Fatalf("order not ascending: %d after %d", p.ID, prev)
		}
		prev = p.ID
	})
}
