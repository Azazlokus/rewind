package api

import (
	"context"
	"encoding/json"
	"fmt"
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

// recordMailer перехватывает отправленные токены для end-to-end проверки флоу.
type recordMailer struct {
	verifyToken string
	resetToken  string
}

func (m *recordMailer) SendVerification(_ context.Context, _, token string) error {
	m.verifyToken = token
	return nil
}

func (m *recordMailer) SendPasswordReset(_ context.Context, _, token string) error {
	m.resetToken = token
	return nil
}

// TestEmailVerifyAndResetEndpoints: register с email → /me показывает email+unverified →
// verify-email подтверждает; request/reset-password меняет пароль и разлогинивает сессии
// (итер. 37). Токены забираем из recordMailer (SMTP нет).
func TestEmailVerifyAndResetEndpoints(t *testing.T) {
	st, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mail := &recordMailer{}
	svc := account.NewService(st, []byte("api-test-secret-value"), time.Hour, 24*time.Hour, account.WithMailer(mail))
	h := NewHandler(svc, st, slog.New(slog.NewTextHandler(io.Discard, nil)), RateLimit{}).Routes()

	code, body := do(t, h, "POST", "/api/register",
		`{"username":"alice","password":"hunter2pass","email":"alice@x.io"}`, "")
	if code != http.StatusCreated {
		t.Fatalf("register code %d body %v", code, body)
	}
	access, _ := body["token"].(string)
	refresh, _ := body["refresh_token"].(string)

	_, me := do(t, h, "GET", "/api/me", "", access)
	if me["email"] != "alice@x.io" || me["email_verified"] != false {
		t.Fatalf("me email/verified: %v", me)
	}

	// Верификация email.
	if mail.verifyToken == "" {
		t.Fatalf("no verification token captured")
	}
	if c, _ := do(t, h, "POST", "/api/verify-email", `{"token":"`+mail.verifyToken+`"}`, ""); c != http.StatusNoContent {
		t.Fatalf("verify-email: %d", c)
	}
	_, me = do(t, h, "GET", "/api/me", "", access)
	if me["email_verified"] != true {
		t.Fatalf("me should be verified: %v", me)
	}
	if c, _ := do(t, h, "POST", "/api/verify-email", `{"token":"garbage"}`, ""); c != http.StatusUnauthorized {
		t.Fatalf("bad verify token: want 401, got %d", c)
	}

	// Сброс пароля.
	if c, _ := do(t, h, "POST", "/api/request-password-reset", `{"email":"alice@x.io"}`, ""); c != http.StatusNoContent {
		t.Fatalf("request reset: %d", c)
	}
	if mail.resetToken == "" {
		t.Fatalf("no reset token captured")
	}
	if c, _ := do(t, h, "POST", "/api/request-password-reset", `{"email":"nobody@x.io"}`, ""); c != http.StatusNoContent {
		t.Fatalf("unknown email reset: want 204, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/reset-password", `{"token":"`+mail.resetToken+`","password":"short"}`, ""); c != http.StatusBadRequest {
		t.Fatalf("weak reset password: want 400, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/reset-password", `{"token":"`+mail.resetToken+`","password":"brandnew12"}`, ""); c != http.StatusNoContent {
		t.Fatalf("reset-password: %d", c)
	}
	// Сессии разлогинены — старый refresh мёртв; логин новым паролем работает.
	if c, _ := do(t, h, "POST", "/api/refresh", `{"refresh_token":"`+refresh+`"}`, ""); c != http.StatusUnauthorized {
		t.Fatalf("refresh after reset: want 401, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/login", `{"username":"alice","password":"brandnew12"}`, ""); c != http.StatusOK {
		t.Fatalf("login new password: %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/reset-password", `{"token":"`+mail.resetToken+`","password":"another123"}`, ""); c != http.StatusUnauthorized {
		t.Fatalf("reuse reset token: want 401, got %d", c)
	}
}

// TestModerationEndpoints: роль-гейтинг, бан (с отзывом сессий + баннер в /me), unban,
// смена роли (admin), репорты (итер. 39).
func TestModerationEndpoints(t *testing.T) {
	st, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := account.NewService(st, []byte("api-test-secret-value"), time.Hour, 24*time.Hour)
	h := NewHandler(svc, st, slog.New(slog.NewTextHandler(io.Discard, nil)), RateLimit{}).Routes()

	reg := func(name string) (access, refresh string, id int64) {
		_, body := do(t, h, "POST", "/api/register", fmt.Sprintf(`{"username":%q,"password":"password12"}`, name), "")
		access, _ = body["token"].(string)
		refresh, _ = body["refresh_token"].(string)
		f, _ := body["id"].(float64)
		return access, refresh, int64(f)
	}
	adminTok, _, adminID := reg("admin1")
	modTok, _, modID := reg("mod1")
	userTok, _, userID := reg("user1")
	victimTok, victimRefresh, victimID := reg("victim")
	_ = userID

	// Бутстрап админа напрямую (chicken-egg), дальше — через API.
	if err := st.SetRole(context.Background(), adminID, "admin"); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}

	// Смена роли — только admin.
	if c, _ := do(t, h, "POST", "/api/mod/role", fmt.Sprintf(`{"account_id":%d,"role":"moderator"}`, modID), modTok); c != http.StatusForbidden {
		t.Fatalf("non-admin role change: want 403, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/mod/role", fmt.Sprintf(`{"account_id":%d,"role":"moderator"}`, modID), adminTok); c != http.StatusNoContent {
		t.Fatalf("admin promotes mod: want 204, got %d", c)
	}
	// Невалидная роль / смена своей роли — 400.
	if c, _ := do(t, h, "POST", "/api/mod/role", fmt.Sprintf(`{"account_id":%d,"role":"wizard"}`, userID), adminTok); c != http.StatusBadRequest {
		t.Fatalf("invalid role: want 400, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/mod/role", fmt.Sprintf(`{"account_id":%d,"role":"user"}`, adminID), adminTok); c != http.StatusBadRequest {
		t.Fatalf("admin changing own role: want 400, got %d", c)
	}

	// Бан — только moderator+; обычный user получает 403.
	banBody := fmt.Sprintf(`{"account_id":%d,"reason":"cheating"}`, victimID)
	if c, _ := do(t, h, "POST", "/api/mod/ban", banBody, userTok); c != http.StatusForbidden {
		t.Fatalf("user banning: want 403, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/mod/ban", banBody, modTok); c != http.StatusNoContent {
		t.Fatalf("mod bans victim: want 204, got %d", c)
	}
	// Модератор не банит равного/старшего (admin).
	if c, _ := do(t, h, "POST", "/api/mod/ban", fmt.Sprintf(`{"account_id":%d,"reason":"x"}`, adminID), modTok); c != http.StatusForbidden {
		t.Fatalf("mod banning admin: want 403, got %d", c)
	}
	// Забанен: /me показывает бан, refresh отозван.
	_, me := do(t, h, "GET", "/api/me", "", victimTok)
	if me["banned"] != true {
		t.Fatalf("victim /me should show banned: %v", me)
	}
	if me["role"] != "user" {
		t.Fatalf("victim role: %v", me["role"])
	}
	if c, _ := do(t, h, "POST", "/api/refresh", fmt.Sprintf(`{"refresh_token":%q}`, victimRefresh), ""); c != http.StatusUnauthorized {
		t.Fatalf("banned victim refresh: want 401 (sessions revoked), got %d", c)
	}

	// Репорт: user на victim → 201; self/unknown/guest — ошибки.
	if c, _ := do(t, h, "POST", "/api/report", fmt.Sprintf(`{"target_id":%d,"reason":"aimbot"}`, victimID), userTok); c != http.StatusCreated {
		t.Fatalf("report: want 201, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/report", fmt.Sprintf(`{"target_id":%d,"reason":"self"}`, userID), userTok); c != http.StatusBadRequest {
		t.Fatalf("self report: want 400, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/report", `{"target_id":99999,"reason":"ghost"}`, userTok); c != http.StatusNotFound {
		t.Fatalf("report unknown target: want 404, got %d", c)
	}
	if c, _ := do(t, h, "POST", "/api/report", fmt.Sprintf(`{"target_id":%d,"reason":"noauth"}`, victimID), ""); c != http.StatusUnauthorized {
		t.Fatalf("guest report: want 401, got %d", c)
	}

	// Список репортов — moderator+; user получает 403.
	if c, _ := do(t, h, "GET", "/api/mod/reports?status=open", "", userTok); c != http.StatusForbidden {
		t.Fatalf("user listing reports: want 403, got %d", c)
	}
	_, rep := do(t, h, "GET", "/api/mod/reports?status=open", "", modTok)
	if reports, _ := rep["reports"].([]any); len(reports) != 1 {
		t.Fatalf("mod reports: want 1, got %v", rep["reports"])
	}

	// Unban → /me больше не забанен.
	if c, _ := do(t, h, "POST", "/api/mod/unban", fmt.Sprintf(`{"account_id":%d}`, victimID), modTok); c != http.StatusNoContent {
		t.Fatalf("unban: want 204, got %d", c)
	}
	_, me = do(t, h, "GET", "/api/me", "", victimTok)
	if _, banned := me["banned"]; banned {
		t.Fatalf("victim should be unbanned: %v", me)
	}

	// Забаненный модератор теряет mod-права, даже с валидным access-токеном (бан
	// отзывает refresh, но access живёт до TTL — requireRole проверяет бан).
	if c, _ := do(t, h, "POST", "/api/mod/ban", fmt.Sprintf(`{"account_id":%d,"reason":"abuse"}`, modID), adminTok); c != http.StatusNoContent {
		t.Fatalf("admin bans mod: want 204, got %d", c)
	}
	if c, _ := do(t, h, "GET", "/api/mod/reports", "", modTok); c != http.StatusForbidden {
		t.Fatalf("banned moderator moderating: want 403, got %d", c)
	}
}

// TestModAntiCheatEndpoint: /api/mod/anticheat отдаёт флаги под ролью moderator+ (итер. 40).
func TestModAntiCheatEndpoint(t *testing.T) {
	st, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := account.NewService(st, []byte("api-test-secret-value"), time.Hour, 24*time.Hour)
	h := NewHandler(svc, st, slog.New(slog.NewTextHandler(io.Discard, nil)), RateLimit{}).Routes()

	reg := func(name string) (string, int64) {
		_, body := do(t, h, "POST", "/api/register", fmt.Sprintf(`{"username":%q,"password":"password12"}`, name), "")
		tok, _ := body["token"].(string)
		f, _ := body["id"].(float64)
		return tok, int64(f)
	}
	adminTok, adminID := reg("admin1")
	userTok, _ := reg("user1")
	_, cheaterID := reg("cheater")
	if err := st.SetRole(context.Background(), adminID, "admin"); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	if _, err := st.AddAntiCheat(context.Background(), cheaterID, "rewind_stale", 7, time.Now()); err != nil {
		t.Fatalf("seed anticheat: %v", err)
	}

	// Обычный user — 403.
	if c, _ := do(t, h, "GET", "/api/mod/anticheat", "", userTok); c != http.StatusForbidden {
		t.Fatalf("user anticheat: want 403, got %d", c)
	}
	// Admin видит флаг.
	code, body := do(t, h, "GET", "/api/mod/anticheat", "", adminTok)
	if code != http.StatusOK {
		t.Fatalf("admin anticheat: %d", code)
	}
	flags, _ := body["anticheat"].([]any)
	if len(flags) != 1 {
		t.Fatalf("want 1 flag, got %v", body["anticheat"])
	}
	first, _ := flags[0].(map[string]any)
	if int64(first["id"].(float64)) != cheaterID || int64(first["total"].(float64)) != 7 {
		t.Fatalf("flag = %v", first)
	}
}
