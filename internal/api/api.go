// Пакет api — REST-фасад бэкенда: регистрация/логин/гость, профиль, лидерборд,
// история матчей. Чистый net/http (без фреймворка — правило репо), JSON здесь
// уместен (не горячий путь). Знает про account и store, но НЕ про игру/транспорт.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arena/internal/account"
	"arena/internal/store"
)

// maxReasonLen — потолок текста причины бана/репорта.
const maxReasonLen = 500

// maxBodyBytes — потолок тела запроса (логин/регистрация — крошечные).
const maxBodyBytes = 4 << 10

// Handler обслуживает /api/*. Роуты монтируются под префиксом /api/ в cmd/server.
type Handler struct {
	accounts *account.Service
	store    store.Store
	log      *slog.Logger
	// limiter — пер-IP рейт-лимит на auth-эндпоинтах (итер. 21); nil — выключен.
	limiter *ipLimiter
}

// NewHandler собирает REST-обработчик. rl настраивает рейт-лимит на auth-эндпоинтах
// (нулевое значение RateLimit — лимит выключен, поведение как до итерации 21).
func NewHandler(a *account.Service, s store.Store, log *slog.Logger, rl RateLimit) *Handler {
	return &Handler{accounts: a, store: s, log: log, limiter: newIPLimiter(rl)}
}

// Routes отдаёт http.Handler со всеми маршрутами. Паттерны включают префикс /api,
// поэтому монтируются в основной mux как Handle("/api/", h.Routes()).
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	// Незалогиненные POST'ы, минтящие токены, — под рейт-лимитом (итер. 21).
	mux.HandleFunc("POST /api/register", h.rateLimited(h.register))
	mux.HandleFunc("POST /api/login", h.rateLimited(h.login))
	mux.HandleFunc("POST /api/guest", h.rateLimited(h.guest))
	mux.HandleFunc("POST /api/refresh", h.rateLimited(h.refresh)) // минтит токены — под лимитом
	mux.HandleFunc("POST /api/logout", h.logout)                  // отзыв, не минтит — без лимита
	// Верификация email и сброс пароля (итер. 37): рассылают письма / жгут токены —
	// под тем же пер-IP лимитом (брутфорс токенов / спам письмами).
	mux.HandleFunc("POST /api/verify-email", h.rateLimited(h.verifyEmail))
	mux.HandleFunc("POST /api/request-password-reset", h.rateLimited(h.requestPasswordReset))
	mux.HandleFunc("POST /api/reset-password", h.rateLimited(h.resetPassword))
	mux.HandleFunc("GET /api/me", h.me)
	// Репорты и модерация (итер. 39). Репорт шлёт любой залогиненный; модераторские
	// действия — под ролью (moderator/admin), проверка в requireRole.
	mux.HandleFunc("POST /api/report", h.report)
	mux.HandleFunc("POST /api/mod/ban", h.requireRole(account.RoleModerator, h.modBan))
	mux.HandleFunc("POST /api/mod/unban", h.requireRole(account.RoleModerator, h.modUnban))
	mux.HandleFunc("POST /api/mod/role", h.requireRole(account.RoleAdmin, h.modRole))
	mux.HandleFunc("GET /api/mod/reports", h.requireRole(account.RoleModerator, h.modReports))
	mux.HandleFunc("GET /api/leaderboard", h.leaderboard)
	mux.HandleFunc("GET /api/players/{id}/stats", h.playerStats)
	mux.HandleFunc("GET /api/players/{id}/matches", h.playerMatches)
	return mux
}

// rateLimited оборачивает обработчик пер-IP лимитом, если лимитер включён; иначе
// возвращает его как есть (сквозной путь).
func (h *Handler) rateLimited(next http.HandlerFunc) http.HandlerFunc {
	if h.limiter == nil {
		return next
	}
	return h.limiter.middleware(next)
}

// ---- DTO ----

type credentialsReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"` // опционален; используется только register (итер. 37)
}

type guestReq struct {
	Name string `json:"name"`
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
}

