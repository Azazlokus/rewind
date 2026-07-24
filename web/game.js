"use strict";

// Клиент Arena — итерация 2 (интерполяция).
//
// Клиент держит буфер снапшотов и рендерит мир на INTERP.delay (100 мс) в
// прошлом, интерполируя позиции между двумя соседними снапшотами. Это скрывает и
// сниженную частоту снапшотов сервера (20 Гц), и сетевой джиттер: чужие игроки
// движутся плавно, без телепортов.
//
// Своего игрока пока тоже рендерим из прошлого (без предсказания) — мгновенный
// отклик появится в итерации 4. Всё, что обязано совпадать с сервером, собрано в
// PROTO / SIM / INTERP. Когда придёт бинарный кодек (итерация 3), поменяются
// только помощники encode/decode.

// ---- протокол (зеркало internal/protocol) ----------------------------------
const PROTO = {
  MsgInput: 0x01,
  MsgJoin: 0x02,
  MsgSnapshot: 0x10,
  MsgJoinAck: 0x11,
  // биты кнопок: 0..3 = WASD, 4 = fire
  BtnUp: 1 << 0,
  BtnLeft: 1 << 1,
  BtnDown: 1 << 2,
  BtnRight: 1 << 3,
  BtnFire: 1 << 4,
};

// ---- константы симуляции (зеркало internal/game) ---------------------------
const SIM = {
  MapSize: 4096,
  PlayerRadius: 16,
  PlayerSpeed: 300,
};

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
  ws: null,
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
  lastFrame: 0, // performance.now() предыдущего кадра, мс
};

function setStatus(text, ok) {
  els.status.textContent = "";
  const b = document.createElement("b");
  b.textContent = text;
  els.status.append("status ", b);
  els.status.dataset.ok = String(ok);
}

// ---- encode / decode (JSON-конверт для итерации 1) -------------------------
function encodeJoin(name) {
  return JSON.stringify({ t: PROTO.MsgJoin, d: { n: name } });
}

function encodeInput(seq, buttons, aim) {
  const aimQ = Math.round((aim / (2 * Math.PI)) * 65536) & 0xffff;
  return JSON.stringify({ t: PROTO.MsgInput, d: { s: seq, b: buttons, a: aimQ } });
}

function decodeServer(data) {
  const env = JSON.parse(data);
  return { type: env.t, d: env.d };
}

// ---- соединение ------------------------------------------------------------
function connect() {
  if (state.ws) return;
  const name = (els.name.value || "player").slice(0, 16);
  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
  state.ws = ws;
  setStatus("connecting", false);
  els.connect.disabled = true;

  ws.onopen = () => ws.send(encodeJoin(name));

  ws.onmessage = (ev) => {
    let msg;
    try {
      msg = decodeServer(ev.data);
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
      pushSnapshot(msg.d);
    }
  };

  ws.onclose = () => teardown("offline");
  ws.onerror = () => teardown("error");
}

function teardown(reason) {
  stopInput();
  state.connected = false;
  state.ws = null;
  state.myID = 0;
  state.buffer = [];
  state.playback = null;
  setStatus(reason, false);
  els.connect.disabled = false;
  els.me.textContent = "–";
}

// ---- буфер снапшотов -------------------------------------------------------
function pushSnapshot(snap) {
  const serverTime = snap.t / INTERP.tickRate;
  const buf = state.buffer;
  // Игнорируем переупорядоченные и дублирующиеся тики.
  if (buf.length && serverTime <= buf[buf.length - 1].serverTime) return;

  const ents = new Map();
  for (const e of snap.e) ents.set(e.i, e);
  buf.push({ serverTime, tick: snap.t, ents });

  // Подрезаем историю, глубже которой playback уже не заглянет.
  const cutoff = serverTime - INTERP.history;
  while (buf.length > 2 && buf[0].serverTime < cutoff) buf.shift();
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
    if (!state.connected || state.ws.readyState !== WebSocket.OPEN) return;
    state.seq = (state.seq + 1) >>> 0;
    state.ws.send(encodeInput(state.seq, buttonsFromKeys(), state.aim));
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

  ctx.fillStyle = "#16181f";
  ctx.fillRect(0, 0, canvas.width, canvas.height);

  const frame = sampleWorld(nowMs);
  if (frame) {
    els.tick.textContent = String(frame.tick);
    els.players.textContent = String(frame.players);
  }

  // Камера центрируется на нашем (интерполированном) игроке; пока его нет —
  // смотрим в центр карты.
  const self = findSelf(frame);
  const camX = self ? self.x : SIM.MapSize / 2;
  const camY = self ? self.y : SIM.MapSize / 2;
  const ox = canvas.width / 2 - camX;
  const oy = canvas.height / 2 - camY;

  drawGrid(ox, oy);

  if (frame) {
    if (self) {
      const sx = self.x + ox;
      const sy = self.y + oy;
      state.aim = Math.atan2(state.mouse.y - sy, state.mouse.x - sx);
    }
    for (const e of frame.entities) drawEntity(e, ox, oy, e.i === state.myID);
  }
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

function drawEntity(e, ox, oy, isSelf) {
  const x = e.x + ox;
  const y = e.y + oy;
  if (x < -32 || x > canvas.width + 32 || y < -32 || y > canvas.height + 32) return;

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

setStatus("offline", false);
requestAnimationFrame(render);
