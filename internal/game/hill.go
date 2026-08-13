package game

// King of the Hill (итерация 29): режим захвата зоны. В центре арены — фиксированный
// круглый «холм»; пока его контролирует одна сторона (стоит внутри без соперников),
// она копит очки контроля. Победитель матча — по очкам холма, а не по фрагам.
// Полностью детерминирован (обход w.order, без rng/time), поэтому в Checksum и
// реплей-безопасен.
//
// Режим совместим и с FFA (сторона = игрок), и с командным (сторона = команда, итер.
// 23). Геометрия холма статична и одинакова во всех мирах, поэтому в Checksum НЕ входит
// (как walls/pickupSpots): в хэш идёт лишь её следствие — накопленный Player.HillScore.
// Раскладку зеркалит клиент (web/game.js: HILL) для отрисовки зоны.

const (
	// hillX/hillY — центр холма (центр карты).
	hillX float32 = MapSize / 2
	hillY float32 = MapSize / 2
	// hillRadius — радиус зоны контроля.
	hillRadius float32 = 300
)

// stepHill начисляет очки контроля холма за тик (итер. 29). Зовётся из World.Step в
// режиме hillMode во время активного матча, по актуальным (после движения) позициям.
//
// Контроль: собираем живых игроков внутри холма и множество их «сторон» (команда в
// teamMode, иначе id игрока). Ровно одна сторона внутри → она контролирует: каждому её
// игроку внутри +1 к HillScore. Ноль или ≥2 сторон (пусто/оспаривается) — никто не
// копит. Обход по w.order — детерминированно.
func (w *World) stepHill() {
	if !w.hillMode || w.matchPhase != matchActive {
		return
	}
	const r2 = hillRadius * hillRadius

	// Первый проход: кто внутри и сколько различных сторон.
	var sides [2]bool // для teamMode: присутствие команд 0/1
	var ffaInside PlayerID
	var ffaCount int
	anyTeam := false
	for _, id := range w.order {
		p := w.players[id]
		if p.dead {
			continue
		}
		dx, dy := p.X-hillX, p.Y-hillY
		if dx*dx+dy*dy > r2 {
			continue
		}
		if w.teamMode {
			sides[p.team&1] = true
			anyTeam = true
		} else {
			ffaCount++
			ffaInside = id
		}
	}

	if w.teamMode {
		if !anyTeam || (sides[0] && sides[1]) {
			return // пусто или оспаривается
		}
		ctrl := uint8(0)
		if sides[1] {
			ctrl = 1
		}
		// Начисляем каждому игроку контролирующей команды, стоящему внутри.
		for _, id := range w.order {
			p := w.players[id]
			if p.dead || p.team != ctrl {
				continue
			}
			dx, dy := p.X-hillX, p.Y-hillY
			if dx*dx+dy*dy <= r2 {
				p.HillScore++
			}
		}
		return
	}

	// FFA: контроль только если внутри ровно один игрок.
	if ffaCount == 1 {
		w.players[ffaInside].HillScore++
	}
}

// hillLeader — игрок с максимумом очков холма; tiebreak — минимальный id (обход
// возрастающего order строгим сравнением). Аналог leader() для hillMode FFA.
func (w *World) hillLeader() PlayerID {
	var best PlayerID
	bestScore := -1
	for _, id := range w.order {
		if s := int(w.players[id].HillScore); s > bestScore {
			bestScore = s
			best = id
		}
	}
	return best
}

// hillWinningTeam — команда с большей суммой очков холма (итер. 29, teamMode). При
// равенстве — команда 0. Детерминированный обход w.order.
func (w *World) hillWinningTeam() uint8 {
	var s [2]int
	for _, id := range w.order {
		p := w.players[id]
		s[p.team&1] += int(p.HillScore)
	}
	if s[1] > s[0] {
		return 1
	}
	return 0
}
