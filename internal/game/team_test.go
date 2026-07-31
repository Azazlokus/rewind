package game

import (
	"fmt"
	"testing"

	"arena/internal/protocol"
)

// TestTeamBalanceAssignment: в командном режиме вход раздаёт игроков в меньшую
// команду — при чётном числе получаем ровный баланс, порядок детерминирован.
func TestTeamBalanceAssignment(t *testing.T) {
	w := NewWorld(1)
	w.SetTeamMode(true)
	var teams []uint8
	for i := range 6 {
		p, err := w.AddPlayer(fmt.Sprintf("p%d", i))
		if err != nil {
			t.Fatal(err)
		}
		teams = append(teams, p.team)
	}
	// smallerTeam кладёт первого в 0, дальше чередует в меньшую → 0,1,0,1,0,1.
	want := []uint8{0, 1, 0, 1, 0, 1}
	for i, got := range teams {
		if got != want[i] {
			t.Fatalf("player %d team=%d, want %d (teams=%v)", i, got, want[i], teams)
		}
	}
	var c0, c1 int
	for _, tm := range teams {
		if tm == 0 {
			c0++
		} else {
			c1++
		}
	}
	if c0 != 3 || c1 != 3 {
		t.Fatalf("unbalanced: team0=%d team1=%d, want 3/3", c0, c1)
	}
}

// TestFriendlyFireDisabled: в командном режиме снаряд проходит сквозь союзника (урона
// нет), но ранит врага. Контроль отделяет «стены нет» от «дружественный огонь off».
func TestFriendlyFireDisabled(t *testing.T) {
	// Союзник: обе — команда 0. Снаряд летит сквозь, HP не меняется.
	w := NewWorld(1)
	w.SetTeamMode(true)
	shooter, _ := w.AddPlayer("s")
	ally, _ := w.AddPlayer("a")
	shooter.team, ally.team = 0, 0
	place(shooter, 1000, 1000)
	place(ally, 1000, 1120)
	w.EnqueueInput(shooter.ID, fireAt(1, 1000, 1000, 1000, 1120))
	for range 12 {
		w.Step(testDt)
	}
	if ally.HP != 100 || ally.dead {
		t.Fatalf("friendly fire hit ally: HP=%d dead=%v", ally.HP, ally.dead)
	}

	// Контроль: тот же выстрел во врага (команда 1) — ранит.
	w2 := NewWorld(1)
	w2.SetTeamMode(true)
	s2, _ := w2.AddPlayer("s")
	enemy, _ := w2.AddPlayer("e")
	s2.team, enemy.team = 0, 1
	place(s2, 1000, 1000)
	place(enemy, 1000, 1120)
	w2.EnqueueInput(s2.ID, fireAt(1, 1000, 1000, 1000, 1120))
	for range 12 {
		w2.Step(testDt)
	}
	if enemy.HP == 100 {
		t.Fatal("control: enemy on other team took no damage (friendly-fire logic too broad)")
	}
}

// TestFriendlyFireOnInFFA: без командного режима свои снаряды ранят кого угодно —
// team в этом случае не должна ничего блокировать (регресс на FFA).
func TestFriendlyFireOnInFFA(t *testing.T) {
	w := NewWorld(1)
	// teamMode выключен; team по умолчанию 0 у обоих.
	shooter, _ := w.AddPlayer("s")
	target, _ := w.AddPlayer("t")
	place(shooter, 1000, 1000)
	place(target, 1000, 1120)
	w.EnqueueInput(shooter.ID, fireAt(1, 1000, 1000, 1000, 1120))
	for range 12 {
		w.Step(testDt)
	}
	if target.HP == 100 {
		t.Fatal("FFA: same-team-id players did not damage each other (teamMode leaked into FFA)")
	}
}

// TestTeamScoringAndWinner: победитель матча — команда с большим суммарным счётом;
// won() и endMatch согласованы, при равенстве побеждает команда 0.
func TestTeamScoringAndWinner(t *testing.T) {
	w := NewWorld(1)
	w.SetTeamMode(true)
	a, _ := w.AddPlayer("a") // team 0
	b, _ := w.AddPlayer("b") // team 1
	c, _ := w.AddPlayer("c") // team 0
	a.team, b.team, c.team = 0, 1, 0
	a.Kills, c.Kills = 3, 4 // команда 0 суммарно 7
	b.Kills = 10            // команда 1 суммарно 10 → побеждает

	if got := w.winningTeam(); got != 1 {
		t.Fatalf("winningTeam=%d, want 1", got)
	}
	w.endMatch()
	if w.winner != PlayerID(1) {
		t.Fatalf("endMatch winner=%d, want team 1", w.winner)
	}
	// won(): игроки команды 1 победили, команды 0 — нет.
	if !w.won(b) {
		t.Fatal("team-1 player should have won")
	}
	if w.won(a) || w.won(c) {
		t.Fatal("team-0 players should not have won")
	}

	// Равный счёт → команда 0 (tiebreak).
	b.Kills = 7
	if got := w.winningTeam(); got != 0 {
		t.Fatalf("tie winningTeam=%d, want 0", got)
	}
}

// TestTeamStateInChecksum: команда игрока и команда снаряда входят в Checksum (влияют
// на дружественный огонь и командный счёт — на будущее состояние).
func TestTeamStateInChecksum(t *testing.T) {
	base := func() *World {
		w := NewWorld(9)
		w.SetTeamMode(true)
		p, _ := w.AddPlayer("p")
		p.team = 0
		return w
	}
	b := base()
	// team игрока.
	w := base()
	w.players[w.order[0]].team = 1
	if w.Checksum() == b.Checksum() {
		t.Fatal("player.team not covered by Checksum")
	}
	// team снаряда.
	wp := base()
	bp := base()
	wp.projectiles = append(wp.projectiles, projectile{team: 0})
	bp.projectiles = append(bp.projectiles, projectile{team: 1})
	if wp.Checksum() == bp.Checksum() {
		t.Fatal("projectile.team not covered by Checksum")
	}
}

