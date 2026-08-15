package game

import (
	"context"
	"errors"
	"testing"
	"time"

	"arena/internal/protocol"
	"arena/internal/transport"
)

// testRoom — headless-комната: настоящие сессии поверх in-memory пайпов,
// управляемые ручными часами. Без сети, без sleep'ов, полностью детерминированно.
type testRoom struct {
	t        *testing.T
	room     *Room
	clock    *ManualClock
	interval time.Duration
	cancel   context.CancelFunc
	ctx      context.Context
}

func newTestRoom(t *testing.T, cfg Config) *testRoom {
	t.Helper()
	clock := NewManualClock(time.Time{})
	cfg.Clock = clock
	if cfg.TickRate == 0 {
		cfg.TickRate = 30
	}
	room := NewRoom("test", cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go room.Run(ctx)
	<-room.Ready() // ждём регистрации тикера, иначе первые Advance потеряются

	tr := &testRoom{
		t: t, room: room, clock: clock,
		interval: room.cfg.TickInterval(), cancel: cancel, ctx: ctx,
	}
	t.Cleanup(tr.stop)
	return tr
}

func (tr *testRoom) stop() {
	tr.cancel()
	select {
	case <-tr.room.Done():
	case <-time.After(2 * time.Second):
		tr.t.Fatal("room did not stop within 2s")
	}
}

// tick продвигает симуляцию на n тиков.
func (tr *testRoom) tick(n int) {
	tr.clock.AdvanceTicks(n, tr.interval)
}

// joinRaw постит событие join прямо в inbox и шагает часами, пока комната не
// заполнит канал ответа. Опрос идёт в основной горутине по конкретному
// req.reply, который комната заполняет синхронно на тике обработки join. Поэтому
// лишних тиков максимум один — критично, чтобы между созданием сессии и запуском
// её write pump не накапливались снапшоты (иначе reliable-JoinAck вытеснится).
//
// Тест в этом же пакете, поэтому обходится без публичного Join с его горутиной:
// та добавляла лишний хоп (room -> reply -> горутина -> канал), в котором часы
// успевали убежать вперёд.
func (tr *testRoom) joinRaw(conn transport.Conn, name string) joinResult {
	return tr.joinRawSpec(conn, name, false)
}

// joinRawSpec — joinRaw с флагом наблюдателя (итер. 22).
func (tr *testRoom) joinRawSpec(conn transport.Conn, name string, spectator bool) joinResult {
	tr.t.Helper()
	req := &joinReq{conn: conn, name: name, spectator: spectator, reply: make(chan joinResult, 1)}
	tr.room.inbox <- event{kind: evJoin, join: req}
	for range 10000 {
		select {
		case res := <-req.reply:
			return res
		default:
			tr.clock.Advance(tr.interval)
		}
	}
	tr.t.Fatal("joinRaw: reply never arrived")
	return joinResult{}
}

// tickUntil продвигает симуляцию по одному тику и читает по одному снапшоту,
// возвращая первый, удовлетворяющий pred. Пейсинг строго 1:1: при дефолтной
// частоте снапшотов (== тикрейт) каждый тик даёт ровно один снапшот, а
// блокирующее чтение ждёт его. Так читатель не отстаёт от часов, очередь клиента
// не переполняется и сессию не выкидывают посреди чтения. Отставание чтения от
// мира на пару тиков не мешает: искомое состояние, наступив, держится.
func (tr *testRoom) tickUntil(c *client, pred func(protocol.Snapshot) bool) protocol.Snapshot {
	tr.t.Helper()
	for range 5000 {
		tr.tick(1)
		msg := c.read()
		if msg.Type == protocol.MsgSnapshot && pred(msg.Snapshot) {
			return msg.Snapshot
		}
	}
	tr.t.Fatal("tickUntil: predicate never satisfied")
	return protocol.Snapshot{}
}

// client — клиентский конец присоединившейся сессии.
type client struct {
	t    *testing.T
	conn transport.Conn
	ctx  context.Context
	id   PlayerID
}

// join подключает нового клиента и завершает рукопожатие, возвращая клиентский
// конец. Pump'ы сессии крутятся в фоне всё время жизни теста.
func (tr *testRoom) join(name string) *client {
	tr.t.Helper()
	server, clientConn := transport.Pipe(64)
	res := tr.joinRaw(server, name)
	if res.err != nil {
		tr.t.Fatalf("join %q: %v", name, res.err)
	}
	sess := res.sess
	go func() { _ = sess.Run(tr.ctx) }()

	c := &client{t: tr.t, conn: clientConn, ctx: tr.ctx, id: sess.ID()}
	// Первое серверное сообщение — JoinAck.
	msg := c.read()
	if msg.Type != protocol.MsgJoinAck {
		tr.t.Fatalf("first message was %v, want JoinAck", msg.Type)
	}
	if PlayerID(msg.JoinAck.YourID) != sess.ID() {
		tr.t.Fatalf("ack id %d != session id %d", msg.JoinAck.YourID, sess.ID())
	}
	return c
}

// joinSpectator подключает наблюдателя (итер. 22) и проверяет, что JoinAck несёт
// YourID == 0 (сигнал «своей сущности нет»).
func (tr *testRoom) joinSpectator(name string) *client {
	tr.t.Helper()
	server, clientConn := transport.Pipe(64)
	res := tr.joinRawSpec(server, name, true)
	if res.err != nil {
		tr.t.Fatalf("spectator join %q: %v", name, res.err)
	}
	sess := res.sess
	go func() { _ = sess.Run(tr.ctx) }()

	c := &client{t: tr.t, conn: clientConn, ctx: tr.ctx, id: sess.ID()}
	msg := c.read()
	if msg.Type != protocol.MsgJoinAck {
		tr.t.Fatalf("spectator first message was %v, want JoinAck", msg.Type)
	}
	if msg.JoinAck.YourID != 0 {
		tr.t.Fatalf("spectator YourID = %d, want 0 (sentinel)", msg.JoinAck.YourID)
	}
	return c
}

func (c *client) read() protocol.ServerMessage {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	data, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("client %d read: %v", c.id, err)
	}
	var msg protocol.ServerMessage
	if err := protocol.DecodeServer(data, &msg); err != nil {
		c.t.Fatalf("decode server message: %v", err)
	}
	return msg
}

