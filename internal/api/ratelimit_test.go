package api

// Тесты HTTP-адаптера рейт-лимита. Сам алгоритм токен-бакета (дозаправка, кап,
// изоляция ключей, свип, конкурентность) покрыт в internal/ratelimit — здесь только
// адаптерные заботы: конфиг-гейт включения, извлечение ключа из *http.Request и
// оформление ответа 429.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRateLimitDisabled(t *testing.T) {
	for _, rl := range []RateLimit{{}, {Burst: 5}, {Window: time.Second}, {Burst: -1, Window: time.Second}} {
		if l := newIPLimiter(rl); l != nil {
			t.Fatalf("config %+v should disable limiter (got non-nil)", rl)
		}
	}
	if l := newIPLimiter(RateLimit{Burst: 1, Window: time.Minute}); l == nil {
		t.Fatal("enabled config should produce a limiter")
	}
}

func TestRateLimitClientKey(t *testing.T) {
	// По умолчанию — хост из RemoteAddr.
	l := newIPLimiter(RateLimit{Burst: 1, Window: time.Minute})
	req := httptest.NewRequest("POST", "/api/login", nil)
	req.RemoteAddr = "9.9.9.9:5555"
	if got := l.clientKey(req); got != "9.9.9.9" {
		t.Fatalf("clientKey from RemoteAddr: got %q, want 9.9.9.9", got)
	}
	// С доверенным заголовком — первый элемент X-Forwarded-For.
	lh := newIPLimiter(RateLimit{Burst: 1, Window: time.Minute, ClientIPHeader: "X-Forwarded-For"})
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 2.2.2.2")
	if got := lh.clientKey(req); got != "1.1.1.1" {
		t.Fatalf("clientKey from header: got %q, want 1.1.1.1", got)
	}
	// Заголовок задан в конфиге, но отсутствует в запросе — падаем на RemoteAddr.
	req2 := httptest.NewRequest("POST", "/api/login", nil)
	req2.RemoteAddr = "8.8.8.8:1"
	if got := lh.clientKey(req2); got != "8.8.8.8" {
		t.Fatalf("clientKey fallback: got %q, want 8.8.8.8", got)
	}
}

func TestRateLimitMiddleware429(t *testing.T) {
	// burst=1, окно минута: второй немедленный запрос того же ключа блокируется
	// (дозаправка за микросекунды ≈ 0), клок подменять не нужно.
	l := newIPLimiter(RateLimit{Burst: 1, Window: time.Minute})
	var served int
	h := l.middleware(func(w http.ResponseWriter, r *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	})
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/login", nil)
		req.RemoteAddr = "7.7.7.7:2222"
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec
	}
	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("first call: got %d, want 200", rec.Code)
	}
	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second call: got %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("429 response missing Retry-After header")
	} else if n, err := strconv.Atoi(ra); err != nil || n < 1 {
		t.Fatalf("Retry-After should be a positive integer, got %q", ra)
	}
	if served != 1 {
		t.Fatalf("handler should have run once (blocked call must not reach it), ran %d", served)
	}
}
