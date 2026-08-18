// Пакет account — идентичность игроков: гости и зарегистрированные аккаунты.
//
// Границы. account знает про store (персистентность аккаунтов) и криптографию
// (argon2id для паролей, HMAC для токен-сессий), но НЕ про игру и сеть. Токен
// самодостаточный и подписанный — сервер проверяет его без обращения к БД, а join
// игровой сессии несёт токен, по которому сессия связывается с аккаунтом.
//
// Гости эфемерны: имя живёт только в токене, строки в БД нет (AccountID == 0).
package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"arena/internal/store"
)

// Доменные ошибки; API-слой мапит их в HTTP-коды.
var (
	// ErrValidation — некорректный username/password/имя (оборачивается с деталью).
	ErrValidation = errors.New("account: validation failed")
	// ErrUsernameTaken — регистрация на занятый username.
	ErrUsernameTaken = errors.New("account: username already taken")
	// ErrInvalidCredentials — неверная пара логин/пароль (не раскрываем, что именно).
	ErrInvalidCredentials = errors.New("account: invalid credentials")
	// ErrBadToken — токен невалиден/просрочен/подделан.
	ErrBadToken = errors.New("account: bad token")
	// ErrBadHash — хранящийся хеш пароля повреждён.
	ErrBadHash = errors.New("account: bad password hash")
)

const (
	minUsernameLen = 3
	maxUsernameLen = 16
	minPasswordLen = 8
	maxPasswordLen = 128
	maxNameLen     = 16 // совпадает с игровым protocol.MaxNameLen
)

// Identity — кто играет: аккаунт (Guest=false, AccountID>0) или гость (Guest=true,
// AccountID=0).
type Identity struct {
	AccountID int64
	Name      string
	Guest     bool
}

// Tokens — то, что получает клиент при логине/регистрации/refresh: короткоживущий
// access-токен (проверяет и API, и game-join, без БД) и долгоживущий refresh-токен для
// его обновления. У гостя Refresh пуст: гости эфемерны (строки в БД нет — обновлять
// нечего), access-токен они просто перевыпускают через /guest.
type Tokens struct {
	Access    string
	Refresh   string
	ExpiresIn int // время жизни access-токена, секунды (клиент планирует обновление)
}

// Service выдаёт и проверяет токен-сессии, регистрирует и логинит аккаунты.
type Service struct {
	store      store.Store
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	clock      func() time.Time // подменяется в тестах; nil — time.Now
}

// NewService строит сервис. secret — ключ подписи access-токенов (обязателен и
// секретен), accessTTL — время жизни access-токена (0 — 15 минут), refreshTTL — время
// жизни refresh-токена (0 — 30 дней).
func NewService(st store.Store, secret []byte, accessTTL, refreshTTL time.Duration) *Service {
	if accessTTL <= 0 {
		accessTTL = 15 * time.Minute
	}
	if refreshTTL <= 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	return &Service{store: st, secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL}
}

// Register заводит аккаунт и возвращает его identity и пару токенов.
func (s *Service) Register(ctx context.Context, username, password string) (Identity, Tokens, error) {
	if err := validateUsername(username); err != nil {
		return Identity{}, Tokens{}, err
	}
	if err := validatePassword(password); err != nil {
		return Identity{}, Tokens{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	acc, err := s.store.CreateAccount(ctx, username, hash)
	if errors.Is(err, store.ErrUsernameTaken) {
		return Identity{}, Tokens{}, ErrUsernameTaken
	}
	if err != nil {
		return Identity{}, Tokens{}, fmt.Errorf("account: register: %w", err)
	}
	id := Identity{AccountID: acc.ID, Name: acc.Username}
	toks, err := s.issueLogin(ctx, id)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	return id, toks, nil
}

// Login проверяет пару логин/пароль и возвращает identity и пару токенов.
func (s *Service) Login(ctx context.Context, username, password string) (Identity, Tokens, error) {
	acc, hash, err := s.store.CredentialsByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, Tokens{}, ErrInvalidCredentials
	}
	if err != nil {
		return Identity{}, Tokens{}, fmt.Errorf("account: login: %w", err)
	}
	ok, err := verifyPassword(password, hash)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	if !ok {
		return Identity{}, Tokens{}, ErrInvalidCredentials
	}
	id := Identity{AccountID: acc.ID, Name: acc.Username}
	toks, err := s.issueLogin(ctx, id)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	return id, toks, nil
}

// Guest выдаёт эфемерную гостевую identity (без записи в БД) и access-токен без refresh.
func (s *Service) Guest(name string) (Identity, Tokens, error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Identity{}, Tokens{}, err
	}
	id := Identity{Name: name, Guest: true}
	access, err := s.accessToken(id)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	return id, Tokens{Access: access, ExpiresIn: int(s.accessTTL.Seconds())}, nil
}

