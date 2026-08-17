# Arena Shooter — сервер на Go

Мультиплеерный топ-даун .io-шутер: авторитетный сервер на Go, браузерный клиент
на canvas. Полноценный неткод (prediction, reconciliation, lag compensation,
interest management), уровень production-инженерии.

## Зафиксированные решения (не пересматривать без явного запроса)

- Go 1.22+, модуль `arena`, без игровых фреймворков.
- Транспорт v1: WebSocket (`github.com/coder/websocket`). Игровой код зависит
  только от интерфейса `transport.Conn` — под WebRTC DataChannel потом без
  переписывания. (Итерация 11: WebRTC DataChannel добавлен рядом с WS через тот же
  `transport.Conn`, без переписывания игрового кода — как и планировалось. Итерация 12:
  снапшоты роутятся на опциональный `transport.UnreliableWriter` — best-effort путь у
  WebRTC, обычный `Write` у WS/Pipe; надёжность выражена на границе транспорта, коды
  сообщений в транспорт не протекают.)
- **Одна комната = одна горутина.** Игровое состояние НЕ защищается мьютексами.
  Всё снаружи заходит в комнату через каналы (`inbox`). Любой код, трогающий
  `World` из другой горутины — баг.
- Тикрейт 30 Гц, fixed timestep. Клиентский ввод 60 Гц. Снапшоты 20 Гц (итер. 2+).
- Протокол бинарный, little-endian, свой кодек. Без protobuf/JSON на горячем
  пути (JSON разрешён только в итерации 1 как временный).
- Горячий путь (tick, encode, decode) — zero-alloc. Буферы через `sync.Pool`,
  срезы переиспользуются. Проверяется бенчмарками с `-benchmem`.
- Клиент: чистый JS + canvas в `web/`, без сборщиков. Один `index.html`, один
  `game.js`.

## Границы пакетов (жёсткие)

- `internal/protocol` — только данные и кодек. **Не импортирует ничего игрового**
  и ничего сетевого. Фаззится и бенчмаркается изолированно.
- `internal/game` — симуляция. **Не знает про WebSocket**, только `transport.Conn`.
  Запускается в тестах вообще без сети (`transport.Pipe`, `ManualClock`).
- `internal/transport` — интерфейс `Conn` + ws-реализация + in-memory `Pipe`.
- `internal/hub` — менеджер комнат. `internal/bot` — headless-клиент.
  `internal/metrics` — Prometheus. `cmd/server` — wiring, конфиг из env, graceful
  shutdown.
- **Бэкенд (итерация 13), отделён от игры:** `internal/store` — персистентность
  (интерфейс `Store` + одна SQL-реализация под SQLite и PostgreSQL), **не знает про
  игру/сеть**; `internal/account` — идентичность (гость + аккаунты, argon2id, HMAC-
  токены), знает только `store` и криптографию; `internal/api` — REST на `net/http`.
  `internal/game` бэкенд **не импортирует** — результаты матчей поедут через отдельный
  persister (итерация 14), а не из горутины комнаты.

## Правила кодинга

1. Ошибки не глотать: обработать или обернуть `fmt.Errorf("...: %w", err)`.
   `_ = err` запрещён вне тестов.
2. Каждая горутина имеет владельца и путь завершения. Нет ответа «кто её
   останавливает» — баг.
3. Каналы закрывает только отправитель. **Room закрывает каналы сессий, не
   наоборот.**
4. Контекст пробрасывается от `main` до сессий; shutdown через отмену контекста.
5. **Комментарии в коде — на русском.** Идентификаторы, строки логов и ошибок —
   английские (это код). **Commit-сообщения — на русском, по Conventional Commits**
   (`type(scope): описание` в императиве, scope = пакет/область). Документация
   (`docs/`, README) — на русском; README дублируется на английский в
   `README.en.md`, обе версии держать синхронными.
6. Внутри симуляции запрещён обход map без сортировки ключей и любой
   недетерминированный источник (время, глобальный `rand`). Время — только через
   `Clock`, случайность — только через `World.rng`.
7. После каждой итерации — короткий отчёт (что сделано, замеры, что отложено) и
   апдейт `BENCHMARKS.md`.

## Harness / команды

```
make run        # go run ./cmd/server
make test       # go test -race -count=1 ./...
make fuzz       # go test -fuzz=FuzzDecode -fuzztime=30s ./internal/protocol
make bench      # go test -bench=. -benchmem -run=^$ ./...
make lint       # go vet ./... && golangci-lint run (если установлен)
make check      # lint + test — ОБЯЗАТЕЛЬНЫЙ ГЕЙТ перед любым коммитом
make loadtest   # go run ./cmd/loadtest -bots=200 -duration=60s
```

`-race` всегда включён в тестах. Ни один коммит с красным `make check`.
golangci-lint (когда доступен): errcheck, govet, staticcheck, ineffassign —
правила не отключать ради удобства.

## Ветки и PR

Работаем на **feature-ветках**, не коммитим прямо в `main`. Ветка от актуального
`main`, имя — по типу Conventional Commits: `feat/<область>`, `fix/<область>`,
`docs/…`, `ci/…`, `chore/…`, `refactor/…` (напр. `feat/webrtc-turn`). Одна ветка —
одна связная задача/итерация.

Цикл на ветке: `git switch -c <тип/область> main` → правки → `/audit` → `make check`
→ коммиты (Conventional Commits, RU) → `git push -u origin <ветка>` →
`gh pr create` (шаблон `.github/pull_request_template.md`) → merge зелёным. В `main`
попадает только через PR с зелёным CI.

**CI** (`.github/workflows/ci.yml`) зеркалит локальный гейт и гоняется на push в
`main` и на каждый PR: `check` (полный `make check` — golangci-lint ставится в CI,
поэтому линт не откатывается на один `go vet`), `integration` (`make integration`),
`fuzz` (`make fuzz`, короткий). Зелёный PR = зелёные все три. Локальный `make check`
всё равно обязателен перед коммитом — CI это дублирующая страховка, не замена.

Помимо CI в `.github/` живёт остальной пайплайн (DevSecOps/релизы): `codeql.yml`
(SAST), `security.yml` (`govulncheck` + `gitleaks`), `dependabot.yml` (обновления
gomod/actions), `docker.yml` (образ → GHCR `ghcr.io/azazlokus/rewind`), `release.yml`
+ `.goreleaser.yaml` (по тегу `vX.Y.Z` — бинари server/loadtest/replay, архивы с
`web/`, GitHub Release). Локально: `make docker`, `make vuln`. Отчёт об уязвимостях —
приватно (`SECURITY.md`), лицензия — MIT (`LICENSE`).

Защита ветки `main` включена в GitHub: required checks (`check`/`integration`/`fuzz`),
обязательный PR, запрет прямого пуша (`gh api .../branches/main/protection`).

## Инструменты Claude Code (`.claude/`)

Кастомный тулинг под этот репозиторий. Пользоваться им, а не изобретать заново.

**Агенты-ревьюеры** (`.claude/agents/`, только чтение — выдают findings, не правят):
- `concurrency-auditor` — модель конкурентности (одна комната = одна горутина,
  каналы закрывает отправитель, `-race`). После правок `internal/game`,
  `transport`, `hub`, wiring.
- `determinism-guard` — недетерминизм симуляции и покрытие `Checksum`. После
  правок шага мира / состояния `Player`/`World`.
- `protocol-guardian` — протокол, fuzz, zero-alloc, зеркало констант в
  `web/game.js`. После правок `internal/protocol` или клиента.

**Команды** (`.claude/commands/`, слэш): `/audit` — оркестратор, гонит трёх
стражей параллельно на текущем дифе и сводит findings (прогонять перед коммитом
итерации); `/check` — обязательный гейт; `/iter-report` — отчёт по итерации
(правило 7).

**Скилы** (`.claude/skills/`): `bench` (бенчи + инвариант zero-alloc), `fuzz`
(фаззинг + триаж крэшера). Официальные `simplify`, `security-review`,
`/code-review` — уже доступны, не дублировать.

**Хуки** (`.claude/hooks/`, активны после `/hooks` или рестарта): `gofmt` после
правок `.go`; `guard-golden` запрещает ручную правку `*.golden`;
`pre-commit-check` гонит `make check` перед `git commit` и блокирует при красном;
`session-start` + statusline печатают ветку и текущую итерацию.

Рабочий цикл: *ветка → исследовать → план → код → `/audit` → `make check` → коммит
→ push → PR → merge зелёным* (см. «Ветки и PR»).
Между несвязанными задачами чистить контекст (`/clear`); многошаговую итерацию
вести списком задач; параллельные независимые правки — в git-worktree-агентах.

## Инструменты тестирования (главное — детерминизм)

- **Headless-симуляция**: `game.Room` строится без сети. Фейковые `transport.Conn`
  через `transport.Pipe`. Тесты подают вводы в inbox, читают снапшоты. Тикер —
  `ManualClock`, никаких `time.Sleep`.
- **Детерминизм**: два `World` с одним seed и одной лентой вводов → после N тиков
  `Checksum()` совпадает байт-в-байт. Фундамент для реплеев и предсказания.
- **Протокол**: round-trip property-тест; golden-дампы в `testdata/` (обновление
  только осознанным `-update`); fuzz декодера (мусор/обрезки не паникуют).
- **E2E** (`//go:build integration`): реальный сервер на случайном порту, 2–3
  WS-клиента, сценарий join→move→shoot→death→respawn.
- **Реплеи**: room пишет seed+вводы; `cmd/replay` проигрывает headless и сверяет
  хэш. Каждый пойманный desync → replay-файл → регрессионный тест.

## Текущий статус

См. план по итерациям в исходном ТЗ. Сделаны итерации 1–4: скелет, интерполяция
(снапшоты 20 Гц, делитель частоты по Брезенхэму, рендер на 100 мс в прошлом),
бинарный протокол (JSON удалён; кодек zero-alloc; клиент на ArrayBuffer/DataView;
~15 КБ/с на клиента при 64 игроках) и предсказание с реконсиляцией: клиент
применяет свой ввод немедленно тем же общим `Step` и держит очередь
неподтверждённых вводов, а сервер потребляет тот же поток из очереди (осушает её
за тик шагом `inputDt = 1/60`); на каждом снапшоте клиент переигрывает
неподтверждённое поверх авторитетной позиции по `lastProcessedSeq` и сглаживает
остаточную коррекцию. Сделана **итерация 5** — бой: стрельба (`BtnFire`, кулдаун),
снаряды-сущности со свит-коллизией, урон, смерть/респаун и reliable-события
`Hit`/`Death`/`Spawn` (сервер копит их за тик, комната рассылает: `Hit` —
участникам, `Death`/`Spawn` — всем); плюс **lag compensation** — в каждом вводе
клиент шлёт `ViewTick` (тик, к которому интерполировал), сервер держит на каждом
игроке кольцо `posHist` (запись за тик под меткой снапшота) и при выстреле мотает
цели к позиции на `clamp(now − ViewTick, 0, maxRewindTicks)` тиков назад (кламп
окна — анти-чит); история и `rewind` снаряда входят в `Checksum`.

