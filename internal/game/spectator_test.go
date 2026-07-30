package game

import (
	"testing"

	"arena/internal/protocol"
)

// TestSpectatorObservesWithoutJoiningSim: наблюдатель (итер. 22) получает снапшоты с
// сущностями игрока, но сам не входит в мир (не считается игроком, нет Player).
func TestSpectatorObservesWithoutJoiningSim(t *testing.T) {
	tr := newTestRoom(t, Config{})

	// Обычный игрок двигается, чтобы попадать в снапшоты.
	p := tr.join("mover")
	spec := tr.joinSpectator("watcher")

	if got := tr.room.Players(); got != 1 {
		t.Fatalf("Players() = %d, want 1 (spectator must not count)", got)
	}
	if got := tr.room.Spectators(); got != 1 {
		t.Fatalf("Spectators() = %d, want 1", got)
	}

	// Двигаем игрока, чтобы у наблюдателя точно шли снапшоты с его сущностью.
	pid := p.id
	for i := 0; i < 30; i++ {
		tr.room.input(tr.ctx, pid, protocol.Input{Seq: uint32(i + 1), Buttons: protocol.BtnRight})
	}

	// Наблюдатель должен увидеть снапшот, содержащий игрока pid.
	snap := tr.tickUntil(spec, func(s protocol.Snapshot) bool {
		for _, e := range s.Entities {
			if PlayerID(e.ID) == pid && e.Kind == protocol.KindPlayer {
				return true
			}
		}
		return false
	})
	if len(snap.Entities) == 0 {
		t.Fatal("spectator snapshot had no entities")
	}
}

// TestSpectatorInputIgnored: ввод от наблюдателя не создаёт игрока и не трогает мир.
func TestSpectatorInputIgnored(t *testing.T) {
	tr := newTestRoom(t, Config{})
	spec := tr.joinSpectator("watcher")

	// Шлём «ввод» от имени id наблюдателя напрямую в комнату (как сделал бы враждебный
	// клиент). Сессия наблюдателя его бы отбросила; проверяем и путь через World.
	before := tr.room.Players()
	for i := 0; i < 10; i++ {
		tr.room.input(tr.ctx, spec.id, protocol.Input{Seq: uint32(i + 1), Buttons: protocol.BtnFire})
	}
	tr.tick(5)
	if got := tr.room.Players(); got != before {
		t.Fatalf("spectator input changed player count: %d -> %d", before, got)
	}
	if tr.room.world.Player(spec.id) != nil {
		t.Fatal("spectator must not have a Player in the world")
	}
}

// TestSpectatorReceivesReliableEvents: наблюдатель получает reliable-состояние (табло
// матча / пикапы) — комната рассылает их всем, включая наблюдателей.
func TestSpectatorReceivesReliableEvents(t *testing.T) {
	tr := newTestRoom(t, Config{})
	_ = tr.join("mover") // хоть один игрок — иначе табло пустое, но событие всё равно шлётся
	spec := tr.joinSpectator("watcher")

	// В течение нескольких тиков наблюдатель должен получить и MatchState, и PickupState
	// (взводятся dirty при входе наблюдателя).
	gotMatch, gotPickup := false, false
	for i := 0; i < 80 && (!gotMatch || !gotPickup); i++ {
		tr.tick(1)
		msg := spec.read()
		switch msg.Type {
		case protocol.MsgMatchState:
			gotMatch = true
		case protocol.MsgPickupState:
			gotPickup = true
		}
	}
	if !gotMatch {
		t.Fatal("spectator did not receive MsgMatchState")
	}
	if !gotPickup {
		t.Fatal("spectator did not receive MsgPickupState")
	}
}

// TestSpectatorLeaveDoesNotTouchPlayers: уход наблюдателя не трогает игроков и
// корректно уменьшает счётчик наблюдателей.
func TestSpectatorLeaveDoesNotTouchPlayers(t *testing.T) {
	tr := newTestRoom(t, Config{})
	_ = tr.join("mover")
	spec := tr.joinSpectator("watcher")
	if tr.room.Spectators() != 1 {
		t.Fatalf("Spectators() = %d, want 1", tr.room.Spectators())
	}

	// Наблюдатель уходит (закрываем его конец) — комната должна снять его через leave.
	_ = spec.conn.Close("bye")
	drained := false
	for i := 0; i < 300 && !drained; i++ {
		tr.tick(1)
		drained = tr.room.Spectators() == 0
	}
	if !drained {
		t.Fatal("spectator not drained after leave")
	}
	if got := tr.room.Players(); got != 1 {
		t.Fatalf("player count changed by spectator leave: %d, want 1", got)
	}
}
