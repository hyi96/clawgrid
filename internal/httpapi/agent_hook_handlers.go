package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

const (
	accountHookStatusPending = "pending_verification"
	accountHookStatusActive  = "active"
)

type accountHookRow struct {
	ID                    string
	URL                   string
	Enabled               bool
	Status                string
	VerificationRequested time.Time
	VerifiedAt            *time.Time
	LastSuccessAt         *time.Time
	LastFailureAt         *time.Time
	ConsecutiveFailures   int
	FailureReason         string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type agentHookDelivery struct {
	URL           string
	AuthToken     string
	Message       string
	Name          string
	WakeMode      string
	Deliver       bool
	TimeoutSecond int
}

type agentHookPayload struct {
	Message        string `json:"message"`
	Name           string `json:"name"`
	WakeMode       string `json:"wakeMode"`
	Deliver        bool   `json:"deliver"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

func normalizeAgentHookURL(raw string) (string, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return "", errors.New("hook_url_required")
	}
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid_hook_url")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return normalized, nil
	case "http":
		host := strings.ToLower(parsed.Hostname())
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return normalized, nil
		}
	}
	return "", errors.New("invalid_hook_url")
}

func normalizeHookToken(raw string) string {
	return strings.TrimSpace(raw)
}

func buildAccountHookResponse(row accountHookRow) map[string]any {
	return map[string]any{
		"id":                        row.ID,
		"url":                       row.URL,
		"enabled":                   row.Enabled,
		"status":                    row.Status,
		"verification_requested_at": row.VerificationRequested,
		"verified_at":               row.VerifiedAt,
		"last_success_at":           row.LastSuccessAt,
		"last_failure_at":           row.LastFailureAt,
		"consecutive_failures":      row.ConsecutiveFailures,
		"failure_reason":            row.FailureReason,
		"created_at":                row.CreatedAt,
		"updated_at":                row.UpdatedAt,
	}
}

func (s *Server) loadAccountHook(ctx context.Context, accountID string) (*accountHookRow, error) {
	var row accountHookRow
	err := s.db.QueryRow(ctx, `
SELECT id,
       url,
       enabled,
       status,
       verification_requested_at,
       verified_at,
       last_success_at,
       last_failure_at,
       consecutive_failures,
       failure_reason,
       created_at,
       updated_at
FROM account_hooks
WHERE account_id = $1`, accountID).Scan(
		&row.ID,
		&row.URL,
		&row.Enabled,
		&row.Status,
		&row.VerificationRequested,
		&row.VerifiedAt,
		&row.LastSuccessAt,
		&row.LastFailureAt,
		&row.ConsecutiveFailures,
		&row.FailureReason,
		&row.CreatedAt,
		&row.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Server) accountHookVerifyURL(token string) string {
	return strings.TrimRight(s.cfg.PublicAPIBase, "/") + "/agent-hooks/verify/" + url.PathEscape(token)
}

func (s *Server) agentHookVerificationMessage(callbackURL string) string {
	return "Clawgrid webhook verification. To verify this hook, make an HTTP POST request with no body to " + callbackURL + "."
}

func (s *Server) deliverAgentHookRequest(ctx context.Context, delivery agentHookDelivery) error {
	payload := agentHookPayload{
		Message:        delivery.Message,
		Name:           delivery.Name,
		WakeMode:       delivery.WakeMode,
		Deliver:        delivery.Deliver,
		TimeoutSeconds: delivery.TimeoutSecond,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("hook_delivery_failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("hook_delivery_failed")
	}
	req.Header.Set("Content-Type", "application/json")
	if delivery.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+delivery.AuthToken)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("hook_delivery_failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New("hook_delivery_failed")
	}
	return nil
}

func (s *Server) accountHookDeliveryEnabled(ctx context.Context, ownerType domain.OwnerType, ownerID string) (bool, error) {
	if ownerType != domain.OwnerAccount {
		return true, nil
	}
	var enabled bool
	err := s.db.QueryRow(ctx, `SELECT enabled FROM account_hooks WHERE account_id = $1`, ownerID).Scan(&enabled)
	if err == pgx.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

func (s *Server) handleAccountHookGet(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	row, err := s.loadAccountHook(r.Context(), actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if row == nil {
		respondJSON(w, http.StatusOK, map[string]any{"hook": nil})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"hook": buildAccountHookResponse(*row)})
}

func (s *Server) handleAccountHookPut(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	var body struct {
		URL       string `json:"url"`
		AuthToken string `json:"auth_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	normalizedURL, err := normalizeAgentHookURL(body.URL)
	if err != nil {
		respondErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(s.cfg.PublicAPIBase) == "" {
		respondErr(w, http.StatusServiceUnavailable, "hook_verification_unavailable")
		return
	}

	existing, err := s.loadAccountHook(r.Context(), actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	enabled := true
	if existing != nil {
		enabled = existing.Enabled
	}
	authToken := normalizeHookToken(body.AuthToken)
	if authToken == "" && existing != nil {
		var storedToken string
		if err := s.db.QueryRow(r.Context(), `SELECT auth_token FROM account_hooks WHERE account_id = $1`, actor.OwnerID).Scan(&storedToken); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		authToken = storedToken
	}
	if authToken == "" {
		respondErr(w, http.StatusBadRequest, "hook_auth_token_required")
		return
	}

	verifyToken := domain.NewID("ahv")
	if _, err := s.db.Exec(r.Context(), `
INSERT INTO account_hooks(
  id,
  account_id,
  url,
  auth_token,
  enabled,
  status,
  verification_token,
  verification_requested_at,
  verified_at,
  last_failure_at,
  consecutive_failures,
  failure_reason,
  updated_at
)
VALUES ($1,$2,$3,$4,$5,$6,$7,now(),NULL,NULL,0,'',now())
ON CONFLICT (account_id)
DO UPDATE SET
  url = EXCLUDED.url,
  auth_token = EXCLUDED.auth_token,
  enabled = EXCLUDED.enabled,
  status = EXCLUDED.status,
  verification_token = EXCLUDED.verification_token,
  verification_requested_at = EXCLUDED.verification_requested_at,
  verified_at = NULL,
  last_failure_at = NULL,
  consecutive_failures = 0,
  failure_reason = '',
  updated_at = now()`,
		domain.NewID("ahk"),
		actor.OwnerID,
		normalizedURL,
		authToken,
		enabled,
		accountHookStatusPending,
		verifyToken,
	); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	callbackURL := s.accountHookVerifyURL(verifyToken)
	if err := s.deliverAgentHook(r.Context(), agentHookDelivery{
		URL:           normalizedURL,
		AuthToken:     authToken,
		Message:       s.agentHookVerificationMessage(callbackURL),
		Name:          "Clawgrid",
		WakeMode:      "now",
		Deliver:       false,
		TimeoutSecond: 30,
	}); err != nil {
		_, _ = s.db.Exec(r.Context(), `
UPDATE account_hooks
SET last_failure_at = now(),
    consecutive_failures = consecutive_failures + 1,
    failure_reason = $2,
    updated_at = now()
WHERE account_id = $1`, actor.OwnerID, err.Error())
		status := http.StatusBadGateway
		if err.Error() == "hook_delivery_failed" {
			status = http.StatusBadGateway
		}
		respondErr(w, status, err.Error())
		return
	}

	row, err := s.loadAccountHook(r.Context(), actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"hook": buildAccountHookResponse(*row)})
}

func (s *Server) handleAccountHookDelete(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	res, err := s.db.Exec(r.Context(), `DELETE FROM account_hooks WHERE account_id = $1`, actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.RowsAffected() == 0 {
		respondErr(w, http.StatusNotFound, "hook not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountHookEnable(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	s.handleAccountHookSetEnabled(w, r, actor, true)
}

func (s *Server) handleAccountHookDisable(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	s.handleAccountHookSetEnabled(w, r, actor, false)
}

func (s *Server) handleAccountHookSetEnabled(w http.ResponseWriter, r *http.Request, actor domain.Actor, enabled bool) {
	if actor.OwnerType != domain.OwnerAccount {
		respondErr(w, http.StatusForbidden, "account required")
		return
	}
	res, err := s.db.Exec(r.Context(), `UPDATE account_hooks SET enabled = $2, updated_at = now() WHERE account_id = $1`, actor.OwnerID, enabled)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.RowsAffected() == 0 {
		respondErr(w, http.StatusNotFound, "hook not found")
		return
	}
	row, err := s.loadAccountHook(r.Context(), actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"hook": buildAccountHookResponse(*row)})
}

func (s *Server) handleAgentHookVerify(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" {
		respondErr(w, http.StatusNotFound, "verification_not_found")
		return
	}
	res, err := s.db.Exec(r.Context(), `
UPDATE account_hooks
SET status = $2,
    verification_token = NULL,
    verified_at = now(),
    last_success_at = now(),
    last_failure_at = NULL,
    consecutive_failures = 0,
    failure_reason = '',
    updated_at = now()
WHERE verification_token = $1`, token, accountHookStatusActive)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.RowsAffected() == 0 {
		respondErr(w, http.StatusNotFound, "verification_not_found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}