Сделана **итерация 6** — масштаб (три под-фазы, всё networking, вне `Checksum`):
**6A** interest management — сетка `aoiGrid` (клетка 256), `broadcast` шлёт каждому
только сущности в коробке `±AOIRadius` вокруг игрока (`Config.AOIRadius`,
`ARENA_AOI_RADIUS`=640, 0 — выкл; клиент не тронут, интерполяция уже терпима ко
входу/выходу сущностей); метрика `EntitiesPerSnapshot`. **6B** дельта-кодирование —
клиент шлёт `Input.AckTick`, сервер держит на сессию кольцо отправленных наборов и
кодирует снапшот дельтой против подтверждённой базы (`baseTick`/`changed`/`removed`;
`diffEntities` слиянием двух указателей; `EntityFieldMask` по квантованным полям;
полный, если база вытеснена/дельта не короче); реконструкция на клиенте (`web/game.js`
`applyDelta`) и в боте (`internal/bot` `reconstructor`) — remove затем changed.
**6C** нагрузка — `cmd/loadtest` (N ботов in-process через `Pipe`, автопилот), пул
буферов снапшотов (`Room.snapPool`, `sync.Pool` по `*[]byte`, write pump возвращает
после записи). Замер: 200 ботов, одна комната — tick p99 ≈ 2.5 мс (цель < 15 мс),
сущностей в снапшоте ~27 из 200, трафик ~7 КБ/с (−85% против полного бродкаста),
0 дропов; пул — −31% аллокаций. Приёмка зелёная (`make check` + `make integration`
+ `make loadtest`).

Сделана **итерация 7** — реплеи (детерминизм как инструмент; `internal/game/replay.go`,
`cmd/replay`). Мир пишет лог: `seed` + принятые события (join/leave/input) со штампом
тика; запись живёт во `World` (хуки в `AddPlayer`/`RemovePlayer`/`EnqueueInput`), опт-ин
(`Config.RecordReplay`/`EnableReplayRecording`), пишутся лишь принятые вводы. `Replay(log)`
реконструирует мир (тот же `NewWorld`+мутации+`Step`, хвостовые события со штампом ≥
`Ticks` — без `Step`) и сверяет `Checksum`; `cmd/replay [-verify]` — headless-проигрыш,
`cmd/loadtest -replay` — запись, `make replay` — демо. Закрыто слепое пятно `Checksum`:
`lastQueuedSeq`/`hasQueued` теперь в хэше. Декодер лога фаззится (`FuzzReplayDecode`,
`make fuzz`; пойман и закрыт крэшер `tickRate=0`). Приёмка зелёная (`make check` +
`make fuzz` + `make replay`).

Сделана **итерация 8** — широкофазная коллизия снаряд×игрок (`internal/game/combat.go`).
Вместо O(снаряды×игроки) — детерминированная сим-сетка живых игроков (`hitGrid`, клетка
256, геометрия общая с AOI), снаряд запрашивает лишь клетки вокруг своего сегмента
(`findHit`), O(снаряды×кандидаты), ~31–38× быстрее на 50–200 игроках. Совпадение с
брутфорсом БЕЗУСЛОВНОЕ: игрок кладётся в клетки по `rewindAABB` (AABB его позиций за окно
перемотки — текущая + кольцо истории за `[1, maxRewindTicks]`, та же индексация, что в
`targetPos`), поэтому запрос расширяется лишь на радиус коллизии и ни одно попадание не
теряется независимо от скорости; жертва — минимум id среди попавших (order-независимо).
`hitGrid`/`hitCand` — транзиентный индекс во `World`, в `Checksum` не входит; zero-alloc.
`TestBroadPhaseAgreesWithBruteforce` (300 сцен × 50 выстрелов, рассинхрон позиции/истории)
+ `TestBroadPhaseSkipsMidTickKilledTarget`. Провод/клиент не тронуты. determinism-guard
чист. Приёмка зелёная.

Сделана **итерация 9** — field-level дельта (`internal/protocol`, `internal/game/delta.go`,
клиент+бот). В дельте (`baseTick != 0`) изменившаяся сущность несёт не всю 12-байтную
запись, а лишь реально изменившиеся ПОЛЯ под 1-байтной маской: `[2B id][1B маска][поля
kind1/x2/y2/vx2/vy2/hp1]`. Маска считается `EntityFieldMask` по КВАНТОВАННОМУ виду (суб-
квантовое дрожание дельту не раздувает); `diffEntities` отдаёт `changed`+`masks`+`removed`.
Новая (отсутствующая в базе) сущность идёт под `FieldAll`. Полный снапшот (`baseTick == 0`)
остался 12-байтным — без регресса на старте. Реконструкция (наложение помеченных полей
поверх копии базы) идентична в трёх местах: `web/game.js` `applyDelta`, `internal/bot`
`reconstructor`, тест-`deltaClient`; absent-поля берутся из базы. Дельта — чисто провод,
`Checksum` симуляции не трогает. Замеры честные: кодек zero-alloc (encode/decode дельты
0 allocs/op); трафик 200 ботов −3 % при AOI 640 (churn: новая сущность = 13B, на 1B больше
полной) и −9 % при полном бродкасте (см. BENCHMARKS.md). Покрытие: `TestDeltaPropertyRoundTrip`
(2000 случайных дельт), `FuzzDecode` (сид дельты с сущностью), усиленный end-to-end
`TestDeltaReconstructionStaysConsistent` (Kind/HP переживают дельту). protocol-guardian
чист. Приёмка зелёная.

Сделана **итерация 10** — статичные стены/препятствия (`internal/game/walls.go`, клиент).
Фиксированная раскладка AABB-стен (`var walls`, обход по индексу — детерминированно).
Игрок сталкивается кругом: `resolveWalls` внутри общего `Step` (после клэмпа границ)
выталкивает круг радиуса `PlayerRadius` из каждой стены — проекция на ближайшую точку
коробки + сдвиг по нормали, вырожденный «центр внутри» выходит через ближайшую грань.
Снаряд — отрезком: `segmentWallHit` (метод слэбов, отрезок vs расширенный на радиус AABB)
в `stepProjectiles` гасит снаряд о стену и подрезает сегмент проверки попадания до точки
входа (цель ПЕРЕД стеной ранена, ЗА — нет). `spawnPoint` перебрасывает точку через `w.rng`
до `spawnTries` раз, пока не выйдет из стен (детерминированно — реплей-безопасно). Стены
статичны и одинаковы во всех мирах, поэтому в `Checksum` НЕ входят: в хэш идёт уже
исправленная позиция (`X`/`Y`) и факт гашения снаряда (`w.projectiles`). Раскладка +
алгоритм зеркалятся в `web/game.js` (`WALLS`, `resolveWalls` в `stepMove`, рендер
`drawWalls`) — предсказание повторяет коллизию тем же кодом; координаты И порядок стен
держать синхронными. Замеры честные: A/B на одной машине — тик +~4–6 % (движение), бой
+~17 % (`segmentWallHit` по ~128 снарядам), всё 0 allocs/op (см. BENCHMARKS.md). Покрытие:
`walls_test.go` (упор в грань, выталкивание изнутри, блок снаряда стеной, спавн вне стен,
детерминизм коллизии). Все три стража чисты (concurrency/determinism/protocol); их совет —
зеркало `WALLS` защищено лишь комментарием (как и весь клиентский мир), тест-парити на JS
не вводил ради консистентности с зафиксированной конвенцией. Приёмка зелёная (`make check`
+ `make integration`).

Сделана **итерация 11** — транспорт WebRTC DataChannel (`internal/transport/webrtc.go`,
`cmd/server`, клиент). Вторая реализация `transport.Conn` поверх WebRTC DataChannel рядом
с WebSocket; симуляция/кодек/сессии не тронуты (абстракция под это и строилась). Весь pion
живёт в `internal/transport` и наружу не протекает: `AcceptWebRTC` (сервер-answerer) и
`DialWebRTC` (клиент-offerer) принимают сигналинг-`Conn` и отдают игровой `Conn` (`rtcConn`);
`cmd/server` `/rtc` апгрейдит сигналинг-WS и зовёт `AcceptWebRTC`, дальше — общий
`gateway.serve` (тот же, что у WS). Сигналинг — 3 JSON-сообщения по WS (config/offer/answer),
ICE **non-trickle** (ждём `GatheringCompletePromise`, отдельных candidate-сообщений и фоновой
горутины сигналинга нет); `ARENA_STUN` задаёт ICE-серверы (пусто — host-кандидаты для
localhost/LAN). Выбор транспорта — явный (`web/game.js` `?transport=webrtc`), **фолбэка нет**,
WS остаётся дефолтом и путём ботов/e2e. DataChannel "game" ordered+reliable (дефолт pion) =
семантика WS, поэтому reliable-события и дельты с ack работают без изменений. Конкурентность:
`rtcConn` своих горутин не заводит — входящие идут колбэком pion в буфер `recv` (backpressure,
не потеря), `shutdown` под `closeOnce` закрывает `done` затем `pc.Close()`; проверено `-race`.
Покрытие (integration): `TestWebRTCLoopbackRoundTrip` (`transport`), `TestE2EWebRTCMovement`
(`cmd/server`), `bot.DialWebRTC`. Провод не тронут (protocol-guardian чист); concurrency-
аудит проведён вручную (агент упал по лимиту сессии) — чисто. Приёмка зелёная (`make check`
+ `make integration`).

Сделана **итерация 12** — WebRTC до продакшена (`internal/transport/webrtc.go`, `conn.go`,
`internal/game/session.go`, `cmd/server`, клиент). Провод (`internal/protocol`) не тронут.
Два независимых улучшения. **(1) Второй DataChannel под снапшоты.** Каналов теперь два:
"game" ordered+reliable (JoinAck/Spawn/Death/Hit/вводы) и "state" unordered+unreliable
(`MaxRetransmits=0`) под снапшоты — потерянный снапшот не ретрансмитим (устарел) и не держит
head-of-line blocking для следующих. Оба создаёт offerer, answerer принимает через
`OnDataChannel` (роутинг по метке в `bindChannel`); оба кладут в один `recv` (различие по типу
сообщения). Надёжность выражена опциональным `transport.UnreliableWriter` (`WriteUnreliable`):
`Session.sendSnapshot` выбирается один раз в `newSession` (type-assert) — best-effort у WebRTC,
обычный `Write` у WS/`Pipe` (без регресса). Клиент устойчив к дропам/reorder уже дельтой (ack
только реконструированного тика, фолбэк на полный при выпавшей базе, `pushSnapshot` игнорит
переупорядоченные); добавлена защита `ackTick` от отката (`lastSnapTick`), зеркалится в
headless-боте (`internal/bot`, `hasSnap`, `TestBotSkipsStaleSnapshot`). `rtcConn` двухканальный:
поля каналов под `mu` при привязке, `opened` закрывается по ОБОИМ открытым (`onChannelOpen`),
после `awaitOpen` поля неизменны и читаются без `mu` (happens-before через `close(opened)`);
своих горутин по-прежнему нет. **(2) TURN.** `ICEServer` несёт `Username`/`Credential` (статические
креды TURN); env `ARENA_TURN`+`ARENA_TURN_USER`/`ARENA_TURN_PASS`; `ARENA_FORCE_RELAY` включает
`ICETransportPolicy=relay` (только через TURN — жёсткие сети/приватность). ICE-серверы и
relay-политику сервер сообщает клиенту в `config`-сигналинге. Покрытие (integration):
`TestWebRTCUnreliableDelivers`, `TestWebRTCTURNRelay` (in-process `pion/turn`, relay-only) —
`transport`; `TestSnapshotRoutedUnreliably`/`FallsBackToReliable` — `game`; `TestICEServersFromEnv`
— `cmd/server`; прежний `TestE2EWebRTCMovement` теперь гоняет снапшоты по unreliable-каналу.
Стражи чисты: concurrency-auditor (happens-before полей доказан, двойного закрытия/утечек нет,
`-race` 3×) и protocol-guardian (провод не тронут). Приёмка зелёная (`make check` + `make integration`).

