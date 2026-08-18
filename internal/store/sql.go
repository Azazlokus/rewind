package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// dialect параметризует sqlStore под конкретную СУБД. Отличий всего два: стиль
// плейсхолдеров (? у SQLite, $N у PostgreSQL) и способ вернуть id вставленной
// строки (LastInsertId у SQLite, RETURNING у PostgreSQL). Остальной SQL общий.
type dialect struct {
	name string // "sqlite" | "postgres"
}

func (d dialect) postgres() bool { return d.name == "postgres" }

// rebind переписывает "?" под диалект: PostgreSQL — в $1,$2,...; SQLite — как есть.
func (d dialect) rebind(q string) string {
	if !d.postgres() {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

// execer — общий интерфейс *sql.DB и *sql.Tx: позволяет апсертить статистику как
// отдельным запросом, так и внутри транзакции матча.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// sqlStore — реализация Store поверх database/sql, общая для SQLite и PostgreSQL.
type sqlStore struct {
	db *sql.DB
	d  dialect
}

func (s *sqlStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

func (s *sqlStore) CreateAccount(ctx context.Context, username, passwordHash, email string) (Account, error) {
	now := time.Now().UTC()
	q := `INSERT INTO accounts(username, password_hash, email, created_at) VALUES(?, ?, ?, ?)`
	id, err := s.insertReturningID(ctx, q, username, passwordHash, nullString(email), now.UnixMilli())
	if err != nil {
		if isUniqueViolation(err) {
			// Отличаем занятый email от username по тексту ошибки (индекс/колонка email).
			if email != "" && strings.Contains(err.Error(), "email") {
				return Account{}, ErrEmailTaken
			}
			return Account{}, ErrUsernameTaken
		}
		return Account{}, fmt.Errorf("store: create account: %w", err)
	}
	return Account{ID: id, Username: username, Email: email, CreatedAt: now}, nil
}

func (s *sqlStore) CredentialsByUsername(ctx context.Context, username string) (Account, string, error) {
	var (
		a        Account
		hash     string
		email    sql.NullString
		verified int
		created  int64
	)
	err := s.db.QueryRowContext(ctx,
		s.d.rebind(`SELECT id, username, password_hash, email, email_verified, created_at FROM accounts WHERE username = ?`),
		username).Scan(&a.ID, &a.Username, &hash, &email, &verified, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, "", ErrNotFound
	}
	if err != nil {
		return Account{}, "", fmt.Errorf("store: credentials by username: %w", err)
	}
	a.Email, a.EmailVerified = email.String, verified != 0
	a.CreatedAt = time.UnixMilli(created).UTC()
	return a, hash, nil
}

func (s *sqlStore) AccountByID(ctx context.Context, id int64) (Account, error) {
	return s.accountBy(ctx, `SELECT id, username, email, email_verified, created_at FROM accounts WHERE id = ?`, id)
}

func (s *sqlStore) AccountByEmail(ctx context.Context, email string) (Account, error) {
	return s.accountBy(ctx, `SELECT id, username, email, email_verified, created_at FROM accounts WHERE email = ?`, email)
}

// accountBy читает аккаунт по WHERE-условию с одним аргументом (id или email).
func (s *sqlStore) accountBy(ctx context.Context, query string, arg any) (Account, error) {
	var (
		a        Account
		email    sql.NullString
		verified int
		created  int64
	)
	err := s.db.QueryRowContext(ctx, s.d.rebind(query), arg).
		Scan(&a.ID, &a.Username, &email, &verified, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: account lookup: %w", err)
	}
	a.Email, a.EmailVerified = email.String, verified != 0
	a.CreatedAt = time.UnixMilli(created).UTC()
	return a, nil
}

func (s *sqlStore) SetEmailVerified(ctx context.Context, accountID int64) error {
	if _, err := s.db.ExecContext(ctx,
		s.d.rebind(`UPDATE accounts SET email_verified = 1 WHERE id = ?`), accountID); err != nil {
		return fmt.Errorf("store: set email verified: %w", err)
	}
	return nil
}

func (s *sqlStore) UpdatePassword(ctx context.Context, accountID int64, passwordHash string) error {
	if _, err := s.db.ExecContext(ctx,
		s.d.rebind(`UPDATE accounts SET password_hash = ? WHERE id = ?`), passwordHash, accountID); err != nil {
		return fmt.Errorf("store: update password: %w", err)
	}
	return nil
}

func (s *sqlStore) AddStats(ctx context.Context, accountID int64, d StatsDelta) error {
	if err := addStats(ctx, s.db, s.d, accountID, d); err != nil {
		return fmt.Errorf("store: add stats: %w", err)
	}
	return nil
}

// addStats апсертит приращение статистики через execer (db или tx). Существующую
// строку в DO UPDATE квалифицируем именем таблицы (stats.kills): PostgreSQL считает
// bare-ссылку неоднозначной, раз такая же колонка есть в excluded (42702). SQLite
// табличную квалификацию тоже принимает, поэтому SQL остаётся общим.
func addStats(ctx context.Context, e execer, d dialect, accountID int64, delta StatsDelta) error {
	const q = `INSERT INTO stats(account_id, kills, deaths, games, wins) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(account_id) DO UPDATE SET
  kills  = stats.kills  + excluded.kills,
  deaths = stats.deaths + excluded.deaths,
  games  = stats.games  + excluded.games,
  wins   = stats.wins   + excluded.wins`
	_, err := e.ExecContext(ctx, d.rebind(q), accountID, delta.Kills, delta.Deaths, delta.Games, delta.Wins)
	return err
}

func (s *sqlStore) Stats(ctx context.Context, accountID int64) (Stats, error) {
	st := Stats{AccountID: accountID}
	err := s.db.QueryRowContext(ctx,
		s.d.rebind(`SELECT kills, deaths, games, wins FROM stats WHERE account_id = ?`),
		accountID).Scan(&st.Kills, &st.Deaths, &st.Games, &st.Wins)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil // событий ещё не было — нули
	}
	if err != nil {
		return Stats{}, fmt.Errorf("store: stats: %w", err)
	}
	return st, nil
}

func (s *sqlStore) Leaderboard(ctx context.Context, limit int) ([]LeaderboardEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, s.d.rebind(
		`SELECT a.id, a.username, s.kills, s.deaths, s.wins
		 FROM stats s JOIN accounts a ON a.id = s.account_id
		 ORDER BY s.kills DESC, s.deaths ASC, a.id ASC
		 LIMIT ?`), limit)
	if err != nil {
		return nil, fmt.Errorf("store: leaderboard: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []LeaderboardEntry
	for rows.Next() {
		var e LeaderboardEntry
		if err := rows.Scan(&e.AccountID, &e.Username, &e.Kills, &e.Deaths, &e.Wins); err != nil {
			return nil, fmt.Errorf("store: scan leaderboard: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate leaderboard: %w", err)
	}
	return out, nil
}

func (s *sqlStore) RecordMatch(ctx context.Context, r MatchResult) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin match: %w", err)
	}
	matchID, err := s.txInsertReturningID(ctx, tx,
		`INSERT INTO matches(mode, seed, started_at, ended_at) VALUES(?, ?, ?, ?)`,
		r.Mode, r.Seed, r.StartedAt.UTC().UnixMilli(), r.EndedAt.UTC().UnixMilli())
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("store: insert match: %w", err)
	}
	for _, p := range r.Participants {
		if p.AccountID == 0 {
			continue // гость — в историю не пишем
		}
		if _, err := tx.ExecContext(ctx, s.d.rebind(
			`INSERT INTO match_participants(match_id, account_id, kills, deaths, won) VALUES(?, ?, ?, ?, ?)`),
			matchID, p.AccountID, p.Kills, p.Deaths, boolToInt(p.Won)); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("store: insert participant: %w", err)
		}
		// В stats из матча идут только games/wins: kills/deaths копятся вживую по
		// событиям смертей (см. persister, итерация 14), чтобы не задвоить.
		if err := addStats(ctx, tx, s.d, p.AccountID, StatsDelta{Games: 1, Wins: boolToInt(p.Won)}); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("store: match stats: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit match: %w", err)
	}
	return matchID, nil
}

