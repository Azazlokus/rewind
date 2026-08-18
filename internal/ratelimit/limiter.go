// Пакет ratelimit — переиспользуемые примитивы контроля доступа по ключу (обычно
// IP): токен-бакет ограничения СКОРОСТИ (Limiter) и кап КОНКУРЕНТНЫХ занятостей
// (ConnLimiter). Пакет чистый: не знает про HTTP, игру и сеть — работает со
// строковыми ключами. Используется и auth-эндпоинтами (internal/api, итер. 21),
// и игровым шлюзом входа (cmd/server, итер. 33).
package ratelimit

import (
	"math"
	"net"
	"strings"
	"sync"
	"time"
)

// sweepInterval — как часто (не чаще) прочищать простаивающие бакеты. Свип идёт под
// mu на запросе, поэтому редкий и дешёвый.
const sweepInterval = time.Minute

// bucket — токен-бакет одного ключа. Поля под Limiter.mu.
type bucket struct {
	tokens float64   // доступные токены (дробные — непрерывная дозаправка)
	last   time.Time // время последнего пересчёта tokens
}

// Limiter — пер-ключ токен-бакет-лимитер скорости. Конкурентно-безопасен (mu
// защищает и карту, и поля бакетов). Никаких фоновых горутин: простаивающие (полные)
// бакеты подчищаются ленивым свипом на самом запросе — карта не растёт при живом
// трафике и статична в тишине.
type Limiter struct {
	burst  float64
	refill float64          // токенов в секунду
	now    func() time.Time // сеам для детерминированных тестов (по умолчанию time.Now)

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// NewLimiter собирает лимитер: ёмкость burst (сколько запросов подряд можно сделать
// «в упор»), полное восстановление бакета за window (скорость дозаправки =
// burst/window токенов в секунду). burst <= 0 или window <= 0 → nil (лимитирование
// выключено; методы на nil-приёмнике всё пропускают — вызывающему не нужен nil-чек).
func NewLimiter(burst int, window time.Duration) *Limiter {
	if burst <= 0 || window <= 0 {
		return nil
	}
	return &Limiter{
		burst:   float64(burst),
		refill:  float64(burst) / window.Seconds(),
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}
}

// Allow пробует списать токен у ключа key. Возвращает (true, 0), если запрос
// разрешён, либо (false, wait) с оценкой времени до появления следующего токена.
// На nil-приёмнике (лимитер выключен) всегда (true, 0).
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)

	b := l.buckets[key]
	if b == nil {
		// Новый ключ: полный бакет, сразу списываем один токен.
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
func (l *Limiter) sweep(now time.Time) {
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

// ClientKey выводит ключ клиента из адреса пира и (опционально) значения доверенного
// заголовка прокси. Если headerValue непуст — берётся его первый CSV-элемент (первый
// элемент X-Forwarded-For — исходный клиент); иначе — хост из remoteAddr. Пустой
// заголовок (header не сконфигурирован или отсутствует в запросе) даёт откат на
// remoteAddr. ДОВЕРЯТЬ заголовку ТОЛЬКО за прокси, который его сам перезаписывает,
// иначе клиент подделает IP и обойдёт лимит.
func ClientKey(remoteAddr, headerValue string) string {
	if headerValue != "" {
		if i := strings.IndexByte(headerValue, ','); i >= 0 {
			headerValue = headerValue[:i]
		}
		return strings.TrimSpace(headerValue)
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr // RemoteAddr без порта (напр. в тестах) — берём как есть
	}
	return host
}
