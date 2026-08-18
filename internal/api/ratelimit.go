package api

// Рейт-лимит на auth-эндпоинтах (итерация 21): защита register/login/guest от
// брутфорса и спама. Пер-IP токен-бакет: у каждого клиента ёмкость Burst запросов,
// восстанавливающаяся со скоростью Burst/Window. Исчерпал — 429 c Retry-After.
//
// Алгоритм токен-бакета живёт в общем internal/ratelimit (итер. 33) — тот же
// примитив использует игровой шлюз входа. Здесь остаётся лишь HTTP-адаптер: достать
// ключ клиента из запроса и оформить ответ 429.

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"arena/internal/ratelimit"
)

// RateLimit конфигурирует пер-IP лимитер на auth-эндпоинтах. Burst — ёмкость бакета
// (сколько запросов подряд можно сделать «в упор»); Window — время полного
// восстановления бакета (скорость дозаправки = Burst/Window). Burst <= 0 или
// Window <= 0 — лимитирование ВЫКЛЮЧЕНО (middleware становится сквозным).
//
// ClientIPHeader (например "X-Forwarded-For") — заголовок, из которого брать IP
// клиента, когда сервис стоит за обратным прокси; берётся первый элемент до запятой.
// Пусто (по умолчанию) — ключ из RemoteAddr (реальный TCP-пир). ВКЛЮЧАТЬ ТОЛЬКО за
// прокси, который сам перезаписывает этот заголовок, иначе клиент подделает IP и
// обойдёт лимит.
type RateLimit struct {
	Burst          int
	Window         time.Duration
	ClientIPHeader string
}

func (rl RateLimit) enabled() bool { return rl.Burst > 0 && rl.Window > 0 }

// ipLimiter — HTTP-адаптер над общим ratelimit.Limiter: извлекает ключ клиента из
// запроса и отвечает 429 c Retry-After исчерпавшему бакет.
type ipLimiter struct {
	lim    *ratelimit.Limiter
	header string
}

// newIPLimiter собирает лимитер из конфига. Возвращает nil, если лимитирование
// выключено (тогда middleware — сквозной).
func newIPLimiter(rl RateLimit) *ipLimiter {
	if !rl.enabled() {
		return nil
	}
	return &ipLimiter{lim: ratelimit.NewLimiter(rl.Burst, rl.Window), header: rl.ClientIPHeader}
}

// clientKey вычисляет ключ клиента: из доверенного заголовка (если задан и
// присутствует) или из хоста RemoteAddr.
func (l *ipLimiter) clientKey(r *http.Request) string {
	var headerValue string
	if l.header != "" {
		headerValue = r.Header.Get(l.header)
	}
	return ratelimit.ClientKey(r.RemoteAddr, headerValue)
}

// middleware оборачивает обработчик пер-IP лимитом. Исчерпавшему бакет клиенту
// отвечает 429 c Retry-After (секунды до следующего токена).
func (l *ipLimiter) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.lim.Allow(l.clientKey(r))
		if !ok {
			retry := int(math.Ceil(wait.Seconds()))
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
			return
		}
		next(w, r)
	}
}
