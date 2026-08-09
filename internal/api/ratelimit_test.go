package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testClock — управляемые часы для детерминированных тестов лимитера (без sleep).
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestLimiter собирает лимитер с управляемыми часами.
func newTestLimiter(t *testing.T, rl RateLimit) (*ipLimiter, *testClock) {
	t.Helper()
	l := newIPLimiter(rl)
	if l == nil {
		t.Fatal("newIPLimiter returned nil for enabled config")
	}
	clk := &testClock{t: time.Unix(1_000_000, 0)}
	l.now = clk.now
	return l, clk
}

func TestRateLimitDisabled(t *testing.T) {
	for _, rl := range []RateLimit{{}, {Burst: 5}, {Window: time.Second}, {Burst: -1, Window: time.Second}} {
		if l := newIPLimiter(rl); l != nil {
			t.Fatalf("config %+v should disable limiter (got non-nil)", rl)
		}
	}
}

func TestRateLimitAllowsBurstThenBlocks(t *testing.T) {
	l, _ := newTestLimiter(t, RateLimit{Burst: 3, Window: 3 * time.Second}) // refill 1 ток/с
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("1.2.3.4"); !ok {
			t.Fatalf("burst request %d should be allowed", i+1)
		}
	}
	ok, wait := l.allow("1.2.3.4")
	if ok {
		t.Fatal("request over burst should be blocked")
	}
	if wait <= 0 {
		t.Fatalf("blocked request should report positive wait, got %v", wait)
	}
}

func TestRateLimitRefills(t *testing.T) {
	l, clk := newTestLimiter(t, RateLimit{Burst: 2, Window: 2 * time.Second}) // refill 1 ток/с
	l.allow("ip")
	l.allow("ip")
	if ok, _ := l.allow("ip"); ok {
		t.Fatal("bucket should be empty after burst")
	}
	clk.advance(time.Second) // +1 токен
	if ok, _ := l.allow("ip"); !ok {
		t.Fatal("one token should have refilled after 1s")
	}
	if ok, _ := l.allow("ip"); ok {
		t.Fatal("only one token refilled — next must block")
	}
}

func TestRateLimitRefillCapsAtBurst(t *testing.T) {
	l, clk := newTestLimiter(t, RateLimit{Burst: 2, Window: 2 * time.Second})
	l.allow("ip")          // 2 -> 1
	clk.advance(time.Hour) // дозаправка не должна превысить burst
	// Доступно ровно burst (2): два подряд ок, третий — нет.
	if ok, _ := l.allow("ip"); !ok {
		t.Fatal("first after long idle should allow")
	}
	if ok, _ := l.allow("ip"); !ok {
		t.Fatal("second after long idle should allow (capped at burst)")
	}
	if ok, _ := l.allow("ip"); ok {
		t.Fatal("third should block — refill capped at burst, not accumulated")
	}
}

func TestRateLimitPerIPIsolation(t *testing.T) {
	l, _ := newTestLimiter(t, RateLimit{Burst: 1, Window: time.Minute})
	if ok, _ := l.allow("A"); !ok {
		t.Fatal("A first request should pass")
	}
	if ok, _ := l.allow("A"); ok {
		t.Fatal("A second request should block")
	}
	if ok, _ := l.allow("B"); !ok {
		t.Fatal("B has its own bucket, should pass")
	}
}

func TestRateLimitClientKey(t *testing.T) {
	// По умолчанию — хост из RemoteAddr.
	l, _ := newTestLimiter(t, RateLimit{Burst: 1, Window: time.Minute})
	req := httptest.NewRequest("POST", "/api/login", nil)
	req.RemoteAddr = "9.9.9.9:5555"
	if got := l.clientKey(req); got != "9.9.9.9" {
		t.Fatalf("clientKey from RemoteAddr: got %q, want 9.9.9.9", got)
	}
	// С доверенным заголовком — первый элемент X-Forwarded-For.
	lh, _ := newTestLimiter(t, RateLimit{Burst: 1, Window: time.Minute, ClientIPHeader: "X-Forwarded-For"})
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
	l, _ := newTestLimiter(t, RateLimit{Burst: 1, Window: time.Minute})
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

func TestRateLimitSweepEvictsIdle(t *testing.T) {
	l, clk := newTestLimiter(t, RateLimit{Burst: 2, Window: 2 * time.Second}) // refill 1 ток/с
	l.allow("A")
	clk.advance(sweepInterval + 2*time.Second) // A успевает полностью восстановиться
	l.allow("B")                               // этот запрос триггерит свип

	l.mu.Lock()
	_, hasA := l.buckets["A"]
	_, hasB := l.buckets["B"]
	l.mu.Unlock()
	if hasA {
		t.Fatal("idle (full) bucket A should have been swept")
	}
	if !hasB {
		t.Fatal("active bucket B should remain")
	}
}

func TestRateLimitConcurrent(t *testing.T) {
	// Замороженные часы: дозаправка не вмешивается, поэтому у одного ключа проходит
	// РОВНО burst запросов, сколько бы горутин ни ломилось. -race ловит гонки по
	// карте/бакетам.
	l, _ := newTestLimiter(t, RateLimit{Burst: 100, Window: time.Minute})
	const goroutines, perG = 40, 10 // 400 запросов на один ключ

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				if ok, _ := l.allow("same"); ok {
					allowed.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 100 {
		t.Fatalf("with frozen clock exactly burst=100 requests must pass, got %d", got)
	}
}
