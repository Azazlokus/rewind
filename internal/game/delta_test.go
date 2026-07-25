package game

import (
	"testing"

	"arena/internal/protocol"
)

// deltaClient — минимальная реконструкция дельт для теста (зеркало bot/reconstruct
// и web/game.js): держит недавние базы, применяет полный/дельту, подтверждает тик.
type deltaClient struct {
	store map[uint32]map[uint16]protocol.Entity
	ack   uint32
}

func newDeltaClient() *deltaClient {
	return &deltaClient{store: make(map[uint32]map[uint16]protocol.Entity)}
}

func (d *deltaClient) apply(s protocol.Snapshot) (map[uint16]protocol.Entity, bool) {
	var next map[uint16]protocol.Entity
	if s.BaseTick == 0 {
		next = make(map[uint16]protocol.Entity)
	} else {
		base, ok := d.store[s.BaseTick]
		if !ok {
			return nil, false
		}
		next = make(map[uint16]protocol.Entity, len(base))
		for k, v := range base {
			next[k] = v
		}
	}
	for _, id := range s.Removed {
		delete(next, id)
	}
	for _, e := range s.Entities {
		next[e.ID] = e
	}
	d.store[s.Tick] = next
	d.ack = s.Tick
	return next, true
}

// TestDeltaReconstructionStaysConsistent прогоняет реальный путь рассылки: клиент
// подтверждает снапшоты, сервер кодирует дельты, клиент реконструирует. Проверяет
// три вещи: (1) сервер действительно шлёт дельты (BaseTick != 0); (2) набор всегда
// содержит ровно обоих игроков — то есть неизменная (стоящая) сущность переживает
// дельты, а движущаяся не теряется; (3) движущийся игрок едет вправо (нет
// рассинхрона, откатывающего позицию).
func TestDeltaReconstructionStaysConsistent(t *testing.T) {
	tr := newTestRoom(t, Config{Seed: 1}) // AOIRadius 0 — полный бродкаст + дельты
	mover := tr.join("mover")
	idle := tr.join("idle")

	dcMover := newDeltaClient()
	dcIdle := newDeltaClient()
	var seq uint32
	deltaSeen := false

	step := func() (moverSet, idleSet map[uint16]protocol.Entity) {
		seq++
		mover.sendInput(protocol.Input{Seq: seq, Buttons: protocol.BtnRight, AckTick: dcMover.ack})
		idle.sendInput(protocol.Input{Seq: seq, Buttons: 0, AckTick: dcIdle.ack})
		tr.tick(1)
		// Оба читаем каждый тик: иначе стоящего idle выкинет как отставшего.
		return reconcileRead(t, mover, dcMover, &deltaSeen), reconcileRead(t, idle, dcIdle, nil)
	}

	// Прогрев: первые снапшоты движущегося идут из очереди ещё до джойна idle (там
	// только он сам). Вычерпываем, пока обе реконструкции не увидят обоих игроков;
	// дельты при этом уже включаются (клиенты подтверждают тики).
	warm := 0
	for {
		m, i := step()
		if len(m) == 2 && len(i) == 2 {
			break
		}
		if warm++; warm > 500 {
			t.Fatal("both players never appeared in reconstruction")
		}
	}

	// Строгая фаза: набор обязан оставаться ровно {mover, idle}, а mover — ехать вправо.
	firstX := dcMover.store[dcMover.ack][uint16(mover.id)].X
	lastX := firstX
	for range 60 {
		m, _ := step()
		e, ok := m[uint16(mover.id)]
		if !ok {
			t.Fatalf("mover %d missing from its own reconstructed set", mover.id)
		}
		if _, ok := m[uint16(idle.id)]; !ok {
			t.Fatalf("idle %d dropped from reconstruction (unchanged entity lost)", idle.id)
		}
		if len(m) != 2 {
			t.Fatalf("reconstructed set has %d entities, want 2", len(m))
		}
		lastX = e.X
	}

	if !deltaSeen {
		t.Fatal("server never sent a delta (BaseTick != 0) — delta path not exercised")
	}
	if !(lastX > firstX) {
		t.Fatalf("mover did not advance right: first=%.2f last=%.2f", firstX, lastX)
	}
}

// reconcileRead читает один снапшот клиента и возвращает реконструированный полный
// набор (id -> сущность). Если deltaSeen не nil, отмечает получение дельты.
func reconcileRead(t *testing.T, c *client, dc *deltaClient, deltaSeen *bool) map[uint16]protocol.Entity {
	t.Helper()
	msg := c.read()
	if msg.Type != protocol.MsgSnapshot {
		return nil
	}
	if deltaSeen != nil && msg.Snapshot.BaseTick != 0 {
		*deltaSeen = true
	}
	set, ok := dc.apply(msg.Snapshot)
	if !ok {
		t.Fatalf("client %d could not reconstruct delta (base %d missing)", c.id, msg.Snapshot.BaseTick)
	}
	return set
}