// TestTeamModeNotInChecksum: сам флаг teamMode — фиксированный параметр мира (как
// tickRate), в Checksum НЕ входит. Два мира с одинаковыми игроками/командами, но
// разным teamMode, совпадают по хэшу.
func TestTeamModeNotInChecksum(t *testing.T) {
	mk := func(tm bool) *World {
		w := NewWorld(5)
		w.SetTeamMode(tm)
		p, _ := w.AddPlayer("p")
		p.team = 0 // фиксируем команду вручную, чтобы не зависеть от авто-баланса
		return w
	}
	if mk(true).Checksum() != mk(false).Checksum() {
		t.Fatal("teamMode flag leaked into Checksum (must be a fixed world param)")
	}
}

// TestTeamDeterminism: два мира с одним seed, командным режимом и одной лентой (4
// игрока, 2 команды, непрерывный бой с дружественным огнём) совпадают по Checksum
// каждый тик — закрывает детерминизм командного счёта и friendly-fire.
func TestTeamDeterminism(t *testing.T) {
	build := func() (*World, []PlayerID) {
		w := NewWorld(42)
		w.SetTeamMode(true)
		var ids []PlayerID
		coords := [][2]float32{{1000, 1000}, {1000, 1100}, {1100, 1000}, {1100, 1100}}
		for i := range 4 {
			p, _ := w.AddPlayer(fmt.Sprintf("p%d", i))
			place(p, coords[i][0], coords[i][1])
			ids = append(ids, p.ID)
		}
		return w, ids
	}
	a, aids := build()
	b, bids := build()

	const ticks = 900
	seq := uint32(0)
	for tick := range ticks {
		seq++
		// Каждый стреляет по диагонали — попадёт и в союзников, и во врагов.
		for i := range aids {
			in := fireAt(seq, 1000, 1000, 1100, 1100)
			a.EnqueueInput(aids[i], in)
			b.EnqueueInput(bids[i], in)
		}
		a.Step(testDt)
		b.Step(testDt)
		if a.Checksum() != b.Checksum() {
			t.Fatalf("determinism broke at tick %d", tick)
		}
	}
}

// TestReplayTeamModeRoundTrip: лог реплея v2 несёт teamMode; после кодека и
// проигрывания Checksum совпадает с живым миром. Без переноса teamMode дружественный
// огонь реконструировался бы иначе и хэш разошёлся бы.
func TestReplayTeamModeRoundTrip(t *testing.T) {
	const tickRate = 30
	dt := tickDt(tickRate)

	// Реплей воспроизводит только записанные события (join/input) — ручной place()
	// в лог не попадёт, поэтому позиции не трогаем: игроки спавнятся из w.rng, а
	// команда (в Checksum) раздаётся при join одинаково. Ленту строим из движения и
	// стрельбы, чтобы состояние (позиции, снаряды, счёт) было нетривиальным.
	w := NewWorld(3)
	w.SetTeamMode(true)
	w.EnableReplayRecording()
	var ids []PlayerID
	for i := range 4 {
		p, err := w.AddPlayer(fmt.Sprintf("p%d", i))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	var seq uint32
	for range 120 {
		seq++
		for i, id := range ids {
			btn := protocol.BtnFire | protocol.BtnRight
			if i%2 == 0 {
				btn |= protocol.BtnUp
			}
			w.EnqueueInput(id, protocol.Input{Seq: seq, Buttons: btn, Aim: protocol.AimFromRadians(float64(i))})
		}
		w.Step(dt)
	}
	want := w.Checksum()

	log := w.ReplayLog(tickRate)
	if log == nil || !log.TeamMode {
		t.Fatalf("replay log missing teamMode: %+v", log)
	}
	decoded, err := DecodeReplay(log.Encode())
	if err != nil {
		t.Fatalf("DecodeReplay: %v", err)
	}
	if !decoded.TeamMode {
		t.Fatal("decoded log lost teamMode (v2 header)")
	}
	got, err := Replay(decoded)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got != want {
		t.Fatalf("team replay checksum %#x != original %#x", got, want)
	}

	// Санити: без переноса teamMode реплей разойдётся (доказывает, что параметр важен).
	decoded.TeamMode = false
	if bad, err := Replay(decoded); err == nil && bad == want {
		t.Fatal("replay matched even with teamMode dropped — friendly fire not affecting hash?")
	}
}

// TestMatchStateCarriesTeam: MatchSnapshot несёт команду каждого игрока и флаг режима
// — провод (MsgMatchState) полагается на это, чтобы раскрасить табло на клиенте.
func TestMatchStateCarriesTeam(t *testing.T) {
	w := NewWorld(1)
	w.SetTeamMode(true)
	a, _ := w.AddPlayer("a")
	b, _ := w.AddPlayer("b")
	a.team, b.team = 0, 1
	snap := w.MatchState(nil)
	if !snap.TeamMode {
		t.Fatal("MatchSnapshot.TeamMode false in team mode")
	}
	byID := map[PlayerID]uint8{}
	for _, s := range snap.Scores {
		byID[s.ID] = s.Team
	}
	if byID[a.ID] != 0 || byID[b.ID] != 1 {
		t.Fatalf("scores carry wrong teams: %v", byID)
	}
}
