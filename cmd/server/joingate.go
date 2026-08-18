package main

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"arena/internal/ratelimit"
)

// joinMetrics — узкий интерфейс метрик, нужный шлюзу входа. nil допустим (метрика
// не пишется). *metrics.Metrics его удовлетворяет.
type joinMetrics interface {
	JoinRejected(reason string)
}

// joinGate ограничивает игровой вход (/ws и /rtc) пер-IP двумя способами (итер. 33):
//   - conns: кап ОДНОВРЕМЕННО живых соединений одного IP. Обработчик сессии ниже
//     блокируется на всё её время жизни, поэтому слот занят ровно пока сессия жива —
//     защита от удержания горутин/комнат/сокетов одним источником.
//   - rate: токен-бакет СКОРОСТИ новых соединений — защита от лавины подключений.
//
// Обёртка вокруг реального обработчика (gateway/rtcGateway). Проверки до апгрейда:
// отклонённому клиенту уходит 429 и HTTP-соединение закрывается, не доходя до
// upgrade/handshake/комнаты.
//
// rate и conns МОГУТ быть общими для нескольких gate (напр. /ws и /rtc), чтобы
// соединения одного IP по обоим транспортам считались вместе. nil-примитив —
// соответствующая проверка выключена (см. ratelimit.NewLimiter/NewConnLimiter).
//
// ВНИМАНИЕ (как у auth-лимитера, итер. 21): за обратным прокси ВСЕ соединения
// приходят с IP прокси. Тогда задать header (ARENA_JOIN_RATE_IP_HEADER, напр.
// X-Forwarded-For), иначе кап посчитает всех клиентов как один IP и заблокирует их
// дальше max. Пустой header доверять ТОЛЬКО за прокси, который его сам перезаписывает.
type joinGate struct {
	next    http.Handler
	rate    *ratelimit.Limiter
	conns   *ratelimit.ConnLimiter
	header  string
	log     *slog.Logger
	metrics joinMetrics
}

func newJoinGate(next http.Handler, rate *ratelimit.Limiter, conns *ratelimit.ConnLimiter, header string, log *slog.Logger, m joinMetrics) *joinGate {
	return &joinGate{next: next, rate: rate, conns: conns, header: header, log: log, metrics: m}
}

func (g *joinGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := ratelimit.ClientKey(r.RemoteAddr, g.headerValue(r))

	// Скорость новых соединений: исчерпал бакет — 429 до апгрейда.
	if ok, wait := g.rate.Allow(key); !ok {
		g.reject(w, r, "rate", wait)
		return
	}
	// Кап живых соединений: занимаем слот на всё время сессии, освобождаем на выходе.
	if !g.conns.Acquire(key) {
		g.reject(w, r, "concurrent", 0)
		return
	}
	defer g.conns.Release(key)

	g.next.ServeHTTP(w, r)
}

func (g *joinGate) headerValue(r *http.Request) string {
	if g.header == "" {
		return ""
	}
	return r.Header.Get(g.header)
}

// reject отвечает 429 и учитывает отклонение в метрике. Для rate добавляет
// Retry-After (секунды до следующего слота).
func (g *joinGate) reject(w http.ResponseWriter, r *http.Request, reason string, wait time.Duration) {
	if g.metrics != nil {
		g.metrics.JoinRejected(reason)
	}
	if g.log != nil {
		g.log.Debug("join rejected", "addr", r.RemoteAddr, "reason", reason)
	}
	if wait > 0 {
		retry := int(math.Ceil(wait.Seconds()))
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
	}
	http.Error(w, "join rate limited", http.StatusTooManyRequests)
}

// проверка на этапе компиляции, что joinGate удовлетворяет http.Handler.
var _ http.Handler = (*joinGate)(nil)
