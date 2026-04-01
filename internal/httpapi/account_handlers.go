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
)

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
	var name, githubLogin, avatarURL, responderDescription string
	if err := s.db.QueryRow(r.Context(), `SELECT name, COALESCE(github_login, ''), COALESCE(avatar_url, ''), COALESCE(responder_description, '') FROM accounts WHERE id = $1`, actor.OwnerID).Scan(&name, &githubLogin, &avatarURL, &responderDescription); err != nil {
		respondErr(w, http.StatusNotFound, "account not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":                    actor.OwnerID,
		"name":                  name,
		"github_login":          githubLogin,
		"avatar_url":            avatarURL,
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
	var responderUp, responderDown, responderAssignmentFailures, responderClaimFailures int
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
	if err := s.db.QueryRow(r.Context(), `
SELECT COUNT(*)::int
FROM assignments
WHERE responder_owner_type = $1
  AND responder_owner_id = $2
  AND status IN ('timeout', 'refused')`, string(actor.OwnerType), actor.OwnerID).Scan(&responderAssignmentFailures); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.db.QueryRow(r.Context(), `
SELECT COUNT(*)::int
FROM wallet_ledger l
WHERE l.owner_type = $1
  AND l.owner_id = $2
  AND (
    l.reason = 'responder_stake_slashed_claim_cancel'
    OR (
      l.reason = 'responder_stake_slashed_timeout'
      AND NOT EXISTS (
        SELECT 1
        FROM assignments a
        WHERE a.job_id = l.job_id
          AND a.responder_owner_type = l.owner_type
          AND a.responder_owner_id = l.owner_id
      )
    )
  )`, string(actor.OwnerType), actor.OwnerID).Scan(&responderClaimFailures); err != nil {
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

	respondJSON(w, http.StatusOK, buildAccountStats(feedbackGiven, repliesReceived, responderUp, responderDown, responderAssignmentFailures+responderClaimFailures, dispatchUp, dispatchDown, responsesSubmitted))
}

func buildAccountStats(feedbackGiven, repliesReceived, responderUp, responderDown, responderFailures, dispatchUp, dispatchDown, responsesSubmitted int) map[string]any {
	responderOutcomeTotal := responderUp + responderDown + responderFailures
	totalJobsReceived := responsesSubmitted + responderFailures
	dispatchRatedTotal := dispatchUp + dispatchDown
	feedbackRate := "n/a"
	if repliesReceived > 0 {
		feedbackRate = fmt.Sprintf("%d / %d", feedbackGiven, repliesReceived)
	}
	jobSuccessRate := "n/a"
	if responderOutcomeTotal > 0 {
		jobSuccessRate = fmt.Sprintf("%.1f%%", (100.0*float64(responderUp))/float64(responderOutcomeTotal))
	}
	dispatchAccuracy := "n/a"
	if dispatchRatedTotal > 0 {
		dispatchAccuracy = fmt.Sprintf("%.1f%%", (100.0*float64(dispatchUp))/float64(dispatchRatedTotal))
	}
	return map[string]any{
		"job_success_rate":    jobSuccessRate,
		"feedback_rate":       feedbackRate,
		"jobs_completed":      responsesSubmitted,
		"total_jobs_received": totalJobsReceived,
		"jobs_dispatched":     dispatchRatedTotal,
		"dispatch_accuracy":   dispatchAccuracy,
		"responses_submitted": responsesSubmitted,
	}
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
	if err := s.enforceRateLimit(r.Context(), "api_key_creation_rate_limited", apiKeyCreateRateLimitSpecs(actor.OwnerID)...); err != nil {
		if err.Error() == "api_key_creation_rate_limited" {
			respondRateLimit(w, err.Error())
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
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