Сделана **итерация 13** — фундамент бэкенда (`internal/store`, `internal/account`, `internal/api`,
wiring `cmd/server`). Персистентность аккаунтов/статистики/истории матчей, архитектурно отделённая
от игрового ядра (модульный монолит, жёсткие границы; `internal/game` бэкенд не импортирует).
**Store**: интерфейс + одна SQL-реализация, параметризованная диалектом, под SQLite (dev/CI, pure-Go
`modernc.org/sqlite`, без cgo) и PostgreSQL (prod, `pgx`); миграции встроены (`embed.FS`) и версионно
применяются на старте; таймстемпы — unix-millis, булевы — INTEGER 0/1 (единый маппинг между СУБД).
**Account**: гость + аккаунты (argon2id PHC, подписанные HMAC-SHA256 токен-сессии, самодостаточные —
проверка без БД); гости эфемерны (имя в токене, без строки в БД, `AccountID==0`). **API**: REST на
чистом `net/http` (роутинг метод+паттерны Go 1.22) — register/login/guest/me/leaderboard/players.
Покрытие: `store` (общий сьют на SQLite всегда + Postgres при `ARENA_TEST_POSTGRES_DSN`, сервис в CI),
`account` (регистрация/логин/гость/токены/валидация), `api` (httptest, коды ошибок), smoke живого
сервера. Игровое ядро не тронуто, поэтому стражи (concurrency/determinism/protocol) не запускались —
изменений в модели конкурентности/детерминизме/проводе нет; `store` конкурентно-безопасен
(`database/sql`). Приёмка зелёная (`make check`). Следующее: итерация 14 — жизненный цикл матча +
persister (комната → канал → `Store`), итерация 15 — клиент/UX.

Сделана **итерация 14** — жизненный цикл матча (`internal/game/match.go`, `world.go`, `combat.go`,
`room.go`, `internal/protocol`, клиент). FFA deathmatch с таймером: раунд на время, счёт, победитель,
антракт и авторестарт — всё в симуляции, детерминировано, персистер отложен на 14B. **Симуляция:**
`Player.Kills/Deaths`, во `World` — `matchPhase`/`matchAt`/`winner`; `stepMatch` в общем `Step` гонит
переходы по `w.Tick` (matchActive `matchDurationTicks`=5400 → matchIntermission `intermissionTicks`=300
→ новый матч). `endMatch` фиксирует победителя (`leader()` — обход `w.order`, tiebreak по минимальному
id); `startMatch` обнуляет счёт и респаунит через `w.rng`; фраг/смерть считаются в `applyDamage`. Всё
новое состояние (Kills/Deaths, phase/matchAt/winner) — будущее, поэтому **в `Checksum`**; константы
длительностей (не конфиг) реплей-безопасны. **Провод:** новое reliable-сообщение `MsgMatchState` (0x15,
`[1B phase][4B remaining][2B winner][1B count]` + count×`[2B id][2B kills][2B deaths][1B nameLen][name]`);
кодек bounds-checked, zero-alloc общего пути не тронут. Комната шлёт его **событийно, не поллингом**
(`broadcastMatchState` при смене фазы / смерти / входе-выходе + новичку сразу), reliable — в тишине
провод молчит; своих горутин не заводит, `matchDirty` и буферы табло — на горутине цикла. **Клиент**
(`web/game.js`) зеркалит формат: табло (K/D, своя строка подсвечена), таймер (отсчитывается локально
между обновлениями), баннер победителя в антракте. **Покрытие:** `match_test.go` (счёт, жизненный цикл,
сортировка табло, покрытие Checksum) + `TestMatchDeterminismAcrossFullCycle` (два мира, одна лента со
стрельбой, ≥5800 тиков через полный цикл, Checksum-равенство каждый тик — закрывает слепое пятно
старого детерминизм-теста к скорингу и переходам фаз с rng); round-trip `MatchState`, fuzz-сид.
Все три стража чисты: concurrency (новых горутин/каналов/`close` нет, `-race` 3×), determinism (всё
состояние в Checksum, недетерминизма нет; их совет — регресс-тест на полный цикл — учтён), protocol
(клиент↔сервер байт-в-байт, fuzz без крэшеров, zero-alloc не просел). Известное детерминированное
ребро задокументировано в `startMatch` (двойной респаун на границе тика — безвреден). Приёмка зелёная
(`make check` + fuzz). Следующее: **14B** — persister (комната → канал → `Store`, результат матча в БД,
токен-джойн для привязки сессий к аккаунтам), затем итерация 15 — клиент/UX (логин, лидерборд, миникарта).

Сделана **итерация 14B** — persister (`internal/persist`, `internal/game/persist.go`, `room.go`, `world.go`,
`internal/protocol`, `cmd/server`, клиент). Персист статистики/истории матчей, отделённый от игрового
ядра: `internal/game` бэкенд по-прежнему НЕ импортирует. **Шов.** Комната шлёт game-определённые события
(`PersistMsg`: `PersistKill` — смерть, `PersistMatch` — итог) в `Config.PersistSink chan<- PersistMsg`;
инъекция канала — из wiring, тип живёт в `game`. Новый пакет `internal/persist` (импортирует `game`+`store`,
стрелка только persist→game) читает канал в СВОЕЙ горутине и переводит события в вызовы `Store`. **Отправка
из комнаты неблокирующая** (`Room.sendPersist`: `select`/`default`): переполнение роняет статистику (счётчик
+ warn-once, оба поля горутины цикла), но НИКОГДА не тормозит тик на внешнем I/O. Своих горутин комната не
заводит. **Статистика.** Kills/Deaths копятся вживую по смертям (`persistKill` в `dispatchEvents` → `AddStats`,
переживает дисконнект); Games/Wins + история — на итог матча (`persistMatchResult` при переходе бой→антракт,
счёт ещё не обнулён `startMatch`; `store.RecordMatch` сам добавляет только games/wins — не задваиваем
kills/deaths). Суицид/окружение (killer==victim / killer 0) — только death. Гости (`AccountID 0`) в статистику
и историю не пишутся; матч без зарегистрированных участников не пишется вовсе. Времена матча: `EndedAt` — от
`Clock` комнаты, `StartedAt` выводится из `matchDurationTicks` (не горячий путь, вне Checksum). **Токен-джойн.**
`protocol.Join` получил `Token` (`[nameLen][name][2B tokenLen][token]`, `MaxTokenLen 512`, `ErrTokenTooLong`);
шлюз `resolveIdentity` проверяет токен (`account.Verify`): валидный — имя ИЗ токена (анти-имперсонация) +
привязка `AccountID`; пустой/битый — деградация до гостя (`AccountID 0`) с именем из `Join`. `Player.AccountID`
— идентити-метаданные, как `Name`: ставится комнатой после `AddPlayer`, **НЕ в Checksum** и не в лог реплея
(симуляцию не трогает — иначе сломало бы реплеи). Провод/кодек zero-alloc горячего пути не тронут (Join —
рукопожатие, не горячий путь). **Lifecycle.** Персистер-горутина принадлежит `cmd/server`; при shutdown канал
закрывается ПОСЛЕ `h.Wait()` (все комнаты стоят → отправителей нет → безопасно), затем `<-persistDone` с
таймаутом grace; операции store идут на свежем `context.Background()` с таймаутом (переживают отмену серверного
контекста при сливе). **Покрытие:** `internal/persist` (live-статы, суицид/окружение, отсев гостей, пустой матч —
на in-memory SQLite), `internal/game` (эмит `persistKill`/`persistMatchResult`, nil-sink no-op, дроп при
переполнении), `cmd/server` (`resolveIdentity`: токен/гость/анти-имперсонация), protocol round-trip+fuzz Join с
токеном, e2e (боты-гости через новый формат). Три стража прогнаны (concurrency: неблокирующая отправка,
close-после-Wait, без новых горутин в игре; determinism: `AccountID` вне Checksum, симуляцию не трогает;
protocol: клиент↔сервер байт-в-байт, fuzz без крэшеров, zero-alloc не просел). Приёмка зелёная (`make check` +
`make integration` + fuzz). Следующее: итерация 15 — клиент/UX (логин, лидерборд, миникарта).

Сделана **итерация 15** — клиент/UX (`web/index.html`, `web/game.js`; **только фронтенд**, Go не тронут).
Три фичи поверх готового REST-бэкенда (итер. 13) и токен-джойна (итер. 14B), всё чистым canvas/JS без
сборщиков. **Логин/регистрация:** панель аккаунта ходит в `/api/register`/`/api/login`, токен-сессия
кладётся в `localStorage` под тем же ключом (`arena_token`), что читает `encodeJoin` — залогиненный игрок
автоматически заходит в матч под аккаунтом (сервер берёт имя из токена), гость по-прежнему просто вводит имя.
`loadMe` (`/api/me`) валидирует токен на старте и показывает статистику; протухший токен — тихий выход из
сессии. Поле name блокируется у залогиненного (имя — из токена). **Лидерборд:** `/api/leaderboard` → таблица
сбоку (K/D/W), автозагрузка + refresh каждые 15 c и по кнопке; своя строка подсвечена. **Миникарта:**
`drawMinimap` в `render` — границы арены (`SIM.MapSize`), стены (`WALLS`) и известные сущности (свой игрок
синий/крупнее, чужие красные; снаряды не рисуем). Набор ограничен interest management (AOI) — честно видно
окрестность в масштабе арены, не всю комнату. **Границы соблюдены:** провод/симуляция/зеркальные константы
(`PROTO`/`SIM`/`WALLS`/квантование) НЕ тронуты — миникарта читает уже-существующие `SIM.MapSize`/`WALLS`;
никаких Go-изменений, поэтому три стража (concurrency/determinism/protocol) не запускались — им нечего
ревьюить (нет изменений в конкурентности/детерминизме/проводе). Проверка: `make check` (Go зелёный, без
регресса), curl-smoke живого сервера против контракта клиента (`/`, `/game.js`, register/me/leaderboard/login/
bad-login — все коды и формы JSON совпадают с тем, что дёргает клиент), `node --check web/game.js`. Приёмка
зелёная. Следующее — по новым запросам (напр.: спектатор/наблюдатель, профиль игрока с историей матчей на
клиенте, звук, мобильное управление).

