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
	svc := account.NewService(st, []byte("api-test-secret-value"), time.Hour)
	h := NewHandler(svc, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