// TestRoomBroadcastsPickupStateOnJoin: вошедшему клиенту комната шлёт reliable
// MsgPickupState с текущим состоянием точек (итерация 19). К моменту входа пикапы
// уже заспавнены (тик 0), поэтому активны все точки, в порядке индекса. Чтение
// 1:1 с тиком — как в tickUntil, чтобы очередь клиента не переполнилась.
func TestRoomBroadcastsPickupStateOnJoin(t *testing.T) {
	tr := newTestRoom(t, Config{})
	c := tr.join("p")
	for range 60 {
		tr.tick(1)
		msg := c.read()
		if msg.Type != protocol.MsgPickupState {
			continue
		}
		got := msg.PickupState.Active
		if len(got) != len(pickupSpots) {
			t.Fatalf("pickupstate: got %d active, want %d (all spots spawned)", len(got), len(pickupSpots))
		}
		for i, pk := range got {
			if int(pk.Spot) != i {
				t.Fatalf("pickupstate[%d]: spot %d, want %d", i, pk.Spot, i)
			}
			if pk.Kind < uint8(pickupMedkit) || pk.Kind > uint8(pickupSpread) {
				t.Fatalf("pickupstate[%d]: invalid kind %d", i, pk.Kind)
			}
		}
		return // получили и проверили
	}
	t.Fatal("no MsgPickupState received after join")
}

// TestRoomBroadcastsFlagStateOnJoin: в режиме CTF новичок получает MsgFlagState с
// обоими флагами на базах (итер. 31).
func TestRoomBroadcastsFlagStateOnJoin(t *testing.T) {
	tr := newTestRoom(t, Config{CtfMode: true})
	c := tr.join("p")
	for range 60 {
		tr.tick(1)
		msg := c.read()
		if msg.Type != protocol.MsgFlagState {
			continue
		}
		got := msg.FlagState.Flags
		if len(got) != 2 {
			t.Fatalf("flagstate: got %d flags, want 2", len(got))
		}
		for i, f := range got {
			if int(f.Team) != i {
				t.Fatalf("flagstate[%d]: team %d, want %d", i, f.Team, i)
			}
			if f.Status != uint8(flagAtBase) {
				t.Fatalf("flagstate[%d]: status %d, want atBase (0)", i, f.Status)
			}
		}
		return // получили и проверили
	}
	t.Fatal("no MsgFlagState received after join in CTF mode")
}

