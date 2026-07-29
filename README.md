# Arena

**Русский** · [English](README.en.md)

[![CI](https://github.com/Azazlokus/rewind/actions/workflows/ci.yml/badge.svg)](https://github.com/Azazlokus/rewind/actions/workflows/ci.yml)
[![CodeQL](https://github.com/Azazlokus/rewind/actions/workflows/codeql.yml/badge.svg)](https://github.com/Azazlokus/rewind/actions/workflows/codeql.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Топ-даун .io-шутер с авторитетным сервером. Сервер на Go, клиент на canvas,
настоящий неткод: client prediction, server reconciliation, lag compensation и
interest management — добавляются итерация за итерацией.

> Статус: **итерация 15 — клиент/UX** (экран логина/регистрации на REST-бэкенде с
> токен-сессией, лидерборд, миникарта — чистый canvas/JS без сборщиков). До этого:
> persister (комната шлёт смерти и итоги матчей в канал, `internal/persist` пишет
> статистику/историю в БД вне горутины комнаты; джойн несёт токен-сессию — итер. 14B),
> жизненный цикл матча (FFA deathmatch с таймером: раунд на время, счёт убийств/смертей,
> детерминированный победитель, антракт и авторестарт; табло/таймер/баннер — итер. 14).
> Ещё раньше: фундамент бэкенда (аккаунты,
> статистика, история матчей — итер. 13), WebRTC доведён до продакшена (снапшоты по
> отдельному unreliable DataChannel, TURN и relay-only — итер. 12), транспорт WebRTC
> DataChannel рядом с WebSocket (итер. 11, `?transport=webrtc`), статичные стены
> (итер. 10), field-level дельта снапшота (итер. 9), широкофазная коллизия снаряд×игрок
> по сим-сетке (итер. 8), реплеи (`cmd/replay`, итер. 7), масштаб (interest management,
> дельты снапшотов, нагрузка 200 ботов — итер. 6), бой и lag compensation (итер. 5).
> План — в `CLAUDE.md`.

## Требования

- Go 1.22+ (разрабатывается на 1.26)
- Современный браузер
- Опционально: `golangci-lint` для полного линт-гейта (цели `make lint`/`check`
  откатываются на `go vet`, если его нет)

## Запуск

```sh
make run          # или: go run ./cmd/server
```

Откройте <http://localhost:8080>. Можно **войти или зарегистрироваться** (тогда матчи
идут под аккаунтом и копят статистику) — или просто ввести имя и играть гостём. Нажмите
**connect**. Движение — **WASD**; камера следует за вашим игроком (синий), остальные —
красные. Мышь целится, **ЛКМ** — огонь; снаряды жёлтые. Серые блоки — статичные стены:
сквозь них не пройти и не прострелить. HP, вспышка урона и экран смерти с респауном — в
HUD; **лидерборд** — сбоку, **миникарта** — снизу справа (итерация 15).

### Docker

```sh
make docker                                   # собрать образ arena-server:dev
docker run --rm -p 8080:8080 arena-server:dev # запустить, открыть http://localhost:8080
```

Многостадийная сборка даёт статический бинарь на distroless-образе (nonroot, без
shell). Готовые образы публикуются в GHCR: `ghcr.io/azazlokus/rewind` (push в `main`
и по тегу `vX.Y.Z`).

### Ручная проверка на двух вкладках (приёмка итерации 1)

1. `make run`
2. Откройте <http://localhost:8080> в **двух** вкладках браузера.
3. Подключитесь в обеих. Двигайтесь WASD в одной — во второй этот игрок (красный)
   движется плавно, и наоборот.

Чужие игроки рендерятся на 100 мс в прошлом с интерполяцией между снапшотами —
плавно даже при задержке 100–200 мс. Свой игрок предсказывается локально: отклик
на WASD мгновенный, а сервер лишь корректирует позицию (реконсиляция со
сглаживанием). Проверьте throttling'ом в devtools: свой персонаж не «залипает» на
задержке, а чужие остаются плавными.

## Конфигурация (переменные окружения)

| Переменная               | По умолчанию       | Смысл                                    |
|--------------------------|--------------------|------------------------------------------|
| `ARENA_ADDR`             | `:8080`            | адрес HTTP/WebSocket                      |
| `ARENA_PPROF_ADDR`       | `127.0.0.1:6060`   | адрес pprof (пусто — отключить)           |
| `ARENA_WEB_DIR`          | `web`              | каталог, отдаваемый на `/`                |
| `ARENA_TICK_RATE`        | `30`               | частота симуляции, Гц                     |
| `ARENA_SNAPSHOT_RATE`    | `20`               | частота снапшотов, Гц (интерполяция скрывает разрыв с тикрейтом) |
| `ARENA_MAX_PLAYERS`      | `64`               | игроков на комнату                        |
| `ARENA_MAX_ROOMS`        | `16`               | комнат на hub                             |
| `ARENA_AOI_RADIUS`       | `640`              | радиус interest management, юниты (0 — выкл) |
| `ARENA_SEED`             | `1`                | seed мира (детерминизм)                   |
| `ARENA_ALLOW_ALL_ORIGIN` | `true`             | пропускать проверку origin (для разработки) |
| `ARENA_STUN`             | (пусто)            | STUN URL для WebRTC через запятую; пусто — только host-кандидаты (localhost/LAN) |
| `ARENA_TURN`             | (пусто)            | TURN URL через запятую (для обхода NAT) |
| `ARENA_TURN_USER`        | (пусто)            | имя пользователя TURN |
| `ARENA_TURN_PASS`        | (пусто)            | пароль TURN |
| `ARENA_FORCE_RELAY`      | `false`            | WebRTC только через TURN-relay (host/srflx отброшены; жёсткие сети/приватность) |
| `ARENA_DB_DRIVER`        | `sqlite`           | бэкенд хранилища: `sqlite` (dev/CI) или `postgres` (prod) |
| `ARENA_DB_DSN`           | `arena.db`         | путь к файлу SQLite (или `:memory:`) либо DSN Postgres |
| `ARENA_AUTH_SECRET`      | (пусто)            | ключ подписи токен-сессий; пусто — эфемерный на запуск (токены не переживут рестарт) |
| `ARENA_TOKEN_TTL`        | `24h`              | время жизни токен-сессии |
| `ARENA_LOG_LEVEL`        | `info`             | `debug`/`info`/`warn`/`error`             |

## Транспорт

По умолчанию игра идёт по WebSocket. Итерация 11 добавила **WebRTC DataChannel** как
альтернативный транспорт: откройте <http://localhost:8080/?transport=webrtc> — клиент
поднимет DataChannel (сигналинг offer/answer по WS `/rtc`), а игровой протокол пойдёт
уже по нему. Фолбэка нет: транспорт выбирается явно, WebSocket остаётся дефолтом (и
путём для ботов/e2e).

Итерация 12 довела WebRTC до продакшена. Каналов теперь **два**: "game"
(ordered+reliable) несёт события и вводы, а снапшоты идут по отдельному "state"
(unordered+unreliable) — потерянный снапшот не ретрансмитится и не держит head-of-line
blocking для следующих. На localhost/LAN хватает host-кандидатов; для обхода NAT
задайте STUN (`ARENA_STUN`) или TURN (`ARENA_TURN` + `ARENA_TURN_USER`/
`ARENA_TURN_PASS`). `ARENA_FORCE_RELAY=true` заставляет соединяться только через
TURN-relay (жёсткие сети/приватность — реальный IP пира не светится). ICE-серверы и
политику relay сервер сообщает клиенту в сигналинге, поэтому обе стороны согласованы.

## Бэкенд (итерация 13)

Персистентный бэкенд для аккаунтов, статистики и истории матчей, архитектурно
отделённый от игрового ядра (модульный монолит с жёсткими границами):

- `internal/store` — интерфейс `Store` + одна SQL-реализация под **SQLite** (dev/CI,
  pure-Go, без внешней СУБД) и **PostgreSQL** (prod); миграции встроены и применяются
  на старте.
- `internal/account` — идентичность: гость + аккаунты (argon2id, подписанные HMAC
  токен-сессии). Гости эфемерны (имя в токене, без строки в БД).
- `internal/api` — REST на чистом `net/http`.
- `internal/persist` (итерация 14B) — шов игра → БД: комнаты шлют смерти и итоги
  матчей в канал, persister пишет их в `store` в своей горутине.

Игровое ядро (`internal/game`) бэкенд не импортирует. Связь идёт через persister:
комната шлёт `game.PersistMsg` в `Config.PersistSink` **неблокирующе** (переполнение
роняет статистику, но никогда не тормозит тик), а `internal/persist` переводит их в
вызовы `store` вне горутины комнаты. Джойн несёт токен-сессию — по нему шлюз
привязывает игрока к аккаунту (см. `MsgJoin` в [docs/protocol.md](docs/protocol.md)).

REST (`/api`):

| Метод + путь                    | Что делает                                   |
|---------------------------------|----------------------------------------------|
| `POST /api/register`            | регистрация `{username,password}` → токен    |
| `POST /api/login`               | логин → токен                                |
| `POST /api/guest`               | гостевой токен `{name}`                       |
| `GET  /api/me`                  | профиль по токену (Bearer)                    |
| `GET  /api/leaderboard`         | топ по убийствам (`?limit`)                   |
| `GET  /api/players/{id}/stats`  | статистика игрока                             |
| `GET  /api/players/{id}/matches`| история матчей игрока (`?limit`)              |

## Эндпоинты

- `/` — веб-клиент
- `/ws` — игровое WebSocket-соединение
- `/rtc` — сигналинг WebRTC (игровой транспорт — DataChannel: "game" reliable + "state" unreliable, итер. 11–12)
- `/api/*` — REST-бэкенд (итер. 13): аккаунты, профиль, лидерборд, история матчей
- `/metrics` — метрики Prometheus
- `/healthz` — проба живости
- pprof на `ARENA_PPROF_ADDR` (только localhost)

## Разработка

```sh
make check        # обязательный гейт перед коммитом: lint + race-тесты
make test         # go test -race -count=1 ./...
make integration  # e2e (реальный сервер + WS-боты), build tag `integration`
make fuzz         # фаззинг декодера протокола
make bench        # бенчмарки с -benchmem (см. BENCHMARKS.md)
make loadtest     # нагрузка: 200 ботов in-process, tick p99 и трафик (итер. 6C)
make replay       # демо реплея: записать сессию и проиграть headless (итер. 7)
make profile      # запуск с pprof и печать эндпоинта
make help         # список всех целей
```

Коммиты — на русском, по [Conventional Commits](https://www.conventionalcommits.org/ru/).

Работаем на feature-ветках (`feat/…`, `fix/…`, `docs/…`, `ci/…`), не коммитим прямо в
`main`: ветка → `make check` → PR → merge зелёным. Подробнее — в
[`CLAUDE.md`](CLAUDE.md) («Ветки и PR»).

## CI/CD и безопасность

Всё в `.github/`:

- **CI** (`ci.yml`) — на каждый PR: `make check`, `make integration`, короткий `make fuzz`.
- **CodeQL** (`codeql.yml`) — SAST от GitHub.
- **Security** (`security.yml`) — `govulncheck` (уязвимости в зависимостях/коде) и
  `gitleaks` (утечки секретов).
- **Dependabot** (`dependabot.yml`) — еженедельные обновления Go-модулей и экшенов.
- **Docker** (`docker.yml`) — сборка образа и публикация в GHCR.
- **Release** (`release.yml` + `.goreleaser.yaml`) — по тегу `vX.Y.Z` GoReleaser
  собирает бинари (server/loadtest/replay) под linux/darwin/windows × amd64/arm64,
  архивы с `web/` и checksums, и создаёт GitHub Release. Отчёт об уязвимостях —
  приватно, см. [`SECURITY.md`](SECURITY.md).

Выпуск релиза:

```sh
git tag v0.1.0 && git push origin v0.1.0   # запускает release.yml
```

## Документация

- [`docs/architecture.md`](docs/architecture.md) — компоненты, модель
  конкурентности, владение горутинами, детерминизм.
- [`docs/protocol.md`](docs/protocol.md) — формат сообщений v1.
- [`docs/testing.md`](docs/testing.md) — harness, детерминизм, fuzz, golden, e2e.
- [`CLAUDE.md`](CLAUDE.md) — зафиксированные решения, границы пакетов, правила.
- [`BENCHMARKS.md`](BENCHMARKS.md) — замеры по итерациям.

## Структура

```
cmd/server/        wiring, конфиг из env, WS-gateway, graceful shutdown
cmd/loadtest/      нагрузочный прогон: N ботов in-process, замер tick p99 и трафика
cmd/replay/        headless-проигрыш лога сессии со сверкой Checksum (реплеи, итер. 7)
internal/
  transport/       интерфейс Conn + WebSocket + WebRTC DataChannel + in-memory Pipe
  protocol/        типы сообщений + кодек (бинарный, дельта-снапшоты)
  game/            game loop, world, systems, clock, sessions — без сети
  hub/             менеджер комнат, распределение игроков
  bot/             headless-клиент (реконструкция дельт; ядро нагрузочного swarm)
  metrics/         инструменты Prometheus
web/               index.html + game.js (без сборщика)
```
