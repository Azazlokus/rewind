# Тестирование и harness

Главный инструмент проекта — **детерминированная headless-симуляция**. Сеть,
таймеры и `time.Sleep` из тестов исключены: комната гоняется через фейковые
`transport.Conn` и ручные часы. Это делает тесты быстрыми, воспроизводимыми и
готовыми стать регрессиями.

## Команды

```sh
make check        # обязательный гейт: lint (vet + golangci-lint) + race-тесты
make test         # go test -race -count=1 ./...
make integration  # e2e с реальным сервером и WS-ботами (build tag `integration`)
make fuzz         # фаззинг декодера протокола
make bench        # бенчмарки с -benchmem (см. ../BENCHMARKS.md)
```

`-race` включён всегда. Ни один коммит с красным `make check`.

## 1. Headless-симуляция

`game.Room` конструируется без сети. Вместо реальных сессий — `transport.Pipe`
(пара in-memory каналов). Тесты подают вводы в inbox через клиентский конец пайпа
и читают снапшоты. Тикер подменяется `ManualClock`: тест сам двигает время.

Ключевой приём в `room_test.go` — `tickUntil`: он крутит часы в фоне и читает
снапшоты, пока не выполнится предикат. Так тест наблюдает мир, не гоняясь за
асинхронным writePump — снапшоты это поток, тест ждёт нужный кадр, а не
конкретную ячейку буфера. Никаких `time.Sleep`.

Покрыто: join/leave/input через inbox, дисконнект медленного клиента,
переполнение комнаты, graceful shutdown.

**Делитель частоты снапшотов** (итерация 2) вынесен в чистый тип `rateDivider` и
тестируется напрямую (`TestRateDivider`, `TestRateDividerEvenSpacing`): 20 Гц из
30 — ровно 20 снапшотов на 30 тиков, распределённых равномерно (разрыв ≤ 2 тика).
Сама клиентская интерполяция живёт в `web/game.js` и проверяется вручную
(throttling в devtools) — это JS, а не Go.

## 2. Детерминизм симуляции

`TestWorldDeterminism`: два независимых `World` с одним seed и одной лентой
вводов → после N тиков `World.Checksum()` совпадает байт-в-байт. Это фундамент
реплеев и клиентского предсказания.

`Checksum()` хеширует полное состояние (тик, порядок игроков, позиции/скорости в
битах float, hp, имена). `TestWorldSeedIndependence` страхует, что тест не
проходит тривиально (разные seed → разные состояния).

Правила детерминизма (см. [architecture.md](architecture.md)): обход только по
отсортированному `order`, случайность только через `World.rng`, время только
через `Clock`.

**Предсказание и реконсиляция** (итерация 4) проверяются `prediction_test.go`:
`TestPredictionMatchesAuthoritative` — свёртка общего `Step` по ленте вводов
приходит туда же, куда авторитетный мир; `TestReconciliationMatchesAuthoritative`
— для любой точки частичного подтверждения клиент, отбросив вводы с `seq <= ack`
и переиграв остаток поверх серверной позиции, приходит байт-в-байт в тот же итог.
Клиентская часть (`stepMove`/`reconcile` в `web/game.js`) зеркалит `Step` и
проверяется вручную (throttling в devtools) — это JS, а не Go.

## 3. Тесты протокола

- **Round-trip property-тест** — случайное сообщение → encode → decode → равно
  исходному.
- **Golden-тесты** — эталонные байтовые дампы в `internal/protocol/testdata/`.
  Обновление осознанным флагом: `go test ./internal/protocol -update`.
- **Fuzz декодера** (`FuzzDecode`) — мусор и обрезанные пакеты не должны вызывать
  панику. Прогон: `make fuzz` (30 с) или дольше локально.

## 4. Интеграционный тест (end-to-end)

`cmd/server/e2e_test.go`, тег сборки `integration`. Поднимает реальный
HTTP/WS-сервер на случайном порту (`httptest`), подключает WS-ботов из
`internal/bot` и проверяет сценарий на настоящем сетевом пути (upgrade →
handshake → inbox → tick → broadcast).

Итерация 1: `TestE2EMovementVisibleToPeer` — один бот двигается, другой видит
смещение (кодовая проверка критерия «два браузера видят движение друг друга»).
В итерациях 5–6 сценарий расширится: join → move → shoot → death → respawn.

Запуск: `make integration`.

## 5. Реплеи (итерации 5–6)

Комната умеет писать лог: seed + все вводы со **штампом тика** (бинарный формат в
`testdata/replays/`). Штамп тика обязателен: `World.Step` осушает вводы пачками —
все, что пришли за тик, — поэтому `cmd/replay` должен подавать ровно те же пачки,
что видела живая комната. Иначе разойдётся пер-тиковая сверка хэшей (а с
появлением пер-тиковых систем в итерации 5 — снаряды, перезарядки — и конечное
состояние). `cmd/replay` проигрывает лог headless и сверяет хэш. Каждый пойманный
desync-баг превращается в replay-файл и становится регрессионным тестом. На
итерации 1 — задел (`cmd/replay` появится позже).

## 6. Бенчмарки и профилирование

- `BenchmarkTick` (комната на 50/200 сущностей), `BenchmarkEncodeSnapshot`,
  `BenchmarkDecodeInput`, `BenchmarkAppendEntities` — все с `-benchmem`. Цель на
  горячем пути — 0 allocs/op (симуляция и кодек zero-alloc с итерации 3).
- `net/http/pprof` на отдельном localhost-порту (`ARENA_PPROF_ADDR`,
  `make profile`).
- Результаты по итерациям фиксируются в [../BENCHMARKS.md](../BENCHMARKS.md).

## 7. Метрики

Prometheus на `/metrics`: `arena_tick_duration_seconds` (histogram),
`arena_snapshot_bytes_total`, `arena_connected_players`, `arena_inbox_depth`.
Реализованы через интерфейс `game.Recorder`, чтобы симуляция не зависела от
Prometheus.