type verifyEmailReq struct {
	Token string `json:"token"`
}

type requestResetReq struct {
	Email string `json:"email"`
}

type resetPasswordReq struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type reportReq struct {
	TargetID int64  `json:"target_id"`
	Reason   string `json:"reason"`
}

type banReq struct {
	AccountID       int64  `json:"account_id"`
	Reason          string `json:"reason"`
	DurationSeconds int64  `json:"duration_seconds"` // 0 — навсегда
}

type roleReq struct {
	AccountID int64  `json:"account_id"`
	Role      string `json:"role"`
}

type identityResp struct {
	Token        string `json:"token"`                   // access-токен
	RefreshToken string `json:"refresh_token,omitempty"` // пусто у гостя
	ExpiresIn    int    `json:"expires_in"`              // TTL access-токена, секунды
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Guest        bool   `json:"guest"`
}

func identityToResp(id account.Identity, t account.Tokens) identityResp {
	return identityResp{
		Token: t.Access, RefreshToken: t.Refresh, ExpiresIn: t.ExpiresIn,
		ID: id.AccountID, Name: id.Name, Guest: id.Guest,
	}
}

// ---- handlers ----

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req credentialsReq
	if !decode(w, r, &req) {
		return
	}
	id, toks, err := h.accounts.Register(r.Context(), req.Username, req.Password, req.Email)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, identityToResp(id, toks))
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req credentialsReq
	if !decode(w, r, &req) {
		return
	}
	id, toks, err := h.accounts.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identityToResp(id, toks))
}

func (h *Handler) guest(w http.ResponseWriter, r *http.Request) {
	var req guestReq
	if !decode(w, r, &req) {
		return
	}
	id, toks, err := h.accounts.Guest(req.Name)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identityToResp(id, toks))
}

// refresh обменивает refresh-токен на свежую пару (ротация). Невалидный/просроченный/
// переиспользованный токен — 401 (ErrBadToken).
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshReq
	if !decode(w, r, &req) {
		return
	}
	id, toks, err := h.accounts.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identityToResp(id, toks))
}

// logout отзывает семейство refresh-токена (весь логин-сеанс). Идемпотентно: 204 даже
// на неизвестный токен (не раскрываем его существование).
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var req refreshReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.accounts.Logout(r.Context(), req.RefreshToken); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifyEmail подтверждает email по одноразовому токену (204). Невалидный токен — 401.
func (h *Handler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var req verifyEmailReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.accounts.VerifyEmail(r.Context(), req.Token); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestPasswordReset шлёт письмо сброса, если аккаунт с таким email есть. Всегда 204
// (не раскрываем существование аккаунта).
func (h *Handler) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req requestResetReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.accounts.RequestPasswordReset(r.Context(), req.Email); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resetPassword меняет пароль по токену сброса и разлогинивает все сессии (204).
// Невалидный токен — 401, слабый пароль — 400.
func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordReq
	if !decode(w, r, &req) {
		return
	}
	if err := h.accounts.ResetPassword(r.Context(), req.Token, req.Password); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// modHandler — обработчик, которому requireRole передаёт уже проверенного актора.
type modHandler func(w http.ResponseWriter, r *http.Request, actor store.Account)

// requireRole оборачивает модераторский обработчик проверкой роли: аутентифицирует
// access-токен, тянет актуальную роль из БД (не из токена — роль/бан могли смениться) и
// пропускает, только если её ранг не ниже minRole. Иначе 401/403.
func (h *Handler) requireRole(minRole string, next modHandler) http.HandlerFunc {
	min := account.RoleRank(minRole)
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := h.authenticate(w, r)
		if !ok {
			return
		}
		if id.Guest || id.AccountID == 0 {
			h.forbidden(w)
			return
		}
		acc, err := h.store.AccountByID(r.Context(), id.AccountID)
		if err != nil {
			h.writeError(w, err)
			return
		}
		if account.RoleRank(acc.Role) < min {
			h.forbidden(w)
			return
		}
		// Забаненный не модерирует, даже если роль ещё позволяет (access-токен живёт до
		// TTL; бан отзывает refresh, но не access — закрываем это здесь).
		if _, banned, err := h.accounts.IsBanned(r.Context(), acc.ID); err != nil {
			h.writeError(w, err)
			return
		} else if banned {
			h.forbidden(w)
			return
		}
		next(w, r, acc)
	}
}

