package game

import (
	rand "math/rand/v2"
	"testing"

	"arena/internal/protocol"
)

// TestDominationAwardsUncontested: в FFA одинокий игрок в зоне копит очки; вне зон — нет.
func TestDominationAwardsUncontested(t *testing.T) {
	w := NewWorld(1)
	w.SetDomMode(true)
	p, _ := w.AddPlayer("p")
	cp := domPoints[0]
	place(p, cp.x, cp.y) // в центре зоны A
	w.Step(dt)
	if p.DomScore != 1 {
		t.Fatalf("DomScore=%d after 1 tick inside, want 1", p.DomScore)
	}
	place(p, 100, 2048) // между зон, вне любой
	w.Step(dt)
	if p.DomScore != 1 {
		t.Fatalf("DomScore=%d after leaving all zones, want unchanged 1", p.DomScore)
	}
}

// TestDominationContestedNoScore: в FFA двое в ОДНОЙ зоне — оспаривается, никто не копит.
func TestDominationContestedNoScore(t *testing.T) {
	w := NewWorld(1)
	w.SetDomMode(true)
	a, _ := w.AddPlayer("a")
	b, _ := w.AddPlayer("b")
	cp := domPoints[1]
	place(a, cp.x, cp.y)
	place(b, cp.x+20, cp.y)
	w.Step(dt)
	if a.DomScore != 0 || b.DomScore != 0 {
		t.Fatalf("contested zone scored: a=%d b=%d, want 0/0", a.DomScore, b.DomScore)
	}
}

// TestDominationScoresAcrossZones: очки начисляются по КАЖДОЙ контрольной точке
// независимо. В командном режиме команда, держащая две разные зоны двумя игроками,
// копит по +1 каждому (сумма 2 за тик) — больше, чем за одну зону: суть доминации.
func TestDominationScoresAcrossZones(t *testing.T) {
	w := NewWorld(1)
	w.SetTeamMode(true)
	w.SetDomMode(true)
	p0, _ := w.AddPlayer("t0") // team 0
	p1, _ := w.AddPlayer("t1") // team 1
	p2, _ := w.AddPlayer("t2") // team 0
	if p0.team != 0 || p1.team != 1 || p2.team != 0 {
		t.Fatalf("unexpected team assignment: %d/%d/%d", p0.team, p1.team, p2.team)
	}
	place(p0, domPoints[0].x, domPoints[0].y) // team 0 держит зону A
	place(p2, domPoints[2].x, domPoints[2].y) // team 0 держит зону C
	place(p1, 100, 2048)                      // враг вне зон
	w.Step(dt)
	if p0.DomScore != 1 || p2.DomScore != 1 {
		t.Fatalf("team 0 holding two zones: p0=%d p2=%d, want 1/1", p0.DomScore, p2.DomScore)
	}
	if p1.DomScore != 0 {
		t.Fatalf("enemy scored %d, want 0", p1.DomScore)
	}
	if got := int(p0.DomScore) + int(p2.DomScore); got != 2 {
		t.Fatalf("team 0 sum per tick=%d holding two zones, want 2", got)
	}
}

// TestDominationTeamContested: враг, зашедший в одну из зон, гасит начисление ТОЛЬКО
// по этой зоне; другая, где соперника нет, продолжает кормить контролёра.
func TestDominationTeamContested(t *testing.T) {
	w := NewWorld(1)
	w.SetTeamMode(true)
	w.SetDomMode(true)
	p0, _ := w.AddPlayer("t0")                   // team 0
	p1, _ := w.AddPlayer("t1")                   // team 1
	p2, _ := w.AddPlayer("t2")                   // team 0
	place(p0, domPoints[0].x, domPoints[0].y)    // team 0 в зоне A
	place(p2, domPoints[2].x, domPoints[2].y)    // team 0 в зоне C
	place(p1, domPoints[0].x+20, domPoints[0].y) // враг оспаривает зону A
	w.Step(dt)
	if p0.DomScore != 0 {
		t.Fatalf("contested zone A still scored: p0=%d, want 0", p0.DomScore)
	}
	if p2.DomScore != 1 {
		t.Fatalf("uncontested zone C should score: p2=%d, want 1", p2.DomScore)
	}
	if p1.DomScore != 0 {
		t.Fatalf("enemy scored %d, want 0", p1.DomScore)
	}
}

