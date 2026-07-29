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
  // фазы матча (зеркало game.matchPhase)
  MatchActive: 0,
  MatchIntermission: 1,
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
  status: document.getElementById("status"),
  tick: document.getElementById("tick"),
  players: document.getElementById("players"),
  me: document.getElementById("me"),
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
};

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

// ---- encode / decode (бинарный протокол, little-endian) --------------------
// encodeJoin зеркалит protocol.AppendJoin:
// [1B type][1B nameLen][name][2B tokenLen][token] (little-endian).
// token — токен-сессия из бэкенда (register/login/guest); пусто → гость.
// UI логина появится в итерации 15; пока токен берётся из localStorage, если есть.
function encodeJoin(name, token) {
  let nameBytes = new TextEncoder().encode(name);
  if (nameBytes.length > 16) nameBytes = nameBytes.slice(0, 16);
  let tokenBytes = new TextEncoder().encode(token || "");
  if (tokenBytes.length > 512) tokenBytes = tokenBytes.slice(0, 512);
  const buf = new Uint8Array(2 + nameBytes.length + 2 + tokenBytes.length);
  const view = new DataView(buf.buffer);
  buf[0] = PROTO.MsgJoin;
  buf[1] = nameBytes.length;
  buf.set(nameBytes, 2);
  view.setUint16(2 + nameBytes.length, tokenBytes.length, true);
  buf.set(tokenBytes, 2 + nameBytes.length + 2);
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
    // [1B phase][4B remaining][2B winner][1B count] count× табло:
    // [2B id][2B kills][2B deaths][1B nameLen][name UTF-8]. Зеркало
    // protocol.AppendMatchState / game.MatchState.
    const phase = dv.getUint8(1);
    const remaining = dv.getUint32(2, true);
    const winner = dv.getUint16(6, true);
    const count = dv.getUint8(8);
    const scores = [];
    let off = 9;
    for (let j = 0; j < count; j++) {
      const id = dv.getUint16(off, true);
      const kills = dv.getUint16(off + 2, true);
      const deaths = dv.getUint16(off + 4, true);
      const nlen = dv.getUint8(off + 6);
      off += 7;
      const name = TEXT_DECODER.decode(new Uint8Array(data, off, nlen));
      off += nlen;
      scores.push({ id, name, kills, deaths });
    }
    return { type, d: { phase, remaining, winner, scores } };
  }
  return { type, d: null };
}

// ---- соединение ------------------------------------------------------------
// Транспорт выбирается явно: по умолчанию WebSocket (/ws), а ?transport=webrtc
// включает WebRTC DataChannel (итерация 11). Фолбэка нет — как выбрали, так и
// подключаемся. Игровой код одинаков: обе стороны шлют/принимают одни и те же
// бинарные кадры через абстракцию state.link.
function connect() {
  if (state.link || state.connecting) return;
  state.connecting = true;
  const name = (els.name.value || "player").slice(0, 16);
  // Токен-сессия из бэкенда, если игрок уже вошёл (UI логина — итерация 15).
  const token = localStorage.getItem("arena_token") || "";
  setStatus("connecting", false);
  els.connect.disabled = true;
  const useRTC = new URLSearchParams(location.search).get("transport") === "webrtc";
  if (useRTC) connectWebRTC(name, token);
  else connectWS(name, token);
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
    setStatus("online", true);
    els.me.textContent = String(state.myID);
    startInput();
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
  }
}

// connectWS — путь WebSocket: соединение и есть игровой транспорт.
function connectWS(name, token) {
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
    ws.send(encodeJoin(name, token));
  };
  ws.onmessage = (ev) => handleServerData(ev.data);
  ws.onclose = () => teardown("offline");
  ws.onerror = () => teardown("error");
}

// connectWebRTC — путь WebRTC: WS /rtc несёт только сигналинг (config/offer/answer),
// игра идёт по DataChannel "game". Зеркалит серверный transport.AcceptWebRTC:
// клиент — offerer, ICE non-trickle (ждём завершения сбора кандидатов, затем шлём
// offer с ними в SDP). Сигналинг закрываем сразу после открытия канала.
function connectWebRTC(name, token) {
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
          dc.send(encodeJoin(name, token));
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
  setStatus(reason, false);
  els.connect.disabled = false;
  els.me.textContent = "–";
}

// ---- reliable-события боя ---------------------------------------------------
// onSpawn: наш (пере)спавн — сбрасываем предсказание на авторитетную точку. Чужой
// спавн игнорируем: он подхватится ближайшим снапшотом.
function onSpawn(d) {
  if (d.i !== state.myID) return;
  state.dead = false;
  state.selfHp = 100;
  state.pred = { x: d.x, y: d.y, vx: 0, vy: 0 };
  state.pending = [];
  state.smoothErr = { x: 0, y: 0 };
  state.predReady = true;
}