Сделана **итерация 16** — профиль игрока с историей матчей на клиенте (`web/index.html`, `web/game.js`;
**только фронтенд**, Go не тронут). Поверх готового REST-бэкенда (итер. 13, эндпоинты уже были) и persister'а
(итер. 14B, он же пишет статистику/историю). Модалка-оверлей `#profile`: клик по строке лидерборда (строки
`clickable`, `id` игрока приходит из `/api/leaderboard`) или кнопка **profile** в панели залогиненного
(`session.id` из `/api/me`/логина, в `localStorage` НЕ кладём) открывает карточку. `openProfile(id, name)` тянет
`/api/players/{id}/stats` и `/api/players/{id}/matches?limit=20` **параллельно** (`Promise.all`) и рисует: строку
статы (`K/D N/N (ratio) · wins W/G`) и таблицу истории (`when`/`mode`/`K`/`D`/`res`; `when` — `fmtWhen` из
`ended_at` unix-секунд в компактное локальное `MM/DD HH:MM`; `res` — W зелёным / L серым). Закрытие — ✕ / клик
по бэкдропу / **Escape**; выход из сессии закрывает открытый профиль. Ошибки бэкенда деградируют мягко (404 →
«player not found», прочее → «profile offline», пустая история → «no matches yet»). **Границы соблюдены:**
провод/симуляция/зеркальные константы НЕ тронуты — клиент читает уже-существующие REST-эндпоинты; никаких
Go-изменений, поэтому три стража (concurrency/determinism/protocol) не запускались (нечего ревьюить — ни
конкурентности, ни детерминизма, ни провода не касались; прецедент — итер. 13/15). Проверка: `make check` (Go
зелёный, без регресса), `node --check web/game.js`, curl-smoke живого сервера против контракта клиента
(register→`id`, `/api/players/{id}/stats`→`{kills,deaths,games,wins}`, `/api/players/{id}/matches`→`{matches}`,
отсутствующий игрок→404, лидерборд несёт `id`). Приёмка зелёная.

Сделана **итерация 17** — серверные боты, наполнитель комнат ИИ (`internal/botfill`, `internal/bot/autopilot.go`,
`cmd/server`, `cmd/loadtest`, `internal/metrics`; `internal/game` НЕ тронут). Чтобы одинокий игрок не скучал,
наполнитель держит в занятой комнате `ARENA_BOT_FILL` игроков. **Боты — обычные клиенты:** пакет `internal/botfill`
подключает их in-process через `transport.Pipe`+`room.Join` (тот же путь, что у людей и нагрузочного swarm), гоняет
автопилот из `internal/bot` и **мир не трогает** — только `Players()` (atomic), `Join`/`State` (канальный API) и
закрытие `Conn`. Поэтому пакет — потребитель шва рядом с `hub` (стрелка `botfill → game/bot/transport`), а не часть
симуляции. **Автопилот вынесен** в `internal/bot` (`Autopilot`/`Drain`) — переиспользуют и `loadtest`, и наполнитель
(дедуп; замеры loadtest не изменились — логика та же). **Управление ботами** (чистая функция `targetBots`,
юнит-тест таблицей): люди = `Players()−наши боты`; держим ботов до `Target`, уступая людям и не переполняя
`MaxPlayers`; в пустой комнате (0 людей) — ноль (не оживляем). **Конкурентность** (профиль стража): наполнитель
владеет 1 горутиной цикла + по 3 на бота (сессия, `Drain`, `Autopilot`), все под `wg`, путь завершения — отмена
`ctx`/закрытие `Conn`; `botHandle.done` закрывает горутина сессии (её владелец). **Снятие бота** — закрытие
клиентского конца `Pipe`; `leave` постится сессией, пока её `botCtx` ещё жив (`cancel` — в defer сессии, ПОСЛЕ
возврата `Run`), поэтому отмена не гонится с постингом `leave` и игрок удаляется надёжно. **Барьер против
«фантомных людей»**: при осушении прунинг завершённых ботов опережал бы `Players()` (сессия постит `leave` до
`close(done)`, но комната обработает его лишь на тике) → на тик бот выглядел бы человеком, наполнитель добавлял бы
замену бесконечно; поэтому увидев `done` зову `room.State(ctx)` — документированная точка синхронизации — перед
чтением `Players()`: happens-before (`leave` в inbox до `close(done)`; `done` виден до `State`; FIFO) гарантирует,
что `leave` обработан. **Порядок shutdown**: `cancel` → `srv.Shutdown` → `h.Wait()` → `<-fillerDone` →
`filler.Wait()` → `close(persistCh)`; комната shutdown НЕ ждёт горутины сессий (их владелец — наполнитель), боты —
гости (`AccountID 0`), персист не шлют. Метрика `arena_active_bots`. По умолчанию выключено (`ARENA_BOT_FILL=0`).
Стражи: **concurrency-auditor** прогнан (профильная итерация — новые горутины/каналы/проводка); determinism/protocol
не запускались (симуляция и провод не тронуты — боты кормят вводы обычным путём, нового состояния `World`/провода
нет). Покрытие: `internal/botfill` (`targetBots` таблицей; fill/yield/drain на реальной комнате, `-race` ×5),
`cmd/server` (`TestBotFillConfig`; e2e `TestE2EBotFillGivesLoneHumanCompany` — реальный WS-человек получает ботов и
осушение по уходу, `-race` ×3). Приёмка зелёная (`make check` + `make integration` + live-smoke бинаря).

Сделана **итерация 18** — звук боя (`web/index.html`, `web/game.js`; **только фронтенд**, Go не тронут).
Web Audio-синтез **без ассетов** (осцилляторы + затухающий шум через `sfx.*`), навешенный на УЖЕ приходящие
reliable-события боя — провод/симуляция не тронуты, звук читает те же события, что и HUD. Хуки: `onSpawn`
(свой респаун → `sfx.respawn`), `onDeath` (своя смерть → `sfx.death`; `killer==мы` → `sfx.kill` — событие
приходит всем), `onHit` (жертва мы → `sfx.hurt`; атакующий мы → `sfx.hit`-хитмаркер — событие приходит
участникам), выстрел — в `startInput` при бите `BtnFire`, троттл `FIRE_SOUND_MS=300` (аппроксимация серверного
кулдауна 0.3 с — косметика, НЕ симуляция; рассинхрон безвреден). `AudioContext` создаётся/резюмится лениво по
первому жесту (`connect`/тумблер) — до жеста браузер аудио глушит. Тумблер **sound** в HUD, состояние в
`localStorage` (`arena_sound`, по умолчанию вкл). **Границы соблюдены:** провод/симуляция/зеркальные константы
НЕ тронуты; никаких Go-изменений, поэтому три стража (concurrency/determinism/protocol) не запускались (нечего
ревьюить; прецедент — итер. 13/15/16). Проверка: `make check` (Go зелёный, без регресса), `node --check
web/game.js`, live-smoke живого сервера (кнопка `sound` в `/`, модуль `sfx`/`audioCtx` и все 6 хуков в
`/game.js`). Приёмка зелёная.

Сделана **итерация 19** — оружие/пикапы (`internal/game/pickups.go`, `world.go`, `combat.go`, `room.go`,
`internal/protocol`, клиент). Бонусы на фиксированных точках арены: **аптечка** (мгновенный хил),
**ускорение стрельбы** и **веер** (оба — временные буфы, чистятся при респауне). Наступив на пикап, игрок его
подбирает. **Симуляция.** `pickups.go`: точки `pickupSpots` (обход по индексу), `stepPickups` в `World.Step`
(фаза 4, после респауна, до `Tick++`) активирует созревшие точки (тип через `w.rng.IntN`, тайминг спавна/
респауна — по `w.Tick`) и отдаёт активный пикап игроку с МЛАДШИМ id (обход `w.order`, break на первом
накрывшем). Эффекты: аптечка — `min(100, HP+medkitHeal)`; ускорение/веер — таймер-баф `Player.rapidUntil`/
`spreadUntil` до `w.Tick+длительность`. `tryFire` читает `rapidUntil` (короче кулдаун) и `spreadUntil` (веер
`spreadCount` снарядов симметрично, общий `spawnProjectile`); `respawn` чистит буфы. Всё новое сим-состояние
(буфы + `active/kind/readyAt` каждой точки) — будущее, поэтому **в `Checksum`**; спавн/подбор детерминированы,
реплей-безопасны (курсор rng уже в хэше самосогласовывает поток розыгрышей). `pickupsDirty` — networking-флаг,
сброс в начале `Step`, **в `Checksum` НЕ входит**. **Провод.** Новое reliable-сообщение `MsgPickupState` (0x16,
`[1B count]` count×`[1B spot][1B kind]`) — полный набор активных точек. Снапшот/дельта/AOI и их пер-тик
счётчики сущностей **НЕ тронуты**: пикапы в снапшоте не едут (раздули бы дельту), координаты точек статичны и
зеркалятся клиентом (`PICKUP_SPOTS`, как `WALLS`), а динамику несёт это событийное сообщение. Кодек zero-alloc,
bounds-checked; round-trip (непустое+пустое), fuzz-сид, golden. **Комната.** `broadcastPickups` событийно (как
табло матча) — при `World.PickupsDirty` (спавн/подбор) и новичку при входе; своих горутин не заводит, буфы
`pickupBuf`/`ppickups` — горутина цикла. **Клиент** (`web/game.js`): зеркало `PICKUP_SPOTS`, декод 0x16, рендер
иконок по типу (`drawPickups`) + миникарта — чистый рендер, подбор авторитетен на сервере (в предсказание не
входит, дрейфа нет). **Покрытие:** `pickups_test.go` (спавн, эффекты аптечки/ускорения/веера, чистка буфов на
респауне, респаун точки, `TestPickupStateInChecksum` — покрытие Checksum, `TestPickupDeterminism` — два мира,
одна лента, равенство Checksum каждый тик через полный цикл), `room_test.go` (`TestRoomBroadcastsPickupStateOnJoin`
— клиент получает `MsgPickupState` по проводу), protocol round-trip/fuzz/golden/zero-alloc-bench. **Три стража
чисты:** determinism (всё новое состояние в Checksum, `pickupsDirty` исключён, rng-поток самосогласован —
вердикт «детерминизм сохранён»), protocol (клиент↔сервер байт-в-байт, fuzz без крэшеров, zero-alloc не просел,
горячий путь снапшота не тронут), concurrency (новых горутин/каналов нет, `-race` ×3 включая integration).
Приёмка зелёная (`make check` + `make integration` + fuzz).

