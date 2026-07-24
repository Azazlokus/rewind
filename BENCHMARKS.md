# Benchmarks

Numbers are recorded per iteration on the dev machine (go1.26, linux/amd64,
24 logical CPUs). Reproduce with `make bench`. These are baselines, not promises:
the point is to catch regressions and to show the effect of each iteration.

## Iteration 1 — skeleton (JSON transport)

Iteration 1 uses a temporary JSON codec, so the encode/decode paths allocate.
This is deliberate: the figures below are the **before** picture that the binary
codec in iteration 3 is measured against. The simulation hot path is already
allocation-free.

### Simulation (zero-alloc hot path)

| Benchmark               | ns/op | B/op | allocs/op |
|-------------------------|------:|-----:|----------:|
| Tick / 50 entities      |   875 |    0 |         0 |
| Tick / 200 entities     |  3397 |    0 |         0 |
| AppendEntities / 200    |  3991 |    0 |         0 |

A 200-entity tick costs ~3.4 µs of pure simulation, far under the 33 ms tick
budget — headroom the later networking work will spend.

### Codec (JSON — to be replaced in iteration 3)

| Benchmark                    | ns/op | B/op  | allocs/op |
|------------------------------|------:|------:|----------:|
| EncodeSnapshot / 50 entities |  6963 |  3075 |         1 |
| EncodeSnapshot / 200 entities| 26222 | 12300 |         1 |
| DecodeInput                  |   781 |   544 |        12 |

### Bytes per tick per client (JSON, full-state broadcast)

| Entities in snapshot | Bytes | Bytes/entity |
|----------------------|------:|-------------:|
| 1                    |   105 |        105.0 |
| 8                    |   546 |         68.2 |
| 32                   |  2081 |         65.0 |
| 64                   |  4129 |         64.5 |

At 30 snapshots/s a 64-player room pushes ~124 KB/s **to each client** with the
JSON codec and full-state broadcast. The two levers that bring this down come
later: the binary codec (iteration 3, target ~13 bytes/entity) and interest
management + deltas (iteration 6, so traffic stops scaling with room size).

### Targets carried forward

- **Iteration 3:** `BenchmarkEncodeSnapshot` → **0 allocs/op**; `BenchmarkDecodeInput`
  → **0 allocs/op**; bytes/entity ~13 (2+1+2+2+2+2+1) + header.
- **Iteration 6:** tick p99 < 15 ms at 200 bots; per-client traffic flat in room size.
