package game

import (
	rand "math/rand/v2"
	"testing"

	"arena/internal/protocol"
)

// TestHillAwardsUncontrolled: в FFA одинокий игрок в зоне копит очки; вне зоны — нет.
func TestHillAwardsUncontested(t *testing.T) {
	w := NewWorld(1)
	w.SetHillMode(true)
	p, _ := w.AddPlayer("p")
	place(p, hillX, hillY) // в центре зоны
	w.Step(dt)
	if p.HillScore != 1 {
		t.Fatalf("HillScore=%d after 1 tick inside, want 1", p.HillScore)
	}
	place(p, 800, 800) // вышел из зоны
	w.Step(dt)
	if p.HillScore != 1 {
		t.Fatalf("HillScore=%d after leaving, want unchanged 1", p.HillScore)
	}
}

// TestHillContestedNoScore: в FFA двое в зоне — оспаривается, никто не копит.
func TestHillContestedNoScore(t *testing.T) {
	w := NewWorld(1)
	w.SetHillMode(true)
	a, _ := w.AddPlayer("a")
	b, _ := w.AddPlayer("b")
	place(a, hillX, hillY)
	place(b, hillX+20, hillY)
	w.Step(dt)
	if a.HillScore != 0 || b.HillScore != 0 {
		t.Fatalf("contested hill scored: a=%d b=%d, want 0/0", a.HillScore, b.HillScore)
	}
}

// TestHillTeamControl: в командном режиме команда без соперников в зоне копит очки
// каждому её игроку внутри; при появлении врага зона оспаривается.
func TestHillTeamControl(t *testing.T) {
	w := NewWorld(1)
	w.SetTeamMode(true)
	w.SetHillMode(true)
	p0, _ := w.AddPlayer("t0") // team 0
	p1, _ := w.AddPlayer("t1") // team 1
	p2, _ := w.AddPlayer("t2") // team 0
	if p0.team != 0 || p1.team != 1 || p2.team != 0 {
		t.Fatalf("unexpected team assignment: %d/%d/%d", p0.team, p1.team, p2.team)
	}
	place(p0, hillX, hillY)
	place(p2, hillX-20, hillY)
	place(p1, 800, 800) // враг вне зоны
	w.Step(dt)
	if p0.HillScore != 1 || p2.HillScore != 1 {
		t.Fatalf("team 0 should score: p0=%d p2=%d, want 1/1", p0.HillScore, p2.HillScore)
	}
	if p1.HillScore != 0 {
		t.Fatalf("enemy scored %d, want 0", p1.HillScore)
	}
	// Враг заходит в зону — оспаривается, очки не растут.
	place(p1, hillX+20, hillY)
	before0, before2 := p0.HillScore, p2.HillScore
	w.Step(dt)
	if p0.HillScore != before0 || p2.HillScore != before2 {
		t.Fatalf("contested hill kept scoring: %d/%d", p0.HillScore, p2.HillScore)
	}
}

// TestHillWinnerByScore: победитель матча в hillMode — по очкам холма (FFA — игрок,
// teamMode — команда), а не по фрагам.
func TestHillWinnerByScore(t *testing.T) {
	// FFA: лидер холма побеждает, хотя фрагов у него нет.
	w := NewWorld(1)
	w.SetHillMode(true)
	a, _ := w.AddPlayer("a")
	b, _ := w.AddPlayer("b")
	a.Kills = 9 // много фрагов, но мало холма
	a.HillScore = 1
	b.HillScore = 7
	w.endMatch()
	if w.winner != b.ID {
		t.Fatalf("FFA hill winner=%d, want %d (most hill points)", w.winner, b.ID)
	}

	// teamMode: побеждает команда с большей суммой очков холма.
	w2 := NewWorld(1)
	w2.SetTeamMode(true)
	w2.SetHillMode(true)
	t0, _ := w2.AddPlayer("t0") // team 0
	t1, _ := w2.AddPlayer("t1") // team 1
	t0.HillScore = 2
	t1.HillScore = 5
	w2.endMatch()
	if w2.winner != PlayerID(1) {
		t.Fatalf("team hill winner=%d, want team 1", w2.winner)
	}
}

// TestHillScoreInChecksum: очки холма входят в Checksum.
func TestHillScoreInChecksum(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	w.Step(dt)
	before := w.Checksum()
	p.HillScore = 3
	if w.Checksum() == before {
		t.Fatal("changing HillScore must change Checksum")
	}
}

// TestHillScoreResetOnMatchStart: старт нового матча обнуляет очки холма.
func TestHillScoreResetOnMatchStart(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	p.HillScore = 5
	w.startMatch()
	if p.HillScore != 0 {
		t.Fatalf("HillScore=%d after startMatch, want 0", p.HillScore)
	}
}

// dirToward — WASD-ввод, ведущий из (x,y) к (tx,ty) (для тестов навигации вводами).
func dirToward(x, y, tx, ty float32) uint8 {
	var b uint8
	if tx-x > 4 {
		b |= protocol.BtnRight
	} else if x-tx > 4 {
		b |= protocol.BtnLeft
	}
	if ty-y > 4 {
		b |= protocol.BtnDown
	} else if y-ty > 4 {
		b |= protocol.BtnUp
	}
	return b
}

// TestReplayHillModeRoundTrip: hillMode переносится логом реплея (v4) и влияет на
// результат. Один игрок вводами доходит до зоны и копит очки (place() в лог не
// попадёт, поэтому ведём его вводами — они пишутся). Негативный контроль: без hillMode
// реплей не начислит очки и Checksum разойдётся.
func TestReplayHillModeRoundTrip(t *testing.T) {
	const tickRate = 30
	d := tickDt(tickRate)
	w := NewWorld(3)
	w.SetHillMode(true)
	w.EnableReplayRecording()
	p, err := w.AddPlayer("p")
	if err != nil {
		t.Fatal(err)
	}
	var seq uint32
	for range 500 { // с запасом на дорогу к центру + накопление в зоне
		seq++
		w.EnqueueInput(p.ID, protocol.Input{Seq: seq, Buttons: dirToward(p.X, p.Y, hillX, hillY)})
		w.Step(d)
	}
	if p.HillScore == 0 {
		t.Fatal("setup: player never scored on the hill — test would be vacuous")
	}
	want := w.Checksum()

	decoded, err := DecodeReplay(w.ReplayLog(tickRate).Encode())
	if err != nil {
		t.Fatalf("DecodeReplay: %v", err)
	}
	if !decoded.HillMode {
		t.Fatal("decoded log lost hillMode (v4 header)")
	}
	got, err := Replay(decoded)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got != want {
		t.Fatalf("hill replay checksum %#x != original %#x", got, want)
	}

	// Без hillMode реплей не начислит очки холма → хэш обязан разойтись.
	decoded.HillMode = false
	if bad, err := Replay(decoded); err == nil && bad == want {
		t.Fatal("dropping hillMode did not change checksum — mode not exercised")
	}
}

// TestHillDeterminism: два мира в hillMode с одной лентой вводов (игроки снуют по карте
// в зону и из неё) дают равный Checksum на каждом тике.
func TestHillDeterminism(t *testing.T) {
	build := func() *World {
		w := NewWorld(11)
		w.SetHillMode(true)
		for range 3 {
			p, err := w.AddPlayer("p")
			if err != nil {
				t.Fatal(err)
			}
			place(p, hillX, hillY) // стартуют в зоне, чтобы контроль/оспаривание игрались
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
