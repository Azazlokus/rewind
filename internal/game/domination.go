package game

// Доминация (итерация 30): режим захвата нескольких контрольных точек. Это прямое
// обобщение King of the Hill (итер. 29) с одной зоны на N: на арене — несколько
// фиксированных круглых зон, и для КАЖДОЙ действует та же логика контроля, что у
// холма (ровно одна сторона внутри — она копит очки; пусто/оспаривается — никто).
// Победитель матча — по сумме очков доминации, а не по фрагам.
//
// Полностью детерминирован (обход w.order по индексу, без rng/time/map), поэтому
// накопленный Player.DomScore ВХОДИТ в Checksum и реплей-безопасен. Совместим и с
// FFA (сторона = игрок), и с командным режимом (сторона = команда, итер. 23).
//
// Геометрия точек статична и одинакова во всех мирах, поэтому в Checksum НЕ входит
// (как walls/pickupSpots/hill): в хэш идёт лишь её следствие — Player.DomScore.
// Раскладку зеркалит клиент (web/game.js: SIM.DomPoints) для отрисовки зон.
//
// Модель начисления умышленно повторяет холм по-зонно: игрок стоит не более чем в
// одной зоне (зоны не пересекаются), поэтому копит ≤1 очка за тик. Преимущество
// команды даёт УДЕРЖАНИЕ БОЛЬШЕГО ЧИСЛА зон разными игроками (их DomScore суммируется).
// Складирование всех в одну зону не выгоднее рассредоточения: брошенные зоны
// достаются сопернику и кормят его очки — стимул держать больше зон эмерджентен.

// controlPoint — центр фиксированной контрольной точки в мировых координатах.
type controlPoint struct{ x, y float32 }

// domPoints — фиксированная раскладка контрольных точек (обход по индексу —
// детерминированно). Три зоны треугольником в открытых квадрантах: подальше от краёв
// и вне стен (те занимают x∈[1500..3100], y∈[1400..2420]). Значения обязаны совпадать
// с SIM.DomPoints в web/game.js.
var domPoints = []controlPoint{
	{1024, 1024}, // A — верхне-левый открытый квадрант
	{3072, 1024}, // B — верхне-правый открытый квадрант
	{2048, 3300}, // C — низ по центру (ниже стен)
}

// domRadius — радиус зоны контроля (чуть меньше холма: зон несколько, широкие
// перекрывали бы полкарты).
const domRadius float32 = 256

// stepDomination начисляет очки контроля по всем контрольным точкам за тик (итер. 30).
// Зовётся из World.Step в режиме domMode во время активного матча, по актуальным
// (после движения) позициям. No-op вне domMode/активного матча.
//
// Для каждой точки применяется та же логика, что у холма: собираем живых игроков в
// круге и множество их «сторон» (команда в teamMode, иначе id игрока); ровно одна
// сторона внутри → она контролирует зону: каждому её игроку внутри +1 к DomScore.
// Ноль или ≥2 сторон (пусто/оспаривается) — по этой зоне никто не копит. Обход по
// w.order — детерминированно.
func (w *World) stepDomination() {
	if !w.domMode || w.matchPhase != matchActive {
		return
	}
	const r2 = domRadius * domRadius
	for pi := range domPoints {
		cp := &domPoints[pi]

		// Первый проход: кто внутри этой зоны и сколько различных сторон.
		var sides [2]bool // teamMode: присутствие команд 0/1
		var ffaInside PlayerID
		var ffaCount int
		anyTeam := false
		for _, id := range w.order {
			p := w.players[id]
			if p.dead {
				continue
			}
			dx, dy := p.X-cp.x, p.Y-cp.y
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
				continue // пусто или оспаривается
			}
			ctrl := uint8(0)
			if sides[1] {
				ctrl = 1
			}
			// Начисляем каждому игроку контролирующей команды, стоящему в этой зоне.
			for _, id := range w.order {
				p := w.players[id]
				if p.dead || p.team != ctrl {
					continue
				}
				dx, dy := p.X-cp.x, p.Y-cp.y
				if dx*dx+dy*dy <= r2 {
					p.DomScore++
				}
			}
			continue
		}

		// FFA: контроль зоны только если внутри ровно один игрок.
		if ffaCount == 1 {
			w.players[ffaInside].DomScore++
		}
	}
}

// domLeader — игрок с максимумом очков доминации; tiebreak — минимальный id (обход
// возрастающего order строгим сравнением). Аналог hillLeader для domMode FFA.
func (w *World) domLeader() PlayerID {
	var best PlayerID
	bestScore := -1
	for _, id := range w.order {
		if s := int(w.players[id].DomScore); s > bestScore {
			bestScore = s
			best = id
		}
	}
	return best
}

// domWinningTeam — команда с большей суммой очков доминации (итер. 30, teamMode). При
// равенстве — команда 0. Детерминированный обход w.order.
func (w *World) domWinningTeam() uint8 {
	var s [2]int
	for _, id := range w.order {
		p := w.players[id]
		s[p.team&1] += int(p.DomScore)
	}
	if s[1] > s[0] {
		return 1
	}
	return 0
}
