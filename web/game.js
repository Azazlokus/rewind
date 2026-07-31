"use strict";

// Клиент Arena — итерация 5 (бой: снаряды, урон, смерть/респаун).
//
// Чужие игроки: клиент держит буфер снапшотов и рендерит их на INTERP.delay
// (100 мс) в прошлом, интерполируя между двумя соседними снапшотами. Это скрывает
// сниженную частоту снапшотов сервера (20 Гц) и сетевой джиттер.
//
// Свой игрок: рендерится из ПРЕДСКАЗАНИЯ, а не из прошлого. Клиент применяет свой
// ввод немедленно тем же шагом движения, что и сервер (stepMove — зеркало
// game.Step), и держит очередь неподтверждённых вводов. На каждом снапшоте берёт
// авторитетную позицию, отбрасывает вводы с seq <= lastProcessedSeq и переигрывает
// остаток поверх неё (реконсиляция). Остаточную коррекцию сглаживает, чтобы не
// было видимого «отката». Всё, что обязано совпадать с сервером, собрано в
// PROTO / SIM / PREDICT.
//
// Бой (итерация 5): выстрел — это бит BtnFire во вводе; снаряды сервер-авторитетны
// и приходят в снапшотах как сущности KindProjectile (интерполируются, не
// предсказываются). Reliable-события Hit/Death/Spawn правят HUD и — для своей
// смерти/респауна — останавливают и перезапускают предсказание.

// ---- протокол (зеркало internal/protocol) ----------------------------------
const PROTO = {
  MsgInput: 0x01,
  MsgJoin: 0x02,
  MsgSnapshot: 0x10,
  MsgJoinAck: 0x11,
  MsgSpawn: 0x12,
  MsgDeath: 0x13,
  MsgHit: 0x14,
  MsgMatchState: 0x15, // состояние матча: фаза, таймер, табло (итерация 14)
  MsgPickupState: 0x16, // состояние пикапов: какие точки заняты и чем (итерация 19)
  MsgKillstreak: 0x17, // веха серии убийств: игрок + длина (итерация 20)
  // фазы матча (зеркало game.matchPhase)
  MatchActive: 0,
  MatchIntermission: 1,
  // типы пикапов (зеркало game.pickupKind)
  PickupMedkit: 1,
  PickupRapid: 2,
  PickupSpread: 3,
  // биты кнопок: 0..3 = WASD, 4 = fire
  BtnUp: 1 << 0,
  BtnLeft: 1 << 1,
  BtnDown: 1 << 2,
  BtnRight: 1 << 3,
  BtnFire: 1 << 4,
  // биты маски изменённых полей сущности в дельта-снапшоте (итерация 9): порядок
  // полей на проводе kind/x/y/vx/vy/hp. Зеркало protocol.FieldKind…FieldHP.
  FieldKind: 1 << 0,
  FieldX: 1 << 1,
  FieldY: 1 << 2,
  FieldVX: 1 << 3,
  FieldVY: 1 << 4,
  FieldHP: 1 << 5,
  FieldAll: 0x3f, // все шесть определённых битов
};

// Флаги в MsgMatchState (зеркало protocol.matchFlagTeamMode): bit0 — командный
// режим (итер. 23), тогда winner — id команды (0/1), а team в табло — команда.
const MATCH_TEAM_MODE = 1 << 0;

// Цвета команд (командный режим, итер. 23): свой игрок и союзники — синяя команда
// визуально не отличаются друг от друга флагом, различаем по team в табло.
const TEAM_COLORS = ["#4d8bff", "#ff5d5d"]; // 0 — синие, 1 — красные

// ---- константы симуляции (зеркало internal/game) ---------------------------
const SIM = {
  MapSize: 4096,
  PlayerRadius: 16,
  PlayerSpeed: 300,
  ProjectileRadius: 4,
};

// Шаг квантования координат/скоростей на проводе (зеркало protocol.CoordScale).
const COORD_SCALE = 16;

// invSqrt2 нормализует диагональ (зеркало game.invSqrt2).
const INV_SQRT2 = 0.70710678;

// Общий декодер имён игроков (табло матча). UTF-8, как на сервере.
const TEXT_DECODER = new TextDecoder();

// WALLS — статичные препятствия (итерация 10), зеркало game.walls. Коробки
// [minX,minY,maxX,maxY] в мировых координатах. РАСКЛАДКА ОБЯЗАНА СОВПАДАТЬ с
// сервером: предсказание своего игрока повторяет коллизию тем же resolveWalls, и
// расхождение проявится дрейфом, а не ошибкой. Порядок стен тоже совпадает —
// разрешение коллизий зависит от него.
const WALLS = [
  { minX: 1500, minY: 1500, maxX: 1620, maxY: 1900 },
  { minX: 2200, minY: 1400, maxX: 2320, maxY: 2000 },
  { minX: 1600, minY: 2300, maxX: 2400, maxY: 2420 },
  { minX: 2600, minY: 1900, maxX: 3100, maxY: 2020 },
];

// PICKUP_SPOTS — фиксированные точки появления пикапов (итерация 19), зеркало
// game.pickupSpots. ПОРЯДОК ОБЯЗАН СОВПАДАТЬ с сервером: сервер шлёт индекс точки
// (spot) в MsgPickupState, а клиент берёт координаты отсюда. Пикапы не входят в
// симуляцию клиента (подбор авторитетен на сервере) — это чистый рендер, поэтому
// зеркалятся только координаты, без логики.
const PICKUP_SPOTS = [
  { x: 700, y: 700 },
  { x: 3396, y: 700 },
  { x: 700, y: 3396 },
  { x: 3396, y: 3396 },
  { x: 2048, y: 3300 },
];

// Цвет пикапа по типу (зеркало game.pickupKind): аптечка/ускорение/веер.
const PICKUP_COLORS = { 1: "#4ade80", 2: "#ffd166", 3: "#5bd6ff" };
// Односимвольная метка пикапа по типу — рисуется в центре иконки.
const PICKUP_GLYPHS = { 1: "+", 2: "»", 3: "≡" };

// Длительности щита неуязвимости (итерация 20), мс — приближённое зеркало
// game.spawnInvulnTicks (60) и killstreakInvulnTicks (45) при 30 Гц. КОСМЕТИКА:
// сервер авторитетен по неуязвимости; клиент рисует кольцо-щит от события Spawn/
// Killstreak на эту длительность. Ранний сброс щита (игрок выстрелил) клиенту не
// виден, поэтому кольцо может подзадержаться — безвредно, это лишь индикатор.
const SPAWN_SHIELD_MS = 2000;
const KILLSTREAK_SHIELD_MS = 1500;
// Сколько держится баннер-объявление серии убийств, мс.
const STREAK_BANNER_MS = 2600;

// resolveWalls выталкивает круг (cx,cy,r) из всех стен по очереди и возвращает
// [x, y] — зеркало game.resolveWalls/resolveWall. Один проход за шаг, как на
// сервере. Мелкая разница float32/float64 гасится реконсиляцией, как и в обычном
// движении.
function resolveWalls(cx, cy, r) {
  for (const wl of WALLS) {
    const qx = clampSim(cx, wl.minX, wl.maxX);
    const qy = clampSim(cy, wl.minY, wl.maxY);
    const dx = cx - qx;
    const dy = cy - qy;
    const d2 = dx * dx + dy * dy;
    if (d2 >= r * r) continue; // круг не касается стены
    if (d2 > 0) {
      // Центр снаружи коробки: толкаем по нормали от ближайшей точки.
      const d = Math.sqrt(d2);
      const push = r - d;
      cx += (dx / d) * push;
      cy += (dy / d) * push;
    } else {
      // Центр внутри коробки: выходим через ближайшую грань (тот же tie-break, что
      // на сервере: left, right, top, bottom).
      const left = cx - wl.minX;
      const right = wl.maxX - cx;
      const top = cy - wl.minY;
      const bottom = wl.maxY - cy;
      const m = Math.min(left, right, top, bottom);
      if (m === left) cx = wl.minX - r;
      else if (m === right) cx = wl.maxX + r;
      else if (m === top) cy = wl.minY - r;
      else cy = wl.maxY + r;
    }
  }
  return [cx, cy];
}

// ---- параметры предсказания ------------------------------------------------
const PREDICT = {
  // Фиксированный шаг предсказания = период ввода (1/InputRate, 60 Гц). Сервер
  // применяет вводы из очереди тем же шагом (game.inputDt) — держать равными.
  dt: 1 / 60,
  // Во сколько раз гасится ошибка коррекции за секунду: rendered = pred + err,
  // err *= smoothDecay^dt каждый кадр. ~0.01/с даёт полужизнь ошибки ~150 мс.
  smoothDecay: 0.01,
  // Ошибка больше этого (юниты) — не сглаживаем, а прыгаем: спавн/респаун/телепорт
  // не должны «тянуться резиной» через всю карту.
  snap: 200,
};

// stepMove — зеркало game.Step: единственный шаг движения игрока. Константы,
// порядок операций и клэмп обязаны совпадать с сервером, иначе предсказание
// дрейфит. Мутирует s = { x, y, vx, vy }.
function stepMove(s, buttons, dt) {
  let dx = 0, dy = 0;
  if (buttons & PROTO.BtnLeft) dx -= 1;
  if (buttons & PROTO.BtnRight) dx += 1;
  if (buttons & PROTO.BtnUp) dy -= 1;
  if (buttons & PROTO.BtnDown) dy += 1;
  if (dx !== 0 && dy !== 0) { dx *= INV_SQRT2; dy *= INV_SQRT2; }
  s.vx = dx * SIM.PlayerSpeed;
  s.vy = dy * SIM.PlayerSpeed;
  const nx = clampSim(s.x + s.vx * dt, SIM.PlayerRadius, SIM.MapSize - SIM.PlayerRadius);
  const ny = clampSim(s.y + s.vy * dt, SIM.PlayerRadius, SIM.MapSize - SIM.PlayerRadius);
  // Коллизия со стенами тем же кодом, что и на сервере (итерация 10).
  [s.x, s.y] = resolveWalls(nx, ny, SIM.PlayerRadius);
}

function clampSim(v, lo, hi) {
  return v < lo ? lo : v > hi ? hi : v;
}

// ---- параметры интерполяции ------------------------------------------------
const INTERP = {
  // Насколько позади новейшего снапшота рендерим, в секундах. 100 мс = два
  // интервала снапшотов при 20 Гц — запас, чтобы следующий кадр успел прийти.
  delay: 0.1,
  // Тикрейт сервера: переводит tick снапшота в момент на серверной шкале
  // времени. Стабильнее, чем время прихода пакета (не зависит от джиттера).
  tickRate: 30,
  // Если playback-часы уходят дальше этого за пределы доступных данных,
  // пересинхронизируемся (после паузы вкладки или всплеска задержки).
  resync: 0.25,
  // Сколько секунд истории держим в буфере позади playback.
  history: 1.0,
};

