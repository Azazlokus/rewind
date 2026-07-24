package game

import (
	"testing"

	"arena/internal/protocol"
)

// BenchmarkTick измеряет голый шаг симуляции (без сети) для комнаты заданного
// размера. Это число итерация 6 должна удержать в бюджете для 200 сущностей;
// harness есть с итерации 1, чтобы регрессии всплывали рано.
func BenchmarkTick(b *testing.B) {
	for _, n := range []int{50, 200} {
		b.Run(sizeName(n), func(b *testing.B) {
			w := NewWorld(1)
			ids := make([]PlayerID, 0, n)
			for range n {
				p, err := w.AddPlayer("p")
				if err != nil {
					b.Fatal(err)
				}
				ids = append(ids, p.ID)
			}
			b.ReportAllocs()
			b.ResetTimer()
			var seq uint32
			for range b.N {
				// Каждый тик приходит ~2 ввода на игрока (клиент 60 Гц / тик 30 Гц):
				// Step осушает очередь. Смесь направлений, чтобы движение считалось.
				seq++
				for i, id := range ids {
					btn := uint8(1 << (i % 4))
					w.EnqueueInput(id, protocol.Input{Seq: 2*seq - 1, Buttons: btn})
					w.EnqueueInput(id, protocol.Input{Seq: 2 * seq, Buttons: btn})
				}
				w.Step(1.0 / 30)
			}
		})
	}
}

// BenchmarkAppendEntities измеряет построение списка сущностей для снапшота в
// переиспользуемый срез — горячий путь, кормящий кодировщик.
func BenchmarkAppendEntities(b *testing.B) {
	w := NewWorld(1)
	for range 200 {
		if _, err := w.AddPlayer("p"); err != nil {
			b.Fatal(err)
		}
	}
	buf := make([]protocol.Entity, 0, 256)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		buf = w.AppendEntities(buf[:0])
	}
	_ = buf
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