// TestRoomBroadcastsWeaponStateOnJoinAndSwitch: новичок получает MsgWeaponState со
// своим стартовым пистолетом, а смена оружия вводом рассылается обновлением (итер. 26).
func TestRoomBroadcastsWeaponStateOnJoinAndSwitch(t *testing.T) {
	tr := newTestRoom(t, Config{})
	c := tr.join("p")

	// 1) После входа приходит состояние оружия: игрок на пистолете (1).
	gotStart := false
	for range 30 {
		tr.tick(1)
		msg := c.read()
		if msg.Type != protocol.MsgWeaponState {
			continue
		}
		if len(msg.WeaponState.Weapons) != 1 || msg.WeaponState.Weapons[0].Weapon != uint8(weaponPistol) {
			t.Fatalf("join weaponstate: got %+v, want one pistol", msg.WeaponState.Weapons)
		}
		gotStart = true
		break
	}
	if !gotStart {
		t.Fatal("no MsgWeaponState received after join")
	}

	// 2) Клиент выбирает ракету (старшие биты Buttons) — приходит обновление.
	c.sendInput(protocol.Input{Seq: 1, Buttons: uint8(weaponRocket) << 5})
	for range 30 {
		tr.tick(1)
		msg := c.read()
		if msg.Type != protocol.MsgWeaponState {
			continue
		}
		if len(msg.WeaponState.Weapons) == 1 && msg.WeaponState.Weapons[0].Weapon == uint8(weaponRocket) {
			return // получили обновлённое оружие
		}
	}
	t.Fatal("weapon switch was not broadcast")
}

func (c *client) send(msg []byte) {
	c.t.Helper()
	if err := c.conn.Write(c.ctx, msg); err != nil {
		c.t.Fatalf("client %d write: %v", c.id, err)
	}
}

func (c *client) sendInput(in protocol.Input) {
	buf, err := protocol.AppendInput(nil, in)
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(buf)
}

// TestRoomJoinLeave проверяет, что игроки появляются в мире и исчезают из него
// через inbox, без прямого доступа к миру.
func TestRoomJoinLeave(t *testing.T) {
	tr := newTestRoom(t, Config{})
	a := tr.join("alice")
	b := tr.join("bob")

	// Оба игрока видны.
	tr.tickUntil(b, func(s protocol.Snapshot) bool { return len(s.Entities) == 2 })

	// Alice отключается; комната обязана её освободить.
	if err := a.conn.Close("bye"); err != nil {
		t.Fatal(err)
	}
	snap := tr.tickUntil(b, func(s protocol.Snapshot) bool { return len(s.Entities) == 1 })
	if PlayerID(snap.Entities[0].ID) != b.id {
		t.Fatalf("remaining entity is %d, want bob %d", snap.Entities[0].ID, b.id)
	}
	if tr.room.Players() != 1 {
		t.Fatalf("room reports %d players, want 1", tr.room.Players())
	}
}

// TestRoomInputMovesPlayer проверяет, что ввод течёт inbox -> мир -> снапшот и
// что подтверждение продвигается.
func TestRoomInputMovesPlayer(t *testing.T) {
	tr := newTestRoom(t, Config{})
	c := tr.join("mover")

	before := tr.tickUntil(c, func(s protocol.Snapshot) bool { return hasEntity(s, c.id) })
	startX := entityByID(t, before, c.id).X

	// Мир применяет свежайший ввод каждый тик, поэтому одной зажатой команды
	// «вправо» достаточно, чтобы увидеть движение и подтверждение.
	c.sendInput(protocol.Input{Seq: 7, Buttons: protocol.BtnRight})

	after := tr.tickUntil(c, func(s protocol.Snapshot) bool {
		e, ok := lookup(s, c.id)
		return ok && s.LastProcessedSeq >= 7 && e.X > startX
	})
	e := entityByID(t, after, c.id)
	if e.VX <= 0 {
		t.Fatalf("expected positive VX while moving right, got %.2f", e.VX)
	}
}

