package game

// Киллстрики и окно неуязвимости (итерация 20).
//
// Две связанные боевые механики, обе — часть детерминированной симуляции (входят в
// Checksum, реплей-безопасны), без розыгрышей rng:
//
//   - Окно неуязвимости (spawn protection): свежереспаунившийся игрок несколько
//     секунд неуязвим — снаряды проходят сквозь него (findHit пропускает), урона и
//     событий нет. Анти-спавн-килл: нельзя фармить того, кто только возродился в
//     гуще боя. Щит спадает, как только игрок сам стреляет (tryFire) — нельзя
//     отсиживаться под щитом и безнаказанно бить.
//   - Киллстрики: серия убийств без смертей (Player.streak). Каждые killstreakStep
//     фрагов подряд — веха: игрок мгновенно долечивается и получает короткий щит
//     (power spike), а всем уходит reliable-событие MsgKillstreak (объявление/
//     фид). Смерть и старт нового матча обнуляют серию.
//
// Неуязвимость первичного входа (AddPlayer) НЕ даётся: щит — только на респауне
// (анти-фарм именно возрождения) и на вехе стрика. Так боевые тесты, стреляющие
// сразу после AddPlayer, остаются валидны, а семантика «зашёл — ты в игре, возродился
// — тебя прикрыли» осмысленна.

const (
	// spawnInvulnTicks — длительность окна неуязвимости после респауна (~2 с при 30 Гц).
	spawnInvulnTicks = 60
	// killstreakStep — серия убийств, дающая веху (награду). Веха на каждом кратном.
	killstreakStep = 3
	// killstreakInvulnTicks — длительность щита, выдаваемого на вехе стрика (~1.5 с).
	killstreakInvulnTicks = 45
)

// invulnerable сообщает, защищён ли игрок окном неуязвимости на текущем тике.
func (p *Player) invulnerable(tick uint32) bool { return tick < p.invulnUntil }

// recordKill обновляет серию убийств после того, как attacker убил victim, и — при
// достижении вехи — награждает: долечивает и даёт короткий щит, эмитя EventKillstreak.
// Зовётся из applyDamage на смерти жертвы (attacker != victim, оба валидны).
// Детерминировано: rng не трогает, порядок эмита событий задан порядком applyDamage.
func (w *World) recordKill(attacker *Player) {
	attacker.streak++
	if attacker.streak%killstreakStep != 0 {
		return
	}
	// Веха: power spike — полный хил + короткий щит.
	attacker.HP = 100
	attacker.invulnUntil = w.Tick + killstreakInvulnTicks
	w.events = append(w.events, Event{Kind: EventKillstreak, Target: attacker.ID, Streak: attacker.streak})
}