Сделана **итерация 20** — киллстрики + окно неуязвимости (`internal/game/killstreak.go`, `combat.go`,
`world.go`, `match.go`, `room.go`, `internal/protocol`, клиент). Две связанные боевые механики, обе —
детерминированная симуляция (в `Checksum`, реплей-безопасны), без розыгрышей rng. **Окно неуязвимости:**
`Player.invulnUntil` (в Checksum); `respawn` ставит его на `spawnInvulnTicks` (~2 c) — свежереспаунившийся
неуязвим, `findHit` его пропускает (снаряд проходит НАСКВОЗЬ, урона/событий нет) — анти-спавн-килл. Щит
спадает, как только игрок сам стреляет (`tryFire` → `invulnUntil = 0`) — нельзя бить из-под щита. Только
респаун (не первичный `AddPlayer` — иначе сломались бы боевые тесты, стреляющие сразу после входа; и семантика
осмысленна). **Киллстрики:** `Player.streak` (в Checksum) — серия убийств без смертей; растёт в `applyDamage`
за фраг (attacker != victim), обнуляется в смерти и в `startMatch`. Каждые `killstreakStep`(3) фрагов подряд —
веха (`recordKill`): мгновенный хил до 100 + короткий щит (`killstreakInvulnTicks`) + reliable-событие
`EventKillstreak`. **Провод:** новое reliable `MsgKillstreak` (0x17, `[2B id][2B streak]`) через тот же
event-пайплайн (`dispatchEvents` → `reliableAll`); снапшот/дельта НЕ тронуты. Кодек zero-alloc, bounds-checked,
round-trip/fuzz/golden. Тест-эталон `findHitBruteforce` получил тот же invuln-скип для паритета с `findHit`.
**Клиент** (`web/game.js`): щит-кольцо (пульсирующее) по событиям `MsgSpawn`/`MsgKillstreak` на зеркалимую
длительность + баннер серии — чистый рендер, неуязвимость авторитетна на сервере (ранний сброс щита выстрелом
клиенту не виден — кольцо может подзадержаться, безвредно). **Покрытие:** `killstreak_test.go` (spawn-invuln
блокирует урон + контроль без щита, снаряд насквозь, выстрел снимает щит, respawn даёт invuln, streak растёт/
обнуляется в смерти, суицид не растит, веха хил+щит+событие, `TestKillstreakStateInChecksum`,
`TestKillstreakDeterminism` — два мира, непрерывный бой, равенство Checksum каждый тик), protocol round-trip/
fuzz/golden/zero-alloc-bench. **Три стража чисты:** determinism (invulnUntil/streak в Checksum, rng не тронут,
паритет findHit↔bruteforce), protocol (клиент↔сервер байт-в-байт, fuzz без крэшеров, снапшот не тронут),
concurrency (новых горутин/каналов нет, dispatch по колее EventDeath/Spawn, `-race` зелёный). Приёмка зелёная
(`make check` + `make integration` + fuzz).

Сделана **итерация 21** — рейт-лимит на auth (`internal/api/ratelimit.go`, `api.go`, `cmd/server/config.go`,
`main.go`; **чистый бэкенд**, игра/провод/симуляция НЕ тронуты). Защита незалогиненных token-минтящих POST'ов
(`/api/register`, `/api/login`, `/api/guest`) от брутфорса/спама. **Алгоритм:** пер-IP токен-бакет (`ipLimiter`)
— ёмкость `Burst`, дозаправка `Burst/Window` токенов/с; исчерпал — `429` + `Retry-After`. **Конкурентность:**
всё под `sync.Mutex` (карта бакетов + поля), проверено `-race` (`TestRateLimitConcurrent` — 400 запросов на ключ
из 40 горутин, ровно `burst` проходит на замороженных часах). **Без фоновых горутин:** простаивающие (полные)
бакеты подчищаются ленивым свипом на запросе (не чаще `sweepInterval`), поэтому карта не растёт при живом трафике
и статична в тишине — не понадобилось владельца/пути завершения горутины. **Ключ клиента:** хост из `RemoteAddr`
по умолчанию; `ClientIPHeader` (env `ARENA_AUTH_RATE_IP_HEADER`, напр. `X-Forwarded-For`) — опт-ин за доверенным
прокси (иначе IP подделают; задокументировано). **Часы инъектируемы** (`now func() time.Time`) — тесты
детерминированы без sleep. **Провод:** middleware навешен в `Routes()` на три auth-роута; выключен (`Burst<=0`) —
сквозной путь, поэтому старые api-тесты не тронуты (передают `RateLimit{}`). **Конфиг:** env `ARENA_AUTH_RATE_BURST`
(деф. 10), `ARENA_AUTH_RATE_WINDOW` (деф. 1m), `ARENA_AUTH_RATE_IP_HEADER` (деф. пусто); включён по умолчанию.
**Стражи игры (concurrency/determinism/protocol) НЕ запускались** — изменений в игровой конкурентности/детерминизме/
проводе нет (прецедент — итер. 13/15/16/18); вместо этого прогнан официальный **security-review** (фича безопасности)
— findings нет (spoofing-байпас и рост карты — исключённые категории rate-limit/DoS, к тому же смягчены дефолтом и
свипом). **Покрытие:** `ratelimit_test.go` (allow/deny, дозаправка и её кап через инжект-часы, изоляция по IP,
`clientKey` из RemoteAddr/заголовка, 429+Retry-After, свип идле-бакетов, `-race` конкурентно), `api_test.go`
(`TestRateLimitWiredThroughRoutes` — лимит реально навешен на роут через `Routes()`), `cmd/server` (`TestAuthRateConfig`).
Приёмка зелёная (`make check` + `make integration`).

Сделана **итерация 22** — спектатор/наблюдатель (`internal/protocol`, `internal/game/session.go`,
`room.go`, `cmd/server/gateway.go`, клиент). Присоединение к комнате БЕЗ спавна. **Ключевое:**
наблюдатель — чисто сетевой концепт на уровне комнаты, в `World`/`Checksum`/симуляцию НЕ входит
вовсе (детерминизм не тронут — determinism-guard не запускался, нечего ревьюить). **Провод:**
`Join.Spectator` — ОПЦИОНАЛЬНЫЙ завершающий байт (`[...token][1B spectator?]`); старый формат без
байта = обычный игрок (обратная совместимость, боты не ломаются). `MsgJoinAck` наблюдателю несёт
`YourID == 0` — sentinel «своей сущности нет» (allocID стартует с 1, 0 никогда не валидный id).
**Сессия/комната:** `Session.spectator` (readPump НЕ форвардит его MsgInput — id наблюдателя не
попадёт в `World.EnqueueInput`; Run зовёт `leaveSpectator`). Наблюдатели живут в `Room.spectators`
(отдельно от `sessions`, ключ — room-local `nextSpecID`, в пространство id сущностей не попадает),
не считаются игроками (`Players()` их не видит; `Spectators()`/`specCount` — отдельно). Им шлётся
весь мир (`broadcastSpectators`, вне AOI — позиции нет; всегда полный снапшот, т.к. ack не шлют) и
reliable-события (`reliableAll` обходит и sessions, и spectators). `removeSpectator` закрывает
очереди (как removeSession, но World не трогает); shutdown закрывает и наблюдателей. `sendSnapshot`
→ `sendSnapshotTo(s, lastSeq, view)` (без обязательного `*Player`). **Клиент** (`web/game.js`,
`index.html`): кнопка **spectate** → `encodeJoin(..., spectator=1)`; на `YourID==0` — режим
наблюдателя: своей сущности нет, ввод не шлётся, свободная камера панорамируется WASD (чистый
рендер, сеть не задействуется). **Покрытие:** `spectator_test.go` (наблюдатель видит игрока в
снапшотах но не в мире, его ввод игнорируется, получает MatchState/PickupState, уход не трогает
игроков), protocol round-trip Join со Spectator + fuzz-сид. **Стражи:** concurrency (модель сессий:
изоляция ключа наблюдателя от World, close-очередей отправителем, shutdown, `-race`) и protocol
(опциональный байт, обратная совместимость, fuzz, YourID sentinel) — прогнаны; determinism не
запускался (World/Checksum/провод горячего пути не тронуты). Приёмка зелёная (`make check` +
`make integration`).

Сделана **итерация 23** — командный режим (`internal/game/world.go`, `combat.go`, `match.go`,
`persist.go`, `replay.go`, `room.go`, `internal/protocol`, `cmd/server`, клиент). Две команды,
баланс при входе, дружественный огонь выключен, счёт/победитель по командам — детерминированная
симуляция (в `Checksum`, реплей-безопасна), без розыгрышей rng. **Симуляция.** `Player.team`
(0/1) ставится в `AddPlayer` детерминированным балансом `smallerTeam()` (обход `w.order`, в
меньшую команду) и дальше неизменна; `projectile.team` = команда стрелка. `findHit` пропускает
цель той же команды (`w.teamMode && tgt.team == pr.team`) — снаряд проходит сквозь союзника.
`endMatch` в teamMode кладёт в `w.winner` id ПОБЕДИВШЕЙ КОМАНДЫ (`winningTeam()` — больше
суммарных фрагов, tiebreak → 0), в FFA — id игрока (`leader()`); получатель различает по флагу.
`persist.go` `won(p)` — победа по совпадению команды. Всё новое СОСТОЯНИЕ (`Player.team`,
`projectile.team`) — **в `Checksum`**; сам флаг `teamMode` — фиксированный параметр мира (как
`tickRate`), в `Checksum` НЕ входит, но пишется в **лог реплея v2** (`SetTeamMode` до первого
join; декодер принимает и v1 FFA-логи). **Провод.** `MsgMatchState` получил `[1B flags]` (бит0 =
`matchFlagTeamMode`) после winner и `[1B team]` в каждой строке табло; заголовок 9 байт, строка 8+
имя. Снапшот/дельта/AOI и пер-тик счётчики сущностей **НЕ тронуты** — команда едет только этим
событийным сообщением (в снапшот не раздувает дельту). Кодек zero-alloc, bounds-checked;
round-trip/fuzz. **Комната.** `Config.TeamMode`; `NewRoom` зовёт `world.SetTeamMode(true)` до
первого join (happens-before старта горутины цикла); `encodeMatchState` несёт team; итог матча
пишется как `Mode="tdm"`. `cmd/server` — env `ARENA_TEAM_MODE`. **Клиент** (`web/game.js`): декод
нового формата (flags@8, count@9, team@off+6 — сверено байт-в-байт с энкодером), карта `id→team` из
табло, раскраска бойцов/миникарты/табло/баннера по командам (свои синие, враги красные), командный
счёт и баннер команды-победителя. **Покрытие:** `team_test.go` (баланс, friendly fire + контроль +
регресс FFA, счёт/победитель/`won`, покрытие `Checksum` team-полей, `teamMode` НЕ в `Checksum`,
`TestTeamDeterminism` — 2 мира ×900 тиков с friendly fire, `TestReplayTeamModeRoundTrip` — лог v2
переносит `teamMode` + негативный контроль, `TestMatchStateCarriesTeam`); `TestBroadPhaseAgreesWithBruteforce`
расширен teamMode-сценами (паритет `findHit`↔bruteforce на team-скипе, совет determinism-guard F1);
protocol round-trip/fuzz. **Три стража чисты:** determinism (team в `Checksum`, `teamMode` — параметр
мира в логе v2, `smallerTeam`/`winningTeam`/`findHit` по `w.order` без rng/time/map, паритет
findHit↔bruteforce; F1 «team-ветка bruteforce не сверялась» закрыт расширением теста), protocol
(**поймал критический баг**: клиентский декод MsgMatchState сдвигал flags/count/off на 1 байт под
2-байтным winner — сломало бы табло в ОБОИХ режимах; исправлено и сверено Node-трейсом против
Go-энкодера), concurrency (новых горутин/каналов нет, `SetTeamMode` в `NewRoom` до старта цикла —
happens-before, `-race` зелёный; мёртвый геттер `TeamMode()` удалён). Приёмка зелёная (`make check`
+ `make integration` + fuzz).