// TestDominationWinnerByScore: победитель матча в domMode — по очкам доминации (FFA —
// игрок, teamMode — команда), а не по фрагам.
func TestDominationWinnerByScore(t *testing.T) {
	// FFA: лидер доминации побеждает, хотя фрагов у него нет.
	w := NewWorld(1)
	w.SetDomMode(true)
	a, _ := w.AddPlayer("a")
	b, _ := w.AddPlayer("b")
	a.Kills = 9 // много фрагов, но мало доминации
	a.DomScore = 1
	b.DomScore = 7
	w.endMatch()
	if w.winner != b.ID {
		t.Fatalf("FFA dom winner=%d, want %d (most dom points)", w.winner, b.ID)
	}

	// teamMode: побеждает команда с большей суммой очков доминации.
	w2 := NewWorld(1)
	w2.SetTeamMode(true)
	w2.SetDomMode(true)
	t0, _ := w2.AddPlayer("t0") // team 0
	t1, _ := w2.AddPlayer("t1") // team 1
	t0.DomScore = 2
	t1.DomScore = 5
	w2.endMatch()
	if w2.winner != PlayerID(1) {
		t.Fatalf("team dom winner=%d, want team 1", w2.winner)
	}
}

// TestDomScoreInChecksum: очки доминации входят в Checksum.
func TestDomScoreInChecksum(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 2048)
	w.Step(dt)
	before := w.Checksum()
	p.DomScore = 3
	if w.Checksum() == before {
		t.Fatal("changing DomScore must change Checksum")
	}
}

// TestDomScoreResetOnMatchStart: старт нового матча обнуляет очки доминации.
func TestDomScoreResetOnMatchStart(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	p.DomScore = 5
	w.startMatch()
	if p.DomScore != 0 {
		t.Fatalf("DomScore=%d after startMatch, want 0", p.DomScore)
	}
}

// TestMatchStateCarriesDomScore: в domMode MatchSnapshot несёт флаг DomMode, кладёт
// очки доминации в слот объектива (HillScore) и сортирует табло по ним — на это
// полагается провод (MsgMatchState) для табло/победителя на клиенте.
func TestMatchStateCarriesDomScore(t *testing.T) {
	w := NewWorld(1)
	w.SetDomMode(true)
	a, _ := w.AddPlayer("a") // id 1
	b, _ := w.AddPlayer("b") // id 2
	a.Kills = 9              // фрагов больше у a
	a.DomScore = 2
	b.DomScore = 6 // но очков доминации больше у b — он выше в табло
	snap := w.MatchState(nil)
	if !snap.DomMode || snap.HillMode {
		t.Fatalf("MatchSnapshot flags: DomMode=%v HillMode=%v, want dom-only", snap.DomMode, snap.HillMode)
	}
	if snap.Scores[0].ID != b.ID {
		t.Fatalf("scoreboard top=%d, want %d (most dom points)", snap.Scores[0].ID, b.ID)
	}
	byID := map[PlayerID]uint16{}
	for _, s := range snap.Scores {
		byID[s.ID] = s.HillScore // слот объектива несёт очки доминации
	}
	if byID[a.ID] != 2 || byID[b.ID] != 6 {
		t.Fatalf("objective slot carries wrong dom points: %v", byID)
	}
}

// TestReplayDomModeRoundTrip: domMode переносится логом реплея (v5) и влияет на
// результат. Игрок вводами доходит до зоны и копит очки (вводы пишутся, place() — нет).
// Негативный контроль: без domMode реплей не начислит очки и Checksum разойдётся.
func TestReplayDomModeRoundTrip(t *testing.T) {
	const tickRate = 30
	d := tickDt(tickRate)
	w := NewWorld(3)
	w.SetDomMode(true)
	w.EnableReplayRecording()
	p, err := w.AddPlayer("p")
	if err != nil {
		t.Fatal(err)
	}
	cp := domPoints[0]
	var seq uint32
	for range 600 { // с запасом на дорогу к зоне A + накопление
		seq++
		w.EnqueueInput(p.ID, protocol.Input{Seq: seq, Buttons: dirToward(p.X, p.Y, cp.x, cp.y)})
		w.Step(d)
	}
	if p.DomScore == 0 {
		t.Fatal("setup: player never scored a zone — test would be vacuous")
	}
	want := w.Checksum()

	decoded, err := DecodeReplay(w.ReplayLog(tickRate).Encode())
	if err != nil {
		t.Fatalf("DecodeReplay: %v", err)
	}
	if !decoded.DomMode {
		t.Fatal("decoded log lost domMode (v5 header)")
	}
	got, err := Replay(decoded)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got != want {
		t.Fatalf("dom replay checksum %#x != original %#x", got, want)
	}

	// Без domMode реплей не начислит очки зон → хэш обязан разойтись.
	decoded.DomMode = false
	if bad, err := Replay(decoded); err == nil && bad == want {
		t.Fatal("dropping domMode did not change checksum — mode not exercised")
	}
}

