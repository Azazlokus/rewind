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
	"net/mail"
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
	// ErrEmailTaken — регистрация на уже занятый email.
	ErrEmailTaken = errors.New("account: email already taken")
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
	mailer     Mailer           // доставка verify/reset (итер. 37); по умолчанию LogMailer
	verifyTTL  time.Duration    // срок жизни токена верификации email
	resetTTL   time.Duration    // срок жизни токена сброса пароля
	clock      func() time.Time // подменяется в тестах; nil — time.Now
}

// Option — необязательная настройка Service (итер. 37: почта и сроки одноразовых токенов).
type Option func(*Service)

// WithMailer подключает доставку писем верификации/сброса (nil игнорируется).
func WithMailer(m Mailer) Option {
	return func(s *Service) {
		if m != nil {
			s.mailer = m
		}
	}
}

// WithTokenTTLs задаёт сроки жизни токенов верификации email и сброса пароля (≤0 —
// оставить дефолт).
func WithTokenTTLs(verify, reset time.Duration) Option {
	return func(s *Service) {
		if verify > 0 {
			s.verifyTTL = verify
		}
		if reset > 0 {
			s.resetTTL = reset
		}
	}
}

// NewService строит сервис. secret — ключ подписи access-токенов (обязателен и
// секретен), accessTTL — время жизни access-токена (0 — 15 минут), refreshTTL — время
// жизни refresh-токена (0 — 30 дней). По умолчанию почта — LogMailer (dev), verifyTTL
// 24 ч, resetTTL 1 ч; переопределяется опциями.
func NewService(st store.Store, secret []byte, accessTTL, refreshTTL time.Duration, opts ...Option) *Service {
	if accessTTL <= 0 {
		accessTTL = 15 * time.Minute
	}
	if refreshTTL <= 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	s := &Service{
		store: st, secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL,
		mailer: LogMailer{}, verifyTTL: 24 * time.Hour, resetTTL: time.Hour,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Register заводит аккаунт и возвращает его identity и пару токенов. email опционален:
// если задан — валидируется, проверяется на занятость и на него уходит письмо
// верификации (итер. 37); пустой — аккаунт без email.
func (s *Service) Register(ctx context.Context, username, password, email string) (Identity, Tokens, error) {
	if err := validateUsername(username); err != nil {
		return Identity{}, Tokens{}, err
	}
	if err := validatePassword(password); err != nil {
		return Identity{}, Tokens{}, err
	}
	email = strings.TrimSpace(email)
	if email != "" {
		if err := validateEmail(email); err != nil {
			return Identity{}, Tokens{}, err
		}
		// Пре-проверка занятости email: иначе unique-нарушение неоднозначно с username.
		if _, err := s.store.AccountByEmail(ctx, email); err == nil {
			return Identity{}, Tokens{}, ErrEmailTaken
		} else if !errors.Is(err, store.ErrNotFound) {
			return Identity{}, Tokens{}, fmt.Errorf("account: register email check: %w", err)
		}
	}
	hash, err := hashPassword(password)
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	acc, err := s.store.CreateAccount(ctx, username, hash, email)
	if errors.Is(err, store.ErrUsernameTaken) {
		return Identity{}, Tokens{}, ErrUsernameTaken
	}
	if errors.Is(err, store.ErrEmailTaken) {
		return Identity{}, Tokens{}, ErrEmailTaken
	}
	if err != nil {
		return Identity{}, Tokens{}, fmt.Errorf("account: register: %w", err)
	}
	if email != "" {
		if err := s.sendVerification(ctx, acc.ID, email); err != nil {
			return Identity{}, Tokens{}, err
		}
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
	rt, err := s.store.RefreshTokenByHash(ctx, hashToken(refresh))
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
	newToken, err := newSecretToken()
	if err != nil {
		return Identity{}, Tokens{}, err
	}
	now := s.now()
	err = s.store.RotateRefreshToken(ctx, rt.ID, store.RefreshToken{
		AccountID: acc.ID, FamilyID: rt.FamilyID, TokenHash: hashToken(newToken),
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
	rt, err := s.store.RefreshTokenByHash(ctx, hashToken(refresh))
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

// VerifyEmail подтверждает email по одноразовому токену (итер. 37). Невалидный/
// потраченный/просроченный токен — ErrBadToken.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	if token == "" {
		return ErrBadToken
	}
	accountID, err := s.store.ConsumeAccountToken(ctx, hashToken(token), store.TokenVerifyEmail, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return ErrBadToken
	}
	if err != nil {
		return fmt.Errorf("account: verify email: %w", err)
	}
	if err := s.store.SetEmailVerified(ctx, accountID); err != nil {
		return fmt.Errorf("account: set verified: %w", err)
	}
	return nil
}

// RequestPasswordReset инициирует сброс: если на email есть аккаунт, кладёт токен сброса
// и шлёт его письмом. Всегда возвращает nil на неизвестный/пустой email — не раскрываем
// существование аккаунта (анти-энумерация).
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	acc, err := s.store.AccountByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("account: reset lookup: %w", err)
	}
	token, err := newSecretToken()
	if err != nil {
		return err
	}
	if err := s.store.CreateAccountToken(ctx, store.AccountToken{
		AccountID: acc.ID, Kind: store.TokenPasswordReset,
		TokenHash: hashToken(token), ExpiresAt: s.now().Add(s.resetTTL),
	}); err != nil {
		return fmt.Errorf("account: store reset token: %w", err)
	}
	if err := s.mailer.SendPasswordReset(ctx, email, token); err != nil {
		return fmt.Errorf("account: send reset: %w", err)
	}
	return nil
}

// ResetPassword меняет пароль по одноразовому токену сброса и разлогинивает все сессии
// аккаунта (все refresh-токены отзываются). Невалидный/потраченный/просроченный токен —
// ErrBadToken; слабый пароль — ErrValidation (проверяется ДО расхода токена).
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if token == "" {
		return ErrBadToken
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}
	accountID, err := s.store.ConsumeAccountToken(ctx, hashToken(token), store.TokenPasswordReset, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return ErrBadToken
	}
	if err != nil {
		return fmt.Errorf("account: reset password: %w", err)
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := s.store.UpdatePassword(ctx, accountID, hash); err != nil {
		return fmt.Errorf("account: update password: %w", err)
	}
	if err := s.store.RevokeAllRefreshTokens(ctx, accountID); err != nil {
		return fmt.Errorf("account: revoke sessions: %w", err)
	}
	return nil
}

// sendVerification кладёт токен верификации email и отправляет его почтой.
func (s *Service) sendVerification(ctx context.Context, accountID int64, email string) error {
	token, err := newSecretToken()
	if err != nil {
		return err
	}
	if err := s.store.CreateAccountToken(ctx, store.AccountToken{
		AccountID: accountID, Kind: store.TokenVerifyEmail,
		TokenHash: hashToken(token), ExpiresAt: s.now().Add(s.verifyTTL),
	}); err != nil {
		return fmt.Errorf("account: store verification token: %w", err)
	}
	if err := s.mailer.SendVerification(ctx, email, token); err != nil {
		return fmt.Errorf("account: send verification: %w", err)
	}
	return nil
}

// IsBanned сообщает, есть ли у аккаунта действующий бан (итер. 39). Через это шлюз
// отказывает забаненному в join, а /api/me показывает статус. Гости (id 0) не банятся.
func (s *Service) IsBanned(ctx context.Context, accountID int64) (store.Ban, bool, error) {
	if accountID == 0 {
		return store.Ban{}, false, nil
	}
	ban, err := s.store.ActiveBan(ctx, accountID, s.now())
	if errors.Is(err, store.ErrNotFound) {
		return store.Ban{}, false, nil
	}
	if err != nil {
		return store.Ban{}, false, fmt.Errorf("account: is banned: %w", err)
	}
	return ban, true, nil
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
	token, err := newSecretToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	if err := s.store.CreateRefreshToken(ctx, store.RefreshToken{
		AccountID: accountID, FamilyID: family, TokenHash: hashToken(token),
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

// validateEmail — базовая проверка адреса через net/mail: разбирается как единственный
// адрес без display-name, длина в разумных пределах.
func validateEmail(e string) error {
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || len(e) > 254 {
		return fmt.Errorf("%w: invalid email", ErrValidation)
	}
	return nil
}
