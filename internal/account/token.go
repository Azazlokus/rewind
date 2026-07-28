package account

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// claims — полезная нагрузка токен-сессии. Токен — компактный подписанный формат
// base64url(json).base64url(hmac_sha256), самодостаточный (без хранения на сервере).
type claims struct {
	AID   int64  `json:"aid"`   // id аккаунта; 0 у гостя
	Name  string `json:"name"`  // отображаемое имя
	Guest bool   `json:"guest"` // гость ли
	Exp   int64  `json:"exp"`   // истечение, unix-секунды
}

func (s *Service) sign(c claims) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(payload)
	return p + "." + base64.RawURLEncoding.EncodeToString(s.mac([]byte(p))), nil
}

func (s *Service) mac(b []byte) []byte {
	h := hmac.New(sha256.New, s.secret)
	h.Write(b)
	return h.Sum(nil)
}

// parse проверяет подпись (constant-time) и срок жизни, возвращает claims.
func (s *Service) parse(token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return claims{}, ErrBadToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, s.mac([]byte(parts[0]))) {
		return claims{}, ErrBadToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims{}, ErrBadToken
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return claims{}, ErrBadToken
	}
	if s.now().Unix() > c.Exp {
		return claims{}, ErrBadToken
	}
	return c, nil
}

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}
