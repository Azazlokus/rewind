package protocol

import "testing"

// benchSnapshot builds a snapshot with n player entities.
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

// BenchmarkEncodeSnapshot measures snapshot encoding into a reused buffer. The
// iteration-3 binary codec must reach 0 allocs/op here; the JSON codec of
// iteration 1 will not, and BENCHMARKS.md records the gap as the baseline.
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

// BenchmarkDecodeInput measures decoding one client input, the hottest inbound
// path. Target for iteration 3: 0 allocs/op.
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
