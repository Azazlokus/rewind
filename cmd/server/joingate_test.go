package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"arena/internal/ratelimit"
)

// fakeJoinMetrics считает отклонения по причине (проверяем, что шлюз отчитывается).
type fakeJoinMetrics struct {
	mu      sync.Mutex
	reasons map[string]int
}

func (f *fakeJoinMetrics) JoinRejected(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reasons == nil {
		f.reasons = make(map[string]int)
	}
	f.reasons[reason]++
}

func (f *fakeJoinMetrics) count(reason string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reasons[reason]
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// TestJoinGateConcurrencyCap: кап живых соединений держит слот на всё время сессии.
// Второе соединение того же IP при занятом капе получает 429 «concurrent», не доходя
// до обработчика; после освобождения слота новое соединение снова проходит.
func TestJoinGateConcurrencyCap(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var served int64
	var servedMu sync.Mutex
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		servedMu.Lock()
		served++
		servedMu.Unlock()
		entered <- struct{}{} // сообщаем, что слот занят и обработчик идёт
		<-release             // держим соединение живым
		w.WriteHeader(http.StatusOK)
	})
	fake := &fakeJoinMetrics{}
	gate := newJoinGate(next, nil, ratelimit.NewConnLimiter(1), "", discardLog(), fake)

	// Соединение 1 занимает единственный слот и держит его.
	rec1 := httptest.NewRecorder()
	done1 := make(chan struct{})
	go func() {
		gate.ServeHTTP(rec1, httptest.NewRequest("GET", "/ws", nil))
		close(done1)
	}()
	<-entered

	// Соединение 2 того же IP — кап исчерпан, 429 до обработчика.
	rec2 := httptest.NewRecorder()
	gate.ServeHTTP(rec2, httptest.NewRequest("GET", "/ws", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second connection: got %d, want 429", rec2.Code)
	}
	servedMu.Lock()
	s := served
	servedMu.Unlock()
	if s != 1 {
		t.Fatalf("blocked connection must not reach handler: served=%d, want 1", s)
	}
	if fake.count("concurrent") != 1 {
		t.Fatalf("expected one 'concurrent' rejection, got %d", fake.count("concurrent"))
	}

	// Освобождаем слот — первое соединение завершается, слот свободен.
	close(release)
	<-done1
	if rec1.Code != http.StatusOK {
		t.Fatalf("first connection should have served 200, got %d", rec1.Code)
	}

	// Новое соединение снова проходит (release уже закрыт — обработчик не блокирует).
	rec3 := httptest.NewRecorder()
	gate.ServeHTTP(rec3, httptest.NewRequest("GET", "/ws", nil))
	if rec3.Code != http.StatusOK {
		t.Fatalf("connection after release: got %d, want 200", rec3.Code)
	}
}

// TestJoinGateRateLimit: бакет скорости пропускает burst, затем 429 c Retry-After и
// причиной «rate».
func TestJoinGateRateLimit(t *testing.T) {
	var served int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	})
	fake := &fakeJoinMetrics{}
	// burst 1, окно минута: второй немедленный запрос блокируется.
	gate := newJoinGate(next, ratelimit.NewLimiter(1, time.Minute), nil, "", discardLog(), fake)

	call := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequest("GET", "/ws", nil))
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
		t.Fatal("rate 429 missing Retry-After")
	} else if n, err := strconv.Atoi(ra); err != nil || n < 1 {
		t.Fatalf("Retry-After should be a positive integer, got %q", ra)
	}
	if served != 1 {
		t.Fatalf("blocked call must not reach handler: served=%d, want 1", served)
	}
	if fake.count("rate") != 1 {
		t.Fatalf("expected one 'rate' rejection, got %d", fake.count("rate"))
	}
}

// TestJoinGateDisabled: оба примитива nil — сквозной путь, все запросы проходят.
func TestJoinGateDisabled(t *testing.T) {
	var served int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	})
	gate := newJoinGate(next, nil, nil, "", discardLog(), nil)
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequest("GET", "/ws", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("disabled gate call %d: got %d, want 200", i, rec.Code)
		}
	}
	if served != 5 {
		t.Fatalf("all calls should reach handler: served=%d, want 5", served)
	}
}

// TestJoinGateClientKeyHeader: за прокси ключ берётся из доверенного заголовка —
// разные X-Forwarded-For изолированы, повтор того же — лимитируется.
func TestJoinGateClientKeyHeader(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	gate := newJoinGate(next, ratelimit.NewLimiter(1, time.Minute), nil, "X-Forwarded-For", discardLog(), nil)

	call := func(xff string) int {
		req := httptest.NewRequest("GET", "/ws", nil)
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := call("1.1.1.1"); code != http.StatusOK {
		t.Fatalf("first from 1.1.1.1: got %d, want 200", code)
	}
	if code := call("1.1.1.1"); code != http.StatusTooManyRequests {
		t.Fatalf("second from 1.1.1.1: got %d, want 429", code)
	}
	if code := call("2.2.2.2"); code != http.StatusOK {
		t.Fatalf("first from 2.2.2.2 (own bucket): got %d, want 200", code)
	}
}
