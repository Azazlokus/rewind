package game

import (
	"testing"

	"arena/internal/protocol"
)

// ctfWorld строит мир в режиме CTF (teamMode обязателен) с n игроками. Команды
// раздаются балансом: p0→team0, p1→team1, p2→team0, …
func ctfWorld(t *testing.T, n int) (*World, []*Player) {
	t.Helper()
	w := NewWorld(1)
	w.SetTeamMode(true)
	w.SetCtfMode(true)
	ps := make([]*Player, 0, n)
	for range n {
		p, err := w.AddPlayer("p")
		if err != nil {
			t.Fatal(err)
		}
		ps = append(ps, p)
	}
	return w, ps
}

// TestCTFPickupEnemyFlag: игрок вражеской команды подбирает флаг касанием на базе.
func TestCTFPickupEnemyFlag(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	enemy := ps[1] // team 1
	if enemy.team != 1 {
		t.Fatalf("p1 team=%d, want 1", enemy.team)
	}
	place(enemy, flagBases[0].x, flagBases[0].y) // на базе команды 0
	w.Step(dt)
	if w.flags[0].status != flagCarried || w.flags[0].carrier != enemy.ID {
		t.Fatalf("flag0 status=%d carrier=%d, want carried by %d", w.flags[0].status, w.flags[0].carrier, enemy.ID)
	}
}

// TestCTFOwnFlagNotPicked: игрок своей команды не подбирает свой флаг на базе.
func TestCTFOwnFlagNotPicked(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	own := ps[0] // team 0
	place(own, flagBases[0].x, flagBases[0].y)
	w.Step(dt)
	if w.flags[0].status != flagAtBase {
		t.Fatalf("own flag status=%d, want atBase (0)", w.flags[0].status)
	}
}

// TestCTFCarryFollowsCarrier: несомый флаг следует за носителем.
func TestCTFCarryFollowsCarrier(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	enemy := ps[1]
	w.flags[0] = flagState{status: flagCarried, carrier: enemy.ID}
	place(enemy, 1234, 2345)
	w.Step(dt)
	if w.flags[0].x != enemy.X || w.flags[0].y != enemy.Y {
		t.Fatalf("carried flag at (%g,%g), want carrier (%g,%g)", w.flags[0].x, w.flags[0].y, enemy.X, enemy.Y)
	}
}