Сделана **итерация 24** — мобильное управление (`web/index.html`, `web/game.js`; **только фронтенд**,
Go не тронут). Твин-стик поверх canvas на сенсорных экранах поверх готового пути ввода. **Ключевое:**
стики кормят ТЕ ЖЕ `state.keys`/`state.aim`, что клавиатура/мышь, поэтому предсказание, кодирование
(`encodeInput`) и отправка на 60 Гц не тронуты — сенсор просто ещё один источник тех же состояний.
**Левый стик** (касание левой половины) — движение: вектор → 8 направлений WASD (`applyMoveStick`,
октанты по нормированной компоненте, мёртвая зона). **Правый стик** (правая половина) — прицел: угол
драга → `state.aim` напрямую + удержание огня (`state.keys.fire`). Обрабатываются Pointer Events с
`pointerType === 'touch'` (мышь/клавиатура — прежним путём; `setPointerCapture` держит палец за canvas);
`state.touchAiming` не даёт `render` перетереть `state.aim` позицией мыши, пока активен правый стик.
Стики рисуются (`drawTouchSticks`), только пока касание удерживается — на десктопе не видны. Canvas
сохраняет внутреннее разрешение 800×600, но `max-width: 100%` масштабирует его под узкий экран телефона;
`touchPoint()` пересчитывает касание из экранных координат в canvas по отношению сторон, поэтому стики
точны при любом масштабе; `viewport` получил `maximum-scale=1, user-scalable=no` (без пинч-зума в бою).
**Границы соблюдены:** провод/симуляция/зеркальные константы НЕ тронуты; Go-изменений нет, поэтому три
стража (concurrency/determinism/protocol) не запускались (нечего ревьюить — прецедент итер. 15/16/18).
Проверка: `make check` (Go зелёный, без регресса), `node --check web/game.js`, юнит-проверка октантного
маппинга стика (кардинали/диагонали/мёртвая зона), live-smoke живого сервера (viewport+responsive canvas
в `/`, `pointerdown`/`applyMoveStick`/`drawTouchSticks`/`touchAiming`/`STICK_RADIUS` в `/game.js`).
Приёмка зелёная.

Сделана **итерация 25** — античит-метрики (`internal/game/recorder.go`, `world.go`, `combat.go`,
`room.go`, `internal/metrics`, `cmd/loadtest`). Наблюдаемость поверх УЖЕ существующего анти-чита
(кламп окна перемотки `clampRewind`) — сервер и без метрики авторитетно зажимает, счётчик лишь делает
попытки видимыми оператору. **Метрика:** Prometheus `arena_anticheat_events_total{kind}` (`CounterVec`),
метки `rewind_stale` (клиент прислал `ViewTick` дальше окна перемотки в прошлое — задержка/lag-switch) и
`rewind_future` (`ViewTick` из будущего — рассинхрон часов/подмена времени). **Симуляция.** Счётчики —
транзиентное поле `World.ac [antiCheatKindCount]uint64`: инкремент в `tryFire` перед `clampRewind` (по
знаку/величине `d = now − ViewTick`), НЕ влияет на исход выстрела. Заявлено и проверено стражем: **в
`Checksum` НЕ входят** и в лог реплея не пишутся — на симуляцию не влияют, реплей-безопасно. Слив —
`DrainAntiCheat()` (читает+обнуляет). **Шов.** Метка — строка в интерфейсе `game.Recorder.AntiCheat(kind
string, n int)` (не `AntiCheatKind`), чтобы `internal/metrics` не импортировал `game` (сохранена
структурная реализация Recorder без связывания). Enum `AntiCheatKind`+`String()` живёт в `game`; комната
переводит в метку. `reportAntiCheat()` сливает после каждого тика в `tick()` — всё на горутине комнаты
(мутация в `Step`, слив там же), новых горутин/каналов нет. `NopRecorder`/`loadtest.stats` получили
no-op. **Покрытие:** `anticheat_test.go` (инкремент stale/future и не-инкремент в окне, drain обнуляет,
`TestAntiCheatCountersNotInChecksum`, `TestRoomReportsAntiCheat` — слив в Recorder без задвоения, метки
стабильны), `internal/metrics` (`TestAntiCheatCounter` — `CounterVec` по меткам, значение через
`dto.Metric` БЕЗ новой зависимости — go.mod не разросся). **Стражи:** determinism (счётчики вне
`Checksum`, реплей-безопасно) и concurrency (мутация/слив на горутине комнаты, `-race`) прогнаны;
protocol не запускался (провод не тронут). Тик по-прежнему 0 allocs/op. Приёмка зелёная (`make check` +
`make integration`).

Сделана **итерация 26** — система оружия (`internal/game/weapon.go`, `combat.go`, `world.go`, `room.go`,
`internal/protocol`, клиент). Четыре типа оружия, задающих картину выстрела: пистолет (базовый), дробовик
(веер дробин), снайперка (одна быстрая пуля, большой урон), ракета (сплэш по площади) — детерминированная
симуляция (в `Checksum`, реплей-безопасна). **Состояние.** `Player.weapon` (выбранное — определяет будущий
выстрел, переживает респаун) и `projectile.weapon` (чем стреляли — снаряд несёт его в полёте, урон/сплэш при
попадании берутся из спека) — оба **в `Checksum`**. Таблица `weaponSpecs` фиксирована (как `walls`/
`pickupSpots`), в `Checksum` НЕ входит — в хэш идут её следствия (`vx/vy` снаряда, изменения HP).
**Переключение — без нового формата провода:** выбор едет в СТАРШИХ битах `Input.Buttons` (биты 5..7,
`WeaponSelect`; 0 — не менять, 1..4 — оружие), формат ввода не меняется. `Step` обрабатывает выбор ДО
выстрела («сменил и выстрелил» одним фреймом стреляет новым). **Сплэш ракеты:** `explode` при контакте с
игроком ИЛИ стеной бьёт по площади с линейным спадом; обход `w.order` + `math.Sqrt` (детерминизм как у
`Cos/Sin`), цели перематываются тем же `rewind`; владельца (без самоурона)/мёртвых/неуязвимых (итер. 20)/
союзников (итер. 23) пропускает. **Провод:** новое reliable `MsgWeaponState` (0x18, `[1B count]` count×
`[2B id][1B weapon]`) — полный набор, событийно при смене/входе (как `MsgPickupState`); снапшот/дельта и
пер-тик счётчики НЕ тронуты. Клиент (`web/game.js`): клавиши 1–4, оружие в HUD + подпись над бойцами, всегда
шлёт текущий выбор в старших битах. **Бафы (итер. 19):** ускорение режет кулдаун любого оружия (÷3, мин 1),
веер превращает однодробинное в `spreadCount`-веер (прежнее «пистолет + веер = 3» сохранено точь-в-точь).
**Покрытие:** `weapon_test.go` (переключение/игнор невалидного, картины выстрела, кулдауны, урон снайперки,
сплэш ракеты + фоллоф/само-урон/команда/неуязвимость/стена, персист через респаун, `TestWeaponInChecksum`,
`TestWeaponDeterminism` — 2 мира ×400 тиков со сменой оружия и ракетами), protocol round-trip/fuzz/golden
(`weaponstate.golden`), `room_test.go` (рассылка при входе и смене). Бенч: `Tick/50ent` ≈ 12.3 мкс,
`Tick/200ent` ≈ 50.2 мкс, `CombatTick` ≈ 15.0 мкс — все 0 allocs/op, без регресса. Приёмка зелёная
(`make check`).

Сделана **итерация 27** — рывок (dash) (`internal/game/systems.go`, `world.go`, `combat.go`,
`internal/protocol`, клиент). Короткое ускорение в сторону движения по действию, с кулдауном —
детерминированная симуляция, но, в отличие от пикапов, **предсказывается клиентом** (общий `Step`
зеркалится). **Почему input-driven:** рывок запускается вводом, поэтому клиент предсказывает его из
СВОЕГО ввода тем же `Step`, а сервер применяет из того же ввода — сходятся без пересылки серверного
буфа (устойчивый буст скорости, у которого предсказание дрейфило бы, отложен). **Состояние:** таймеры
`dashCD`/`dashT` (секунды) живут в `MoveState` (их трогает общий `Step` и зеркалит предсказание),
спадают на `dt`, **в `Checksum`**, сбрасываются при респауне. **Провод:** `Buttons` занят целиком
(WASD+огонь+оружие), поэтому действия едут отдельным ОПЦИОНАЛЬНЫМ байтом `Input.Actions` (бит
`ActDash`); старый ввод без байта декодируется с `actions=0` (обратная совместимость — боты/e2e не
ломаются). Сервер гейтит рывок своим кулдауном (анти-чит). **Реконсиляция без двойного счёта:** таймеры
рывка на клиенте — локальны, реплей неподтверждённых вводов их НЕ пересчитывает, а переигрывает движение
по сохранённому в очереди флагу `dashActive`. **Покрытие:** `dash_test.go` (ускорение/спад, только при
движении, кулдаун, сброс на респауне, `TestDashInChecksum`, `TestDashDeterminism` — 2 мира ×300 тиков с
рывками), protocol round-trip (Actions)/fuzz-сид/golden (`input.golden` пересобран). Клиент: клавиша
Space, HUD-индикатор перезарядки. Бенч: `Tick/50ent` ≈ 12.8 мкс, `Tick/200ent` ≈ 52 мкс — 0 allocs/op,
рывок +~3 %. Приёмка зелёная (`make check` + `make integration` + fuzz).