// ---- DOM -------------------------------------------------------------------
const canvas = document.getElementById("view");
const ctx = canvas.getContext("2d");
const els = {
  name: document.getElementById("name"),
  connect: document.getElementById("connect"),
  spectate: document.getElementById("spectate"),
  sound: document.getElementById("sound"),
  status: document.getElementById("status"),
  tick: document.getElementById("tick"),
  players: document.getElementById("players"),
  me: document.getElementById("me"),
  // Аккаунт + лидерборд (итерация 15).
  authOut: document.getElementById("authOut"),
  authIn: document.getElementById("authIn"),
  authUser: document.getElementById("authUser"),
  authPass: document.getElementById("authPass"),
  authLogin: document.getElementById("authLogin"),
  authRegister: document.getElementById("authRegister"),
  authLogout: document.getElementById("authLogout"),
  authName: document.getElementById("authName"),
  authStats: document.getElementById("authStats"),
  authMsg: document.getElementById("authMsg"),
  authProfile: document.getElementById("authProfile"),
  lbBody: document.getElementById("lbBody"),
  lbRefresh: document.getElementById("lbRefresh"),
  // Профиль игрока (итерация 16).
  profile: document.getElementById("profile"),
  profName: document.getElementById("profName"),
  profClose: document.getElementById("profClose"),
  profStats: document.getElementById("profStats"),
  profMatches: document.getElementById("profMatches"),
};

// ---- состояние клиента -----------------------------------------------------
const state = {
  ws: null, // игровой WebSocket (путь /ws) или сигналинг-сокет (до передачи в link)
  pc: null, // RTCPeerConnection (путь WebRTC) или null
  link: null, // активный игровой транспорт: { send, close, isOpen } — WS или DataChannel
  connecting: false, // идёт установка соединения (защита от повторного connect)
  connected: false,
  myID: 0,
  seq: 0,
  keys: { w: false, a: false, s: false, d: false, fire: false },
  aim: 0, // радианы
  mouse: { x: canvas.width / 2, y: canvas.height / 2 },
  inputTimer: 0,

  // Буфер интерполяции: снапшоты по возрастанию serverTime.
  buffer: [], // { serverTime, tick, ents: Map<id, entity> }
  playback: null, // текущий момент рендера на серверной шкале, сек
  lastFrame: 0, // performance.now() предыдущего кадра, мс (playback-часы)
  lastRenderMs: 0, // performance.now() предыдущего кадра, мс (затухание ошибки)

  // Предсказание своего игрока.
  pred: { x: 0, y: 0, vx: 0, vy: 0 }, // предсказанное состояние
  pending: [], // неподтверждённые вводы: { seq, buttons, dt }
  smoothErr: { x: 0, y: 0 }, // остаточная ошибка коррекции (гасится к нулю)
  predReady: false, // есть ли авторитетная база (пришёл снапшот с нашим id)
  selfHp: 100, // последний известный HP своего игрока (для рендера)
  dead: false, // мертвы ли мы сейчас (между Death и Spawn)
  flashMs: 0, // performance.now() последнего попадания по нам (краткая вспышка)

  // Дельта-реконструкция (итерация 6B): недавние полные наборы как базы для дельт
  // и тик, подтверждаемый серверу (по нему сервер кодирует следующую дельту).
  snapStore: new Map(), // tick -> Map<id, entity>
  ackTick: 0,
  lastSnapTick: -1, // тик новейшего применённого снапшота; -1 — ещё ни одного

  // Матч (итерация 14). Обновляется reliable-сообщением MatchState (событийно: смена
  // фазы, смерть, вход/выход). Таймер отсчитываем локально от recvMs, чтобы он шёл
  // плавно между редкими обновлениями; на каждом MatchState пере-синхронизируется.
  match: null, // { phase, remaining (тиков на момент приёма), winner, scores, recvMs }

  // Пикапы (итерация 19). Обновляются reliable-сообщением MsgPickupState (полное
  // состояние, событийно). Массив активных: { spot, kind }; координаты берутся из
  // PICKUP_SPOTS по spot. Чистый рендер, в симуляцию/предсказание не входит.
  pickups: [],

  // Неуязвимость/киллстрики (итерация 20), чистый рендер. shields: id → expiryMs
  // (щит-кольцо), взводится по событиям Spawn и Killstreak. streakBanner —
  // последнее объявление серии { text, untilMs }.
  shields: new Map(),
  streakBanner: null,

  // Наблюдатель (итерация 22): подключились без спавна (JoinAck YourID == 0). Своей
  // сущности нет, ввод не шлём; WASD панорамирует свободную камеру specCam.
  spectator: false,
  specCam: { x: SIM.MapSize / 2, y: SIM.MapSize / 2 },

  // Сенсорное управление (итерация 24): два виртуальных стика поверх canvas. Левый —
  // движение (направление → биты WASD в state.keys), правый — прицел (угол → state.aim)
  // и удержание огня. Стики кормят ТЕ ЖЕ state.keys/state.aim, что клавиатура/мышь,
  // поэтому предсказание, кодирование и отправка ввода не меняются. id — pointerId
  // активного касания (null — стик отпущен). touchAiming: правый стик держит прицел,
  // тогда render не перетирает state.aim позицией мыши.
  touch: {
    move: { id: null, cx: 0, cy: 0, dx: 0, dy: 0 },
    aim: { id: null, cx: 0, cy: 0, dx: 0, dy: 0 },
  },
  touchAiming: false,
};

// Радиус базы виртуального стика и мёртвая зона (доля радиуса) для сенсорного ввода.
const STICK_RADIUS = 64;
const STICK_DEADZONE = 0.28;
// Порог октанта для 8-направленного движения: нормированная компонента больше порога
// зажимает соответствующую клавишу. cos(67.5°) ≈ 0.38 даёт ровные диагонали.
const STICK_OCTANT = 0.38;

// SNAP_KEEP — сколько недавних реконструированных наборов держим как базы. Не
// меньше кольца баз сервера (baselineRingLen), с запасом.
const SNAP_KEEP = 32;

function setStatus(text, ok) {
  els.status.textContent = "";
  const b = document.createElement("b");
  b.textContent = text;
  els.status.append("status ", b);
  els.status.dataset.ok = String(ok);
}

// ---- звук (итерация 18) ----------------------------------------------------
// Синтез через Web Audio, без ассетов: короткие тоны/шум на уже приходящих reliable-
// событиях боя (Hit/Death/Spawn) и на своём выстреле. Провод/симуляция не тронуты —
// звук читает те же события, что и HUD. AudioContext создаётся/резюмится по первому
// жесту пользователя (connect или тумблер): до жеста браузер аудио глушит.
const SOUND_KEY = "arena_sound";
const sound = {
  ctx: null,
  on: localStorage.getItem(SOUND_KEY) !== "off", // по умолчанию включён
  lastFireMs: 0,
};
// FIRE_SOUND_MS — троттл звука выстрела ≈ серверный кулдаун (0.3 с). Аппроксимация для
// косметики, НЕ симуляция: рассинхрон с реальным выстрелом безвреден (звук, не позиция).
const FIRE_SOUND_MS = 300;

// audioCtx лениво создаёт и резюмит AudioContext; зовётся только из жеста пользователя.
function audioCtx() {
  if (!sound.ctx) {
    const AC = window.AudioContext || window.webkitAudioContext;
    if (!AC) return null;
    sound.ctx = new AC();
  }
  if (sound.ctx.state === "suspended") sound.ctx.resume();
  return sound.ctx;
}

// tone проигрывает один осциллятор с экспоненциальной огибающей (щелчок/бип); freq2 —
// частота, к которой скользим за dur (свип), null — без свипа.
function tone(freq, freq2, type, dur, gain, delay) {
  const ctx = sound.ctx;
  if (!ctx || !sound.on) return;
  const t0 = ctx.currentTime + (delay || 0);
  const osc = ctx.createOscillator();
  const g = ctx.createGain();
  osc.type = type;
  osc.frequency.setValueAtTime(freq, t0);
  if (freq2) osc.frequency.exponentialRampToValueAtTime(freq2, t0 + dur);
  g.gain.setValueAtTime(0.0001, t0);
  g.gain.exponentialRampToValueAtTime(gain, t0 + 0.006);
  g.gain.exponentialRampToValueAtTime(0.0001, t0 + dur);
  osc.connect(g).connect(ctx.destination);
  osc.start(t0);
  osc.stop(t0 + dur + 0.03);
}

// noiseBurst — затухающий белый шум через highpass (удары/смерть).
function noiseBurst(dur, gain, hp) {
  const ctx = sound.ctx;
  if (!ctx || !sound.on) return;
  const t0 = ctx.currentTime;
  const n = Math.max(1, Math.floor(ctx.sampleRate * dur));
  const buf = ctx.createBuffer(1, n, ctx.sampleRate);
  const data = buf.getChannelData(0);
  for (let i = 0; i < n; i++) data[i] = (Math.random() * 2 - 1) * (1 - i / n);
  const src = ctx.createBufferSource();
  src.buffer = buf;
  const filt = ctx.createBiquadFilter();
  filt.type = "highpass";
  filt.frequency.value = hp;
  const g = ctx.createGain();
  g.gain.value = gain;
  src.connect(filt).connect(g).connect(ctx.destination);
  src.start(t0);
}

// sfx — конкретные звуки боя (короткие, ненавязчивые).
const sfx = {
  shoot() { tone(520, 150, "square", 0.09, 0.06, 0); },
  hit() { tone(1250, 1650, "sine", 0.05, 0.09, 0); }, // хитмаркер: я попал
  hurt() { noiseBurst(0.14, 0.10, 400); tone(190, 90, "sawtooth", 0.12, 0.06, 0); },
  kill() { tone(620, null, "square", 0.08, 0.10, 0); tone(940, null, "square", 0.10, 0.10, 0.08); },
  death() { noiseBurst(0.3, 0.12, 200); tone(300, 70, "sawtooth", 0.35, 0.09, 0); },
  respawn() { tone(420, null, "triangle", 0.09, 0.09, 0); tone(660, null, "triangle", 0.12, 0.09, 0.09); },
};

function renderSound() {
  els.sound.textContent = sound.on ? "sound: on" : "sound: off";
}

// toggleSound — переключает и персистит; включение — жест, резюмим контекст.
function toggleSound() {
  sound.on = !sound.on;
  localStorage.setItem(SOUND_KEY, sound.on ? "on" : "off");
  renderSound();
  if (sound.on) audioCtx();
}