// report — жалоба залогиненного игрока на другого (итер. 39). Гость не репортит.
func (h *Handler) report(w http.ResponseWriter, r *http.Request) {
	id, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if id.Guest || id.AccountID == 0 {
		h.forbidden(w)
		return
	}
	var req reportReq
	if !decode(w, r, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if req.TargetID <= 0 || req.TargetID == id.AccountID || reason == "" || len(reason) > maxReasonLen {
		badRequest(w, "invalid report")
		return
	}
	if _, err := h.store.AccountByID(r.Context(), req.TargetID); err != nil {
		h.writeError(w, err) // несуществующий target → 404
		return
	}
	if err := h.store.CreateReport(r.Context(), store.Report{
		ReporterID: id.AccountID, TargetID: req.TargetID, Reason: reason, CreatedAt: time.Now(),
	}); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// modBan банит аккаунт (moderator+). Нельзя банить себя и равного/старшего по роли;
// сессии забаненного отзываются (refresh мёртв — уже подключённый выпадет, зайти не сможет).
func (h *Handler) modBan(w http.ResponseWriter, r *http.Request, actor store.Account) {
	var req banReq
	if !decode(w, r, &req) {
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if req.AccountID <= 0 || req.AccountID == actor.ID || reason == "" || len(reason) > maxReasonLen {
		badRequest(w, "invalid ban request")
		return
	}
	target, err := h.store.AccountByID(r.Context(), req.AccountID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if account.RoleRank(target.Role) >= account.RoleRank(actor.Role) {
		h.forbidden(w) // модератор не банит равного или старшего
		return
	}
	var expires time.Time
	if req.DurationSeconds > 0 {
		expires = time.Now().Add(time.Duration(req.DurationSeconds) * time.Second)
	}
	if err := h.store.BanAccount(r.Context(), store.Ban{
		AccountID: req.AccountID, Reason: reason, CreatedBy: actor.ID,
		CreatedAt: time.Now(), ExpiresAt: expires,
	}); err != nil {
		h.writeError(w, err)
		return
	}
	if err := h.store.RevokeAllRefreshTokens(r.Context(), req.AccountID); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// modUnban снимает активные баны аккаунта (moderator+).
func (h *Handler) modUnban(w http.ResponseWriter, r *http.Request, _ store.Account) {
	var req banReq
	if !decode(w, r, &req) {
		return
	}
	if req.AccountID <= 0 {
		badRequest(w, "invalid account id")
		return
	}
	if err := h.store.LiftBans(r.Context(), req.AccountID, time.Now()); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// modRole меняет роль аккаунта (admin only). Свою роль менять нельзя (анти-lockout).
func (h *Handler) modRole(w http.ResponseWriter, r *http.Request, actor store.Account) {
	var req roleReq
	if !decode(w, r, &req) {
		return
	}
	if req.AccountID <= 0 || req.AccountID == actor.ID || !account.ValidRole(req.Role) {
		badRequest(w, "invalid role request")
		return
	}
	if _, err := h.store.AccountByID(r.Context(), req.AccountID); err != nil {
		h.writeError(w, err)
		return
	}
	if err := h.store.SetRole(r.Context(), req.AccountID, req.Role); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// modReports отдаёт жалобы (moderator+); ?status=open|reviewed фильтрует, пусто — все.
func (h *Handler) modReports(w http.ResponseWriter, r *http.Request, _ store.Account) {
	reports, err := h.store.ListReports(r.Context(), r.URL.Query().Get("status"), queryInt(r, "limit", 50))
	if err != nil {
		h.writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(reports))
	for _, rp := range reports {
		out = append(out, map[string]any{
			"id": rp.ID, "reporter_id": rp.ReporterID, "target_id": rp.TargetID,
			"reason": rp.Reason, "created_at": rp.CreatedAt.Unix(), "status": rp.Status,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": out})
}

// banExpiryUnix — Unix-секунды истечения бана, 0 — бессрочный.
func banExpiryUnix(b store.Ban) int64 {
	if b.ExpiresAt.IsZero() {
		return 0
	}
	return b.ExpiresAt.Unix()
}

func (h *Handler) forbidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	id, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	resp := map[string]any{"id": id.AccountID, "name": id.Name, "guest": id.Guest}
	if !id.Guest {
		st, err := h.store.Stats(r.Context(), id.AccountID)
		if err != nil {
			h.writeError(w, err)
			return
		}
		resp["stats"] = statsResp(st)
		// email + статус верификации (итер. 37), роль (итер. 39): читаем из аккаунта.
		if acc, err := h.store.AccountByID(r.Context(), id.AccountID); err == nil {
			resp["email"] = acc.Email
			resp["email_verified"] = acc.EmailVerified
			resp["role"] = acc.Role
		}
		// Статус бана (итер. 39): клиент покажет баннер и заблокирует connect.
		if ban, banned, err := h.accounts.IsBanned(r.Context(), id.AccountID); err == nil && banned {
			resp["banned"] = true
			resp["ban"] = map[string]any{"reason": ban.Reason, "expires_at": banExpiryUnix(ban)}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) leaderboard(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 10)
	entries, err := h.store.Leaderboard(r.Context(), limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"id": e.AccountID, "name": e.Username,
			"kills": e.Kills, "deaths": e.Deaths, "wins": e.Wins,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"leaderboard": out})
}

func (h *Handler) playerStats(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := h.store.AccountByID(r.Context(), id); err != nil {
		h.writeError(w, err)
		return
	}
	st, err := h.store.Stats(r.Context(), id)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statsResp(st))
}

func (h *Handler) playerMatches(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit := queryInt(r, "limit", 20)
	matches, err := h.store.MatchesByAccount(r.Context(), id, limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		out = append(out, map[string]any{
			"id": m.ID, "mode": m.Mode, "ended_at": m.EndedAt.Unix(),
			"kills": m.Kills, "deaths": m.Deaths, "won": m.Won,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": out})
}

func statsResp(s store.Stats) map[string]any {
	return map[string]any{"kills": s.Kills, "deaths": s.Deaths, "games": s.Games, "wins": s.Wins}
}

// ---- helpers ----

// authenticate достаёт identity из заголовка Authorization: Bearer <token>.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (account.Identity, bool) {
	auth := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || tok == "" {
		h.writeError(w, account.ErrBadToken)
		return account.Identity{}, false
	}
	id, err := h.accounts.Verify(strings.TrimSpace(tok))
	if err != nil {
		h.writeError(w, err)
		return account.Identity{}, false
	}
	return id, true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid player id"})
		return 0, false
	}
	return id, true
}

func queryInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// decode читает JSON-тело с потолком размера. При ошибке сам пишет 400 и вернёт false.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError мапит доменные ошибки в HTTP-коды, не раскрывая внутренности.
func (h *Handler) writeError(w http.ResponseWriter, err error) {
	var (
		status int
		msg    string
	)
	switch {
	case errors.Is(err, account.ErrValidation):
		status, msg = http.StatusBadRequest, strings.TrimPrefix(err.Error(), "account: validation failed: ")
	case errors.Is(err, account.ErrUsernameTaken):
		status, msg = http.StatusConflict, "username already taken"
	case errors.Is(err, account.ErrEmailTaken):
		status, msg = http.StatusConflict, "email already taken"
	case errors.Is(err, account.ErrInvalidCredentials):
		status, msg = http.StatusUnauthorized, "invalid credentials"
	case errors.Is(err, account.ErrBadToken):
		status, msg = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, store.ErrNotFound):
		status, msg = http.StatusNotFound, "not found"
	default:
		h.log.Error("api internal error", "err", err)
		status, msg = http.StatusInternalServerError, "internal error"
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