Сделана **итерация 28** — умный ИИ ботов (`internal/bot/ai.go`, `internal/game/walls.go`,
`internal/botfill`). Наполнитель (итер. 17) раньше гонял ботов случайным блужданием — теперь у них
ИИ: видят мир из снапшотов, идут к ближайшему врагу по A* вокруг стен и целятся. **Границы:** бот
остаётся обычным клиентом (симуляцию/провод НЕ трогает, только снапшоты+ввод), поэтому весь ИИ в
`internal/bot`, а `internal/bot` по-прежнему НЕ импортирует `game` — геометрию стен получает
параметром через новый `game.Obstacles()` (read-only снимок AABB; `botfill` конвертит в `bot.Rect`).
**Навигация:** `bot.Nav` — сетка 32×32 (клетка 128), клетка заблокирована при пересечении раздутой
стены; `Nav.Path` — A* (8 связностей, без срезки углов, октальная эвристика, двоичная куча). Сетка
строится один раз и делится (read-only) между ботами; путь пересчитывается редко (~2/с на бота), между
— рулёжка к текущей путевой точке (A* не на каждом кадре). **Конкурентность (профиль стража):** как и
прежде две клиентские горутины на бота — `Brain.Observe` читает снапшоты и публикует снимок мира
(`atomic.Pointer[View]`), `Brain.Drive` на 60 Гц читает снимок и шлёт ввод; владелец и путь завершения
те же (`botCtx`/закрытие соединения). Простой `Autopilot` оставлен для `cmd/loadtest` (массовость, не
бой). **Покрытие:** `ai_test.go` (A* огибает стену — путь не в заблокированных клетках и отклоняется от
прямой; путь на пустой карте; `dirToButtons` по октантам; `think` преследует и стреляет по врагу в
дальности, не стреляет вне, стоит мёртвым), `botfill` (`-race`) и e2e `TestE2EBotFill…` — умный бот в
реальной комнате. Стражи: **concurrency-auditor** — профильная (новые горутины/`atomic.Pointer`);
determinism/protocol не запускались (симуляция/провод не тронуты — прецедент клиентских итераций).
Провода/`Checksum`-изменений нет, тик не тронут. Приёмка зелёная (`make check` + `make integration`).

**Фикс после итер. 27 — рывок в логе реплея** (`internal/game/replay.go`): лог реплея писал `Input`, но
НЕ поле `Actions` (рывок, итер. 27), поэтому реплей сессии с рывками рассинхронивался — латентный баг
детерминизма (живой детерминизм-тест его не ловил, т.к. кормит оба мира одним `Actions` напрямую; ломался
только путь запись→декод→реплей). Лог поднят до **v3**: input-событие несёт байт `Actions` в конце;
декодер принимает v1/v2 (без него, `Actions=0`) и v3. Регресс: `TestReplayDashRoundTrip` (рывок переживает
кодек+реплей; негативный контроль — затирание `Actions` расходит хэш). `FuzzReplayDecode` чист. Приёмка
зелёная (`make check` + fuzz).

Сделана **итерация 29** — King of the Hill (`internal/game/hill.go`, `world.go`, `match.go`, `replay.go`,
`room.go`, `internal/protocol`, `cmd/server`, клиент). Режим захвата зоны: в центре арены фиксированный
круглый холм; пока его контролирует ровно одна сторона (в командном режиме — команда, иначе — отдельный
игрок), её игроки в зоне копят очки — детерминированная симуляция (в `Checksum`, реплей-безопасна), без
розыгрышей rng. **Симуляция.** `Player.HillScore` (в `Checksum`) копится в `stepHill` — новая фаза 5 общего
`Step` (после `stepPickups`, до `Tick++`): собирает живых игроков в круге, считает различные стороны
(`team` в teamMode, иначе id игрока); ровно одна сторона внутри → контроль (каждому её игроку внутри +1),
0 или ≥2 → оспаривание, очки не растут. `startMatch` обнуляет `HillScore`. `endMatch` в hillMode кладёт в
`w.winner` победителя по очкам холма (`hillLeader` — FFA-игрок с max `HillScore`, tiebreak по min id;
`hillWinningTeam` — команда с большей суммой), совмещается с teamMode. Геометрия холма (`hillX`/`hillY`/
`hillRadius`) статична — как `walls`/`pickupSpots`, в `Checksum` НЕ входит (в хэш идёт лишь `HillScore`).
Флаг `hillMode` — фиксированный параметр мира (как `teamMode`, `SetHillMode` до первого join): в `Checksum`
не входит, но пишется в **лог реплея v4** (декодер принимает v1–v3). **Провод.** `MsgMatchState` получил бит
`matchFlagHillMode` в байте флагов и поле `[2B HillScore]` в каждой строке табло (фикс. часть строки 8→10
байт); снапшот/дельта и пер-тик счётчики сущностей НЕ тронуты (холм едет только этим событийным сообщением).
Кодек zero-alloc, bounds-checked; round-trip/fuzz/сиды обновлены. **Комната.** `Config.HillMode`; `NewRoom`
зовёт `SetHillMode(true)` до старта горутины цикла (happens-before); табло сортируется/победитель считается
по `HillScore` в hillMode. `cmd/server` — env `ARENA_HILL_MODE`. **Клиент** (`web/game.js`): зеркало
`SIM.Hill*`, декод нового формата `MsgMatchState`, `drawHill` рисует зону с локально вычисленным цветом
контролёра (тот же расчёт, что на сервере — без нового поля в проводе), табло/баннер/миникарта по очкам
холма. **Покрытие:** `hill_test.go` (начисление без соперника, оспаривание, командный контроль, победитель
по очкам, покрытие `Checksum`, сброс на старте матча, `TestHillDeterminism` ×300 тиков,
`TestReplayHillModeRoundTrip` — лог v4 переносит `hillMode` + негативный контроль), обновлены round-trip/
fuzz-сиды `MsgMatchState`. **Три стража чисты:** determinism (`HillScore` в `Checksum`, `hillMode` — параметр
мира в логе v4, `stepHill`/`hillLeader`/`hillWinningTeam` по `w.order` без rng/времени/map), protocol
(флаг + поле в `MsgMatchState`, снапшот не тронут, fuzz без крэшеров, zero-alloc не просел), concurrency
(новых горутин/каналов нет, `SetHillMode` в `NewRoom` до старта цикла). Бенч: `Tick/50ent` ≈ 12.5 мкс,
`Tick/200ent` ≈ 50.6 мкс — 0 allocs/op, без регресса (вне hillMode `stepHill` — ранний выход). Приёмка
зелёная (`make check` + `make integration` + fuzz). Из режимов на будущее остаются CTF и доминация
(несколько точек) — по одному на PR.

Сделана **итерация 30** — доминация (`internal/game/domination.go`, `world.go`, `match.go`, `replay.go`,
`room.go`, `internal/protocol`, `cmd/server`, клиент). Режим захвата НЕСКОЛЬКИХ контрольных точек —
прямое обобщение King of the Hill (итер. 29) с одной зоны на N: детерминированная симуляция (в `Checksum`,
реплей-безопасна), без розыгрышей rng. **Симуляция.** `Player.DomScore` (в `Checksum`) копится в
`stepDomination` — новая фаза 6 общего `Step` (после `stepHill`, до `Tick++`, no-op вне domMode): для КАЖДОЙ
точки `domPoints` (3 зоны треугольником, обход по индексу) действует та же логика контроля, что у холма
(`sides`/`ffaCount` — ровно одна сторона внутри → каждому её игроку в зоне +1), очки суммируются по всем
удерживаемым зонам. `startMatch` обнуляет `DomScore`. `endMatch` в domMode кладёт в `w.winner` победителя по
очкам зон (`domLeader` — FFA-игрок с max `DomScore`, tiebreak по min id; `domWinningTeam` — команда с большей
суммой), совмещается с teamMode. Геометрия точек (`domPoints`/`domRadius`) статична — как `walls`/`hill`, в
`Checksum` НЕ входит (в хэш идёт лишь `DomScore`). Флаг `domMode` — фиксированный параметр мира (как `hillMode`,
`SetDomMode` до первого join): в `Checksum` не входит, но пишется в **лог реплея v5** (декодер принимает v1–v4).
**Провод.** НОВЫХ сообщений нет: `MsgMatchState` получил бит `matchFlagDomMode` (bit2), а очки зон едут в том же
слоте `objScore` (историческое поле `HillScore`), что и очки холма — режимы взаимоисключающи, БАЙТОВАЯ РАСКЛАДКА
НЕ МЕНЯЛАСЬ. Снапшот/дельта и пер-тик счётчики сущностей НЕ тронуты. Кодек zero-alloc, bounds-checked;
round-trip/fuzz-сиды обновлены. **Комната.** `Config.DomMode`; `NewRoom` зовёт `SetDomMode(true)` до старта
горутины цикла (happens-before); табло сортируется/победитель — по очкам зон в domMode. `cmd/server` — env
`ARENA_DOM_MODE`. **Клиент** (`web/game.js`): зеркало `SIM.DomPoints`/`SIM.DomRadius`, декод флага domMode
(смещения не менялись), `drawDomination` рисует все зоны с локально вычисленным цветом контролёра (общие хелперы
`zoneControllerColor`/`drawZone`, на них отрефакторен и `drawHill`), табло/баннер/миникарта по `objMode =
hill||dom`. **Покрытие:** `domination_test.go` (начисление без соперника, оспаривание, по-зонность/сумма,
командный контроль + оспаривание отдельной зоны, победитель по очкам, покрытие `Checksum`, сброс на старте
матча, tiebreak, `MatchState` несёт очки зон в слоте объектива, `TestDominationDeterminism` ×300 тиков,
`TestDominationDeterminismAcrossFullCycle` — полный цикл матча под равенством `Checksum` каждый тик,
`TestReplayDomModeRoundTrip` — лог v5 + негативный контроль), round-trip/fuzz `MsgMatchState`, поправлено
смещение eventCount в `replay_test.go`. **Три стража чисты:** determinism (`DomScore` в `Checksum`, `domMode` —
параметр мира в логе v5, `stepDomination`/`domLeader`/`domWinningTeam` по `w.order` без rng/времени/map; совет —
полноцикловый тест — учтён), protocol (флаг bit2, слот `objScore` переиспользован, снапшот не тронут, fuzz без
крэшеров, zero-alloc не просел, байт-в-байт клиент↔сервер перепроверен — критичное ребро итер. 23 чисто),
concurrency (новых горутин/каналов нет, `SetDomMode` в `NewRoom` до старта цикла, `-race` ×3). Бенч: `Tick/50ent`
≈ 13.0 мкс, `Tick/200ent` ≈ 53.9 мкс, `CombatTick` ≈ 15.1 мкс — 0 allocs/op, без регресса (вне domMode
`stepDomination` — ранний выход). Приёмка зелёная (`make check` + `make integration` + fuzz). Из режимов на
будущее остаётся CTF — по одному на PR.