// TestCTFCapture: носитель вражеского флага у своей базы при своём флаге дома —
// захват: вражеский флаг домой, +1 к Captures, событие EventCapture.
func TestCTFCapture(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	carrier := ps[1] // team 1, несёт флаг команды 0
	w.flags[0] = flagState{status: flagCarried, carrier: carrier.ID}
	place(carrier, flagBases[1].x, flagBases[1].y) // у своей (team 1) базы
	w.Step(dt)
	if carrier.Captures != 1 {
		t.Fatalf("Captures=%d after capture, want 1", carrier.Captures)
	}
	if w.flags[0].status != flagAtBase {
		t.Fatalf("captured flag status=%d, want returned to base", w.flags[0].status)
	}
	var sawEvent bool
	for _, e := range w.Events() {
		if e.Kind == EventCapture && e.Target == carrier.ID {
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Fatal("capture did not emit EventCapture")
	}
}

// TestCTFCaptureBlockedWhenOwnFlagOut: захват не засчитывается, если свой флаг не дома.
func TestCTFCaptureBlockedWhenOwnFlagOut(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	carrier := ps[1]
	w.flags[0] = flagState{status: flagCarried, carrier: carrier.ID}
	w.flags[1] = flagState{status: flagDropped, x: 2000, y: 2000, dropAt: w.Tick + 500} // свой флаг НЕ дома
	place(carrier, flagBases[1].x, flagBases[1].y)
	w.Step(dt)
	if carrier.Captures != 0 {
		t.Fatalf("Captures=%d, want 0 (own flag not home)", carrier.Captures)
	}
	if w.flags[0].status != flagCarried {
		t.Fatalf("enemy flag status=%d, want still carried (no capture)", w.flags[0].status)
	}
}

// TestCTFDropOnDeath: гибель носителя роняет флаг на месте смерти.
func TestCTFDropOnDeath(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	carrier := ps[1]
	w.flags[0] = flagState{status: flagCarried, carrier: carrier.ID}
	place(carrier, 700, 1000)
	carrier.dead = true
	carrier.respawnAt = w.Tick + 500 // не воскреснет в этом тике
	w.Step(dt)
	if w.flags[0].status != flagDropped {
		t.Fatalf("flag status=%d after carrier death, want dropped", w.flags[0].status)
	}
	if w.flags[0].x != 700 || w.flags[0].y != 1000 {
		t.Fatalf("dropped at (%g,%g), want death position (700,1000)", w.flags[0].x, w.flags[0].y)
	}
}

// TestCTFAutoReturn: брошенный флаг, которого никто не тронул, авто-возвращается на базу.
func TestCTFAutoReturn(t *testing.T) {
	w, _ := ctfWorld(t, 2)
	w.flags[0] = flagState{status: flagDropped, x: 700, y: 700, dropAt: w.Tick} // срок настал
	w.Step(dt)
	if w.flags[0].status != flagAtBase {
		t.Fatalf("flag status=%d, want auto-returned to base", w.flags[0].status)
	}
}

// TestCTFReturnOwnDroppedFlag: игрок своей команды возвращает свой брошенный флаг касанием.
func TestCTFReturnOwnDroppedFlag(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	teammate := ps[0] // team 0 — свой для флага команды 0
	w.flags[0] = flagState{status: flagDropped, x: 900, y: 900, dropAt: w.Tick + 500}
	place(teammate, 900, 900)
	w.Step(dt)
	if w.flags[0].status != flagAtBase {
		t.Fatalf("flag status=%d, want returned by teammate touch", w.flags[0].status)
	}
}

// TestCTFDisconnectReturnsFlag: уход носителя из мира возвращает флаг на базу.
func TestCTFDisconnectReturnsFlag(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	carrier := ps[1]
	w.flags[0] = flagState{status: flagCarried, carrier: carrier.ID}
	w.RemovePlayer(carrier.ID)
	w.Step(dt)
	if w.flags[0].status != flagAtBase {
		t.Fatalf("flag status=%d after carrier left, want returned to base", w.flags[0].status)
	}
}

// TestCTFWinnerByCaptures: победитель матча в ctfMode — команда с большей суммой захватов.
func TestCTFWinnerByCaptures(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	ps[0].Captures = 1 // team 0
	ps[1].Captures = 3 // team 1
	w.endMatch()
	if w.winner != PlayerID(1) {
		t.Fatalf("ctf winner=%d, want team 1", w.winner)
	}
}

// TestCaptureInChecksum: захваты и состояние флагов входят в Checksum.
func TestCaptureInChecksum(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	before := w.Checksum()
	ps[0].Captures = 2
	if w.Checksum() == before {
		t.Fatal("changing Captures must change Checksum")
	}
	after := w.Checksum()
	w.flags[0].status = flagCarried
	if w.Checksum() == after {
		t.Fatal("changing flag status must change Checksum")
	}
}

// TestCTFResetOnMatchStart: старт матча обнуляет захваты и возвращает флаги на базы.
func TestCTFResetOnMatchStart(t *testing.T) {
	w, ps := ctfWorld(t, 2)
	ps[0].Captures = 4
	w.flags[0] = flagState{status: flagCarried, carrier: ps[1].ID}
	w.startMatch()
	if ps[0].Captures != 0 {
		t.Fatalf("Captures=%d after startMatch, want 0", ps[0].Captures)
	}
	if w.flags[0].status != flagAtBase || w.flags[0].x != flagBases[0].x {
		t.Fatalf("flag not reset to base after startMatch: %+v", w.flags[0])
	}
}

// TestReplayCtfModeRoundTrip: ctfMode переносится логом реплея (v6) и влияет на
// результат. Игрок вводами доносит вражеский флаг до своей базы (вводы пишутся,
// place()/прямая установка флага — нет, поэтому переносим носителя вводами от базы).
// Негативный контроль: без ctfMode реплей не тронет флаги и Checksum разойдётся.
func TestReplayCtfModeRoundTrip(t *testing.T) {
	const tickRate = 30
	d := tickDt(tickRate)
	w := NewWorld(3)
	w.SetTeamMode(true)
	w.SetCtfMode(true)
	w.EnableReplayRecording()
	a, err := w.AddPlayer("a") // team 0
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.AddPlayer("b") // team 1
	if err != nil {
		t.Fatal(err)
	}
	_ = a
	// b (team 1) идёт к базе команды 0, подбирает флаг, несёт к своей базе (team 1).
	var seq uint32
	target := flagBases[0] // сначала к вражескому флагу
	grabbed := false
	for range 4000 {
		seq++
		if !grabbed && w.flags[0].status == flagCarried && w.flags[0].carrier == b.ID {
			grabbed = true
			target = flagBases[1] // подобрал — теперь домой
		}
		w.EnqueueInput(b.ID, protocol.Input{Seq: seq, Buttons: dirToward(b.X, b.Y, target.x, target.y)})
		w.Step(d)
	}
	if b.Captures == 0 {
		t.Fatal("setup: player never captured — test would be vacuous")
	}
	want := w.Checksum()

	decoded, err := DecodeReplay(w.ReplayLog(tickRate).Encode())
	if err != nil {
		t.Fatalf("DecodeReplay: %v", err)
	}
	if !decoded.CtfMode {
		t.Fatal("decoded log lost ctfMode (v6 header)")
	}
	got, err := Replay(decoded)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got != want {
		t.Fatalf("ctf replay checksum %#x != original %#x", got, want)
	}

	// Без ctfMode реплей не тронет флаги/захваты → хэш обязан разойтись.
	decoded.CtfMode = false
	if bad, err := Replay(decoded); err == nil && bad == want {
		t.Fatal("dropping ctfMode did not change checksum — mode not exercised")
	}
}

// TestCTFDeterminismAcrossFullCycle: два мира в ctfMode с одной лентой вводов проходят
// полный цикл матча под равенством Checksum на каждом тике, включая захват (носитель
// доносит флаг), дроп, подбор и переходы фаз (endMatch→ctfWinningTeam, startMatch→
// resetFlags). Закрывает слепое пятно к рассинхрону в этих путях (совет determinism-guard).
func TestCTFDeterminismAcrossFullCycle(t *testing.T) {
	build := func() (*World, PlayerID, PlayerID) {
		w := NewWorld(21)
		w.SetTeamMode(true)
		w.SetCtfMode(true)
		a, _ := w.AddPlayer("a") // team 0
		b, _ := w.AddPlayer("b") // team 1
		place(a, 2048, 2048)     // в центре, не трогает флаги (иначе унёс бы свой флаг b)
		// b несёт флаг команды 0 из точки рядом со своей базой — быстро захватит (свой
		// флаг команды 1 остаётся дома, поэтому захват засчитается).
		w.flags[0] = flagState{status: flagCarried, carrier: b.ID}
		place(b, flagBases[1].x-100, flagBases[1].y)
		return w, a.ID, b.ID
	}
	feed := func(w *World, a, b PlayerID, tick int) {
		// a качается в центре (не трогает флаги); b правит к своей базе — захватывает.
		ap := w.players[a]
		bp := w.players[b]
		if ap != nil {
			btn := protocol.BtnRight
			if tick%2 == 1 {
				btn = protocol.BtnLeft
			}
			w.EnqueueInput(a, protocol.Input{Seq: uint32(tick + 1), Buttons: btn})
		}
		if bp != nil {
			w.EnqueueInput(b, protocol.Input{Seq: uint32(tick + 1), Buttons: dirToward(bp.X, bp.Y, flagBases[1].x, flagBases[1].y)})
		}
	}
	wa, aa, ab := build()
	wb, ba, bb := build()
	total := matchDurationTicks + intermissionTicks + 120
	sawCapture, sawIntermission := false, false
	for tick := range total {
		feed(wa, aa, ab, tick)
		feed(wb, ba, bb, tick)
		wa.Step(dt)
		wb.Step(dt)
		if wa.Checksum() != wb.Checksum() {
			t.Fatalf("desync at tick %d (phase %d)", wa.Tick, wa.matchPhase)
		}
		if wa.matchPhase == matchIntermission {
			sawIntermission = true
		}
		if wa.players[ab] != nil && wa.players[ab].Captures > 0 {
			sawCapture = true
		}
	}
	if !sawCapture {
		t.Fatal("no capture occurred — capture path not exercised")
	}
	if !sawIntermission {
		t.Fatal("match never reached intermission — phase transition not exercised")
	}
}
