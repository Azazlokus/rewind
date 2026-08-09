package protocol

import "testing"

// benchSnapshot строит снапшот с n сущностями-игроками.
func benchSnapshot(n int) Snapshot {
	ents := make([]Entity, n)
	for i := range ents {
		ents[i] = Entity{
			ID: uint16(i + 1), Kind: KindPlayer,
			X: float32(i * 7), Y: float32(i * 3),
			VX: 12, VY: -34, HP: uint8(i % 100),
		}
	}
	return Snapshot{Tick: 12345, LastProcessedSeq: 678, Entities: ents}
}

// BenchmarkEncodeSnapshot измеряет кодирование снапшота в переиспользуемый буфер.
// Бинарный кодек итерации 3 обязан здесь достичь 0 allocs/op; JSON-кодек
// итерации 1 — нет, и BENCHMARKS.md фиксирует разрыв как базовую линию.
func BenchmarkEncodeSnapshot(b *testing.B) {
	for _, n := range []int{50, 200} {
		b.Run(sizeName(n), func(b *testing.B) {
			snap := benchSnapshot(n)
			buf := make([]byte, 0, 8192)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var err error
				buf, err = AppendSnapshot(buf[:0], &snap)
				if err != nil {
					b.Fatal(err)
				}
			}
			_ = buf
		})
	}
}

// BenchmarkDecodeInput измеряет декодирование одного клиентского ввода — самого
// горячего входящего пути. Цель для итерации 3: 0 allocs/op.
func BenchmarkDecodeInput(b *testing.B) {
	buf, err := AppendInput(nil, Input{Seq: 42, Buttons: BtnUp | BtnFire, Aim: 30000})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := DecodeClient(buf); err != nil {
			b.Fatal(err)
		}
	}
}

// benchDeltaSnapshot строит дельту с n изменёнными сущностями (типичный случай —
// сдвиг X,Y,VX,VY) против базы: раскладка field-level дельты (итерация 9).
func benchDeltaSnapshot(n int) Snapshot {
	ents := make([]Entity, n)
	masks := make([]uint8, n)
	for i := range ents {
		ents[i] = Entity{
			ID: uint16(i + 1),
			X:  float32(i * 7), Y: float32(i * 3),
			VX: 12, VY: -34,
		}
		masks[i] = FieldX | FieldY | FieldVX | FieldVY
	}
	return Snapshot{Tick: 12345, BaseTick: 12340, LastProcessedSeq: 678, Entities: ents, Masks: masks}
}

// BenchmarkEncodeSnapshotDelta измеряет кодирование дельта-снапшота — горячий путь
// итерации 9. Инвариант zero-alloc обязан сохраниться.
func BenchmarkEncodeSnapshotDelta(b *testing.B) {
	for _, n := range []int{50, 200} {
		b.Run(sizeName(n), func(b *testing.B) {
			snap := benchDeltaSnapshot(n)
			buf := make([]byte, 0, 8192)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				var err error
				buf, err = AppendSnapshot(buf[:0], &snap)
				if err != nil {
					b.Fatal(err)
				}
			}
			_ = buf
		})
	}
}

// BenchmarkDecodeSnapshotDelta измеряет декодирование дельты в переиспользуемую
// структуру — тоже 0 allocs/op.
func BenchmarkDecodeSnapshotDelta(b *testing.B) {
	for _, n := range []int{50, 200} {
		b.Run(sizeName(n), func(b *testing.B) {
			snap := benchDeltaSnapshot(n)
			buf, err := AppendSnapshot(nil, &snap)
			if err != nil {
				b.Fatal(err)
			}
			var out ServerMessage
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := DecodeServer(buf, &out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDecodePickupState измеряет декодирование MsgPickupState (итерация 19):
// сообщение редкое (событийное), но декодер должен оставаться zero-alloc при
// переиспользовании ServerMessage — Active растёт по месту.
func BenchmarkDecodePickupState(b *testing.B) {
	buf, err := AppendPickupState(nil, PickupState{
		Active: []Pickup{{Spot: 0, Kind: 1}, {Spot: 1, Kind: 2}, {Spot: 4, Kind: 3}},
	})
	if err != nil {
		b.Fatal(err)
	}
	var out ServerMessage
	if err := DecodeServer(buf, &out); err != nil { // прогрев Active
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := DecodeServer(buf, &out); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeKillstreak измеряет декодирование MsgKillstreak (итерация 20):
// фиксированные 4 байта, должно быть zero-alloc.
func BenchmarkDecodeKillstreak(b *testing.B) {
	buf, err := AppendKillstreak(nil, Killstreak{ID: 7, Streak: 6})
	if err != nil {
		b.Fatal(err)
	}
	var out ServerMessage
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := DecodeServer(buf, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func sizeName(n int) string {
	switch n {
	case 50:
		return "50ent"
	case 200:
		return "200ent"
	default:
		return "n"
	}
}
