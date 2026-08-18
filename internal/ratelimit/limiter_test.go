package ratelimit

import (
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
func newTestLimiter(t *testing.T, burst int, window time.Duration) (*Limiter, *testClock) {
	t.Helper()
	l := NewLimiter(burst, window)
	if l == nil {
		t.Fatal("NewLimiter returned nil for enabled config")
	}
	clk := &testClock{t: time.Unix(1_000_000, 0)}
	l.now = clk.now
	return l, clk
}

func TestLimiterDisabled(t *testing.T) {
	for _, tc := range []struct {
		burst  int
		window time.Duration
	}{{0, 0}, {5, 0}, {0, time.Second}, {-1, time.Second}} {
		if l := NewLimiter(tc.burst, tc.window); l != nil {
			t.Fatalf("NewLimiter(%d, %v) should disable (got non-nil)", tc.burst, tc.window)
		}
	}
	// nil-приёмник всё пропускает.
	var l *Limiter
	if ok, wait := l.Allow("x"); !ok || wait != 0 {
		t.Fatalf("nil limiter Allow = (%v, %v), want (true, 0)", ok, wait)
	}
}

func TestLimiterAllowsBurstThenBlocks(t *testing.T) {
	l, _ := newTestLimiter(t, 3, 3*time.Second) // refill 1 ток/с
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("1.2.3.4"); !ok {
			t.Fatalf("burst request %d should be allowed", i+1)
		}
	}
	ok, wait := l.Allow("1.2.3.4")
	if ok {
		t.Fatal("request over burst should be blocked")
	}
	if wait <= 0 {
		t.Fatalf("blocked request should report positive wait, got %v", wait)
	}
}

func TestLimiterRefills(t *testing.T) {
	l, clk := newTestLimiter(t, 2, 2*time.Second) // refill 1 ток/с
	l.Allow("ip")
	l.Allow("ip")
	if ok, _ := l.Allow("ip"); ok {
		t.Fatal("bucket should be empty after burst")
	}
	clk.advance(time.Second) // +1 токен
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("one token should have refilled after 1s")
	}
	if ok, _ := l.Allow("ip"); ok {
		t.Fatal("only one token refilled — next must block")
	}
}

func TestLimiterRefillCapsAtBurst(t *testing.T) {
	l, clk := newTestLimiter(t, 2, 2*time.Second)
	l.Allow("ip")          // 2 -> 1
	clk.advance(time.Hour) // дозаправка не должна превысить burst
	// Доступно ровно burst (2): два подряд ок, третий — нет.
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("first after long idle should allow")
	}
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("second after long idle should allow (capped at burst)")
	}
	if ok, _ := l.Allow("ip"); ok {
		t.Fatal("third should block — refill capped at burst, not accumulated")
	}
}

func TestLimiterPerKeyIsolation(t *testing.T) {
	l, _ := newTestLimiter(t, 1, time.Minute)
	if ok, _ := l.Allow("A"); !ok {
		t.Fatal("A first request should pass")
	}
	if ok, _ := l.Allow("A"); ok {
		t.Fatal("A second request should block")
	}
	if ok, _ := l.Allow("B"); !ok {
		t.Fatal("B has its own bucket, should pass")
	}
}

func TestLimiterSweepEvictsIdle(t *testing.T) {
	l, clk := newTestLimiter(t, 2, 2*time.Second) // refill 1 ток/с
	l.Allow("A")
	clk.advance(sweepInterval + 2*time.Second) // A успевает полностью восстановиться
	l.Allow("B")                               // этот запрос триггерит свип

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

func TestLimiterConcurrent(t *testing.T) {
	// Замороженные часы: дозаправка не вмешивается, поэтому у одного ключа проходит
	// РОВНО burst запросов, сколько бы горутин ни ломилось. -race ловит гонки по
	// карте/бакетам.
	l, _ := newTestLimiter(t, 100, time.Minute)
	const goroutines, perG = 40, 10 // 400 запросов на один ключ

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				if ok, _ := l.Allow("same"); ok {
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

func TestClientKey(t *testing.T) {
	// По умолчанию (заголовка нет) — хост из RemoteAddr.
	if got := ClientKey("9.9.9.9:5555", ""); got != "9.9.9.9" {
		t.Fatalf("ClientKey from RemoteAddr: got %q, want 9.9.9.9", got)
	}
	// С доверенным заголовком — первый элемент X-Forwarded-For.
	if got := ClientKey("9.9.9.9:5555", "1.1.1.1, 2.2.2.2"); got != "1.1.1.1" {
		t.Fatalf("ClientKey from header: got %q, want 1.1.1.1", got)
	}
	// Пустое значение заголовка — откат на RemoteAddr.
	if got := ClientKey("8.8.8.8:1", ""); got != "8.8.8.8" {
		t.Fatalf("ClientKey fallback: got %q, want 8.8.8.8", got)
	}
	// RemoteAddr без порта — берётся как есть.
	if got := ClientKey("no-port", ""); got != "no-port" {
		t.Fatalf("ClientKey without port: got %q, want no-port", got)
	}
}
