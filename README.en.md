# Arena

[Русский](README.md) · **English**

An authoritative-server, top-down .io arena shooter. Go server, canvas client,
built for real netcode: client prediction, server reconciliation, lag
compensation and interest management, added iteration by iteration.

> Status: **iteration 9 — field-level delta done** (a snapshot delta now carries only
> the entity FIELDS that changed, under a bitmask, not the whole record). Earlier:
> broad-phase projectile×player collision over a sim grid (iter. 8), replays (record
> seed + tick-stamped inputs, replay headless and verify the checksum — `cmd/replay`,
> iter. 7), scale (interest management, snapshot deltas, a 200-bot load run — iter. 6),
> combat and lag compensation (iter. 5). See the plan in `CLAUDE.md`.

## Requirements

- Go 1.22+ (developed on 1.26)
- A modern browser
- Optional: `golangci-lint` for the full lint gate (the `make lint`/`check`
  targets fall back to `go vet` when it is absent)

## Run

```sh
make run          # or: go run ./cmd/server
```

Then open <http://localhost:8080>, type a name and click **connect**. Move with
**WASD**; the camera follows your player (blue), everyone else is red. The mouse
aims, **left click** fires; projectiles are yellow. HP, a damage flash and a
death/respawn screen live in the HUD.

### Manual two-tab check (iteration 1 acceptance)

1. `make run`
2. Open <http://localhost:8080> in **two** browser tabs.
3. Connect in both. Move with WASD in one tab — the other tab shows that player
   (red) moving smoothly, and vice-versa.

Remote players are rendered 100 ms in the past, interpolated between snapshots —
smooth even under 100–200 ms of latency. Your own player is predicted locally:
WASD response is instant and the server only corrects the position
(reconciliation with smoothing). Try devtools throttling: your own character
never lags behind the latency, while remote players stay smooth.

## Configuration (environment)

| Variable                 | Default            | Meaning                                  |
|--------------------------|--------------------|------------------------------------------|
| `ARENA_ADDR`             | `:8080`            | HTTP/WebSocket listen address            |
| `ARENA_PPROF_ADDR`       | `127.0.0.1:6060`   | pprof address (empty disables it)        |
| `ARENA_WEB_DIR`          | `web`              | directory served at `/`                  |
| `ARENA_TICK_RATE`        | `30`               | simulation Hz                            |
| `ARENA_SNAPSHOT_RATE`    | `20`               | snapshot Hz (interpolation hides the gap with the tick rate) |
| `ARENA_MAX_PLAYERS`      | `64`               | players per room                         |
| `ARENA_MAX_ROOMS`        | `16`               | rooms per hub                            |
| `ARENA_AOI_RADIUS`       | `640`              | interest-management radius, units (0 disables) |
| `ARENA_SEED`             | `1`                | world seed (determinism)                 |
| `ARENA_ALLOW_ALL_ORIGIN` | `true`             | skip WebSocket origin checks (dev)       |
| `ARENA_LOG_LEVEL`        | `info`             | `debug`/`info`/`warn`/`error`            |

## Endpoints

- `/` — web client
- `/ws` — WebSocket game connection
- `/metrics` — Prometheus metrics
- `/healthz` — liveness probe
- pprof on `ARENA_PPROF_ADDR` (localhost only)

## Develop

```sh
make check        # mandatory pre-commit gate: lint + race tests
make test         # go test -race -count=1 ./...
make integration  # end-to-end tests (real server + WS bots), build tag `integration`
make fuzz         # fuzz the protocol decoder
make bench        # benchmarks with -benchmem (see BENCHMARKS.md)
make loadtest     # load run: 200 bots in-process, tick p99 and traffic (iter. 6C)
make replay       # replay demo: record a session and play it back headless (iter. 7)
make profile      # run with pprof and print the endpoint
make help         # list all targets
```

Commit messages are written in Russian, following
[Conventional Commits](https://www.conventionalcommits.org/).

## Documentation

Prose docs are written in Russian:

- [`docs/architecture.md`](docs/architecture.md) — components, concurrency model,
  goroutine ownership, determinism.
- [`docs/protocol.md`](docs/protocol.md) — wire format v1.
- [`docs/testing.md`](docs/testing.md) — harness, determinism, fuzz, golden, e2e.
- [`CLAUDE.md`](CLAUDE.md) — fixed decisions, package boundaries, rules.
- [`BENCHMARKS.md`](BENCHMARKS.md) — per-iteration measurements.

## Layout

```
cmd/server/        wiring, env config, WS gateway, graceful shutdown
cmd/loadtest/      load run: N bots in-process, measures tick p99 and traffic
cmd/replay/        headless replay of a session log, verifying the checksum (iter. 7)
internal/
  transport/       Conn interface + WebSocket impl + in-memory Pipe (for tests)
  protocol/        message types + codec (binary, delta snapshots)
  game/            room loop, world, systems, clock, sessions — no networking
  hub/             room manager, player assignment
  bot/             headless client (delta reconstruction; core of the load-test swarm)
  metrics/         Prometheus instruments
web/               index.html + game.js (no build step)
```