// onDeath: наша смерть — прекращаем предсказание (нас нет в снапшотах) и чистим
// очередь неподтверждённых вводов, чтобы она не росла, пока мы мертвы.
function onDeath(d) {
  if (d.v !== state.myID) return;
  state.dead = true;
  state.selfHp = 0;
  state.predReady = false;
  state.pending = [];
  state.smoothErr = { x: 0, y: 0 };
}

// onHit: попадание по нам — обновляем HP и заводим краткую вспышку.
function onHit(d) {
  if (d.v === state.myID) {
    state.selfHp = d.hp;
    state.flashMs = performance.now();
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
els.connect.addEventListener("click", connect);

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
  // интерполированном; иначе — центр карты.
  const self = findSelf(frame);
  // Пока мертвы, держим камеру на месте гибели (state.pred не сбрасывается до
  // респауна), а не прыгаем в центр карты.
  const camX = selfPred ? selfPred.x : self ? self.x : state.dead ? state.pred.x : SIM.MapSize / 2;
  const camY = selfPred ? selfPred.y : self ? self.y : state.dead ? state.pred.y : SIM.MapSize / 2;
  const ox = canvas.width / 2 - camX;
  const oy = canvas.height / 2 - camY;

  drawGrid(ox, oy);
  drawWalls(ox, oy);

  if (frame) {
    if (self) {
      const sx = self.x + ox;
      const sy = self.y + oy;
      state.aim = Math.atan2(state.mouse.y - sy, state.mouse.x - sx);
    }
    for (const e of frame.entities) drawEntity(e, ox, oy, e.i === state.myID);
  }

  drawHud(nowMs);
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
  ctx.fillStyle = isSelf ? "#2b6cff" : "#e0574d";
  ctx.fill();

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

  // Табло сверху справа: имя, K/D. Своя строка подсвечена. До 8 строк.
  const rows = m.scores.slice(0, 8);
  const pad = 8, lh = 18, w = 190;
  const h = pad * 2 + (rows.length + 1) * lh;
  const x = canvas.width - w - 12, y = 12;
  ctx.fillStyle = "rgba(14,15,19,0.72)";
  ctx.fillRect(x, y, w, h);
  ctx.strokeStyle = "#3a3f4d";
  ctx.strokeRect(x, y, w, h);
  ctx.font = "bold 12px system-ui, sans-serif";
  ctx.fillStyle = "#8b93a7";
  ctx.fillText("PLAYER", x + pad, y + pad + 12);
  ctx.textAlign = "right";
  ctx.fillText("K / D", x + w - pad, y + pad + 12);
  ctx.textAlign = "left";
  ctx.font = "12px system-ui, sans-serif";
  for (let i = 0; i < rows.length; i++) {
    const r = rows[i];
    const ry = y + pad + (i + 2) * lh - 4;
    if (r.id === state.myID) {
      ctx.fillStyle = "rgba(43,108,255,0.25)";
      ctx.fillRect(x + 2, ry - 12, w - 4, lh);
    }
    ctx.fillStyle = r.id === state.myID ? "#9db4ff" : "#cdd3e0";
    const name = r.name.length > 14 ? r.name.slice(0, 13) + "…" : r.name;
    ctx.fillText(name, x + pad, ry);
    ctx.textAlign = "right";
    ctx.fillText(`${r.kills} / ${r.deaths}`, x + w - pad, ry);
    ctx.textAlign = "left";
  }

  // Баннер победителя в антракте по центру экрана.
  if (intermission) {
    const champ = m.scores.find((s) => s.id === m.winner);
    const label = m.winner && champ ? `${champ.name} WINS` : "MATCH OVER";
    ctx.textAlign = "center";
    ctx.fillStyle = "rgba(0,0,0,0.5)";
    ctx.fillRect(0, canvas.height / 2 - 70, canvas.width, 84);
    ctx.fillStyle = "#ffd166";
    ctx.font = "bold 34px system-ui, sans-serif";
    ctx.fillText(label, canvas.width / 2, canvas.height / 2 - 30);
    if (champ) {
      ctx.fillStyle = "#cdd3e0";
      ctx.font = "16px system-ui, sans-serif";
      ctx.fillText(`${champ.kills} kills`, canvas.width / 2, canvas.height / 2);
    }
    ctx.textAlign = "left";
  }
}

setStatus("offline", false);
requestAnimationFrame(render);