// TestRoomDropsSlowClient проверяет, что цикл отключает клиента, который никогда
// не читает свои снапшоты, вместо того чтобы блокироваться на нём.
func TestRoomDropsSlowClient(t *testing.T) {
	tr := newTestRoom(t, Config{SessionQueue: 2, MaxBacklog: 3})

	// Клиент, который подключается, но никогда не читает: его пайп заполняется,
	// write pump вклинивается, его исходящая очередь заполняется, и комната
	// обязана махнуть на него рукой.
	server, _ := transport.Pipe(1)
	res := tr.joinRaw(server, "slowpoke")
	if res.err != nil {
		t.Fatalf("join: %v", res.err)
	}
	go func() { _ = res.sess.Run(tr.ctx) }()
	waitForPlayers(t, tr, 1)

	// Достаточно тиков, чтобы переполнить SessionQueue + MaxBacklog многократно.
	tr.tick(40)
	waitForPlayers(t, tr, 0)
}

// TestRoomFull проверяет, что вместимость соблюдается.
func TestRoomFull(t *testing.T) {
	tr := newTestRoom(t, Config{MaxPlayers: 1})
	tr.join("first")

	server, _ := transport.Pipe(8)
	res := tr.joinRaw(server, "second")
	if !errors.Is(res.err, ErrRoomFull) {
		t.Fatalf("expected ErrRoomFull, got %v", res.err)
	}
}

// TestGracefulShutdown проверяет, что отмена контекста останавливает комнату
// после текущего тика и освобождает сессии.
func TestGracefulShutdown(t *testing.T) {
	tr := newTestRoom(t, Config{})
	tr.join("a")
	tr.join("b")
	waitForPlayers(t, tr, 2)

	tr.cancel()
	select {
	case <-tr.room.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("room did not stop after context cancel")
	}
	if tr.room.Players() != 0 {
		t.Fatalf("players remained after shutdown: %d", tr.room.Players())
	}
}

// TestRateDivider проверяет делитель частоты снапшотов: над любым окном он даёт
// SnapshotRate тиков-«да» на каждые TickRate, равномерно распределённых.
func TestRateDivider(t *testing.T) {
	cases := []struct{ num, den, ticks, want int }{
		{20, 30, 30, 20}, // итерация 2: 20 Гц из 30
		{20, 30, 300, 200},
		{30, 30, 30, 30}, // каждый тик
		{10, 30, 30, 10}, // каждый третий
		{1, 30, 30, 1},
	}
	for _, c := range cases {
		d := rateDivider{num: c.num, den: c.den}
		got := 0
		for range c.ticks {
			if d.tick() {
				got++
			}
		}
		if got != c.want {
			t.Errorf("%d/%d over %d ticks: got %d snapshots, want %d",
				c.num, c.den, c.ticks, got, c.want)
		}
	}
}

// TestRateDividerEvenSpacing проверяет, что снапшоты 20/30 не идут пачкой:
// разрыв между соседними тиками-«да» не больше двух.
func TestRateDividerEvenSpacing(t *testing.T) {
	d := rateDivider{num: 20, den: 30}
	last := -1
	maxGap := 0
	for tick := range 90 {
		if d.tick() {
			if last >= 0 && tick-last > maxGap {
				maxGap = tick - last
			}
			last = tick
		}
	}
	if maxGap > 2 {
		t.Fatalf("snapshots bunched: max gap %d ticks, want <= 2", maxGap)
	}
}

// --- вспомогательное ---------------------------------------------------------

// waitForPlayers продвигает тики, пока комната не сообщит want игроков.
// Продвижение часов уступает горутинам read pump, поэтому событие leave от
// только что закрытого соединения обрабатывается без sleep.
func waitForPlayers(t *testing.T, tr *testRoom, want int) {
	t.Helper()
	for range 500 {
		if tr.room.Players() == want {
			return
		}
		tr.tick(1)
	}
	t.Fatalf("player count did not reach %d (still %d)", want, tr.room.Players())
}

func lookup(s protocol.Snapshot, id PlayerID) (protocol.Entity, bool) {
	for _, e := range s.Entities {
		if PlayerID(e.ID) == id {
			return e, true
		}
	}
	return protocol.Entity{}, false
}

func hasEntity(s protocol.Snapshot, id PlayerID) bool {
	_, ok := lookup(s, id)
	return ok
}

func entityByID(t *testing.T, s protocol.Snapshot, id PlayerID) protocol.Entity {
	t.Helper()
	e, ok := lookup(s, id)
	if !ok {
		t.Fatalf("entity %d not in snapshot", id)
	}
	return e
}