// ---- аккаунт + лидерборд (итерация 15) -------------------------------------
// Логин/регистрация ходят в REST-бэкенд (/api, итерация 13), токен-сессия живёт в
// localStorage под тем же ключом, что читает encodeJoin — залогиненный игрок
// автоматически заходит в матч под своим аккаунтом. Гость по-прежнему просто вводит
// имя и жмёт connect (токена нет — сервер заводит анонимного гостя, AccountID 0).
const TOKEN_KEY = "arena_token";
const NAME_KEY = "arena_name";
const session = {
  token: localStorage.getItem(TOKEN_KEY) || "",
  name: localStorage.getItem(NAME_KEY) || "",
  id: 0, // AccountID залогиненного; проставляется из ответа /api/me или логина (не персистим)
};

// api дергает REST-эндпоинт и возвращает JSON; на не-2xx бросает {error} с сервера.
async function api(method, path, body, token) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  if (token) opts.headers["Authorization"] = "Bearer " + token;
  const resp = await fetch(path, opts);
  const text = await resp.text();
  const data = text ? JSON.parse(text) : {};
  if (!resp.ok) throw new Error(data.error || `HTTP ${resp.status}`);
  return data;
}

function authMsg(text, ok) {
  els.authMsg.textContent = text;
  els.authMsg.dataset.ok = String(!!ok);
}

// startSession сохраняет токен и имя из ответа бэкенда и обновляет UI.
function startSession(resp) {
  session.token = resp.token;
  session.name = resp.name;
  session.id = resp.id;
  localStorage.setItem(TOKEN_KEY, resp.token);
  localStorage.setItem(NAME_KEY, resp.name);
  renderSession();
  loadMe(); // подтянуть статистику
}

function endSession() {
  session.token = "";
  session.name = "";
  session.id = 0;
  closeProfile();
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(NAME_KEY);
  renderSession();
}

// renderSession переключает панель между «залогинен» и «гость». У залогиненного имя
// приходит из токена (сервер его же и использует), поэтому поле name блокируем.
function renderSession() {
  const on = !!session.token;
  els.authOut.hidden = on;
  els.authIn.hidden = !on;
  if (on) {
    els.authName.textContent = session.name || "player";
    els.name.value = session.name || els.name.value;
  }
  els.name.disabled = on;
  if (!on) els.authStats.textContent = "";
}

// loadMe валидирует токен и показывает статистику; протухший токен — выход из сессии.
async function loadMe() {
  if (!session.token) return;
  try {
    const me = await api("GET", "/api/me", undefined, session.token);
    session.name = me.name;
    session.id = me.id;
    els.authName.textContent = me.name;
    if (me.stats) {
      const s = me.stats;
      els.authStats.textContent = `K/D ${s.kills}/${s.deaths} · wins ${s.wins} · games ${s.games}`;
    } else {
      els.authStats.textContent = "guest";
    }
  } catch (e) {
    authMsg("session expired", false);
    endSession();
  }
}

async function doAuth(path) {
  const username = els.authUser.value.trim();
  const password = els.authPass.value;
  if (!username || !password) { authMsg("enter username and password", false); return; }
  els.authLogin.disabled = els.authRegister.disabled = true;
  try {
    const resp = await api("POST", path, { username, password });
    authMsg("", true);
    els.authPass.value = "";
    startSession(resp);
  } catch (e) {
    authMsg(e.message, false);
  } finally {
    els.authLogin.disabled = els.authRegister.disabled = false;
  }
}

// renderLeaderboard рисует таблицу; своя строка (если залогинен) подсвечена.
function renderLeaderboard(entries) {
  els.lbBody.textContent = "";
  if (!entries.length) {
    const tr = els.lbBody.insertRow();
    const td = tr.insertCell();
    td.colSpan = 4; td.className = "muted"; td.textContent = "no players yet";
    return;
  }
  for (const e of entries) {
    const tr = els.lbBody.insertRow();
    if (session.name && e.name === session.name) tr.style.background = "#1d2740";
    // Клик по строке открывает профиль этого игрока (id приходит из бэкенда).
    tr.className = "clickable";
    tr.title = "open profile";
    tr.addEventListener("click", () => openProfile(e.id, e.name));
    const name = tr.insertCell();
    name.className = "name"; name.textContent = e.name; name.title = e.name;
    tr.insertCell().textContent = e.kills;
    tr.insertCell().textContent = e.deaths;
    tr.insertCell().textContent = e.wins;
  }
}

async function loadLeaderboard() {
  try {
    const data = await api("GET", "/api/leaderboard?limit=10");
    renderLeaderboard(data.leaderboard || []);
  } catch (e) {
    els.lbBody.textContent = "";
    const tr = els.lbBody.insertRow();
    const td = tr.insertCell();
    td.colSpan = 4; td.className = "muted"; td.textContent = "leaderboard offline";
  }
}

// ---- профиль игрока (итерация 16) ------------------------------------------
// Модалка поверх всего: статистика + история матчей залогиненного или любого
// игрока из лидерборда. Данные — из готового REST-бэкенда (/api/players/{id}/…,
// итерация 13). Открывается по клику на строку лидерборда или кнопку profile.

