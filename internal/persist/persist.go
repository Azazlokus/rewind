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
	"errors"
	"fmt"
	"log/slog"
	"time"

	"arena/internal/game"
	"arena/internal/store"
)

// opTimeout ограничивает одну запись в store. Отдельный (не серверный) контекст: при
// shutdown серверный контекст уже отменён, а остаток очереди слить в БД надо — поэтому
// каждая операция получает свежий bounded-контекст от Background.
const opTimeout = 5 * time.Second

// Config настраивает автобан по античиту (итер. 40). Порог 0 — автобан ВЫКЛЮЧЕН
// (безопасный дефолт: rewind_stale может быть высоким пингом легитимного игрока, а не
// читом; статистика всё равно копится для мод-обзора).
type Config struct {
	AntiCheatBanThreshold int64         // сумма событий, с которой автобан; 0 — выключено
	AntiCheatBanDuration  time.Duration // срок автобана; 0 — навсегда
}

// Persister переводит игровые события в записи store. Потокобезопасность самого
// store гарантирует его реализация (database/sql); persister работает в одной горутине.
type Persister struct {
	store store.Store
	log   *slog.Logger
	cfg   Config
}

// New строит persister поверх готового store.
func New(st store.Store, log *slog.Logger, cfg Config) *Persister {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Persister{store: st, log: log, cfg: cfg}
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
		case game.PersistAntiCheat:
			p.recordAntiCheat(msg.AntiCheatAccount, msg.AntiCheatKind, msg.AntiCheatCount)
		default:
			p.log.Warn("unknown persist message", "kind", msg.Kind)
		}
	}
}

// recordAntiCheat копит античит-события аккаунта и, если включён порог, автобанит при
// его превышении (итер. 40). Гости (id 0) уже отсеяны комнатой; проверка дублируется.
func (p *Persister) recordAntiCheat(accountID int64, kind string, n int) {
	if accountID == 0 || n <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	total, err := p.store.AddAntiCheat(ctx, accountID, kind, n, time.Now())
	if err != nil {
		p.log.Warn("persist anticheat", "account", accountID, "err", err)
		return
	}
	if p.cfg.AntiCheatBanThreshold <= 0 || total < p.cfg.AntiCheatBanThreshold {
		return
	}
	p.autoBan(ctx, accountID, total)
}

// autoBan банит аккаунт за превышение античит-порога, если он ещё не забанен, и
// отзывает его сессии (как модераторский бан, итер. 39). CreatedBy 0 — система.
func (p *Persister) autoBan(ctx context.Context, accountID, total int64) {
	if _, err := p.store.ActiveBan(ctx, accountID, time.Now()); err == nil {
		return // уже под активным баном — не дублируем
	} else if !errors.Is(err, store.ErrNotFound) {
		p.log.Warn("anticheat ban check", "account", accountID, "err", err)
		return
	}
	var expires time.Time
	if p.cfg.AntiCheatBanDuration > 0 {
		expires = time.Now().Add(p.cfg.AntiCheatBanDuration)
	}
	if err := p.store.BanAccount(ctx, store.Ban{
		AccountID: accountID,
		Reason:    fmt.Sprintf("anti-cheat: %d rewind violations", total),
		CreatedBy: 0, // система
		CreatedAt: time.Now(),
		ExpiresAt: expires,
	}); err != nil {
		p.log.Warn("anticheat auto-ban", "account", accountID, "err", err)
		return
	}
	if err := p.store.RevokeAllRefreshTokens(ctx, accountID); err != nil {
		p.log.Warn("anticheat revoke sessions", "account", accountID, "err", err)
	}
	p.log.Warn("account auto-banned for anti-cheat", "account", accountID, "total", total)
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