func (s *sqlStore) MatchesByAccount(ctx context.Context, accountID int64, limit int) ([]Match, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, s.d.rebind(
		`SELECT m.id, m.mode, m.ended_at, mp.kills, mp.deaths, mp.won
		 FROM match_participants mp JOIN matches m ON m.id = mp.match_id
		 WHERE mp.account_id = ?
		 ORDER BY m.ended_at DESC, m.id DESC
		 LIMIT ?`), accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: matches by account: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Match
	for rows.Next() {
		var (
			m     Match
			ended int64
			won   int
		)
		if err := rows.Scan(&m.ID, &m.Mode, &ended, &m.Kills, &m.Deaths, &won); err != nil {
			return nil, fmt.Errorf("store: scan match: %w", err)
		}
		m.EndedAt = time.UnixMilli(ended).UTC()
		m.Won = won != 0
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate matches: %w", err)
	}
	return out, nil
}

func (s *sqlStore) CreateRefreshToken(ctx context.Context, rt RefreshToken) error {
	if _, err := s.insertReturningID(ctx,
		`INSERT INTO refresh_tokens(account_id, family_id, token_hash, issued_at, expires_at, revoked_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		rt.AccountID, rt.FamilyID, rt.TokenHash,
		rt.IssuedAt.UTC().UnixMilli(), rt.ExpiresAt.UTC().UnixMilli(), revokedMillis(rt.RevokedAt)); err != nil {
		return fmt.Errorf("store: create refresh token: %w", err)
	}
	return nil
}

func (s *sqlStore) RefreshTokenByHash(ctx context.Context, hash string) (RefreshToken, error) {
	var (
		rt                       RefreshToken
		issued, expires, revoked int64
	)
	err := s.db.QueryRowContext(ctx, s.d.rebind(
		`SELECT id, account_id, family_id, token_hash, issued_at, expires_at, revoked_at
		 FROM refresh_tokens WHERE token_hash = ?`), hash).
		Scan(&rt.ID, &rt.AccountID, &rt.FamilyID, &rt.TokenHash, &issued, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return RefreshToken{}, ErrNotFound
	}
	if err != nil {
		return RefreshToken{}, fmt.Errorf("store: refresh token by hash: %w", err)
	}
	rt.IssuedAt = time.UnixMilli(issued).UTC()
	rt.ExpiresAt = time.UnixMilli(expires).UTC()
	if revoked != 0 {
		rt.RevokedAt = time.UnixMilli(revoked).UTC()
	}
	return rt, nil
}

func (s *sqlStore) RotateRefreshToken(ctx context.Context, oldID int64, next RefreshToken) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin rotate: %w", err)
	}
	now := next.IssuedAt.UTC().UnixMilli()
	// Отзыв старого — только пока он активен. 0 затронутых строк = уже отозван
	// (гонка/повтор): откатываемся, не плодя второго ребёнка, и сигналим о компрометации.
	res, err := tx.ExecContext(ctx, s.d.rebind(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE id = ? AND revoked_at = 0`), now, oldID)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: revoke rotated: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: rotate rows affected: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return ErrTokenRevoked
	}
	if _, err := tx.ExecContext(ctx, s.d.rebind(
		`INSERT INTO refresh_tokens(account_id, family_id, token_hash, issued_at, expires_at, revoked_at)
		 VALUES(?, ?, ?, ?, ?, ?)`),
		next.AccountID, next.FamilyID, next.TokenHash,
		next.IssuedAt.UTC().UnixMilli(), next.ExpiresAt.UTC().UnixMilli(), revokedMillis(next.RevokedAt)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: insert rotated: %w", err)
	}
	// Ленивая уборка: чистим просроченные токены этого аккаунта в той же транзакции —
	// без фоновой горутины таблица остаётся ограниченной сроком жизни refresh-токена.
	if _, err := tx.ExecContext(ctx, s.d.rebind(
		`DELETE FROM refresh_tokens WHERE account_id = ? AND expires_at < ?`), next.AccountID, now); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("store: prune expired: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit rotate: %w", err)
	}
	return nil
}

