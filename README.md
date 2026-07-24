# Arena

**Русский** · [English](README.en.md)

Топ-даун .io-шутер с авторитетным сервером. Сервер на Go, клиент на canvas,
настоящий неткод: client prediction, server reconciliation, lag compensation и
interest management — добавляются итерация за итерацией.

> Статус: **итерация 3 — бинарный протокол**. План — в `CLAUDE.md`.

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
**WASD**; камера следует за вашим игроком (синий), остальные — красные.

### Ручная проверка на двух вкладках (приёмка итерации 1)

1. `make run`
2. Откройте <http://localhost:8080> в **двух** вкладках браузера.
3. Подключитесь в обеих. Двигайтесь WASD в одной — во второй этот игрок (красный)
   движется плавно, и наоборот.

Клиент рендерит мир на 100 мс в прошлом, интерполируя между снапшотами, поэтому
чужие игроки движутся плавно даже при сетевой задержке 100–200 мс (проверьте
throttling'ом в devtools). Мгновенный отклик своего персонажа (предсказание)
появится в итерации 4 — пока свой игрок тоже отзывается с задержкой.

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
| `ARENA_SEED`             | `1`                | seed мира (детерминизм)                   |
| `ARENA_ALLOW_ALL_ORIGIN` | `true`             | пропускать проверку origin (для разработки) |
| `ARENA_LOG_LEVEL`        | `info`             | `debug`/`info`/`warn`/`error`             |

## Эндпоинты

- `/` — веб-клиент
- `/ws` — игровое WebSocket-соединение
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
internal/
  transport/       интерфейс Conn + ws-реализация + in-memory Pipe (для тестов)
  protocol/        типы сообщений + кодек (JSON в итер. 1, бинарный в итер. 3)
  game/            game loop, world, systems, clock, sessions — без сети
  hub/             менеджер комнат, распределение игроков
  bot/             headless-клиент (вырастает в нагрузочный swarm)
  metrics/         инструменты Prometheus
web/               index.html + game.js (без сборщика)
```
