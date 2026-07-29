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

## Статус: дорожная карта пройдена; профиль игрока (итер. 16)

Итерации 1–11 (исходное ТЗ) + 12 (WebRTC до продакшена) + 13 (фундамент бэкенда) + 14 (жизненный цикл
матча) + 14B (persister) + 15 (клиент/UX: логин, лидерборд, миникарта) + 16 (профиль игрока с историей матчей
на клиенте) сделаны. Дальнейшие работы — по новым запросам; тот же воркфлоу на feature-ветке: ветка →
исследовать → план → код → /audit → `make check` → коммит → push → PR → merge зелёным, отчёт и BENCHMARKS/docs
по правилу 7.

## Общий стиль работы

- Отвечать по-русски (общение), код и комментарии — по-английски.
- Не расширять объём без запроса; держаться плана итераций.
- Перед правкой формата протокола или движения — помнить, что клиент (`web/game.js`)
  зеркалит константы: менять обе стороны синхронно.
- Документация: `docs/` (архитектура, протокол, тестирование) поддерживается вместе
  с кодом. README — двуязычный: `README.md` (RU, основной) + `README.en.md` (EN).
