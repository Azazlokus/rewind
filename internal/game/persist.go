package game

import "time"

// Персист-шов (итерация 14B). Комната шлёт сюда-определённые сообщения в канал
// Config.PersistSink; читает их отдельный persister (internal/persist), который
// переводит их в вызовы store. Игра про store/БД ничего не знает — только
// конструирует эти чистые данные. Отправка из комнаты неблокирующая: переполнение
// канала роняет сообщение, но НИКОГДА не тормозит горутину цикла (см. Room.sendPersist).

// PersistKind различает сообщения к persister.
type PersistKind uint8

const (
	// PersistKill — инкремент статистики за одну смерть. Kills убийце, Deaths жертве;
	// суицид/гашение окружением (Killer == Victim) даёт только Deaths.
	PersistKill PersistKind = iota + 1
	// PersistMatch — итог завершившегося матча (games/wins + история).
	PersistMatch
)

// PersistMsg — единица работы для persister. Значимо ровно одно наполнение,
// выбранное Kind: для PersistKill — Killer/Victim, для PersistMatch — Match.
type PersistMsg struct {
	Kind PersistKind
	// Аккаунты убийцы и жертвы (PersistKill); 0 — гость, persister его пропускает.
	Killer int64
	Victim int64
	// Итог матча (PersistMatch).
	Match MatchResult
}

// MatchResult — итог одного матча для персиста. Игровую часть (участники + winner)
// заполняет World.MatchResult; Mode/Seed/времена проставляет комната (она владеет
// Clock и конфигом). Гости (AccountID == 0) остаются в списке — persister сам их
// отфильтрует.
type MatchResult struct {
	Mode      string
	Seed      int64
	StartedAt time.Time
	EndedAt   time.Time
	Winner    PlayerID
	Players   []MatchResultPlayer
}

// MatchResultPlayer — вклад одного участника в матч.
type MatchResultPlayer struct {
	AccountID int64
	Kills     uint16
	Deaths    uint16
	Won       bool
}

// MatchResult снимает итог текущего матча. Зовётся комнатой на переходе бой →
// антракт (счёт ещё не обнулён startMatch), это не горячий путь, поэтому метод
// свободно аллоцирует свежий срез участников — получатель волен держать его
// сколько угодно (комната больше на него не ссылается, гонки нет). Обход только по
// w.order — порядок детерминирован, но для персиста это несущественно.
func (w *World) MatchResult() MatchResult {
	players := make([]MatchResultPlayer, 0, len(w.order))
	for _, id := range w.order {
		p := w.players[id]
		players = append(players, MatchResultPlayer{
			AccountID: p.AccountID,
			Kills:     p.Kills,
			Deaths:    p.Deaths,
			Won:       id == w.winner,
		})
	}
	return MatchResult{Winner: w.winner, Players: players}
}
