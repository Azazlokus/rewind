# Arena

**Русский** · [English](README.en.md)

Топ-даун .io-шутер с авторитетным сервером. Сервер на Go, клиент на canvas,
настоящий неткод: client prediction, server reconciliation, lag compensation и
interest management — добавляются итерация за итерацией.

> Статус: **итерация 11 — транспорт WebRTC DataChannel готов** (игровой `transport.Conn`
> теперь есть и поверх WebRTC DataChannel; сигналинг offer/answer по WS `/rtc`,
> выбирается `?transport=webrtc`, WebSocket остаётся дефолтом). До этого: статичные
> стены (итер. 10), field-level дельта снапшота (итер. 9), широкофазная коллизия
> снаряд×игрок по сим-сетке (итер. 8), реплеи (`cmd/replay`, итер. 7), масштаб
> (interest management, дельты снапшотов, нагрузка 200 ботов — итер. 6), бой и lag
> compensation (итер. 5). План — в `CLAUDE.md`.

## Требования

- Go 1.22+ (разрабатывается на 1.26)
- Современный браузер
- Опционально: `golangci-lint` для полного линт-гейта (цели `make lint`/`check`
  откатываются на `go vet`, если его нет)

## Запуск

```sh
make run          # или: go run ./cmd/server
```

Откройте <http://localhost:8080>, введите имя и нажмите **connect**. Движение —
**WASD**; камера следует за вашим игроком (синий), остальные — красные. Мышь
целится, **ЛКМ** — огонь; снаряды жёлтые. Серые блоки — статичные стены: сквозь них
не пройти и не прострелить. HP, вспышка урона и экран смерти с респауном — в HUD.

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
| `ARENA_STUN`             | (пусто)            | STUN/TURN URL для WebRTC через запятую; пусто — только host-кандидаты (localhost/LAN) |
| `ARENA_LOG_LEVEL`        | `info`             | `debug`/`info`/`warn`/`error`             |

## Транспорт

По умолчанию игра идёт по WebSocket. Итерация 11 добавила **WebRTC DataChannel** как
альтернативный транспорт: откройте <http://localhost:8080/?transport=webrtc> — клиент
поднимет DataChannel (сигналинг offer/answer по WS `/rtc`), а игровой протокол пойдёт
уже по нему. Фолбэка нет: транспорт выбирается явно, WebSocket остаётся дефолтом (и
путём для ботов/e2e). На localhost/LAN хватает host-кандидатов; для NAT задайте
`ARENA_STUN` (обе стороны получают один список).

## Эндпоинты

- `/` — веб-клиент
- `/ws` — игровое WebSocket-соединение
- `/rtc` — сигналинг WebRTC (игровой транспорт — DataChannel, итер. 11)
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
