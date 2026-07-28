package account

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argonParams — параметры argon2id. Значения по умолчанию — разумный баланс для
// логина на сервере (64 МБ, 1 проход, 4 потока).
type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
	saltLen uint32
}

var defaultArgon = argonParams{memory: 64 * 1024, time: 1, threads: 4, keyLen: 32, saltLen: 16}

// hashPassword возвращает argon2id-хеш в формате PHC ($argon2id$v=..$m=..,t=..,p=..$salt$hash).
func hashPassword(password string) (string, error) {
	salt := make([]byte, defaultArgon.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("account: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt,
		defaultArgon.time, defaultArgon.memory, defaultArgon.threads, defaultArgon.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, defaultArgon.memory, defaultArgon.time, defaultArgon.threads,
		b64(salt), b64(key)), nil
}

// verifyPassword сверяет пароль с PHC-хешем в постоянное время. Ошибка — только на
// битом хеше; несовпадение пароля — (false, nil).
func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrBadHash
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return false, ErrBadHash
	}
	salt, err := b64decode(parts[4])
	if err != nil {
		return false, ErrBadHash
	}
	want, err := b64decode(parts[5])
	if err != nil {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func b64(b []byte) string { return base64.RawStdEncoding.EncodeToString(b) }

func b64decode(s string) ([]byte, error) { return base64.RawStdEncoding.DecodeString(s) }
