// Пакет store — персистентность бэкенда (аккаунты, статистика, история матчей).
//
// Границы. store НЕ знает про игру и сеть: только домен + СУБД. Игровое ядро
// (internal/game) его не импортирует — результаты матчей доезжают до store через
// отдельный persister (итерация 14), а не из горутины комнаты. Одна реализация
// (sqlStore) обслуживает и SQLite, и PostgreSQL через параметризацию диалектом:
// SQLite — для dev/CI/тестов (pure-Go, без внешней СУБД), PostgreSQL — для prod.
//
// Идентичность. В store живут только ЗАРЕГИСТРИРОВАННЫЕ аккаунты. Гости эфемерны
// (имя в токене, без строки в БД — см. internal/account), поэтому их статистика не
// копится и таблицы не пухнут.
package store

import (
	"context"
	"errors"
	"time"
)

// Доменные ошибки. Вызывающий отличает их через errors.Is.
var (
	// ErrNotFound — запрошенной сущности нет.
	ErrNotFound = errors.New("store: not found")
	// ErrUsernameTaken — регистрация на уже занятый username.
	ErrUsernameTaken = errors.New("store: username already taken")
)

// Account — зарегистрированный игрок.
type Account struct {
	ID        int64
	Username  string
	CreatedAt time.Time
}

// Stats — накопленная статистика аккаунта.
type Stats struct {
	AccountID int64
	Kills     int64
	Deaths    int64
	Games     int64
	Wins      int64
}

// StatsDelta — приращение статистики за событие/матч (может быть отрицательным? нет,
// только неотрицательные приращения). Складывается в строку stats аккаунта.
type StatsDelta struct {
	Kills  int
	Deaths int
	Games  int
	Wins   int
}

// LeaderboardEntry — строка таблицы лидеров (аккаунт + агрегаты).
type LeaderboardEntry struct {
	AccountID int64
	Username  string
	Kills     int64
	Deaths    int64
	Wins      int64
}

// MatchResult — итог одного матча для персиста (наполняется в итерации 14, когда
// появится жизненный цикл матча; схема готова уже сейчас).
type MatchResult struct {
	Mode         string
	Seed         int64
	StartedAt    time.Time
	EndedAt      time.Time
	Participants []MatchParticipant
}

// MatchParticipant — вклад одного аккаунта в матч. Гости (AccountID == 0) в историю
// не пишутся.
type MatchParticipant struct {
	AccountID int64
	Kills     int
	Deaths    int
	Won       bool
}

// Match — запись истории матчей для аккаунта.
type Match struct {
	ID      int64
	Mode    string
	EndedAt time.Time
	Kills   int
	Deaths  int
	Won     bool
}

// Store — контракт персистентности. Все методы принимают context для отмены/дедлайна.
// Реализация обязана быть безопасной для конкурентного вызова (её дёргают HTTP-хендлеры
// и persister из разных горутин).
type Store interface {
	// CreateAccount заводит аккаунт с уже вычисленным хешем пароля. Возвращает
	// ErrUsernameTaken, если username занят.
	CreateAccount(ctx context.Context, username, passwordHash string) (Account, error)
	// CredentialsByUsername отдаёт аккаунт и хеш пароля для проверки при логине.
	// ErrNotFound, если такого username нет.
	CredentialsByUsername(ctx context.Context, username string) (Account, string, error)
	// AccountByID — аккаунт по id (ErrNotFound, если нет).
	AccountByID(ctx context.Context, id int64) (Account, error)

	// AddStats прибавляет приращение к статистике аккаунта (создаёт строку stats при
	// первом обращении). Гостей (id 0) вызывающий сюда не передаёт.
	AddStats(ctx context.Context, accountID int64, d StatsDelta) error
	// Stats — накопленная статистика аккаунта (нули, если событий ещё не было).
	Stats(ctx context.Context, accountID int64) (Stats, error)
	// Leaderboard — топ по убийствам, не длиннее limit.
	Leaderboard(ctx context.Context, limit int) ([]LeaderboardEntry, error)

	// RecordMatch пишет матч и вклад участников (итерация 14). Возвращает id матча.
	RecordMatch(ctx context.Context, r MatchResult) (int64, error)
	// MatchesByAccount — недавние матчи аккаунта, не длиннее limit, свежие первыми.
	MatchesByAccount(ctx context.Context, accountID int64, limit int) ([]Match, error)

	// Close освобождает соединение с СУБД.
	Close() error
}