// fmtWhen форматирует unix-секунды (ended_at) в компактное локальное MM/DD HH:MM.
function fmtWhen(sec) {
  const d = new Date(sec * 1000);
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// renderMatches рисует историю: свежие матчи сверху (бэкенд отдаёт по убыванию времени).
function renderMatches(matches) {
  els.profMatches.textContent = "";
  if (!matches.length) {
    const tr = els.profMatches.insertRow();
    const td = tr.insertCell();
    td.colSpan = 5; td.className = "muted"; td.textContent = "no matches yet";
    return;
  }
  for (const m of matches) {
    const tr = els.profMatches.insertRow();
    const when = tr.insertCell();
    when.className = "name"; when.textContent = fmtWhen(m.ended_at);
    tr.insertCell().textContent = m.mode;
    tr.insertCell().textContent = m.kills;
    tr.insertCell().textContent = m.deaths;
    const res = tr.insertCell();
    res.textContent = m.won ? "W" : "L";
    res.style.color = m.won ? "#4ade80" : "#6b7080";
  }
}

// openProfile тянет статистику и историю параллельно и показывает модалку.
async function openProfile(id, name) {
  if (!id) return;
  els.profName.textContent = name || `#${id}`;
  els.profStats.textContent = "loading…";
  els.profMatches.textContent = "";
  els.profile.hidden = false;
  try {
    const [stats, hist] = await Promise.all([
      api("GET", `/api/players/${id}/stats`),
      api("GET", `/api/players/${id}/matches?limit=20`),
    ]);
    const kd = stats.deaths ? (stats.kills / stats.deaths).toFixed(2) : stats.kills.toFixed(2);
    els.profStats.textContent =
      `K/D ${stats.kills}/${stats.deaths} (${kd}) · wins ${stats.wins}/${stats.games}`;
    renderMatches(hist.matches || []);
  } catch (e) {
    els.profStats.textContent = e.message === "not found" ? "player not found" : "profile offline";
    els.profMatches.textContent = "";
  }
}

function closeProfile() {
  els.profile.hidden = true;
}

// ---- encode / decode (бинарный протокол, little-endian) --------------------
// encodeJoin зеркалит protocol.AppendJoin:
// [1B type][1B nameLen][name][2B tokenLen][token][1B spectator] (little-endian).
// token — токен-сессия из бэкенда (register/login/guest); пусто → гость.
// spectator (итер. 22) — 1 = наблюдатель без спавна.
function encodeJoin(name, token, spectator) {
  let nameBytes = new TextEncoder().encode(name);
  if (nameBytes.length > 16) nameBytes = nameBytes.slice(0, 16);
  let tokenBytes = new TextEncoder().encode(token || "");
  if (tokenBytes.length > 512) tokenBytes = tokenBytes.slice(0, 512);
  const buf = new Uint8Array(2 + nameBytes.length + 2 + tokenBytes.length + 1);
  const view = new DataView(buf.buffer);
  buf[0] = PROTO.MsgJoin;
  buf[1] = nameBytes.length;
  buf.set(nameBytes, 2);
  view.setUint16(2 + nameBytes.length, tokenBytes.length, true);
  buf.set(tokenBytes, 2 + nameBytes.length + 2);
  buf[2 + nameBytes.length + 2 + tokenBytes.length] = spectator ? 1 : 0;
  return buf;
}

// encodeInput зеркалит protocol.AppendInput:
// [1B type][4B seq][1B buttons][2B aim][4B viewTick][4B ackTick] — 15 байт тела.
// viewTick — серверный тик, к которому клиент интерполирует (что игрок видит);
// сервер по нему перематывает цели для lag compensation. ackTick — последний
// реконструированный снапшот; сервер кодирует следующий дельтой против него.
// 0 в обоих — данных ещё нет.
function encodeInput(seq, buttons, aim, viewTick, ackTick) {
  const buf = new ArrayBuffer(16);
  const dv = new DataView(buf);
  dv.setUint8(0, PROTO.MsgInput);
  dv.setUint32(1, seq >>> 0, true);
  dv.setUint8(5, buttons);
  // Отрицательный угол корректно заворачивается через & 0xffff.
  const aimQ = Math.round((aim / (2 * Math.PI)) * 65536) & 0xffff;
  dv.setUint16(6, aimQ, true);
  dv.setUint32(8, viewTick >>> 0, true);
  dv.setUint32(12, ackTick >>> 0, true);
  return buf;
}

// currentViewTick — серверный тик, к которому сейчас идёт интерполяция (момент,
// который игрок видит на экране). Сервер перематывает к нему цели, чтобы
// попадания считались по тому, что видел стрелок. Зеркалит семантику
// Snapshot.Tick: tick = serverTime * tickRate. 0 — снапшотов ещё не было, тогда
// сервер бьёт по настоящему.
function currentViewTick() {
  if (state.playback === null) return 0;
  const t = Math.round(state.playback * INTERP.tickRate);
  return t > 0 ? t >>> 0 : 0;
}

// decodeServer разбирает ArrayBuffer серверного сообщения в форму
// { type, d:{...} }, повторяя раскладку provider'а в форме, которую ждёт
// pushSnapshot: снапшот -> { t, ls, e:[{i,k,x,y,vx,vy,hp}] }.
function decodeServer(data) {
  const dv = new DataView(data);
  const type = dv.getUint8(0);
  if (type === PROTO.MsgJoinAck) {
    return { type, d: { i: dv.getUint16(1, true), t: dv.getUint32(3, true) } };
  }
  if (type === PROTO.MsgSnapshot) {
    // [4B tick][4B baseTick][4B ls][1B count] count×запись [1B removed] removed×2B.
    // bt === 0 — полный снапшот: запись фиксированная 12B. bt !== 0 — дельта
    // (field-level, итерация 9): запись [2B id][1B маска][присутствующие поля] в
    // порядке kind/x/y/vx/vy/hp; отсутствующие поля берутся из базы в applyDelta.
    const t = dv.getUint32(1, true);
    const bt = dv.getUint32(5, true);
    const ls = dv.getUint32(9, true);
    const count = dv.getUint8(13);
    const e = [];
    let off = 14;
    if (bt === 0) {
      for (let j = 0; j < count; j++) {
        e.push({
          i: dv.getUint16(off, true),
          k: dv.getUint8(off + 2),
          x: dv.getUint16(off + 3, true) / COORD_SCALE,
          y: dv.getUint16(off + 5, true) / COORD_SCALE,
          vx: dv.getInt16(off + 7, true) / COORD_SCALE,
          vy: dv.getInt16(off + 9, true) / COORD_SCALE,
          hp: dv.getUint8(off + 11),
        });
        off += 12;
      }
    } else {
      for (let j = 0; j < count; j++) {
        const i = dv.getUint16(off, true);
        const m = dv.getUint8(off + 2) & PROTO.FieldAll; // неизвестные биты отбрасываем
        off += 3;
        const ent = { i, m };
        if (m & PROTO.FieldKind) {
          ent.k = dv.getUint8(off);
          off += 1;
        }
        if (m & PROTO.FieldX) {
          ent.x = dv.getUint16(off, true) / COORD_SCALE;
          off += 2;
        }
        if (m & PROTO.FieldY) {
          ent.y = dv.getUint16(off, true) / COORD_SCALE;
          off += 2;
        }
        if (m & PROTO.FieldVX) {
          ent.vx = dv.getInt16(off, true) / COORD_SCALE;
          off += 2;
        }
        if (m & PROTO.FieldVY) {
          ent.vy = dv.getInt16(off, true) / COORD_SCALE;
          off += 2;
        }
        if (m & PROTO.FieldHP) {
          ent.hp = dv.getUint8(off);
          off += 1;
        }
        e.push(ent);
      }
    }
    const rcount = dv.getUint8(off);
    off += 1;
    const r = [];
    for (let j = 0; j < rcount; j++) {
      r.push(dv.getUint16(off, true));
      off += 2;
    }
    return { type, d: { t, bt, ls, e, r } };
  }
  if (type === PROTO.MsgSpawn) {
    return {
      type,
      d: {
        i: dv.getUint16(1, true),
        x: dv.getUint16(3, true) / COORD_SCALE,
        y: dv.getUint16(5, true) / COORD_SCALE,
      },
    };
  }
  if (type === PROTO.MsgDeath) {
    return { type, d: { v: dv.getUint16(1, true), k: dv.getUint16(3, true) } };
  }
  if (type === PROTO.MsgHit) {
    return {
      type,
      d: {
        a: dv.getUint16(1, true),
        v: dv.getUint16(3, true),
        dmg: dv.getUint8(5),
        hp: dv.getUint8(6),
      },
    };
  }
  if (type === PROTO.MsgMatchState) {
    // [1B phase][4B remaining][2B winner][1B flags][1B count] count× табло:
    // [2B id][2B kills][2B deaths][1B team][1B nameLen][name UTF-8]. Зеркало
    // protocol.AppendMatchState / game.MatchState. Флаг MATCH_TEAM_MODE (bit0)
    // включает командный режим (итер. 23): winner — id команды-победителя (0/1),
    // а team в табло — команда игрока.
    const phase = dv.getUint8(1);
    const remaining = dv.getUint32(2, true);
    const winner = dv.getUint16(6, true); // байты 6..7
    const flags = dv.getUint8(8); // байт 8 (после 2-байтного winner)
    const teamMode = (flags & MATCH_TEAM_MODE) !== 0;
    const count = dv.getUint8(9);
    const scores = [];
    let off = 10;
    for (let j = 0; j < count; j++) {
      const id = dv.getUint16(off, true);
      const kills = dv.getUint16(off + 2, true);
      const deaths = dv.getUint16(off + 4, true);
      const team = dv.getUint8(off + 6);
      const nlen = dv.getUint8(off + 7);
      off += 8;
      const name = TEXT_DECODER.decode(new Uint8Array(data, off, nlen));
      off += nlen;
      scores.push({ id, name, kills, deaths, team });
    }
    return { type, d: { phase, remaining, winner, teamMode, scores } };
  }
  if (type === PROTO.MsgPickupState) {
    // [1B count] count× [1B spot][1B kind]. Полный набор активных точек; точка не в
    // списке — пуста. Зеркало protocol.AppendPickupState.
    const count = dv.getUint8(1);
    const active = [];
    let off = 2;
    for (let j = 0; j < count; j++) {
      active.push({ spot: dv.getUint8(off), kind: dv.getUint8(off + 1) });
      off += 2;
    }
    return { type, d: { active } };
  }
  if (type === PROTO.MsgKillstreak) {
    // [2B id][2B streak]. Зеркало protocol.AppendKillstreak.
    return { type, d: { i: dv.getUint16(1, true), streak: dv.getUint16(3, true) } };
  }
  return { type, d: null };
}

// ---- соединение ------------------------------------------------------------
// Транспорт выбирается явно: по умолчанию WebSocket (/ws), а ?transport=webrtc
// включает WebRTC DataChannel (итерация 11). Фолбэка нет — как выбрали, так и
// подключаемся. Игровой код одинаков: обе стороны шлют/принимают одни и те же
// бинарные кадры через абстракцию state.link.
function connect() {
  connectAs(false);
}

// spectate подключается наблюдателем (итер. 22): без спавна, свободная камера.
function spectate() {
  connectAs(true);
}

function connectAs(spectator) {
  if (state.link || state.connecting) return;
  state.connecting = true;
  audioCtx(); // клик — жест: разблокируем/резюмим звук
  // Залогинен — токен-сессия и имя из аккаунта (сервер возьмёт имя из токена);
  // иначе гость: имя из поля, токена нет (итерация 15).
  const token = session.token;
  const name = ((token ? session.name : els.name.value) || "player").slice(0, 16);
  setStatus(spectator ? "connecting (spectator)" : "connecting", false);
  els.connect.disabled = true;
  if (els.spectate) els.spectate.disabled = true;
  const useRTC = new URLSearchParams(location.search).get("transport") === "webrtc";
  if (useRTC) connectWebRTC(name, token, spectator);
  else connectWS(name, token, spectator);
}

// handleServerData разбирает один входящий игровой кадр (ArrayBuffer) — общий путь
// для WebSocket и DataChannel.
function handleServerData(data) {
  let msg;
  try {
    msg = decodeServer(data);
  } catch (e) {
    console.warn("bad message", e);
    return;
  }
  if (msg.type === PROTO.MsgJoinAck) {
    state.myID = msg.d.i;
    state.connected = true;
    if (msg.d.i === 0) {
      // YourID == 0 — наблюдатель (итер. 22): своей сущности нет, ввод не шлём,
      // камера свободная (WASD в render). Стартуем её из центра карты.
      state.spectator = true;
      state.specCam = { x: SIM.MapSize / 2, y: SIM.MapSize / 2 };
      setStatus("spectating", true);
      els.me.textContent = "spec";
    } else {
      setStatus("online", true);
      els.me.textContent = String(state.myID);
      startInput();
    }
  } else if (msg.type === PROTO.MsgSnapshot) {
    // Unreliable-канал даёт снапшоты best-effort и без гарантии порядка (итерация
    // 12): устаревший/переупорядоченный тик игнорируем целиком, чтобы он не откатил
    // ackTick и не попал в буфер. На WS (всегда по порядку) условие не срабатывает.
    if ((msg.d.t >>> 0) <= state.lastSnapTick) return;
    const full = applyDelta(msg.d);
    if (full) {
      state.lastSnapTick = full.t >>> 0;
      state.ackTick = full.t >>> 0; // подтверждаем реконструированный тик
      pushSnapshot(full);
    }
  } else if (msg.type === PROTO.MsgSpawn) {
    onSpawn(msg.d);
  } else if (msg.type === PROTO.MsgDeath) {
    onDeath(msg.d);
  } else if (msg.type === PROTO.MsgHit) {
    onHit(msg.d);
  } else if (msg.type === PROTO.MsgMatchState) {
    state.match = { ...msg.d, recvMs: performance.now() };
  } else if (msg.type === PROTO.MsgPickupState) {
    state.pickups = msg.d.active;
  } else if (msg.type === PROTO.MsgKillstreak) {
    onKillstreak(msg.d);
  }
}

// connectWS — путь WebSocket: соединение и есть игровой транспорт.
function connectWS(name, token, spectator) {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.binaryType = "arraybuffer"; // входящие кадры приходят как ArrayBuffer
  state.ws = ws;
  ws.onopen = () => {
    state.link = {
      send: (buf) => ws.send(buf),
      close: () => ws.close(),
      isOpen: () => ws.readyState === WebSocket.OPEN,
    };
    ws.send(encodeJoin(name, token, spectator));
  };
  ws.onmessage = (ev) => handleServerData(ev.data);
  ws.onclose = () => teardown("offline");
  ws.onerror = () => teardown("error");
}

// connectWebRTC — путь WebRTC: WS /rtc несёт только сигналинг (config/offer/answer),
// игра идёт по DataChannel "game". Зеркалит серверный transport.AcceptWebRTC:
// клиент — offerer, ICE non-trickle (ждём завершения сбора кандидатов, затем шлём
// offer с ними в SDP). Сигналинг закрываем сразу после открытия канала.
function connectWebRTC(name, token, spectator) {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const sig = new WebSocket(`${proto}://${location.host}/rtc`);
  let pc = null;
  sig.onmessage = async (ev) => {
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch (e) {
      console.warn("bad signaling", e);
      return;
    }
    try {
      if (msg.kind === "config") {
        // Сервер диктует ICE-серверы. Поля urls/username/credential совпадают с
        // RTCIceServer, TURN-креды (если есть) прокидываются как есть.
        pc = new RTCPeerConnection({
          iceServers: (msg.iceServers || []).map((s) => ({
            urls: s.urls, username: s.username, credential: s.credential,
          })),
          // Сервер может потребовать соединение только через TURN-relay.
          iceTransportPolicy: msg.forceRelay ? "relay" : "all",
        });
        state.pc = pc;
        const dc = pc.createDataChannel("game"); // ordered+reliable: JoinAck, события, вводы
        dc.binaryType = "arraybuffer";
        // Второй канал "state" — unordered+unreliable под снапшоты (итерация 12):
        // потерянный снапшот не ретрансмитим, и он не держит head-of-line blocking.
        // Только приём (сервер→клиент); диспетчер общий по типу сообщения.
        const stateCh = pc.createDataChannel("state", { ordered: false, maxRetransmits: 0 });
        stateCh.binaryType = "arraybuffer";
        stateCh.onmessage = (e) => handleServerData(e.data);
        dc.onopen = () => {
          state.link = {
            send: (buf) => dc.send(buf), // вводы — надёжным каналом
            close: () => { try { pc.close(); } catch (_) {} },
            isOpen: () => dc.readyState === "open",
          };
          try { sig.close(); } catch (_) {} // сигналинг больше не нужен (non-trickle)
          dc.send(encodeJoin(name, token, spectator));
        };
        dc.onmessage = (e) => handleServerData(e.data);
        dc.onclose = () => teardown("offline");
        pc.onconnectionstatechange = () => {
          const s = pc.connectionState;
          if (s === "failed" || s === "disconnected" || s === "closed") teardown("offline");
        };
        // Non-trickle: собираем всех кандидатов, затем шлём offer с ними в SDP.
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        await iceGatheringComplete(pc);
        sig.send(JSON.stringify({ kind: "offer", sdp: pc.localDescription.sdp }));
      } else if (msg.kind === "answer" && pc) {
        await pc.setRemoteDescription({ type: "answer", sdp: msg.sdp });
      }
    } catch (e) {
      console.warn("webrtc signaling failed", e);
      teardown("error");
    }
  };
  sig.onerror = () => { if (!state.link) teardown("error"); };
  // Штатное закрытие сигналинга после рукопожатия — не разрыв игры.
  sig.onclose = () => { if (!state.link) teardown("offline"); };
}

// iceGatheringComplete резолвится, когда ICE-агент собрал всех кандидатов
// (non-trickle: только тогда localDescription.sdp полон).
function iceGatheringComplete(pc) {
  if (pc.iceGatheringState === "complete") return Promise.resolve();
  return new Promise((resolve) => {
    const check = () => {
      if (pc.iceGatheringState === "complete") {
        pc.removeEventListener("icegatheringstatechange", check);
        resolve();
      }
    };
    pc.addEventListener("icegatheringstatechange", check);
  });
}

function teardown(reason) {
  stopInput();
  state.connected = false;
  state.connecting = false;
  if (state.pc) { try { state.pc.close(); } catch (_) {} state.pc = null; }
  state.ws = null;
  state.link = null;
  state.myID = 0;
  state.seq = 0;
  state.buffer = [];
  state.snapStore = new Map();
  state.ackTick = 0;
  state.lastSnapTick = -1;
  state.playback = null;
  state.pending = [];
  state.pred = { x: 0, y: 0, vx: 0, vy: 0 };
  state.smoothErr = { x: 0, y: 0 };
  state.predReady = false;
  state.selfHp = 100;
  state.dead = false;
  state.flashMs = 0;
  state.match = null;
  state.pickups = [];
  state.shields = new Map();
  state.streakBanner = null;
  state.spectator = false;
  setStatus(reason, false);
  els.connect.disabled = false;
  if (els.spectate) els.spectate.disabled = false;
  els.me.textContent = "–";
}

// ---- reliable-события боя ---------------------------------------------------
// onSpawn: наш (пере)спавн — сбрасываем предсказание на авторитетную точку. Чужой
// спавн игнорируем: он подхватится ближайшим снапшотом.
function onSpawn(d) {
  // Щит-визуал окна неуязвимости (итерация 20): любой (пере)родившийся несколько
  // секунд под спавн-защитой. Spawn приходит всем, поэтому кольцо видно на всех.
  state.shields.set(d.i, performance.now() + SPAWN_SHIELD_MS);
  if (d.i !== state.myID) return;
  state.dead = false;
  state.selfHp = 100;
  state.pred = { x: d.x, y: d.y, vx: 0, vy: 0 };
  state.pending = [];
  state.smoothErr = { x: 0, y: 0 };
  state.predReady = true;
  sfx.respawn();
}

// onKillstreak: игрок достиг вехи серии убийств (итерация 20). Показываем баннер и
// вешаем щит-кольцо (веха даёт короткую неуязвимость на сервере). Killstreak идёт
// всем; свою серию подсвечиваем словом «you».
function onKillstreak(d) {
  state.shields.set(d.i, performance.now() + KILLSTREAK_SHIELD_MS);
  const who = d.i === state.myID ? "you" : "player " + d.i;
  state.streakBanner = { text: who + " — " + d.streak + " kill streak!", untilMs: performance.now() + STREAK_BANNER_MS };
}

// onDeath: наша смерть — прекращаем предсказание (нас нет в снапшотах) и чистим
// очередь неподтверждённых вводов, чтобы она не росла, пока мы мертвы. Death приходит
// всем, поэтому здесь же ловим свой фраг (killer — мы, жертва — другой).
function onDeath(d) {
  if (d.v === state.myID) {
    state.dead = true;
    state.selfHp = 0;
    state.predReady = false;
    state.pending = [];
    state.smoothErr = { x: 0, y: 0 };
    sfx.death();
  } else if (d.k === state.myID) {
    sfx.kill(); // мы кого-то убили
  }
}

// onHit: попадание по нам — обновляем HP и заводим краткую вспышку. Hit приходит
// участникам, поэтому когда атакующий — мы, играем хитмаркер.
function onHit(d) {
  if (d.v === state.myID) {
    state.selfHp = d.hp;
    state.flashMs = performance.now();
    sfx.hurt();
  } else if (d.a === state.myID) {
    sfx.hit();
  }
}

// ---- дельта-реконструкция (итерация 6B) ------------------------------------
// applyDelta восстанавливает полный набор сущностей из снапшота — полного
// (bt === 0) или дельты (bt !== 0, против недавней подтверждённой базы). Зеркало
// server sendSnapshot и bot reconstructor. Возвращает { t, ls, e:[...] } либо null,
// если базы дельты нет (снапшот пропускаем и не подтверждаем).
function applyDelta(d) {
  let base;
  if (d.bt === 0) {
    base = new Map();
  } else {
    const stored = state.snapStore.get(d.bt);
    if (!stored) return null; // базы нет — реконструировать нечем
    base = new Map(stored); // копия: хранимую базу не мутируем
  }
  for (const id of d.r) base.delete(id);
  // Полный снапшот несёт сущность целиком — кладём как есть. Дельта (field-level,
  // итерация 9) несёт лишь помеченные маской ent.m поля: остальные берём из базы
  // (для новой сущности — нули, но её маска FieldAll всё перезапишет). Зеркало bot
  // reconstructor. Итоговая запись всегда полная (7 полей) — рендер читает их все.
  for (const ent of d.e) {
    if (d.bt === 0) {
      base.set(ent.i, ent);
      continue;
    }
    const prev = base.get(ent.i);
    const cur = prev
      ? { i: prev.i, k: prev.k, x: prev.x, y: prev.y, vx: prev.vx, vy: prev.vy, hp: prev.hp }
      : { i: ent.i, k: 0, x: 0, y: 0, vx: 0, vy: 0, hp: 0 };
    if (ent.m & PROTO.FieldKind) cur.k = ent.k;
    if (ent.m & PROTO.FieldX) cur.x = ent.x;
    if (ent.m & PROTO.FieldY) cur.y = ent.y;
    if (ent.m & PROTO.FieldVX) cur.vx = ent.vx;
    if (ent.m & PROTO.FieldVY) cur.vy = ent.vy;
    if (ent.m & PROTO.FieldHP) cur.hp = ent.hp;
    base.set(ent.i, cur);
  }
  state.snapStore.set(d.t, base);
  // Вытесняем старейшую базу (Map обходит ключи в порядке вставки, тики растут).
  if (state.snapStore.size > SNAP_KEEP) {
    state.snapStore.delete(state.snapStore.keys().next().value);
  }
  const e = [];
  for (const ent of base.values()) e.push(ent);
  return { t: d.t, ls: d.ls, e };
}

// ---- буфер снапшотов -------------------------------------------------------
function pushSnapshot(snap) {
  const serverTime = snap.t / INTERP.tickRate;
  const buf = state.buffer;
  // Игнорируем переупорядоченные и дублирующиеся тики.
  if (buf.length && serverTime <= buf[buf.length - 1].serverTime) return;

  // Реконсиляция своего игрока идёт по НОВЕЙШЕМУ снапшоту (без задержки
  // интерполяции): авторитетная позиция + переигровка неподтверждённых вводов.
  reconcile(snap);

  const ents = new Map();
  for (const e of snap.e) ents.set(e.i, e);
  buf.push({ serverTime, tick: snap.t, ents });

  // Подрезаем историю, глубже которой playback уже не заглянет.
  const cutoff = serverTime - INTERP.history;
  while (buf.length > 2 && buf[0].serverTime < cutoff) buf.shift();
}

// seqLE сравнивает 32-битные seq с учётом заворачивания: возвращает a <= b.
function seqLE(a, b) {
  return ((b - a) >>> 0) < 0x80000000;
}

// reconcile синхронизирует предсказание своего игрока с авторитетным снапшотом:
// снимает подтверждённые вводы, переигрывает остаток поверх серверной позиции и
// заводит остаточную ошибку в сглаживание, чтобы коррекция не была видна рывком.
function reconcile(snap) {
  let mine = null;
  for (const e of snap.e) {
    if (e.i === state.myID) { mine = e; break; }
  }
  if (!mine) return; // нас ещё нет в снапшоте — предсказывать не от чего

  const ack = snap.ls >>> 0;
  // Отбрасываем подтверждённые вводы (seq <= ack); хвост — неподтверждённое.
  while (state.pending.length && seqLE(state.pending[0].seq, ack)) {
    state.pending.shift();
  }

  // Где мы рисовали своего игрока до коррекции — чтобы сгладить скачок.
  const oldX = state.pred.x + state.smoothErr.x;
  const oldY = state.pred.y + state.smoothErr.y;

  // Переигрываем неподтверждённые вводы поверх авторитетной позиции.
  const p = { x: mine.x, y: mine.y, vx: mine.vx, vy: mine.vy };
  for (const inp of state.pending) stepMove(p, inp.buttons, inp.dt);
  state.pred = p;
  state.selfHp = mine.hp;
  state.predReady = true;

  // rendered = pred + smoothErr; держим его в старой точке и гасим ошибку к нулю.
  state.smoothErr.x = oldX - p.x;
  state.smoothErr.y = oldY - p.y;
  // Большая коррекция (спавн/респаун/телепорт) — не тянем резину, прыгаем сразу.
  if (Math.hypot(state.smoothErr.x, state.smoothErr.y) > PREDICT.snap) {
    state.smoothErr.x = 0;
    state.smoothErr.y = 0;
  }
}

// sampleWorld продвигает playback-часы и возвращает интерполированный кадр:
// { tick, players, entities:[{i,k,x,y,hp}] } — или null, если данных ещё нет.
function sampleWorld(nowMs) {
  const buf = state.buffer;
  if (buf.length === 0) return null;

  const newest = buf[buf.length - 1].serverTime;
  const oldest = buf[0].serverTime;
  const target = newest - INTERP.delay;

  const dt = state.lastFrame ? (nowMs - state.lastFrame) / 1000 : 0;
  state.lastFrame = nowMs;

  if (state.playback === null) {
    state.playback = target;
  } else {
    state.playback += dt;
    // Пересинхронизация: убежали за новейший снапшот (буфер кончился) или
    // отстали слишком сильно (вкладка засыпала, всплеск задержки).
    if (state.playback > newest || state.playback < target - INTERP.resync) {
      state.playback = target;
    }
  }
  if (state.playback < oldest) state.playback = oldest;

  // Ищем пару снапшотов s0..s1, охватывающую playback.
  let idx = 0;
  for (let k = 0; k < buf.length; k++) {
    if (buf[k].serverTime <= state.playback) idx = k;
    else break;
  }
  const s0 = buf[idx];
  const s1 = buf[Math.min(idx + 1, buf.length - 1)];
  let alpha = 0;
  if (s1.serverTime > s0.serverTime) {
    alpha = (state.playback - s0.serverTime) / (s1.serverTime - s0.serverTime);
    alpha = Math.max(0, Math.min(1, alpha));
  }

  // Базой берём s1 (текущий набор): ушедшие сущности (есть в s0, нет в s1) сами
  // отсеиваются, только что появившиеся показываем на позиции s1.
  const entities = [];
  for (const [id, e1] of s1.ents) {
    const e0 = s0.ents.get(id);
    if (e0) {
      entities.push({
        i: id, k: e1.k, hp: e1.hp,
        x: e0.x + (e1.x - e0.x) * alpha,
        y: e0.y + (e1.y - e0.y) * alpha,
      });
    } else {
      entities.push({ i: id, k: e1.k, hp: e1.hp, x: e1.x, y: e1.y });
    }
  }
  return { tick: buf[buf.length - 1].tick, players: entities.length, entities };
}

// ---- ввод на 60 Гц ---------------------------------------------------------
function buttonsFromKeys() {
  let b = 0;
  if (state.keys.w) b |= PROTO.BtnUp;
  if (state.keys.a) b |= PROTO.BtnLeft;
  if (state.keys.s) b |= PROTO.BtnDown;
  if (state.keys.d) b |= PROTO.BtnRight;
  if (state.keys.fire) b |= PROTO.BtnFire;
  return b;
}

function startInput() {
  stopInput();
  state.inputTimer = setInterval(() => {
    if (!state.connected || !state.link || !state.link.isOpen()) return;
    state.seq = (state.seq + 1) >>> 0;
    const buttons = buttonsFromKeys();
    // Пока мертвы — не предсказываем и не копим вводы (сервер их всё равно
    // игнорирует), но шлём, чтобы соединение жило.
    if (!state.dead) {
      // Звук выстрела: троттлим ≈ к серверному кулдауну, чтобы совпадать с реальными
      // выстрелами (косметика; сервер всё равно авторитетен).
      if (buttons & PROTO.BtnFire) {
        const now = performance.now();
        if (now - sound.lastFireMs >= FIRE_SOUND_MS) {
          sound.lastFireMs = now;
          sfx.shoot();
        }
      }
      // Предсказываем немедленно: свой игрок отзывается в тот же кадр, не
      // дожидаясь сервера. Шаг тем же stepMove, что и на сервере.
      if (state.predReady) stepMove(state.pred, buttons, PREDICT.dt);
      // Держим ввод неподтверждённым, пока сервер не подтвердит его seq: на
      // снапшоте он переиграется поверх авторитетной позиции.
      state.pending.push({ seq: state.seq, buttons, dt: PREDICT.dt });
    }
    state.link.send(encodeInput(state.seq, buttons, state.aim, currentViewTick(), state.ackTick));
  }, 1000 / 60);
}

function stopInput() {
  if (state.inputTimer) {
    clearInterval(state.inputTimer);
    state.inputTimer = 0;
  }
}

// panSpecCam панорамирует свободную камеру наблюдателя по WASD (итер. 22). Чисто
// клиентское движение камеры — сеть не задействуется (наблюдатель ввод не шлёт).
function panSpecCam(dt) {
  if (dt <= 0) return;
  const speed = 700; // юнитов/с
  let dx = 0, dy = 0;
  if (state.keys.a) dx -= 1;
  if (state.keys.d) dx += 1;
  if (state.keys.w) dy -= 1;
  if (state.keys.s) dy += 1;
  if (dx !== 0 && dy !== 0) { dx *= INV_SQRT2; dy *= INV_SQRT2; }
  state.specCam.x = Math.max(0, Math.min(SIM.MapSize, state.specCam.x + dx * speed * dt));
  state.specCam.y = Math.max(0, Math.min(SIM.MapSize, state.specCam.y + dy * speed * dt));
}

// ---- клавиатура / мышь -----------------------------------------------------
const keyMap = { KeyW: "w", KeyA: "a", KeyS: "s", KeyD: "d" };
window.addEventListener("keydown", (e) => {
  const k = keyMap[e.code];
  if (k) { state.keys[k] = true; e.preventDefault(); }
});
window.addEventListener("keyup", (e) => {
  const k = keyMap[e.code];
  if (k) { state.keys[k] = false; e.preventDefault(); }
});
canvas.addEventListener("mousemove", (e) => {
  const r = canvas.getBoundingClientRect();
  state.mouse.x = e.clientX - r.left;
  state.mouse.y = e.clientY - r.top;
});
canvas.addEventListener("mousedown", () => { state.keys.fire = true; });
window.addEventListener("mouseup", () => { state.keys.fire = false; });

// ---- сенсорное управление (итерация 24) ------------------------------------
// Твин-стик поверх canvas: касание левой половины — стик движения, правой — стик
// прицела+огня. Кормит те же state.keys/state.aim, что мышь/клавиатура, поэтому
// путь ввода (предсказание/кодек/отправка) не тронут. Работает и у наблюдателя
// (левый стик панорамирует specCam через panSpecCam — тот читает state.keys).

// applyMoveStick переводит вектор левого стика в 8-направленный WASD (state.keys).
function applyMoveStick() {
  const s = state.touch.move;
  let up = false, down = false, left = false, right = false;
  const mag = Math.hypot(s.dx, s.dy) / STICK_RADIUS;
  if (mag > STICK_DEADZONE) {
    const nx = s.dx / (mag * STICK_RADIUS); // нормированное направление
    const ny = s.dy / (mag * STICK_RADIUS);
    if (nx > STICK_OCTANT) right = true;
    if (nx < -STICK_OCTANT) left = true;
    if (ny > STICK_OCTANT) down = true;
    if (ny < -STICK_OCTANT) up = true;
  }
  state.keys.w = up; state.keys.s = down; state.keys.a = left; state.keys.d = right;
}

// touchPoint переводит клиентские координаты касания в координаты canvas.
function touchPoint(e) {
  const r = canvas.getBoundingClientRect();
  return {
    x: (e.clientX - r.left) * (canvas.width / r.width),
    y: (e.clientY - r.top) * (canvas.height / r.height),
  };
}

canvas.addEventListener("pointerdown", (e) => {
  if (e.pointerType !== "touch") return; // мышь/перо идут прежним путём
  const p = touchPoint(e);
  const t = state.touch;
  if (p.x < canvas.width / 2) {
    if (t.move.id !== null) return;
    t.move.id = e.pointerId;
    t.move.cx = p.x; t.move.cy = p.y; t.move.dx = 0; t.move.dy = 0;
  } else {
    if (t.aim.id !== null) return;
    t.aim.id = e.pointerId;
    t.aim.cx = p.x; t.aim.cy = p.y; t.aim.dx = 0; t.aim.dy = 0;
    state.keys.fire = true; // правый стик удерживает огонь
    state.touchAiming = true;
  }
  canvas.setPointerCapture(e.pointerId);
  e.preventDefault();
}, { passive: false });

canvas.addEventListener("pointermove", (e) => {
  if (e.pointerType !== "touch") return;
  const t = state.touch;
  const p = touchPoint(e);
  if (e.pointerId === t.move.id) {
    // Смещение от центра базы, зажатое радиусом (для отрисовки knob).
    let dx = p.x - t.move.cx, dy = p.y - t.move.cy;
    const m = Math.hypot(dx, dy);
    if (m > STICK_RADIUS) { dx = dx / m * STICK_RADIUS; dy = dy / m * STICK_RADIUS; }
    t.move.dx = dx; t.move.dy = dy;
    applyMoveStick();
    e.preventDefault();
  } else if (e.pointerId === t.aim.id) {
    let dx = p.x - t.aim.cx, dy = p.y - t.aim.cy;
    const m = Math.hypot(dx, dy);
    if (m > STICK_DEADZONE * STICK_RADIUS) state.aim = Math.atan2(dy, dx); // угол прицела
    if (m > STICK_RADIUS) { dx = dx / m * STICK_RADIUS; dy = dy / m * STICK_RADIUS; }
    t.aim.dx = dx; t.aim.dy = dy;
    e.preventDefault();
  }
}, { passive: false });

function endTouch(e) {
  if (e.pointerType !== "touch") return;
  const t = state.touch;
  if (e.pointerId === t.move.id) {
    t.move.id = null; t.move.dx = 0; t.move.dy = 0;
    state.keys.w = state.keys.a = state.keys.s = state.keys.d = false; // стоп при отпускании
  } else if (e.pointerId === t.aim.id) {
    t.aim.id = null; t.aim.dx = 0; t.aim.dy = 0;
    state.keys.fire = false;
    state.touchAiming = false; // прицел снова следует за мышью (десктоп), если появится
  }
}
canvas.addEventListener("pointerup", endTouch);
canvas.addEventListener("pointercancel", endTouch);
els.connect.addEventListener("click", connect);
if (els.spectate) els.spectate.addEventListener("click", spectate);
els.sound.addEventListener("click", toggleSound);

// Аккаунт + лидерборд (итерация 15).
els.authLogin.addEventListener("click", () => doAuth("/api/login"));
els.authRegister.addEventListener("click", () => doAuth("/api/register"));
els.authLogout.addEventListener("click", () => { endSession(); authMsg("logged out", true); });
els.authPass.addEventListener("keydown", (e) => { if (e.key === "Enter") doAuth("/api/login"); });
els.lbRefresh.addEventListener("click", loadLeaderboard);

// Профиль игрока (итерация 16): открыть свой, закрыть по ✕ / клику вне карточки / Escape.
els.authProfile.addEventListener("click", () => openProfile(session.id, session.name));
els.profClose.addEventListener("click", closeProfile);
els.profile.addEventListener("click", (e) => { if (e.target === els.profile) closeProfile(); });
window.addEventListener("keydown", (e) => { if (e.key === "Escape") closeProfile(); });

// Стартовое состояние: восстановить сессию (провалидировать токен), загрузить лидерборд
// и обновлять его периодически (после матчей статистика меняется).
renderSession();
renderSound();
loadMe();
loadLeaderboard();
setInterval(loadLeaderboard, 15000);

// ---- рендеринг -------------------------------------------------------------
function findSelf(frame) {
  if (!frame) return null;
  for (const e of frame.entities) if (e.i === state.myID) return e;
  return null;
}

function render(nowMs) {
  requestAnimationFrame(render);

  // Ошибку предсказания гасим по реальному времени кадра.
  const dtFrame = state.lastRenderMs ? (nowMs - state.lastRenderMs) / 1000 : 0;
  state.lastRenderMs = nowMs;
  if (state.predReady && dtFrame > 0) {
    const k = Math.pow(PREDICT.smoothDecay, dtFrame);
    state.smoothErr.x *= k;
    state.smoothErr.y *= k;
  }

  ctx.fillStyle = "#16181f";
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  const frame = sampleWorld(nowMs);
  if (frame) {
    els.tick.textContent = String(frame.tick);
    els.players.textContent = String(frame.players);
  }

  // Свой игрок — из предсказания (мгновенный отклик), а не из интерполированного
  // прошлого. Подменяем его позицию в кадре предсказанной (со сглаживанием).
  const selfPred = state.predReady
    ? { x: state.pred.x + state.smoothErr.x, y: state.pred.y + state.smoothErr.y }
    : null;
  if (frame && selfPred) {
    let found = false;
    for (const e of frame.entities) {
      if (e.i === state.myID) { e.x = selfPred.x; e.y = selfPred.y; found = true; break; }
    }
    // Только что заспавнились: интерполяция (на 100 мс позади) ещё не показывает
    // нас — рисуем себя сами по предсказанию.
    if (!found) {
      frame.entities.push({ i: state.myID, k: 1, hp: state.selfHp, x: selfPred.x, y: selfPred.y });
      frame.players = frame.entities.length;
    }
  }

  // Камера центрируется на своём игроке: предсказанном, если он есть; иначе на
  // интерполированном; иначе — центр карты. Наблюдатель (итер. 22) своего игрока не
  // имеет — у него свободная камера, панорамируемая WASD.
  if (state.spectator) panSpecCam(dtFrame);
  const self = findSelf(frame);
  // Пока мертвы, держим камеру на месте гибели (state.pred не сбрасывается до
  // респауна), а не прыгаем в центр карты.
  const camX = state.spectator ? state.specCam.x
    : selfPred ? selfPred.x : self ? self.x : state.dead ? state.pred.x : SIM.MapSize / 2;
  const camY = state.spectator ? state.specCam.y
    : selfPred ? selfPred.y : self ? self.y : state.dead ? state.pred.y : SIM.MapSize / 2;
  const ox = canvas.width / 2 - camX;
  const oy = canvas.height / 2 - camY;

  drawGrid(ox, oy);
  drawWalls(ox, oy);
  drawPickups(ox, oy);

  if (frame) {
    // Прицел следует за мышью — но не когда активен правый сенсорный стик (итер. 24):
    // он задаёт state.aim напрямую углом, перетирать позицией мыши нельзя.
    if (self && !state.touchAiming) {
      const sx = self.x + ox;
      const sy = self.y + oy;
      state.aim = Math.atan2(state.mouse.y - sy, state.mouse.x - sx);
    }
    for (const e of frame.entities) drawEntity(e, ox, oy, e.i === state.myID);
  }

  drawHud(nowMs);
  drawMinimap(frame);
  drawStreakBanner(nowMs);
  drawTouchSticks();
}

// drawTouchSticks рисует активные виртуальные стики (итерация 24): база + knob для
// стика движения (левый, синий) и прицела (правый, красный). Рисуются только пока
// касание удерживается, поэтому на десктопе не видны и ничего не загораживают.
function drawTouchSticks() {
  const draw = (s, color) => {
    if (s.id === null) return;
    ctx.save();
    ctx.strokeStyle = color;
    ctx.fillStyle = color;
    ctx.globalAlpha = 0.35;
    ctx.lineWidth = 3;
    ctx.beginPath();
    ctx.arc(s.cx, s.cy, STICK_RADIUS, 0, 2 * Math.PI);
    ctx.stroke();
    ctx.globalAlpha = 0.6;
    ctx.beginPath();
    ctx.arc(s.cx + s.dx, s.cy + s.dy, STICK_RADIUS * 0.4, 0, 2 * Math.PI);
    ctx.fill();
    ctx.restore();
  };
  draw(state.touch.move, "#4d8bff");
  draw(state.touch.aim, "#ff5d5d");
}

// drawStreakBanner рисует объявление серии убийств (итерация 20) вверху по центру,
// затухая к концу окна. Взводится в onKillstreak по reliable-событию MsgKillstreak.
function drawStreakBanner(nowMs) {
  const b = state.streakBanner;
  if (!b) return;
  if (nowMs >= b.untilMs) {
    state.streakBanner = null;
    return;
  }
  const left = b.untilMs - nowMs;
  const alpha = Math.min(1, left / 500); // последние 0.5 с — затухание
  ctx.save();
  ctx.globalAlpha = alpha;
  ctx.font = "bold 20px ui-monospace, monospace";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.fillStyle = "#ffd166";
  ctx.fillText(b.text, canvas.width / 2, 40);
  ctx.restore();
}

function drawGrid(ox, oy) {
  const step = 256;
  ctx.strokeStyle = "#20232d";
  ctx.lineWidth = 1;
  ctx.beginPath();
  const startX = Math.floor(-ox / step) * step;
  for (let x = startX; x < -ox + canvas.width; x += step) {
    const sx = x + ox;
    ctx.moveTo(sx, 0); ctx.lineTo(sx, canvas.height);
  }
  const startY = Math.floor(-oy / step) * step;
  for (let y = startY; y < -oy + canvas.height; y += step) {
    const sy = y + oy;
    ctx.moveTo(0, sy); ctx.lineTo(canvas.width, sy);
  }
  ctx.stroke();

  // Граница карты.
  ctx.strokeStyle = "#3a3f4d";
  ctx.strokeRect(ox, oy, SIM.MapSize, SIM.MapSize);
}

// drawWalls рисует статичные препятствия (итерация 10) под сущностями: заливка +
// контур. Координаты мировые, смещаются камерой (ox, oy), как и всё остальное.
function drawWalls(ox, oy) {
  ctx.fillStyle = "#2a2f3d";
  ctx.strokeStyle = "#48506a";
  ctx.lineWidth = 2;
  for (const wl of WALLS) {
    const x = wl.minX + ox;
    const y = wl.minY + oy;
    const w = wl.maxX - wl.minX;
    const h = wl.maxY - wl.minY;
    if (x > canvas.width || y > canvas.height || x + w < 0 || y + h < 0) continue;
    ctx.fillRect(x, y, w, h);
    ctx.strokeRect(x, y, w, h);
  }
}

// drawPickups рисует активные пикапы (итерация 19) над стенами, под сущностями.
// Позиция — из PICKUP_SPOTS по индексу spot, цвет и метка — по типу. Чистый рендер:
// какие точки активны и чем, диктует сервер (state.pickups); подбор авторитетен на
// сервере, клиент лишь отображает. save/restore, чтобы не утёк стиль текста в HUD.
function drawPickups(ox, oy) {
  if (!state.pickups.length) return;
  const r = 12;
  ctx.save();
  ctx.font = "bold 14px ui-monospace, monospace";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.lineWidth = 2;
  for (const p of state.pickups) {
    const spot = PICKUP_SPOTS[p.spot];
    if (!spot) continue;
    const x = spot.x + ox;
    const y = spot.y + oy;
    if (x < -32 || x > canvas.width + 32 || y < -32 || y > canvas.height + 32) continue;
    const color = PICKUP_COLORS[p.kind] || "#d6d9e0";
    ctx.beginPath();
    ctx.arc(x, y, r, 0, 2 * Math.PI);
    ctx.fillStyle = "#14161d";
    ctx.fill();
    ctx.strokeStyle = color;
    ctx.stroke();
    ctx.fillStyle = color;
    ctx.fillText(PICKUP_GLYPHS[p.kind] || "?", x, y + 1);
  }
  ctx.restore();
}

// teamOf возвращает команду игрока по табло матча (0/1) или -1, если режим не
// командный либо игрок в табло не найден. Табло — единственный источник команд на
// клиенте (провод несёт их в MsgMatchState, не в снапшоте).
function teamOf(id) {
  const m = state.match;
  if (!m || !m.teamMode) return -1;
  const s = m.scores.find((x) => x.id === id);
  return s ? s.team : -1;
}

// entityFill — цвет кружка игрока. В FFA свой синий, чужие красные (как было). В
// командном режиме (итер. 23) свои (по команде) синие, враги красные; наблюдатель
// (нет своей команды) видит абсолютные цвета команд.
function entityFill(id, isSelf) {
  const t = teamOf(id);
  if (t >= 0) {
    const my = teamOf(state.myID);
    if (my < 0) return TEAM_COLORS[t];
    return t === my ? "#2b6cff" : "#e0574d";
  }
  return isSelf ? "#2b6cff" : "#e0574d";
}

function drawEntity(e, ox, oy, isSelf) {
  const x = e.x + ox;
  const y = e.y + oy;
  if (x < -32 || x > canvas.width + 32 || y < -32 || y > canvas.height + 32) return;

  // Снаряд — маленькая жёлтая точка, без HP-полоски и прицела.
  if (e.k === 2) {
    ctx.beginPath();
    ctx.arc(x, y, SIM.ProjectileRadius + 1, 0, 2 * Math.PI);
    ctx.fillStyle = "#ffd166";
    ctx.fill();
    return;
  }

  ctx.beginPath();
  ctx.arc(x, y, SIM.PlayerRadius, 0, 2 * Math.PI);
  ctx.fillStyle = entityFill(e.i, isSelf);
  ctx.fill();

  // Щит-кольцо неуязвимости (итерация 20): пульсирующее кольцо, пока активен щит
  // (спавн-защита / веха стрика). Косметика — сервер авторитетен; экспайр по таймеру.
  const shieldUntil = state.shields.get(e.i);
  if (shieldUntil) {
    if (performance.now() < shieldUntil) {
      const pulse = 2 + Math.sin(performance.now() / 120) * 1.5;
      ctx.beginPath();
      ctx.arc(x, y, SIM.PlayerRadius + 4, 0, 2 * Math.PI);
      ctx.strokeStyle = "#7fdcff";
      ctx.lineWidth = pulse;
      ctx.stroke();
    } else {
      state.shields.delete(e.i); // истёк — не копим мёртвые записи
    }
  }

  // Индикатор прицела для нашего игрока.
  if (isSelf) {
    ctx.strokeStyle = "#9db4ff";
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(x, y);
    ctx.lineTo(x + Math.cos(state.aim) * 26, y + Math.sin(state.aim) * 26);
    ctx.stroke();
  }

  // Полоска HP.
  const w = 30, h = 4;
  ctx.fillStyle = "#0e0f13";
  ctx.fillRect(x - w / 2, y - SIM.PlayerRadius - 10, w, h);
  ctx.fillStyle = "#4ade80";
  ctx.fillRect(x - w / 2, y - SIM.PlayerRadius - 10, (w * Math.max(0, e.hp)) / 100, h);
}

// drawMinimap рисует обзорную карту снизу справа: границы арены, стены и известные
// сущности. Набор ограничен interest management (AOI) — на карте видно окрестность
// игрока в масштабе всей арены, а не всю комнату. Свой игрок — синий и крупнее.
function drawMinimap(frame) {
  if (!state.connected) return;
  const size = 148, pad = 12;
  const x0 = canvas.width - size - pad;
  const y0 = canvas.height - size - pad;
  const s = size / SIM.MapSize; // мировые юниты → пиксели миникарты
  ctx.save();
  ctx.fillStyle = "rgba(10,11,15,0.78)";
  ctx.fillRect(x0, y0, size, size);
  ctx.strokeStyle = "#2a2e3a";
  ctx.lineWidth = 1;
  ctx.strokeRect(x0 + 0.5, y0 + 0.5, size, size);
  // Стены.
  ctx.fillStyle = "#3a4152";
  for (const wl of WALLS) {
    ctx.fillRect(x0 + wl.minX * s, y0 + wl.minY * s, (wl.maxX - wl.minX) * s, (wl.maxY - wl.minY) * s);
  }
  // Пикапы (итерация 19): точки цвета типа — видно, где что лежит.
  for (const p of state.pickups) {
    const spot = PICKUP_SPOTS[p.spot];
    if (spot) dot(x0 + spot.x * s, y0 + spot.y * s, 2, PICKUP_COLORS[p.kind] || "#d6d9e0");
  }
  // Сущности (снаряды не рисуем — шум). Свой игрок последним и крупнее — поверх чужих.
  // В командном режиме чужие красятся по команде (свои синие, враги красные).
  if (frame) {
    for (const e of frame.entities) {
      if (e.k !== 1 || e.i === state.myID) continue;
      dot(x0 + e.x * s, y0 + e.y * s, 2, entityFill(e.i, false));
    }
    const me = findSelf(frame);
    if (me) dot(x0 + me.x * s, y0 + me.y * s, 3, entityFill(state.myID, true));
  }
  ctx.restore();
}

function dot(x, y, r, color) {
  ctx.fillStyle = color;
  ctx.beginPath();
  ctx.arc(x, y, r, 0, 2 * Math.PI);
  ctx.fill();
}

// drawHud рисует поверх мира: вспышку урона, HP-полосу своего игрока и оверлей
// смерти.
function drawHud(nowMs) {
  // Красная вспышка при получении урона (~150 мс, затухает).
  const since = nowMs - state.flashMs;
  if (state.flashMs && since >= 0 && since < 150) {
    ctx.fillStyle = `rgba(224,87,77,${0.35 * (1 - since / 150)})`;
    ctx.fillRect(0, 0, canvas.width, canvas.height);
  }
  if (!state.connected) return;

  drawMatch(nowMs);

  // HP-полоса своего игрока внизу слева.
  const bw = 200, bh = 14, bx = 16, by = canvas.height - 30;
  ctx.fillStyle = "#0e0f13";
  ctx.fillRect(bx, by, bw, bh);
  ctx.fillStyle = state.dead ? "#e0574d" : "#4ade80";
  ctx.fillRect(bx, by, (bw * Math.max(0, state.selfHp)) / 100, bh);
  ctx.strokeStyle = "#3a3f4d";
  ctx.strokeRect(bx, by, bw, bh);
  ctx.fillStyle = "#cdd3e0";
  ctx.font = "12px system-ui, sans-serif";
  ctx.fillText(`HP ${Math.max(0, state.selfHp)}`, bx + 6, by + 11);

  // Оверлей смерти.
  if (state.dead) {
    ctx.fillStyle = "rgba(0,0,0,0.45)";
    ctx.fillRect(0, canvas.height / 2 - 40, canvas.width, 80);
    ctx.textAlign = "center";
    ctx.fillStyle = "#e0574d";
    ctx.font = "bold 28px system-ui, sans-serif";
    ctx.fillText("YOU DIED", canvas.width / 2, canvas.height / 2 - 4);
    ctx.fillStyle = "#cdd3e0";
    ctx.font = "16px system-ui, sans-serif";
    ctx.fillText("respawning…", canvas.width / 2, canvas.height / 2 + 22);
    ctx.textAlign = "left";
  }
}

// fmtClock форматирует секунды в M:SS для таймера матча.
function fmtClock(sec) {
  sec = Math.max(0, Math.ceil(sec));
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

// drawMatch рисует таймер матча (сверху по центру), табло (сверху справа) и, в
// антракте, баннер победителя. Таймер отсчитывается локально от момента приёма
// последнего MatchState (recvMs), поэтому идёт плавно между редкими обновлениями.
function drawMatch(nowMs) {
  const m = state.match;
  if (!m) return;
  const elapsed = (nowMs - m.recvMs) / 1000;
  const remSec = m.remaining / INTERP.tickRate - elapsed;
  const intermission = m.phase === PROTO.MatchIntermission;

  // Таймер сверху по центру.
  ctx.textAlign = "center";
  ctx.font = "bold 22px system-ui, sans-serif";
  ctx.fillStyle = intermission ? "#ffd166" : "#cdd3e0";
  ctx.fillText(fmtClock(remSec), canvas.width / 2, 30);
  if (intermission) {
    ctx.font = "12px system-ui, sans-serif";
    ctx.fillStyle = "#8b93a7";
    ctx.fillText("next match", canvas.width / 2, 46);
  }
  ctx.textAlign = "left";

  // Табло сверху справа: имя, K/D. Своя строка подсвечена. До 8 строк. В командном
  // режиме сверху добавляется строка с суммарным счётом команд (синие vs красные).
  const rows = m.scores.slice(0, 8);
  const teamTotals = m.teamMode ? [0, 0] : null;
  if (teamTotals) for (const s of m.scores) teamTotals[s.team & 1] += s.kills;
  const pad = 8, lh = 18, w = 190;
  const extra = teamTotals ? 1 : 0; // строка командного счёта
  const h = pad * 2 + (rows.length + 1 + extra) * lh;
  const x = canvas.width - w - 12, y = 12;
  ctx.fillStyle = "rgba(14,15,19,0.72)";
  ctx.fillRect(x, y, w, h);
  ctx.strokeStyle = "#3a3f4d";
  ctx.strokeRect(x, y, w, h);
  ctx.font = "bold 12px system-ui, sans-serif";
  let top = y + pad + 12;
  if (teamTotals) {
    // Командный счёт: BLUE слева, RED справа, цветом команды.
    ctx.fillStyle = TEAM_COLORS[0];
    ctx.fillText(`BLUE ${teamTotals[0]}`, x + pad, top);
    ctx.textAlign = "right";
    ctx.fillStyle = TEAM_COLORS[1];
    ctx.fillText(`${teamTotals[1]} RED`, x + w - pad, top);
    ctx.textAlign = "left";
    top += lh;
  }
  ctx.fillStyle = "#8b93a7";
  ctx.fillText("PLAYER", x + pad, top);
  ctx.textAlign = "right";
  ctx.fillText("K / D", x + w - pad, top);
  ctx.textAlign = "left";
  ctx.font = "12px system-ui, sans-serif";
  for (let i = 0; i < rows.length; i++) {
    const r = rows[i];
    const ry = top + (i + 1) * lh;
    if (r.id === state.myID) {
      ctx.fillStyle = "rgba(43,108,255,0.25)";
      ctx.fillRect(x + 2, ry - 12, w - 4, lh);
    }
    // В командном режиме имя красится цветом команды; иначе своя строка голубая.
    ctx.fillStyle = m.teamMode
      ? TEAM_COLORS[r.team & 1]
      : r.id === state.myID ? "#9db4ff" : "#cdd3e0";
    const name = r.name.length > 14 ? r.name.slice(0, 13) + "…" : r.name;
    ctx.fillText(name, x + pad, ry);
    ctx.textAlign = "right";
    ctx.fillStyle = "#cdd3e0";
    ctx.fillText(`${r.kills} / ${r.deaths}`, x + w - pad, ry);
    ctx.textAlign = "left";
  }

  // Баннер победителя в антракте по центру экрана. В командном режиме winner — id
  // команды (0/1), поэтому баннер называет команду, а не игрока.
  if (intermission) {
    let label = "MATCH OVER", sub = "";
    if (m.teamMode && teamTotals) {
      const t = m.winner & 1;
      label = `${t === 0 ? "BLUE" : "RED"} TEAM WINS`;
      sub = `${teamTotals[t]} kills`;
    } else {
      const champ = m.scores.find((s) => s.id === m.winner);
      if (m.winner && champ) {
        label = `${champ.name} WINS`;
        sub = `${champ.kills} kills`;
      }
    }
    ctx.textAlign = "center";
    ctx.fillStyle = "rgba(0,0,0,0.5)";
    ctx.fillRect(0, canvas.height / 2 - 70, canvas.width, 84);
    ctx.fillStyle = m.teamMode ? TEAM_COLORS[m.winner & 1] : "#ffd166";
    ctx.font = "bold 34px system-ui, sans-serif";
    ctx.fillText(label, canvas.width / 2, canvas.height / 2 - 30);
    if (sub) {
      ctx.fillStyle = "#cdd3e0";
      ctx.font = "16px system-ui, sans-serif";
      ctx.fillText(sub, canvas.width / 2, canvas.height / 2);
    }
    ctx.textAlign = "left";
  }
}

setStatus("offline", false);
requestAnimationFrame(render);
