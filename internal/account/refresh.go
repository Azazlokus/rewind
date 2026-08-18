package account

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Refresh-токены (итерация 36). В отличие от access-токена (самодостаточный HMAC,
// проверяется без БД), refresh-токен — непрозрачная случайная строка, живущая в store:
// его можно отозвать (logout) и ротировать. На сервере лежит только его SHA-256 —
// утечка БД не раскрывает действующих токенов. Быстрый хеш (не argon2) здесь уместен:
// токен высокоэнтропийный, а по хешу нужен индексируемый поиск.

// newSecretToken генерирует непрозрачный токен: 32 байта энтропии в base64url без
// паддинга. Годится и для refresh-токенов, и для одноразовых verify/reset (итер. 37).
func newSecretToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("account: secret token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newFamilyID — идентификатор семейства (одна цепочка ротаций от одного логина). При
// детекте переиспользования гасится всё семейство.
func newFamilyID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("account: family id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// hashToken — SHA-256 в hex: и ключ индексируемого поиска, и то, что реально хранится
// (открытого токена на сервере нет — ни для refresh, ни для verify/reset).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
