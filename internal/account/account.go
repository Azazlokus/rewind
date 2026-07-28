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

// Service выдаёт и проверяет токен-сессии, регистрирует и логинит аккаунты.
type Service struct {
	store  store.Store
	secret []byte
	ttl    time.Duration
	clock  func() time.Time // подменяется в тестах; nil — time.Now
}

// NewService строит сервис. secret — ключ подписи токенов (обязателен и секретен),
// ttl — время жизни токена (0 — 24 часа).
func NewService(st store.Store, secret []byte, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Service{store: st, secret: secret, ttl: ttl}
}

// Register заводит аккаунт и возвращает его identity и токен-сессию.
func (s *Service) Register(ctx context.Context, username, password string) (Identity, string, error) {
	if err := validateUsername(username); err != nil {
		return Identity{}, "", err
	}
	if err := validatePassword(password); err != nil {
		return Identity{}, "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return Identity{}, "", err
	}
	acc, err := s.store.CreateAccount(ctx, username, hash)
	if errors.Is(err, store.ErrUsernameTaken) {
		return Identity{}, "", ErrUsernameTaken
	}
	if err != nil {
		return Identity{}, "", fmt.Errorf("account: register: %w", err)
	}
	return s.identityWithToken(Identity{AccountID: acc.ID, Name: acc.Username})
}

// Login проверяет пару логин/пароль и возвращает identity и токен.
func (s *Service) Login(ctx context.Context, username, password string) (Identity, string, error) {
	acc, hash, err := s.store.CredentialsByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, "", ErrInvalidCredentials
	}
	if err != nil {
		return Identity{}, "", fmt.Errorf("account: login: %w", err)
	}
	ok, err := verifyPassword(password, hash)
	if err != nil {
		return Identity{}, "", err
	}
	if !ok {
		return Identity{}, "", ErrInvalidCredentials
	}
	return s.identityWithToken(Identity{AccountID: acc.ID, Name: acc.Username})
}

// Guest выдаёт эфемерную гостевую identity (без записи в БД) и токен.
func (s *Service) Guest(name string) (Identity, string, error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Identity{}, "", err
	}
	return s.identityWithToken(Identity{Name: name, Guest: true})
}

// Verify проверяет токен и возвращает identity. Через него авторизуются и HTTP-API,
// и join игровой сессии.
func (s *Service) Verify(token string) (Identity, error) {
	c, err := s.parse(token)
	if err != nil {
		return Identity{}, err
	}
	return Identity{AccountID: c.AID, Name: c.Name, Guest: c.Guest}, nil
}

func (s *Service) identityWithToken(id Identity) (Identity, string, error) {
	tok, err := s.sign(claims{
		AID: id.AccountID, Name: id.Name, Guest: id.Guest,
		Exp: s.now().Add(s.ttl).Unix(),
	})
	if err != nil {
		return Identity{}, "", fmt.Errorf("account: issue token: %w", err)
	}
	return id, tok, nil
}

func validateUsername(u string) error {
	if len(u) < minUsernameLen || len(u) > maxUsernameLen {
		return fmt.Errorf("%w: username must be %d–%d chars", ErrValidation, minUsernameLen, maxUsernameLen)
	}
	for _, r := range u {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return fmt.Errorf("%w: username allows only letters, digits, underscore", ErrValidation)
		}
	}
	return nil
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
