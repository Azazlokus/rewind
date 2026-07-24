package game

import (
	"testing"

	"arena/internal/protocol"
)

// BenchmarkTick measures a bare simulation step (no networking) for a room of a
// given size. This is the number iteration 6 must keep under budget for 200
// entities; the harness exists from iteration 1 so regressions show up early.
func BenchmarkTick(b *testing.B) {
	for _, n := range []int{50, 200} {
		b.Run(sizeName(n), func(b *testing.B) {
			w := NewWorld(1)
			for i := range n {
				p, err := w.AddPlayer("p")
				if err != nil {
					b.Fatal(err)
				}
				// A mix of held directions so movement actually runs.
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

// BenchmarkAppendEntities measures building the entity list for a snapshot into
// a reused slice — the hot path that feeds the encoder.
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
