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
			for i := range n {
				p, err := w.AddPlayer("p")
				if err != nil {
					b.Fatal(err)
				}
				// Смесь зажатых направлений, чтобы движение реально считалось.
				w.SetInput(p.ID, protocol.Input{
					Seq:     1,
					Buttons: uint8(1 << (i % 4)),
				})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
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
