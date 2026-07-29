// Пакет persist — шов между игрой и хранилищем (итерация 14B).
//
// Комнаты (internal/game) шлют события — смерти и итоги матчей — в канал, ничего не
// зная про store/БД. Persister читает канал в СВОЕЙ горутине (вне горутин комнат) и
// переводит события в вызовы store: статистику убийств копит вживую по смертям,
// историю и games/wins пишет на итог матча. Так запись в БД никогда не попадает на
// горутину цикла комнаты, а игровое ядро остаётся без зависимости на бэкенд.
//
// Границы: persist импортирует game (типы событий) и store (запись). game persist НЕ
// импортирует — стрелка зависимости всегда persist → game, никогда наоборот.
package persist

import (
	"context"
	"log/slog"
	"time"

	"arena/internal/game"
	"arena/internal/store"
)

// opTimeout ограничивает одну запись в store. Отдельный (не серверный) контекст: при
// shutdown серверный контекст уже отменён, а остаток очереди слить в БД надо — поэтому
// каждая операция получает свежий bounded-контекст от Background.
const opTimeout = 5 * time.Second

// Persister переводит игровые события в записи store. Потокобезопасность самого
// store гарантирует его реализация (database/sql); persister работает в одной горутине.
type Persister struct {
	store store.Store
	log   *slog.Logger
}

// New строит persister поверх готового store.
func New(st store.Store, log *slog.Logger) *Persister {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Persister{store: st, log: log}
}

// Run потребляет события, пока in не закроют. Владелец (cmd/server) закрывает канал
// ПОСЛЕ остановки всех комнат (отправителей больше нет) и дожидается возврата Run,
// чтобы хвост очереди успел долиться в БД. Возвращается, когда канал закрыт и осушён.
func (p *Persister) Run(in <-chan game.PersistMsg) {
	for msg := range in {
		switch msg.Kind {
		case game.PersistKill:
			p.recordKill(msg.Killer, msg.Victim)
		case game.PersistMatch:
			p.recordMatch(msg.Match)
		default:
			p.log.Warn("unknown persist message", "kind", msg.Kind)
		}
	}
}

// recordKill копит статистику одной смерти: жертве +1 death, убийце +1 kill. Суицид/
// гашение окружением (killer == victim) даёт только death. Гости (id 0) уже отсеяны
// комнатой, но проверка дублируется на всякий случай.
func (p *Persister) recordKill(killer, victim int64) {
	if victim != 0 {
		p.addStats(victim, store.StatsDelta{Deaths: 1})
	}
	if killer != 0 && killer != victim {
		p.addStats(killer, store.StatsDelta{Kills: 1})
	}
}

func (p *Persister) addStats(accountID int64, d store.StatsDelta) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := p.store.AddStats(ctx, accountID, d); err != nil {
		p.log.Warn("persist stats", "account", accountID, "err", err)
	}
}

// recordMatch пишет историю матча и games/wins участников. store.RecordMatch сам
// отсеивает гостей и добавляет только {Games, Wins} (kills/deaths уже учтены вживую
// в recordKill — не задваиваем). Если зарегистрированных участников нет, матч не
// пишем — пустая строка истории бесполезна.
func (p *Persister) recordMatch(m game.MatchResult) {
	parts := make([]store.MatchParticipant, 0, len(m.Players))
	for _, pl := range m.Players {
		if pl.AccountID == 0 {
			continue
		}
		parts = append(parts, store.MatchParticipant{
			AccountID: pl.AccountID,
			Kills:     int(pl.Kills),
			Deaths:    int(pl.Deaths),
			Won:       pl.Won,
		})
	}
	if len(parts) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	id, err := p.store.RecordMatch(ctx, store.MatchResult{
		Mode:         m.Mode,
		Seed:         m.Seed,
		StartedAt:    m.StartedAt,
		EndedAt:      m.EndedAt,
		Participants: parts,
	})
	if err != nil {
		p.log.Warn("persist match", "err", err)
		return
	}
	p.log.Debug("match recorded", "match_id", id, "participants", len(parts))
}
