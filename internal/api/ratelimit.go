package api

// Рейт-лимит на auth-эндпоинтах (итерация 21): защита register/login/guest от
// брутфорса и спама. Пер-IP токен-бакет: у каждого клиента ёмкость Burst запросов,
// восстанавливающаяся со скоростью Burst/Window. Исчерпал — 429 c Retry-After.
//
// Живёт в api (это его middleware, не игровой код), конкурентно-безопасен под mu.
// Никаких фоновых горутин: простаивающие (полные) бакеты подчищаются ленивым свипом
// на самом запросе — карта не растёт при живом трафике и статична в тишине.

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sweepInterval — как часто (не чаще) прочищать простаивающие бакеты. Свип идёт под
// mu на запросе, поэтому редкий: auth-эндпоинты низкочастотны.
const sweepInterval = time.Minute

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

// bucket — токен-бакет одного клиента. Поля под ipLimiter.mu.
type bucket struct {
	tokens float64   // доступные токены (дробные — непрерывная дозаправка)
	last   time.Time // время последнего пересчёта tokens
}

// ipLimiter — пер-IP токен-бакет-лимитер. Конкурентно-безопасен (mu защищает и
// карту, и поля бакетов).
type ipLimiter struct {
	burst  float64
	refill float64 // токенов в секунду
	header string
	now    func() time.Time // сеам для детерминированных тестов (по умолчанию time.Now)

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// newIPLimiter собирает лимитер из конфига. Возвращает nil, если лимитирование
// выключено (тогда middleware — сквозной).
func newIPLimiter(rl RateLimit) *ipLimiter {
	if !rl.enabled() {
		return nil
	}
	return &ipLimiter{
		burst:   float64(rl.Burst),
		refill:  float64(rl.Burst) / rl.Window.Seconds(),
		header:  rl.ClientIPHeader,
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

// allow пробует списать токен у клиента key. Возвращает (true, 0), если запрос
// разрешён, либо (false, wait) с оценкой времени до появления следующего токена.
func (l *ipLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)

	b := l.buckets[key]
	if b == nil {
		// Новый клиент: полный бакет, сразу списываем один токен.
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return true, 0
	}
	// Дозаправка за прошедшее время (с потолком burst).
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.refill)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// Токенов нет: сколько ждать до одного целого.
	wait := time.Duration((1 - b.tokens) / l.refill * float64(time.Second))
	return false, wait
}

// sweep удаляет простаивающие (успевшие полностью восстановиться) бакеты, чтобы
// карта не росла без предела. Ленивый: не чаще sweepInterval, под уже взятым mu.
func (l *ipLimiter) sweep(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.refill >= l.burst {
			delete(l.buckets, k) // полон → простаивает → эквивалентен свежему
		}
	}
}

// clientKey вычисляет ключ клиента: из доверенного заголовка (если задан и присутствует)
// или из хоста RemoteAddr.
func (l *ipLimiter) clientKey(r *http.Request) string {
	if l.header != "" {
		if v := r.Header.Get(l.header); v != "" {
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i] // первый элемент X-Forwarded-For — исходный клиент
			}
			return strings.TrimSpace(v)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // RemoteAddr без порта (напр. в тестах) — берём как есть
	}
	return host
}

// middleware оборачивает обработчик пер-IP лимитом. Исчерпавшему бакет клиенту
// отвечает 429 c Retry-After (секунды до следующего токена).
func (l *ipLimiter) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, wait := l.allow(l.clientKey(r))
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