Сделана **итерация 31** — Capture the Flag (`internal/game/ctf.go`, `world.go`, `match.go`, `replay.go`,
`combat.go`, `room.go`, `internal/protocol`, `cmd/server`, клиент). Последний режим дорожной карты: две команды,
у каждой база с флагом; подбор вражеского флага касанием, перенос и захват на своей базе — детерминированная
симуляция (в `Checksum`, реплей-безопасна), без розыгрышей rng. **CTF подразумевает командный режим** — `NewRoom`
включает и teamMode, и ctfMode. **Симуляция.** `Player.Captures` (в `Checksum`) считается в `stepCTF` — новая
фаза 7 общего `Step` (после `stepDomination`, до `Tick++`, no-op вне ctfMode). Флаги — `World.flags [2]flagState`
(`status` atBase/carried/dropped, `carrier`, `x`/`y`, `dropAt`), базы `flagBases` (2 точки по краям, команда0
слева, команда1 справа). Три прохода по `w.order` (детерминированно, по минимальному id): (0) снятие флага с
мёртвого/ушедшего носителя (дроп на месте) + авто-возврат брошенного после `flagReturnTicks` (20 c); (1) подбор
ВРАЖЕСКОГО флага касанием / возврат СВОЕГО брошенного касанием; (2) захват на своей базе — только если СВОЙ флаг
дома (канон CTF), `Captures++` и `Event{Kind: EventCapture, Target: id}`. `respawn`/дисконнект возвращают
несомый флаг. `startMatch` обнуляет `Captures` + `resetFlags()`. `endMatch` в ctfMode кладёт в `w.winner` id
команды-победителя (`ctfWinningTeam` — больше захватов, tiebreak → 0). Всё новое СОСТОЯНИЕ (`Player.Captures` +
все поля обоих флагов) — **в `Checksum`**; геометрия баз статична (как `walls`/`domPoints`) — в `Checksum` НЕ
входит. Флаг `ctfMode` — фиксированный параметр мира (как `domMode`, `SetCtfMode` до первого join): в `Checksum`
не входит, но пишется в **лог реплея v6** (декодер принимает v1–v5). **Провод.** Два новых **reliable**-
сообщения: `MsgFlagState` (0x19, `[1B count]` count×`[1B team][1B status][2B carrier][2B x][2B y]`) — полный
набор флагов, событийно при подборе/захвате/возврате и новичку при входе (как `MsgPickupState`); `MsgCapture`
(0x1a, `[2B player][1B team]`) — через event-пайплайн (`dispatchEvents` → `reliableAll`, как `Death`/
`Killstreak`). `MsgMatchState` получил бит `matchFlagCtfMode` (bit3), захваты едут в том же слоте `objScore`, что
очки холма/зон (режимы взаимоисключающи — байтовая раскладка не менялась). Снапшот/дельта и пер-тик счётчики
сущностей НЕ тронуты. Кодек zero-alloc, bounds-checked; round-trip/fuzz/golden (`flagstate.golden`,
`capture.golden`). **Комната.** `Config.CtfMode`; `NewRoom` зовёт `SetTeamMode(true)`+`SetCtfMode(true)` до
старта горутины цикла (happens-before); `broadcastFlags` событийно (при `FlagsDirty`) + новичку;
`dispatchEvents` `case EventCapture` берёт команду через `Player(ev.Target).team`. `cmd/server` — env
`ARENA_CTF_MODE`. **Клиент** (`web/game.js`): зеркало `SIM.FlagBases`/`SIM.FlagBaseRadius`, декод обоих
сообщений (x/y ÷ COORD_SCALE), `drawBases`/`drawFlags` (флаг у носителя следует за ним, брошенный — на земле),
баннер + звук `sfx.capture` на `MsgCapture`, табло/баннер/миникарта по захватам (`objMode = hill||dom||ctf`).
**Покрытие:** `ctf_test.go` (подбор вражеского, свой не подбирается, следование за носителем, захват, захват
заблокирован при унесённом своём, дроп на смерти, авто-возврат, возврат своего касанием, дисконнект,
победитель по захватам, `TestCaptureInChecksum`, сброс на старте, `TestReplayCtfModeRoundTrip` — лог v6 +
негативный контроль, `TestCTFDeterminismAcrossFullCycle` — полный цикл под равенством `Checksum` каждый тик),
`room_test.go` (`TestRoomBroadcastsFlagStateOnJoin`), protocol round-trip/fuzz/golden. Бенч: `Tick/50ent`
≈ 12.4 мкс, `Tick/200ent` ≈ 50.3 мкс, `CombatTick` ≈ 15.1 мкс — 0 allocs/op, без регресса (вне ctfMode
`stepCTF` — ранний выход). **Три стража чисты:** determinism (`Player.Captures` + все поля обоих флагов в
`Checksum`, `ctfMode` — параметр мира в логе v6 с верными смещениями, `stepCTF`/`ctfWinningTeam` по `w.order`/
индексам без `w.rng`/времени/map, регресс-тесты полного цикла уже есть), protocol (клиент↔сервер байт-в-байт
сверен, включая критичное ребро `MsgMatchState` из итер. 23; fuzz ~22M execs без крэшеров; zero-alloc горячего
пути снапшота/дельты не тронут; golden осознанны), concurrency (новых горутин/каналов нет, всё CTF-состояние на
горутине комнаты, `SetCtfMode`/`SetTeamMode` в `NewRoom` до старта цикла — happens-before, `EventCapture` по
колее `EventDeath`, алиасинг буферов безопасен, `-race` ×3 + integration). Приёмка зелёная (`make check` +
`make integration` + fuzz + `make replay`). **Все режимы дорожной карты пройдены** (FFA, командный, KotH,
доминация, CTF).

Сделана **итерация 32** — локальный стек наблюдаемости (`docker-compose.yml`, `deploy/`, `Makefile`, README;
**Go/симуляция/провод НЕ тронуты** — чисто ops/инфра). Поверх УЖЕ существующих `/metrics` (Prometheus,
итер. 6/17/25), `/healthz` и Postgres-хранилища (итер. 13) — одна команда поднимает сервер + PostgreSQL +
Prometheus + Grafana с преднастроенным дашбордом и алертами, чтобы метрики были видны без ручной настройки
мониторинга. **Состав.** `docker-compose.yml` (корень — сервер собирается из корневого `Dockerfile`): `postgres`
(17-alpine, healthcheck `pg_isready`, том данных), `server` (build из `Dockerfile`, ждёт postgres healthy,
`ARENA_DB_DRIVER=postgres` + DSN на сервис, порт 8080), `prometheus` (v3.1.0, скрейп `server:8080/metrics` каждые
5 с + правила алертов), `grafana` (11.4.0, provisioning datasource+дашборд). Секреты — в `.env` (шаблон
`.env.example`, `.env` в `.gitignore`); `ARENA_AUTH_SECRET` пуст по умолчанию → сервер генерит эфемерный и
предупреждает (без захардкоженного секрета — и чтобы не ловить gitleaks). У distroless-образа нет shell, поэтому
HEALTHCHECK контейнера сервера намеренно не задаётся — живость видит Prometheus по `up{job="arena"}` (алерт
`ArenaServerDown`). **Конфиги** (`deploy/`): `prometheus/prometheus.yml` (+ `alerts.yml` — 4 правила:
`ArenaServerDown` critical, `ArenaTickP99High` >15 мс warning, `ArenaInboxBacklog` >64 warning, `ArenaAntiCheatSpike`
info; Alertmanager намеренно не поднят — маршрутизация деплой-специфична, правила всё равно видны в UI/Grafana),
`grafana/provisioning/` (datasource Prometheus uid `arena-prometheus` + провайдер дашбордов), `grafana/dashboards/
arena.json` (8 панелей: server up, игроки, боты, inbox, тик p50/p99 с порогом 15 мс, трафик снапшотов, сущности в
снапшоте, античит по типу). **Имена метрик в алертах и дашборде — зеркало `internal/metrics/metrics.go`**
(задокументировано; правило «менять обе стороны» как у клиентских констант). `Makefile`: `compose-up`/`compose-down`
(`V=1` — с томами)/`compose-logs`. README RU/EN — секция «Стек наблюдаемости», цели make, ссылка на `deploy/README.md`
(ops-гайд: порты, дашборд, таблица алертов). **Стражи (concurrency/determinism/protocol) НЕ запускались** — Go не
тронут, изменений в игровой конкурентности/детерминизме/проводе нет (прецедент — итер. 13/15/16/18/24). **Проверка:**
Docker в dev-среде недоступен (Docker Desktop WSL-интеграция выключена) → стек вживую НЕ поднимался; проведена
статическая валидация — все 5 YAML + `arena.json` парсятся (PyYAML/json), структура compose цела (тома объявлены,
bind-mounts существуют, `depends_on` резолвятся), все `arena_*` из алертов/дашборда резолвятся в метрики из
`metrics.go`; `make check` (Go) зелёный без регресса. `BENCHMARKS.md` не трогался — горячего пути нет. **Отложено на
живую машину с Docker:** `docker compose up --build` end-to-end (сборка образа, миграции Postgres на старте, скрейп
Prometheus, автопровижининг Grafana) — прогнать при доступном Docker перед прод-использованием.

## Статус: дорожная карта режимов пройдена; идёт инфра-программа (итер. 32 — стек наблюдаемости)

Итерации 1–11 (исходное ТЗ) + 12 (WebRTC до продакшена) + 13 (фундамент бэкенда) + 14 (жизненный цикл
матча) + 14B (persister) + 15 (клиент/UX: логин, лидерборд, миникарта) + 16 (профиль игрока с историей матчей
на клиенте) + 17 (серверные боты — наполнитель комнат ИИ) + 18 (звук боя — Web Audio) + 19 (оружие/пикапы —
аптечки, ускорение, веер) + 20 (киллстрики + окно неуязвимости) + 21 (рейт-лимит на auth) + 22 (спектатор/
наблюдатель) + 23 (командный режим) + 24 (мобильное управление) + 25 (античит-метрики) + 26 (система оружия) +
27 (рывок) + 28 (умный ИИ ботов) + 29 (King of the Hill) + 30 (доминация) + 31 (Capture the Flag) сделаны.
**Все игровые режимы дорожной карты пройдены** (FFA deathmatch, командный, King of the Hill, доминация,
Capture the Flag). Далее — программа по новым запросам (инфра/прод, бэкенд/аккаунты, качество/DevEx): начата
итерацией **32** (локальный стек наблюдаемости — docker-compose + Prometheus + Grafana). Тот же воркфлоу на
feature-ветке: ветка → исследовать → план → код → /audit → `make check` → коммит → push → PR → merge зелёным,
отчёт и BENCHMARKS/docs по правилу 7.

**После итер. 25 — обслуживание BENCHMARKS.md** (без изменений кода): секции итер. 19/20/25 несли
скопированные приблизительные микробенч-числа (`~48 мкс` тик, `~14.4 мкс` бой, `~1.1 нс` декод);
заменены свежими замерами на текущей dev-машине (Intel Core Ultra 9 275HX, go1.26, `-count=3`):
`Tick/50ent` ≈ 12.3 мкс, `Tick/200ent` ≈ 51.3 мкс, `CombatTick` ≈ 15.6 мкс — все 0 allocs/op. Исторические
A/B-замеры (итер. 5/8/10) и модельные/нагрузочные `~`-величины (трафик, p99 под нагрузкой) не тронуты.

## Общий стиль работы

- Отвечать по-русски (общение), код и комментарии — по-английски.
- Не расширять объём без запроса; держаться плана итераций.
- Перед правкой формата протокола или движения — помнить, что клиент (`web/game.js`)
  зеркалит константы: менять обе стороны синхронно.
- Документация: `docs/` (архитектура, протокол, тестирование) поддерживается вместе
  с кодом. README — двуязычный: `README.md` (RU, основной) + `README.en.md` (EN).
