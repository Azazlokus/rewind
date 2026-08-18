package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arena/internal/account"
	"arena/internal/store"
)

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := account.NewService(st, []byte("api-test-secret-value"), time.Hour, 24*time.Hour)
	// Рейт-лимит выключен: эти тесты проверяют логику эндпоинтов, а не лимит (его
	// стережёт ratelimit_test.go). Нулевой RateLimit — сквозной путь.
	h := NewHandler(svc, st, slog.New(slog.NewTextHandler(io.Discard, nil)), RateLimit{})
	return h.Routes()
}

// do выполняет запрос и возвращает статус + распарсенный JSON-объект.
func do(t *testing.T, h http.Handler, method, path, body, token string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if b := rec.Body.Bytes(); len(b) > 0 {
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s %s: bad json %q: %v", method, path, b, err)
		}
	}
	return rec.Code, out
}

func TestRegisterLoginFlow(t *testing.T) {
	h := newTestHandler(t)

	// Регистрация.
	code, body := do(t, h, "POST", "/api/register", `{"username":"alice","password":"hunter2pass"}`, "")
	if code != http.StatusCreated {
		t.Fatalf("register code %d body %v", code, body)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("no token in register response: %v", body)
	}

	// /api/me с токеном.
	code, body = do(t, h, "GET", "/api/me", "", token)
	if code != http.StatusOK || body["name"] != "alice" || body["guest"] != false {
		t.Fatalf("me: code %d body %v", code, body)
	}
	if _, ok := body["stats"]; !ok {
		t.Fatalf("me should include stats for account: %v", body)
	}

	// Логин.
	code, _ = do(t, h, "POST", "/api/login", `{"username":"alice","password":"hunter2pass"}`, "")
	if code != http.StatusOK {
		t.Fatalf("login code %d", code)
	}
}

// TestRefreshLogoutEndpoints: register отдаёт refresh_token+expires_in; /api/refresh
// ротирует его на новую пару, переиспользование старого — 401, /api/logout — 204 (итер. 36).
func TestRefreshLogoutEndpoints(t *testing.T) {
	h := newTestHandler(t)

	code, body := do(t, h, "POST", "/api/register", `{"username":"alice","password":"hunter2pass"}`, "")
	if code != http.StatusCreated {
		t.Fatalf("register code %d body %v", code, body)
	}
	refresh, _ := body["refresh_token"].(string)
	if refresh == "" {
		t.Fatalf("register must return refresh_token: %v", body)
	}
	if _, ok := body["expires_in"].(float64); !ok {
		t.Fatalf("register must return expires_in: %v", body)
	}

	// Обмен refresh на новую пару (ротация).
	code, body = do(t, h, "POST", "/api/refresh", `{"refresh_token":"`+refresh+`"}`, "")
	if code != http.StatusOK {
		t.Fatalf("refresh code %d body %v", code, body)
	}
	next, _ := body["refresh_token"].(string)
	access, _ := body["token"].(string)
	if next == "" || next == refresh || access == "" {
		t.Fatalf("refresh must rotate: old=%q new=%q access=%q", refresh, next, access)
	}
	// Новый access авторизует /api/me.
	if c, _ := do(t, h, "GET", "/api/me", "", access); c != http.StatusOK {
		t.Fatalf("me with rotated access: %d", c)
	}
	// Переиспользование старого refresh — 401 (и гасит семейство).
	if c, _ := do(t, h, "POST", "/api/refresh", `{"refresh_token":"`+refresh+`"}`, ""); c != http.StatusUnauthorized {
		t.Fatalf("reused refresh: want 401, got %d", c)
	}
	// Logout — 204, идемпотентно.
	if c, _ := do(t, h, "POST", "/api/logout", `{"refresh_token":"`+next+`"}`, ""); c != http.StatusNoContent {
		t.Fatalf("logout: want 204, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/logout", `{"refresh_token":"unknown"}`, ""); c != http.StatusNoContent {
		t.Fatalf("logout unknown: want 204, got %d", c)
	}
	// Мусорный refresh — 401.
	if c, _ := do(t, h, "POST", "/api/refresh", `{"refresh_token":"garbage"}`, ""); c != http.StatusUnauthorized {
		t.Fatalf("garbage refresh: want 401, got %d", c)
	}
}

func TestErrorCodes(t *testing.T) {
	h := newTestHandler(t)
	_, _ = do(t, h, "POST", "/api/register", `{"username":"bob","password":"password12"}`, "")

	cases := []struct {
		name, method, path, body, token string
		want                            int
	}{
		{"validation", "POST", "/api/register", `{"username":"x","password":"short"}`, "", http.StatusBadRequest},
		{"dup username", "POST", "/api/register", `{"username":"bob","password":"password12"}`, "", http.StatusConflict},
		{"wrong password", "POST", "/api/login", `{"username":"bob","password":"nope1234567"}`, "", http.StatusUnauthorized},
		{"me no token", "GET", "/api/me", "", "", http.StatusUnauthorized},
		{"me bad token", "GET", "/api/me", "", "garbage.token", http.StatusUnauthorized},
		{"unknown player", "GET", "/api/players/9999/stats", "", "", http.StatusNotFound},
		{"bad player id", "GET", "/api/players/abc/stats", "", "", http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := do(t, h, c.method, c.path, c.body, c.token)
			if code != c.want {
				t.Fatalf("got %d want %d (body %v)", code, c.want, body)
			}
		})
	}
}

// TestRateLimitWiredThroughRoutes: включённый лимитер реально навешен на auth-роут
// через Routes() — третий register с того же IP при burst=2 получает 429 (итер. 21).
func TestRateLimitWiredThroughRoutes(t *testing.T) {
	st, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := account.NewService(st, []byte("api-test-secret-value"), time.Hour, 24*time.Hour)
	h := NewHandler(svc, st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		RateLimit{Burst: 2, Window: time.Minute}) // за <1мс дозаправка ≈ 0
	routes := h.Routes()

	fire := func(user string) int {
		body := `{"username":"` + user + `","password":"password12"}`
		req := httptest.NewRequest("POST", "/api/register", strings.NewReader(body))
		req.RemoteAddr = "5.5.5.5:9999"
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := fire("user1"); c != http.StatusCreated {
		t.Fatalf("register 1: got %d, want 201", c)
	}
	if c := fire("user2"); c != http.StatusCreated {
		t.Fatalf("register 2: got %d, want 201", c)
	}
	if c := fire("user3"); c != http.StatusTooManyRequests {
		t.Fatalf("register 3 (over burst): got %d, want 429", c)
	}
}

func TestGuestAndLeaderboard(t *testing.T) {
	h := newTestHandler(t)

	code, body := do(t, h, "POST", "/api/guest", `{"name":"Casual"}`, "")
	if code != http.StatusOK || body["guest"] != true || body["name"] != "Casual" {
		t.Fatalf("guest: code %d body %v", code, body)
	}

	// Пустой лидерборд — валидный ответ с пустым списком.
	code, body = do(t, h, "GET", "/api/leaderboard", "", "")
	if code != http.StatusOK {
		t.Fatalf("leaderboard code %d", code)
	}
	if _, ok := body["leaderboard"]; !ok {
		t.Fatalf("leaderboard key missing: %v", body)
	}
}
