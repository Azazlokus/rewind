# Arena

[Русский](README.md) · **English**

[![CI](https://github.com/Azazlokus/rewind/actions/workflows/ci.yml/badge.svg)](https://github.com/Azazlokus/rewind/actions/workflows/ci.yml)
[![CodeQL](https://github.com/Azazlokus/rewind/actions/workflows/codeql.yml/badge.svg)](https://github.com/Azazlokus/rewind/actions/workflows/codeql.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

An authoritative-server, top-down .io arena shooter. Go server, canvas client,
built for real netcode: client prediction, server reconciliation, lag
compensation and interest management, added iteration by iteration.

> Status: **iteration 26 — weapon system** (4 types: pistol/shotgun/sniper/rocket with splash;
> selection via keys 1–4 rides the high bits of `Buttons` — the input wire format is unchanged;
> `Player.weapon`/`projectile.weapon` are in `Checksum`, weapons reach the client via the reliable
> `MsgWeaponState`, the snapshot is untouched). Before that: anti-cheat metrics (Prometheus
> `arena_anticheat_events_total{kind}`: counts server-side rewind-clamp hits — a `ViewTick` from the
> future or beyond the window; observation on top of the existing anti-cheat, the counters live in
> `World` outside `Checksum`, drained by the room into the `Recorder` after each tick, iter. 25).
> Even earlier: mobile controls (a twin-stick
> overlay on the canvas: left stick moves, right stick aims and fires; both feed the same
> `state.keys`/`state.aim` as mouse/keyboard, so prediction and the wire are untouched; the canvas
> scales down to a phone screen — pure frontend, iter. 24). Team mode (two teams, balanced on join,
> friendly fire disabled,
> team scoring and winner; `Player.team` and projectiles are in `Checksum`, the `teamMode` flag
> is a world parameter carried in replay log v2; team rides to the client via `MsgMatchState`,
> the snapshot is untouched; the client colors fighters/minimap/scoreboard by team;
> `ARENA_TEAM_MODE`, iter. 23). Spectator/observer (join a room without spawning: `Join` with a
> spectator flag, a session with no `Player` — not in the simulation/combat, receives the whole
> world and events; the client gets a free WASD camera and a **spectate** button; spectators are
> outside AOI and outside `Checksum`, iter. 22). Auth rate limiting (a per-IP token bucket on
> `/api/register`/`login`/`guest` — brute force/spam bounded, 429 + `Retry-After`; no background
> goroutines; on by default, iter. 21). Killstreaks + invulnerability window
> (a fresh spawn is invulnerable, shots pass through; the shield drops when you fire; a kill
> streak grants a heal and a brief shield and a reliable `MsgKillstreak`; all in `Checksum`,
> iter. 20). Weapons/pickups (medkits,
> fire-rate boost and a spread fan on fixed spots; spawning is deterministic via `w.rng`, buffs
> and spot state live in `Checksum`; the wire is a separate reliable `MsgPickupState`, iter. 19).
> Combat sound (shoot/hit/death/kill/respawn via Web Audio over the
> combat events already arriving — pure frontend, synthesized with no assets, an HUD toggle,
> iter. 18). Server-side bots (a filler keeps `ARENA_BOT_FILL` players in an occupied room,
> adding AI bots and yielding to humans — bots are ordinary clients over a Pipe and never
> touch the world, iter. 17), player profile (a modal with stats and match history — iter.
> 16), client/UX (login/registration screen on the REST backend with a session token, a
> leaderboard and a minimap — pure canvas/JS, no bundlers, iter. 15). Before that: persister
> (the room ships deaths and match results down a channel;
> `internal/persist` writes stats/history to the DB off the room goroutine; the join
> carries a session token — iter. 14B), match lifecycle (FFA deathmatch with a timer: a
> timed round, kill/death scoring, a deterministic winner, an intermission and
> auto-restart; scoreboard, timer and winner banner — iter. 14). Before that: backend foundation
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

Then open <http://localhost:8080>. You can **log in or register** (matches then run
under your account and accumulate stats) — or just type a name and play as a guest.
Click **connect**. Move with **WASD**; the camera follows your player (blue), everyone
else is red. The mouse aims, **left click** fires; projectiles are yellow. Gray blocks
are static walls: you can neither walk nor shoot through them. HP, a damage flash and a
death/respawn screen live in the HUD; the **leaderboard** is on the side and the
**minimap** is bottom-right (iteration 15). Clicking a player in the leaderboard (or the
**profile** button when signed in) opens a **profile** — stats and match history (iteration 16).
The **sound** button in the HUD toggles combat audio (shoot, hit, death, kill, respawn — Web
Audio, synthesized with no assets, iteration 18).

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
| `ARENA_BOT_FILL`         | `0`                | keep this many players (humans+bots) in an occupied room; 0 disables (iter. 17) |
| `ARENA_TEAM_MODE`        | `false`            | team mode: 2 teams, friendly fire off, team scoring (iter. 23) |
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
| `ARENA_AUTH_RATE_BURST`  | `10`               | per-IP auth rate limit: burst requests; 0 disables (iter. 21) |
| `ARENA_AUTH_RATE_WINDOW` | `1m`               | full bucket refill time (rate ≈ burst/window) |
| `ARENA_AUTH_RATE_IP_HEADER` | (empty)         | header carrying the client IP behind a proxy (e.g. `X-Forwarded-For`); empty means `RemoteAddr`. Enable only behind a trusted proxy |
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
  bot/             headless client (delta reconstruction; swarm/bot autopilot)
  botfill/         AI bot filler for rooms (iter. 17) — bots as ordinary clients
  metrics/         Prometheus instruments
  store/           persistence (SQLite/PostgreSQL), migrations — outside the game (iter. 13)
  account/         accounts and guests: argon2id, HMAC tokens (iter. 13)
  api/             REST over net/http: register/login/leaderboard/profile (iter. 13)
  persist/         game→DB seam: stats and match history off the room goroutine (iter. 14B)
web/               index.html + game.js (no build step)
```

## Bots (iteration 17)

So a player who joins alone is not left on their own, the `internal/botfill` filler keeps
`ARENA_BOT_FILL` players in an occupied room: while a room holds at least one live human it
tops it up with AI bots to the target and removes them as humans arrive or the room empties
(it never animates an empty room). Bots are **ordinary clients**: the filler wires them
in-process over `transport.Pipe` and `room.Join` (the same path as humans and the load-test
swarm) and runs the autopilot from `internal/bot`; it never touches the room's world — only
Players()/Join/State and closing the connection. Disabled by default (`0`). Metric:
`arena_active_bots`.

## Weapons/pickups (iteration 19)

Bonuses are scattered across the arena on fixed spots: a **medkit** (instant heal), a
**fire-rate boost** and a **spread** fan (both are timed buffs, cleared on respawn). Stepping
onto a pickup collects it. This is part of the simulation: the spots and the algorithm are
deterministic (the type is rolled from `w.rng`, spawn/respawn timing runs off `w.Tick`, and
collection iterates spots by index and players by `order`), so the player's buffs and the spot
state (occupied/type/timer) live in `Checksum` and are replay-safe.

On the wire pickups are **not** carried in the snapshot (that would bloat the delta and the
per-tick entity counters): the spot layout is fixed and mirrored by the client (`PICKUP_SPOTS`,
like `WALLS`), while which spots are occupied and with what is sent as a separate reliable
`MsgPickupState`, **event-driven** (like the match scoreboard). The client draws active pickups
at their spots and on the minimap — pure rendering; collection is authoritative on the server.

## Killstreaks and invulnerability window (iteration 20)

Two related combat mechanics, both deterministic and in `Checksum`:

- **Invulnerability window** (spawn protection): a freshly respawned player is invulnerable for
  a couple of seconds — shots pass through them (`findHit` skips them), no damage. This is
  anti-spawn-farm: you can't farm someone who just respawned into the fray. The shield **drops
  the moment the player fires** (`tryFire`) — you can't sit under the shield and shoot with
  impunity. Granted on respawn (not on initial join) and on a streak milestone.
- **Killstreaks**: a run of kills without dying (`Player.streak`). Every `killstreakStep` frags
  in a row is a milestone: an instant heal to 100 plus a brief shield (a power spike), and a
  reliable `MsgKillstreak` event goes to everyone (a feed/announcement). Death and a new match
  reset the streak.

The client draws a pulsing shield ring (off `MsgSpawn`/`MsgKillstreak` events, duration mirrored)
and a streak banner — pure rendering: invulnerability is authoritative on the server and not part
of prediction.

## Auth rate limiting (iteration 21)

The unauthenticated token-minting POSTs (`/api/register`, `/api/login`, `/api/guest`) are
protected by a per-IP **token bucket**: a client gets `ARENA_AUTH_RATE_BURST` burst requests that
refill at `burst/window`. Once drained, it gets `429 Too Many Requests` with a `Retry-After`
header. Password brute force and registration/guest-token spam are bounded in rate, while a normal
player never notices the limit.

It lives as middleware in `internal/api` (not game code), is concurrency-safe under a mutex and
runs **no background goroutines**: idle buckets are reaped by a lazy sweep on the request path, so
the map does not grow under live traffic and stays static when quiet. The client key comes from
`RemoteAddr`; behind a reverse proxy you can set `ARENA_AUTH_RATE_IP_HEADER` (e.g. `X-Forwarded-For`)
— only if the proxy overwrites that header, otherwise the IP can be spoofed. On by default
(`ARENA_AUTH_RATE_BURST=0` disables). The game and the wire are untouched.

## Spectator/observer (iteration 22)

You can join a room as a **spectator**: the **spectate** button (or `Join` with the `spectator`
flag). A spectator is a session with **no `Player` in the world**: it does not spawn, takes no
part in combat, does not count as a player (`Players()` ignores it) and is entirely outside
`Checksum`/the simulation — it is a pure networking concept at the room layer. It is sent the
**whole world** (outside AOI, since it has no position) and reliable events (deaths, spawns, the
scoreboard, pickups, killstreaks). `MsgJoinAck` carries `YourID == 0` — the "you have no entity"
signal.

A spectator **sends no input** (the server ignores it even if a hostile client sends some): the
camera is free, panned with WASD on the client (pure rendering, no network). The spectator's
session key is room-local and never enters the entity id space, so it cannot leak into the world.
The changes touch only `session`/`room` and the client; the simulation and `World` are untouched.

## Team mode (iteration 23)

Enabled with `ARENA_TEAM_MODE`. Players are split into **two teams**; joining balances them (a
newcomer lands on the smaller team, deterministically over `w.order`). **Friendly fire is off** —
a projectile passes through an ally (`findHit` skips a same-team target). The match is scored per
team: the winner is the team with the higher total kills (ties go to team 0).

A player's team (`Player.team`) and a projectile's team are **simulation state and are in
`Checksum`** (they drive friendly fire and scoring). The `teamMode` flag itself is a fixed world
parameter (like `tickRate`): not in `Checksum`, but written into the **replay log v2** so a replay
reconstructs combat correctly (the decoder still accepts v1 FFA logs). Wire: team rides to the
client via `MsgMatchState` (a `teamMode` flag plus a `team` byte per scoreboard row) — the
snapshot/delta and their per-tick entity counters are **untouched** (no hot-path regression). The
client builds an `id→team` map from the scoreboard and colors fighters, the minimap, the
scoreboard and the winner banner by it (allies blue, enemies red).

## Mobile controls (iteration 24)

Pure frontend: a **twin-stick** overlay on the canvas for touch screens. A touch on the left half is
a virtual **movement** stick (direction → 8-way WASD in `state.keys`), on the right half an **aim**
stick (angle → `state.aim`) that holds fire. Both feed the **same** `state.keys`/`state.aim` as the
keyboard/mouse, so the input path (prediction, `encodeInput`, the 60 Hz send loop) is **untouched** —
touch is just another source of the same state. The sticks render only while a touch is held (invisible
on desktop). They are handled via Pointer Events with `pointerType === 'touch'` (mouse/keyboard keep
their old path); `touchAiming` stops the renderer from overwriting `state.aim` with the mouse position
while the right stick is active. The canvas keeps its 800×600 internal resolution, but `max-width: 100%`
scales it down to a narrow phone screen; `touchPoint()` maps a touch from screen to canvas coordinates
by the aspect ratio, so the sticks stay accurate at any scale. The wire/simulation/constants are
untouched — no Go changes.

## Anti-cheat metrics (iteration 25)

A Prometheus counter `arena_anticheat_events_total{kind}` surfaces hits of the server-side
lag-compensation rewind clamp. Labels: `rewind_stale` — the client sent a `ViewTick` further into
the past than the rewind window (high latency, an interpolation artifact, or a lag switch);
`rewind_future` — a `ViewTick` from the future (clock desync or client-side time tampering). This is
**observation**, not a decision: the server clamps the offset authoritatively even without the metric
(`clampRewind`) — the counter just tallies the attempts. The counters live in `World` as a transient
field (`ac`), are **not in `Checksum`** and never written to the replay log (they do not affect the
simulation — replay-safe); they are incremented in `tryFire` and drained by the room into the
`Recorder` after each tick (`DrainAntiCheat`), all on the room goroutine. The wire/client are
untouched. Exposed on `/metrics` alongside `arena_tick_duration_seconds` and the rest.

## Weapon system (iteration 26)

Four weapon types that shape a shot: **pistol** (basic single), **shotgun** (a pellet spread,
close-range damage), **sniper** (one fast bullet, big damage), **rocket** (area detonation on
impact). All deterministic simulation (in `Checksum`, replay-safe).

- **Selection via keys 1–4**, carried in the **high bits of `Input.Buttons`** (bits 5..7): the input
  wire format is unchanged. `Player.weapon` (selected) and `projectile.weapon` (what fired it — the
  projectile carries it in flight) are in `Checksum`; the `weaponSpecs` table is fixed (like `walls`)
  and stays out of the hash.
- **The rocket** explodes on a player OR a wall: `explode` deals area damage with linear falloff,
  skipping the owner (no self-damage), invulnerable players and teammates; targets are rewound by the
  same `rewind` as a direct hit (lag comp).
- **Wire**: every player's current weapon travels in the reliable `MsgWeaponState` (0x18) message,
  event-driven on switch/join — the snapshot/delta are untouched. The client draws its own weapon in
  the HUD and a tag over other fighters.
- **With buffs (iter. 19)**: rapid-fire shortens any weapon's cooldown; spread turns a single-pellet
  weapon into a fan (the old "pistol + spread = 3" is preserved). The tick is still 0 allocs/op.