// TestDominationTiebreak: при равных очках доминации побеждает меньший id (FFA) и
// команда 0 (teamMode) — тот же tiebreak, что у holl/leader. Прямой контроль ребра.
func TestDominationTiebreak(t *testing.T) {
	// FFA: равные очки → лидер с меньшим id.
	w := NewWorld(1)
	w.SetDomMode(true)
	a, _ := w.AddPlayer("a") // id 1
	b, _ := w.AddPlayer("b") // id 2
	a.DomScore = 4
	b.DomScore = 4
	if got := w.domLeader(); got != a.ID {
		t.Fatalf("domLeader tie=%d, want %d (min id)", got, a.ID)
	}

	// teamMode: равные суммы → команда 0.
	w2 := NewWorld(1)
	w2.SetTeamMode(true)
	w2.SetDomMode(true)
	t0, _ := w2.AddPlayer("t0") // team 0
	t1, _ := w2.AddPlayer("t1") // team 1
	t0.DomScore = 3
	t1.DomScore = 3
	if got := w2.domWinningTeam(); got != 0 {
		t.Fatalf("domWinningTeam tie=%d, want 0", got)
	}
}

// TestDominationDeterminismAcrossFullCycle: два мира в domMode с одной лентой вводов
// проходят ПОЛНЫЙ цикл матча (бой → антракт → новый матч) под равенством Checksum на
// каждом тике. Закрывает слепое пятно TestDominationDeterminism (×300 тиков не задевает
// endMatch→domLeader и startMatch→сброс DomScore) — совет determinism-guard.
func TestDominationDeterminismAcrossFullCycle(t *testing.T) {
	build := func() (*World, []PlayerID) {
		w := NewWorld(21)
		w.SetDomMode(true)
		ids := make([]PlayerID, 0, 3)
		for i := range 3 {
			p, err := w.AddPlayer("p")
			if err != nil {
				t.Fatal(err)
			}
			cp := domPoints[i] // каждый — в своей зоне, чтобы очки реально копились
			place(p, cp.x, cp.y)
			ids = append(ids, p.ID)
		}
		return w, ids
	}
	// Детерминированная лента: лёгкое горизонтальное покачивание по чётности тика —
	// игрок остаётся в своей зоне (radius 256 ≫ дрейфа ~10 юнитов/тик) и продолжает
	// копить, при этом упражняется путь движения.
	feed := func(w *World, ids []PlayerID, tick int) {
		btn := protocol.BtnRight
		if tick%2 == 1 {
			btn = protocol.BtnLeft
		}
		for _, id := range ids {
			w.EnqueueInput(id, protocol.Input{Seq: uint32(tick + 1), Buttons: btn})
		}
	}
	a, aIDs := build()
	b, bIDs := build()
	total := matchDurationTicks + intermissionTicks + 120
	sawScore, sawIntermission := false, false
	for tick := range total {
		feed(a, aIDs, tick)
		feed(b, bIDs, tick)
		a.Step(dt)
		b.Step(dt)
		if a.Checksum() != b.Checksum() {
			t.Fatalf("desync at tick %d (phase %d)", a.Tick, a.matchPhase)
		}
		if a.matchPhase == matchIntermission {
			sawIntermission = true
		}
		for _, id := range aIDs {
			if a.players[id].DomScore > 0 {
				sawScore = true
			}
		}
	}
	if !sawScore {
		t.Fatal("input feed produced no dom score — scoring path not exercised")
	}
	if !sawIntermission {
		t.Fatal("match never reached intermission — phase transition not exercised")
	}
	if a.matchPhase != matchActive {
		t.Fatalf("match cycle did not restart to active: phase %d", a.matchPhase)
	}
}

// TestDominationDeterminism: два мира в domMode с одной лентой вводов (игроки снуют по
// карте между зонами) дают равный Checksum на каждом тике.
func TestDominationDeterminism(t *testing.T) {
	build := func() *World {
		w := NewWorld(11)
		w.SetDomMode(true)
		for i := range 3 {
			p, err := w.AddPlayer("p")
			if err != nil {
				t.Fatal(err)
			}
			cp := domPoints[i%len(domPoints)]
			place(p, cp.x, cp.y) // стартуют по зонам, чтобы контроль/оспаривание игрались
		}
		return w
	}
	wa, wb := build(), build()
	src := rand.New(rand.NewPCG(4, 4))
	ids := wa.order
	for tick := range 300 {
		for _, id := range ids {
			in := protocol.Input{Seq: uint32(tick + 1), Buttons: uint8(src.UintN(16))}
			wa.EnqueueInput(id, in)
			wb.EnqueueInput(id, in)
		}
		wa.Step(dt)
		wb.Step(dt)
		if wa.Checksum() != wb.Checksum() {
			t.Fatalf("worlds diverged at tick %d", tick)
		}
	}
}