func (s *sqlStore) RevokeRefreshFamily(ctx context.Context, familyID string) error {
	now := time.Now().UTC().UnixMilli()
	if _, err := s.db.ExecContext(ctx, s.d.rebind(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at = 0`),
		now, familyID); err != nil {
		return fmt.Errorf("store: revoke refresh family: %w", err)
	}
	return nil
}

func (s *sqlStore) RevokeAllRefreshTokens(ctx context.Context, accountID int64) error {
	now := time.Now().UTC().UnixMilli()
	if _, err := s.db.ExecContext(ctx, s.d.rebind(
		`UPDATE refresh_tokens SET revoked_at = ? WHERE account_id = ? AND revoked_at = 0`),
		now, accountID); err != nil {
		return fmt.Errorf("store: revoke all refresh tokens: %w", err)
	}
	return nil
}

func (s *sqlStore) CreateAccountToken(ctx context.Context, t AccountToken) error {
	if _, err := s.insertReturningID(ctx,
		`INSERT INTO account_tokens(account_id, kind, token_hash, expires_at, used_at) VALUES(?, ?, ?, ?, 0)`,
		t.AccountID, string(t.Kind), t.TokenHash, t.ExpiresAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("store: create account token: %w", err)
	}
	return nil
}

func (s *sqlStore) ConsumeAccountToken(ctx context.Context, hash string, kind AccountTokenKind, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin consume token: %w", err)
	}
	var id, accountID, used, expires int64
	err = tx.QueryRowContext(ctx, s.d.rebind(
		`SELECT id, account_id, used_at, expires_at FROM account_tokens WHERE token_hash = ? AND kind = ?`),
		hash, string(kind)).Scan(&id, &accountID, &used, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return 0, ErrNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("store: consume token lookup: %w", err)
	}
	nowMs := now.UTC().UnixMilli()
	if used != 0 || nowMs > expires {
		_ = tx.Rollback()
		return 0, ErrNotFound // потрачен или просрочен — как несуществующий
	}
	// Пометка «потрачен» под гонкой: 0 затронутых строк = кто-то потратил параллельно.
	res, err := tx.ExecContext(ctx, s.d.rebind(
		`UPDATE account_tokens SET used_at = ? WHERE id = ? AND used_at = 0`), nowMs, id)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("store: consume token mark: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("store: consume token rows: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return 0, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit consume token: %w", err)
	}
	return accountID, nil
}

// nullString отдаёт nil для пустой строки (колонка NULL), иначе саму строку.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// revokedMillis кодирует RevokedAt: нулевое время — 0 (активен), иначе unix-миллисекунды.
func revokedMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

// insertReturningID вставляет строку и возвращает её id, скрывая разницу диалектов.
func (s *sqlStore) insertReturningID(ctx context.Context, query string, args ...any) (int64, error) {
	if s.d.postgres() {
		var id int64
		err := s.db.QueryRowContext(ctx, s.d.rebind(query+` RETURNING id`), args...).Scan(&id)
		return id, err
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// txInsertReturningID — то же внутри транзакции.
func (s *sqlStore) txInsertReturningID(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	if s.d.postgres() {
		var id int64
		err := tx.QueryRowContext(ctx, s.d.rebind(query+` RETURNING id`), args...).Scan(&id)
		return id, err
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation распознаёт нарушение UNIQUE у обоих драйверов по тексту ошибки
// (modernc SQLite — "UNIQUE constraint failed"; pgx — SQLSTATE 23505), не завязываясь
// на конкретные типы драйверов.
func isUniqueViolation(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key")
}
