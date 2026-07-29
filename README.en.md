# Arena

[Русский](README.md) · **English**

[![CI](https://github.com/Azazlokus/rewind/actions/workflows/ci.yml/badge.svg)](https://github.com/Azazlokus/rewind/actions/workflows/ci.yml)
[![CodeQL](https://github.com/Azazlokus/rewind/actions/workflows/codeql.yml/badge.svg)](https://github.com/Azazlokus/rewind/actions/workflows/codeql.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

An authoritative-server, top-down .io arena shooter. Go server, canvas client,
built for real netcode: client prediction, server reconciliation, lag
compensation and interest management, added iteration by iteration.

> Status: **iteration 14B — persister** (the room ships deaths and match results down a
> channel; `internal/persist` writes stats/history to the DB off the room goroutine; the
> join carries a session token, binding the session to an account). Earlier: match
> lifecycle (FFA deathmatch with a timer: a timed round, kill/death scoring, a
> deterministic winner, an intermission and auto-restart; scoreboard, timer and winner
> banner — iter. 14). Before that: backend foundation
> (accounts, stats, match history — iter. 13), WebRTC taken to production (snapshots on
> a separate unreliable DataChannel, TURN and relay-only — iter. 12), WebRTC DataChannel
> transport alongside WebSocket (iter. 11, `?transport=webrtc`), static walls (iter. 10),
> field-level snapshot delta (iter. 9), broad-phase projectile×player collision over a
> sim grid (iter. 8), replays (`cmd/replay`, iter. 7), scale (interest management,
> snapshot deltas, a 200-bot load run — iter. 6), combat and lag compensation (iter. 5).
> See the plan in `CLAUDE.md`.

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
aims, **left click** fires; projectiles are yellow. Gray blocks are static walls:
you can neither walk nor shoot through them. HP, a damage flash and a death/respawn
screen live in the HUD.

### Docker

```sh
make docker                                   # build the arena-server:dev image
docker run --rm -p 8080:8080 arena-server:dev # run, open http://localhost:8080
```

A multi-stage build produces a static binary on a distroless image (nonroot, no
shell). Prebuilt images are published to GHCR: `ghcr.io/azazlokus/rewind` (on push to
`main` and on `vX.Y.Z` tags).

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
| `ARENA_STUN`             | (empty)            | comma-separated STUN URLs for WebRTC; empty means host candidates only (localhost/LAN) |
| `ARENA_TURN`             | (empty)            | comma-separated TURN URLs (NAT traversal) |
| `ARENA_TURN_USER`        | (empty)            | TURN username |
| `ARENA_TURN_PASS`        | (empty)            | TURN password |
| `ARENA_FORCE_RELAY`      | `false`            | WebRTC over TURN relay only (host/srflx dropped; restrictive networks/privacy) |
| `ARENA_DB_DRIVER`        | `sqlite`           | storage backend: `sqlite` (dev/CI) or `postgres` (prod) |
| `ARENA_DB_DSN`           | `arena.db`         | SQLite file path (or `:memory:`) or a Postgres DSN |
| `ARENA_AUTH_SECRET`      | (empty)            | token-session signing key; empty means an ephemeral per-run secret (tokens won't survive a restart) |
| `ARENA_TOKEN_TTL`        | `24h`              | token-session lifetime |
| `ARENA_LOG_LEVEL`        | `info`             | `debug`/`info`/`warn`/`error`            |

## Transport

By default the game runs over WebSocket. Iteration 11 added a **WebRTC DataChannel**
as an alternative transport: open <http://localhost:8080/?transport=webrtc> and the
client brings up a DataChannel (offer/answer signaling over the `/rtc` WebSocket), then
the game protocol flows over it. There is no fallback: the transport is chosen
explicitly, and WebSocket stays the default (and the path for bots/e2e).

Iteration 12 took WebRTC to production. There are now **two** channels: "game"
(ordered+reliable) carries events and inputs, while snapshots ride a separate "state"
(unordered+unreliable) channel — a lost snapshot is not retransmitted and does not
head-of-line-block the ones behind it. Host candidates suffice on localhost/LAN; for
NAT traversal set STUN (`ARENA_STUN`) or TURN (`ARENA_TURN` + `ARENA_TURN_USER`/
`ARENA_TURN_PASS`). `ARENA_FORCE_RELAY=true` forces connections through the TURN relay
only (restrictive networks/privacy — the peer's real IP is not exposed). The server
tells the client the ICE servers and relay policy over signaling, so both sides agree.

## Backend (iteration 13)

A persistent backend for accounts, stats and match history, architecturally separated
from the game core (a modular monolith with hard boundaries):

- `internal/store` — a `Store` interface + one SQL implementation over **SQLite**
  (dev/CI, pure-Go, no external DB) and **PostgreSQL** (prod); migrations are embedded
  and applied on startup.
- `internal/account` — identity: guests + accounts (argon2id, signed HMAC token
  sessions). Guests are ephemeral (name in the token, no DB row).
- `internal/api` — REST over plain `net/http`.
- `internal/persist` (iteration 14B) — the game→DB seam: rooms ship deaths and match
  results down a channel, the persister writes them to `store` in its own goroutine.

The game core (`internal/game`) does not import the backend. The link goes through the
persister: the room ships `game.PersistMsg` into `Config.PersistSink` **non-blockingly**
(overflow drops stats but never stalls the tick), and `internal/persist` turns them into
`store` calls off the room goroutine. The join carries a session token — the gateway
binds the player to an account by it (see `MsgJoin` in [docs/protocol.md](docs/protocol.md)).

REST (`/api`):

| Method + path                   | What it does                                 |
|---------------------------------|----------------------------------------------|
| `POST /api/register`            | register `{username,password}` → token       |
| `POST /api/login`               | log in → token                               |
| `POST /api/guest`               | guest token `{name}`                          |
| `GET  /api/me`                  | profile by token (Bearer)                     |
| `GET  /api/leaderboard`         | top by kills (`?limit`)                       |
| `GET  /api/players/{id}/stats`  | a player's stats                              |
| `GET  /api/players/{id}/matches`| a player's match history (`?limit`)           |

## Endpoints

- `/` — web client
- `/ws` — WebSocket game connection
- `/rtc` — WebRTC signaling (the game transport is the DataChannel: "game" reliable + "state" unreliable, iter. 11–12)
- `/api/*` — REST backend (iter. 13): accounts, profile, leaderboard, match history
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

Work happens on feature branches (`feat/…`, `fix/…`, `docs/…`, `ci/…`), not directly on
`main`: branch → `make check` → PR → merge when green. See [`CLAUDE.md`](CLAUDE.md)
("Ветки и PR") for details.

## CI/CD and security

Everything lives in `.github/`:

- **CI** (`ci.yml`) — on every PR: `make check`, `make integration`, a short `make fuzz`.
- **CodeQL** (`codeql.yml`) — GitHub SAST.
- **Security** (`security.yml`) — `govulncheck` (deps/code vulnerabilities) and
  `gitleaks` (secret scanning).
- **Dependabot** (`dependabot.yml`) — weekly Go-module and Action updates.
- **Docker** (`docker.yml`) — builds and publishes the image to GHCR.
- **Release** (`release.yml` + `.goreleaser.yaml`) — on a `vX.Y.Z` tag GoReleaser
  builds binaries (server/loadtest/replay) for linux/darwin/windows × amd64/arm64,
  archives with `web/` and checksums, and cuts a GitHub Release. Report
  vulnerabilities privately — see [`SECURITY.md`](SECURITY.md).

Cut a release:

```sh
git tag v0.1.0 && git push origin v0.1.0   # triggers release.yml
```

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
  transport/       Conn interface + WebSocket + WebRTC DataChannel + in-memory Pipe
  protocol/        message types + codec (binary, delta snapshots)
  game/            room loop, world, systems, clock, sessions — no networking
  hub/             room manager, player assignment
  bot/             headless client (delta reconstruction; core of the load-test swarm)
  metrics/         Prometheus instruments
web/               index.html + game.js (no build step)
```
