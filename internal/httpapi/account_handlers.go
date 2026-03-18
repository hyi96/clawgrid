package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

const guestSessionCookieName = "clawgrid_guest_session"

func (s *Server) handleGuestSessionCreate(w http.ResponseWriter, r *http.Request) {
	if !s.allowGuestBrowserRequest(r) {
		respondErr(w, http.StatusForbidden, "guest_frontend_only")
		return
	}
	ctx := r.Context()
	guestID := domain.NewID("gst")
	token := domain.NewID("gk")
	_, err := s.db.Exec(ctx, `INSERT INTO guest_sessions(id, guest_token_hash) VALUES ($1,$2)`, guestID, hash(s.cfg.GuestTokenSecret+token))
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.ensureWallet(ctx, domain.OwnerGuest, guestID)
	_, _ = s.db.Exec(ctx, `UPDATE wallets SET balance = GREATEST(balance, $1) WHERE owner_type = 'guest' AND owner_id = $2`, s.cfg.GuestInitialBalance, guestID)
	http.SetCookie(w, &http.Cookie{
		Name:     guestSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 30,
	})
	respondJSON(w, http.StatusCreated, map[string]any{"guest_id": guestID})
}

func (s *Server) handleAccountRegister(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Name           string `json:"name"`
		Email          string `json:"email"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	name := strings.TrimSpace(body.Name)
	password := body.Password
	if name == "" {
		respondErr(w, http.StatusBadRequest, "name_required")
		return
	}
	if utf8.RuneCountInString(name) > accountUsernameLimit {
		respondErr(w, http.StatusBadRequest, "name_too_long")
		return
	}
	if len(password) < accountPasswordMinBytes {
		respondErr(w, http.StatusBadRequest, "password_too_short")
		return
	}
	if len(password) > accountPasswordMaxBytes {
		respondErr(w, http.StatusBadRequest, "password_too_long")
		return
	}
	if err := s.verifyTurnstile(r.Context(), body.TurnstileToken, r.RemoteAddr); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "turnstile_unavailable" {
			status = http.StatusServiceUnavailable
		}
		respondErr(w, status, err.Error())
		return
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	accountID := domain.NewID("acct")
	if _, err := s.db.Exec(ctx, `INSERT INTO accounts(id, name, email, password_hash) VALUES ($1,$2,$3,$4)`, accountID, name, body.Email, passwordHash); err != nil {
		if isAccountsNameUniqueViolation(err) {
			respondErr(w, http.StatusConflict, "username_taken")
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.ensureWallet(ctx, domain.OwnerAccount, accountID)
	_, _ = s.db.Exec(ctx, `UPDATE wallets SET balance = GREATEST(balance, $1) WHERE owner_type = 'account' AND owner_id = $2`, s.cfg.AccountInitialBalance, accountID)
	_, apiKey, err := s.createAPIKey(ctx, accountID, "default")
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessionToken, err := s.createAccountSession(ctx, accountID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{"account_id": accountID, "api_key": apiKey, "session_token": sessionToken})
}

func (s *Server) handleAccountLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || body.Password == "" {
		respondErr(w, http.StatusBadRequest, "name_and_password_required")
		return
	}
	var accountID, passwordHash string
	if err := s.db.QueryRow(ctx, `SELECT id, password_hash FROM accounts WHERE lower(name) = lower($1)`, name).Scan(&accountID, &passwordHash); err != nil {
		respondErr(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if passwordHash == "" {
		respondErr(w, http.StatusUnauthorized, "password_not_set")
		return
	}
	if err := comparePassword(passwordHash, body.Password); err != nil {
		respondErr(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	sessionToken, err := s.createAccountSession(ctx, accountID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"account_id": accountID, "session_token": sessionToken})
}

func (s *Server) handleAccountLogout(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	if actor.AuthCredentialType == domain.AuthCredentialAccountSession && actor.AuthCredentialID != "" {
		if _, err := s.db.Exec(r.Context(), `UPDATE account_sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, actor.AuthCredentialID); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked_session": true})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked_session": false})
}

func (s *Server) handleAccountMe(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	var name, email, responderDescription string
	if err := s.db.QueryRow(r.Context(), `SELECT name, COALESCE(email, ''), COALESCE(responder_description, '') FROM accounts WHERE id = $1`, actor.OwnerID).Scan(&name, &email, &responderDescription); err != nil {
		respondErr(w, http.StatusNotFound, "account not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":                    actor.OwnerID,
		"name":                  name,
		"email":                 email,
		"responder_description": responderDescription,
		"auth_credential_type":  actor.AuthCredentialType,
	})
}

func (s *Server) handleAccountMePatch(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	var body struct {
		ResponderDescription string `json:"responder_description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	responderDescription := strings.TrimSpace(body.ResponderDescription)
	if utf8.RuneCountInString(responderDescription) > responderDescriptionLimit {
		respondErr(w, http.StatusBadRequest, "responder_description_too_long")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE accounts SET responder_description = $1 WHERE id = $2`, responderDescription, actor.OwnerID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "responder_description": responderDescription})
}

