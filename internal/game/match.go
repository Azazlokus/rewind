package game

// Матч (итерация 14): FFA deathmatch с таймером. Полностью детерминирован — все
// переходы завязаны на w.Tick, победитель считается детерминированным обходом order,
// точки респауна берутся из w.rng. Поэтому матч входит в Checksum и безопасен для
// реплеев. Игровое состояние по-прежнему мутирует только горутина комнаты.

// matchPhase — фаза матча.
type matchPhase uint8

const (
	matchActive       matchPhase = iota // идёт бой, счёт растёт
	matchIntermission                   // матч кончился, показываем победителя, скоро новый
)

// Длительности фаз в тиках. При 30 Гц: 3 минуты боя и 10 секунд антракта. Фикс —
// значит одинаковы во всех мирах, реплей их не хранит.
const (
	matchDurationTicks = 30 * 60 * 3 // 5400
	intermissionTicks  = 30 * 10     // 300
)

// stepMatch продвигает состояние матча на один тик. Зовётся из World.Step после
// инкремента Tick (сравнения идут по уже актуальному тику).
func (w *World) stepMatch() {
	if w.Tick < w.matchAt {
		return
	}
	switch w.matchPhase {
	case matchActive:
		w.endMatch()
	case matchIntermission:
		w.startMatch()
	}
}

// endMatch завершает матч: фиксирует победителя и уходит в антракт. В командном
// режиме (итер. 23) w.winner несёт id ПОБЕДИВШЕЙ КОМАНДЫ (0/1), в FFA — id игрока-
// лидера; получатель различает их по флагу teamMode (на проводе — MatchState.TeamMode).
func (w *World) endMatch() {
	w.matchPhase = matchIntermission
	w.matchAt = w.Tick + intermissionTicks
	switch {
	case w.hillMode && w.teamMode:
		w.winner = PlayerID(w.hillWinningTeam()) // команда по сумме очков холма (итер. 29)
	case w.hillMode:
		w.winner = w.hillLeader() // игрок по очкам холма
	case w.teamMode:
		w.winner = PlayerID(w.winningTeam())
	default:
		w.winner = w.leader()
	}
}

// winningTeam возвращает команду (0/1) с большим суммарным счётом убийств (итер. 23).
// При равенстве — команда 0. Детерминированный обход w.order.
func (w *World) winningTeam() uint8 {
	var k [2]int
	for _, id := range w.order {
		p := w.players[id]
		k[p.team&1] += int(p.Kills)
	}
	if k[1] > k[0] {
		return 1
	}
	return 0
}

// startMatch стартует новый матч: сбрасывает счёт, респаунит живых в свежие точки.
//
// Обход только по w.order — детерминировано; respawn розыгрывает w.rng в том же
// зафиксированном порядке во всех мирах. Известное поведенческое ребро: если игрок
// умер в антракте так, что его respawnAt совпал с этим тиком, шаг респауна в Step
// уже воскресил его выше по тику, и здесь он попадёт под повторный respawn (ещё один
// розыгрыш rng + второй EventSpawn). Это ДЕТЕРМИНИРОВАНО (все миры делают одинаково)
// и безвредно (итоговое состояние — живой игрок в свежей точке), поэтому не чиним:
// правка меняла бы политику респауна, а не корректность.
func (w *World) startMatch() {
	w.matchPhase = matchActive
	w.matchAt = w.Tick + matchDurationTicks
	w.winner = 0
	for _, id := range w.order {
		p := w.players[id]
		p.Kills = 0
		p.Deaths = 0
		p.HillScore = 0 // новый матч — очки холма с нуля (итер. 29)
		p.streak = 0    // новый матч — серия убийств с нуля (итерация 20)
		if !p.dead {
			w.respawn(p) // мёртвые возродятся своим чередом по respawnAt
		}
	}
}

// leader — игрок с максимумом убийств; при равенстве — минимальный id (order
// отсортирован по возрастанию, поэтому строгое сравнение даёт этот tiebreak).
// Возвращает 0, если игроков нет.
func (w *World) leader() PlayerID {
	var best PlayerID
	bestKills := -1
	for _, id := range w.order {
		if k := int(w.players[id].Kills); k > bestKills {
			bestKills = k
			best = id
		}
	}
	return best
}

// MatchScore — строка табло: игрок, его счёт и команда (итер. 23), плюс очки холма
// (итер. 29).
type MatchScore struct {
	ID        PlayerID
	Name      string
	Kills     uint16
	Deaths    uint16
	Team      uint8
	HillScore uint16
}

// MatchSnapshot — текущее состояние матча для рассылки (не входит в Checksum).
type MatchSnapshot struct {
	Phase     matchPhase
	Remaining uint32 // тиков до смены фазы
	Winner    PlayerID
	TeamMode  bool         // командный режим (итер. 23): Winner — id команды, а не игрока
	HillMode  bool         // King of the Hill (итер. 29): Winner и сортировка — по очкам холма
	Scores    []MatchScore // по убыванию счёта (холм в hillMode, иначе убийства), затем по id
}

// MatchState собирает состояние матча в переданный (переиспользуемый) срез и
// возвращает снимок. Табло отсортировано детерминированно. Зовётся комнатой для
// рассылки — не на горячем Checksum-пути.
func (w *World) MatchState(dst []MatchScore) MatchSnapshot {
	dst = dst[:0]
	for _, id := range w.order {
		p := w.players[id]
		dst = append(dst, MatchScore{ID: id, Name: p.Name, Kills: p.Kills, Deaths: p.Deaths, Team: p.team, HillScore: p.HillScore})
	}
	// Сортировка вставками: table маленькая (≤ MaxPlayers), стабильна и без аллокаций.
	// В hillMode лидируют по очкам холма, иначе — по убийствам.
	for i := 1; i < len(dst); i++ {
		for j := i; j > 0 && lessScore(dst[j], dst[j-1], w.hillMode); j-- {
			dst[j], dst[j-1] = dst[j-1], dst[j]
		}
	}
	var remaining uint32
	if w.matchAt > w.Tick {
		remaining = w.matchAt - w.Tick
	}
	return MatchSnapshot{Phase: w.matchPhase, Remaining: remaining, Winner: w.winner, TeamMode: w.teamMode, HillMode: w.hillMode, Scores: dst}
}

// lessScore: больше основного счёта — выше; при равенстве меньший id — выше. Основной
// счёт — очки холма в hillMode (итер. 29), иначе убийства.
func lessScore(a, b MatchScore, hill bool) bool {
	ka, kb := a.Kills, b.Kills
	if hill {
		ka, kb = a.HillScore, b.HillScore
	}
	if ka != kb {
		return ka > kb
	}
	return a.ID < b.ID
}