// Refresh проверяет refresh-токен, ротирует его (старый отзывается, выдаётся новый в
// том же семействе) и возвращает свежую пару токенов. Повторное предъявление уже
// отозванного токена трактуется как компрометация: гасится всё семейство, дальше клиент
// обязан перелогиниться.
func (s *Service) Refresh(ctx context.Context, refresh string) (Identity, Tokens, error) {
	if refresh == "" {
		return Identity{}, Tokens{}, ErrBadToken
	}
	rt, err := s.store.RefreshTokenByHash(ctx, refreshHash(refresh))
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, Tokens{}, ErrBadToken
	}
	if err != nil {
		return Identity{}, Tokens{}, fmt.Errorf("account: refresh lookup: %w", err)
	}
	// Отозванный/использованный токен предъявлен повторно — гасим семейство.
	if !rt.RevokedAt.IsZero() {
		if err := s.store.RevokeRefreshFamily(ctx, rt.FamilyID); err != nil {
			return Identity{}, Tokens{}, fmt.Errorf("account: revoke family: %w", err)
		}
		return Identity{}, Tokens{}, ErrBadToken
	}
	if s.now().After(rt.ExpiresAt) {
		return Identity{}, Tokens{}, ErrBadToken
	}
	acc, err := s.store.AccountByID(ctx, rt.AccountID)
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, Tokens{}, ErrBadToken
	}
	if err != nil {
		return Identity{}, Tokens{}, fmt.Errorf("account: refresh account: %w", err)
	}
	newToken, err := newRefreshSecret()
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	now := s.now()
	err = s.store.RotateRefreshToken(ctx, rt.ID, store.RefreshToken{
		AccountID: acc.ID, FamilyID: rt.FamilyID, TokenHash: refreshHash(newToken),
		IssuedAt: now, ExpiresAt: now.Add(s.refreshTTL),
	})
	// Токен отозвали между проверкой и ротацией (гонка/повтор) — тоже компрометация.
	if errors.Is(err, store.ErrTokenRevoked) {
		if rerr := s.store.RevokeRefreshFamily(ctx, rt.FamilyID); rerr != nil {
			return Identity{}, Tokens{}, fmt.Errorf("account: revoke family: %w", rerr)
		}
		return Identity{}, Tokens{}, ErrBadToken
	}
	if err != nil {
		return Identity{}, Tokens{}, fmt.Errorf("account: rotate refresh: %w", err)
	}
	id := Identity{AccountID: acc.ID, Name: acc.Username}
	access, err := s.accessToken(id)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	return id, Tokens{Access: access, Refresh: newToken, ExpiresIn: int(s.accessTTL.Seconds())}, nil
}

// Logout отзывает семейство refresh-токенов предъявленного токена (весь логин-сеанс).
// Идемпотентно: пустой/неизвестный токен — no-op (не раскрываем, существует ли он).
func (s *Service) Logout(ctx context.Context, refresh string) error {
	if refresh == "" {
		return nil
	}
	rt, err := s.store.RefreshTokenByHash(ctx, refreshHash(refresh))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("account: logout lookup: %w", err)
	}
	if err := s.store.RevokeRefreshFamily(ctx, rt.FamilyID); err != nil {
		return fmt.Errorf("account: logout revoke: %w", err)
	}
	return nil
}

// Verify проверяет access-токен и возвращает identity. Через него авторизуются и
// HTTP-API, и join игровой сессии — без обращения к БД.
func (s *Service) Verify(token string) (Identity, error) {
	c, err := s.parse(token)
	if err != nil {
		return Identity{}, err
	}
	return Identity{AccountID: c.AID, Name: c.Name, Guest: c.Guest}, nil
}

// issueLogin выдаёт пару токенов новому логину: access и refresh в НОВОМ семействе
// (хеш refresh пишется в store). Только для аккаунтов (id.AccountID != 0).
func (s *Service) issueLogin(ctx context.Context, id Identity) (Tokens, error) {
	access, err := s.accessToken(id)
	if err != nil {
		return Tokens{}, err
	}
	family, err := newFamilyID()
	if err != nil {
		return Tokens{}, err
	}
	refresh, err := s.storeRefresh(ctx, id.AccountID, family)
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{Access: access, Refresh: refresh, ExpiresIn: int(s.accessTTL.Seconds())}, nil
}

// storeRefresh генерирует refresh-токен, кладёт его хеш в store и возвращает открытый
// токен (его видит только клиент).
func (s *Service) storeRefresh(ctx context.Context, accountID int64, family string) (string, error) {
	token, err := newRefreshSecret()
	if err != nil {
		return "", err
	}
	now := s.now()
	if err := s.store.CreateRefreshToken(ctx, store.RefreshToken{
		AccountID: accountID, FamilyID: family, TokenHash: refreshHash(token),
		IssuedAt: now, ExpiresAt: now.Add(s.refreshTTL),
	}); err != nil {
		return "", fmt.Errorf("account: store refresh: %w", err)
	}
	return token, nil
}

// accessToken подписывает короткоживущий self-contained access-токен.
func (s *Service) accessToken(id Identity) (string, error) {
	tok, err := s.sign(claims{
		AID: id.AccountID, Name: id.Name, Guest: id.Guest,
		Exp: s.now().Add(s.accessTTL).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("account: issue token: %w", err)
	}
	return tok, nil
}

func validateUsername(u string) error {
	if len(u) < minUsernameLen || len(u) > maxUsernameLen {
		return fmt.Errorf("%w: username must be %d–%d chars", ErrValidation, minUsernameLen, maxUsernameLen)
	}
	for _, r := range u {
		if !isUsernameChar(r) {
			return fmt.Errorf("%w: username allows only letters, digits, underscore", ErrValidation)
		}
	}
	return nil
}

// isUsernameChar — разрешённый в username символ: латиница, цифры, подчёркивание.
func isUsernameChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
}

func validatePassword(p string) error {
	if len(p) < minPasswordLen || len(p) > maxPasswordLen {
		return fmt.Errorf("%w: password must be %d–%d chars", ErrValidation, minPasswordLen, maxPasswordLen)
	}
	return nil
}

func validateName(n string) error {
	if n == "" || utf8.RuneCountInString(n) > maxNameLen || !utf8.ValidString(n) {
		return fmt.Errorf("%w: name must be 1–%d characters", ErrValidation, maxNameLen)
	}
	return nil
}