func (s *Server) handleAccountStats(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	var responderUp, responderDown int
	if err := s.db.QueryRow(r.Context(), `
SELECT
  COUNT(*) FILTER (WHERE j.prompter_vote = 'up')::int AS up_count,
  COUNT(*) FILTER (WHERE j.prompter_vote = 'down')::int AS down_count
FROM jobs j
JOIN messages m ON m.id = j.response_message_id
WHERE m.owner_type = $1
  AND m.owner_id = $2
  AND j.prompter_vote IN ('up','down')`, string(actor.OwnerType), actor.OwnerID).Scan(&responderUp, &responderDown); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var responsesSubmitted int
	if err := s.db.QueryRow(r.Context(), `SELECT COUNT(*)::int FROM messages WHERE owner_type = $1 AND owner_id = $2 AND type = 'text' AND role = 'responder'`, string(actor.OwnerType), actor.OwnerID).Scan(&responsesSubmitted); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var feedbackGiven, repliesReceived int
	if err := s.db.QueryRow(r.Context(), `
SELECT
  COUNT(*) FILTER (WHERE prompter_vote IN ('up','down'))::int AS feedback_given,
  COUNT(*) FILTER (WHERE response_message_id IS NOT NULL)::int AS replies_received
FROM jobs
WHERE owner_type = $1
  AND owner_id = $2`, string(actor.OwnerType), actor.OwnerID).Scan(&feedbackGiven, &repliesReceived); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var dispatchUp, dispatchDown int
	if err := s.db.QueryRow(r.Context(), `
SELECT
  COUNT(*) FILTER (WHERE j.prompter_vote = 'up')::int AS up_count,
  COUNT(*) FILTER (WHERE j.prompter_vote = 'down')::int AS down_count
FROM jobs j
JOIN LATERAL (
  SELECT a.dispatcher_owner_type, a.dispatcher_owner_id
  FROM assignments a
  WHERE a.job_id = j.id
  ORDER BY a.assigned_at DESC
  LIMIT 1
) last_asn ON TRUE
WHERE last_asn.dispatcher_owner_type = $1
  AND last_asn.dispatcher_owner_id = $2
  AND j.prompter_vote IN ('up','down')`, string(actor.OwnerType), actor.OwnerID).Scan(&dispatchUp, &dispatchDown); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, buildAccountStats(feedbackGiven, repliesReceived, responderUp, responderDown, dispatchUp, dispatchDown, responsesSubmitted))
}

func buildAccountStats(feedbackGiven, repliesReceived, responderUp, responderDown, dispatchUp, dispatchDown, responsesSubmitted int) map[string]any {
	responderRatedTotal := responderUp + responderDown
	dispatchRatedTotal := dispatchUp + dispatchDown
	feedbackRate := "n/a"
	if repliesReceived > 0 {
		feedbackRate = fmt.Sprintf("%d / %d", feedbackGiven, repliesReceived)
	}
	jobSuccessRate := "n/a"
	if responderRatedTotal > 0 {
		jobSuccessRate = fmt.Sprintf("%.1f%%", (100.0*float64(responderUp))/float64(responderRatedTotal))
	}
	dispatchAccuracy := "n/a"
	if dispatchRatedTotal > 0 {
		dispatchAccuracy = fmt.Sprintf("%.1f%%", (100.0*float64(dispatchUp))/float64(dispatchRatedTotal))
	}
	return map[string]any{
		"job_success_rate":    jobSuccessRate,
		"feedback_rate":       feedbackRate,
		"jobs_completed":      responderRatedTotal,
		"jobs_dispatched":     dispatchRatedTotal,
		"dispatch_accuracy":   dispatchAccuracy,
		"responses_submitted": responsesSubmitted,
	}
}

func isAccountsNameUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && pgErr.ConstraintName == "accounts_name_lower_unique"
}

func (s *Server) handleAPIKeysList(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id, COALESCE(label,''), created_at, last_used_at FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL ORDER BY created_at DESC`, actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, label string
		var created time.Time
		var last *time.Time
		_ = rows.Scan(&id, &label, &created, &last)
		items = append(items, map[string]any{"id": id, "label": label, "created_at": created, "last_used_at": last})
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAPIKeysCreate(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	id, key, err := s.createAPIKey(r.Context(), actor.OwnerID, body.Label)
	if err != nil {
		if errors.Is(err, errAPIKeyLimitReached) {
			respondErr(w, http.StatusConflict, "api_key_limit_reached")
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{"id": id, "api_key": key})
}

func (s *Server) handleAPIKeysDelete(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	id := r.PathValue("key_id")
	res, err := s.db.Exec(r.Context(), `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND account_id = $2 AND revoked_at IS NULL`, id, actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.RowsAffected() == 0 {
		respondErr(w, http.StatusNotFound, "key not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}
