# Локальный стек наблюдаемости (docker-compose)

Поднимает игровой сервер вместе с PostgreSQL, Prometheus и Grafana одной командой —
чтобы видеть метрики сервера на дашборде и получать алерты, не настраивая мониторинг
руками. Конфиги стека лежат здесь (`deploy/`), сам compose-файл — в корне репозитория
(`docker-compose.yml`), потому что сервер собирается из корневого `Dockerfile`.

## Запуск

```sh
cp .env.example .env        # правьте пароли/секреты под себя
make compose-up             # docker compose up -d --build
```

Порты наружу:

| Сервис     | URL                     | Что там                                             |
|------------|-------------------------|-----------------------------------------------------|
| Игра       | <http://localhost:8080> | клиент, WebSocket `/ws`, REST `/api/`, `/metrics`, `/healthz` |
| Prometheus | <http://localhost:9090> | метрики + вкладка **Alerts** (правила из `prometheus/alerts.yml`) |
| Grafana    | <http://localhost:3000> | дашборд **Arena → Overview** (логин `admin`, пароль из `.env`) |

Остановить: `make compose-down` (данные в томах сохраняются). Снести вместе с томами
(Postgres/Prometheus/Grafana начнут с чистого листа): `make compose-down V=1`.
Логи сервера: `make compose-logs`.

## Что внутри

- **postgres** (`postgres:17-alpine`) — хранилище аккаунтов/статистики/истории матчей.
  Сервер подключается к нему (`ARENA_DB_DRIVER=postgres`) и применяет миграции на старте.
- **server** — собирается из корневого `Dockerfile` (distroless, nonroot). У образа нет
  shell, поэтому HEALTHCHECK контейнера не задаём: живость видит Prometheus по
  `up{job="arena"}` (алерт `ArenaServerDown`).
- **prometheus** (`prom/prometheus:v3.1.0`) — скрейпит `server:8080/metrics` каждые 5 с,
  вычисляет правила алертов. Alertmanager намеренно не поднят (маршрутизация в Slack/почту
  деплой-специфична) — правила всё равно видны во вкладке Alerts и в Grafana.
- **grafana** (`grafana/grafana:11.4.0`) — источник данных Prometheus и дашборд «Arena»
  прописаны через provisioning (`grafana/provisioning/`), правок в UI не требуют.

## Дашборд «Arena — Overview»

Панели поверх метрик из `internal/metrics`:

- **Server** — `up{job="arena"}` (UP/DOWN).
- **Connected players** / **Active bots** / **Inbox depth** — текущие значения.
- **Tick duration (p50/p99)** — `arena_tick_duration_seconds`, порог-линия 15 мс (бюджет p99, итер. 6).
- **Snapshot bandwidth** — `rate(arena_snapshot_bytes_total[1m])`.
- **Entities per snapshot (p50/p99)** — `arena_entities_per_snapshot` (ограничено AOI, итер. 6).
- **Anti-cheat clamps** — `rate(arena_anticheat_events_total[…])` по типу (итер. 25).

## Алерты (`prometheus/alerts.yml`)

| Алерт                | Условие                                             | Severity |
|----------------------|-----------------------------------------------------|----------|
| `ArenaServerDown`    | `up{job="arena"} == 0` 30 с                          | critical |
| `ArenaTickP99High`   | тик p99 > 15 мс 2 мин                                | warning  |
| `ArenaInboxBacklog`  | `arena_inbox_depth > 64` 1 мин                       | warning  |
| `ArenaAntiCheatSpike`| клампы > 5/с за 5 мин                                | info     |

Чтобы алерты не только «горели», но и уходили в канал, добавьте Alertmanager и `alerting:`
секцию в `prometheus/prometheus.yml` (вне объёма локального стека).

## Правки конфигов

Имена метрик в `alerts.yml` и дашборде — зеркало `internal/metrics/metrics.go`. Меняете
метрику — правьте обе стороны. YAML-файлы и `arena.json` валидируются локально (JSON/YAML)
и подхватываются provisioning при рестарте соответствующего сервиса.
