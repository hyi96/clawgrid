package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Server) handleJobClaim(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	limited, err := s.isRateLimited(r.Context(), claimFailureRateLimitSpecs(actor.OwnerID)...)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if limited {
		respondRateLimit(w, "claim_rate_limited")
		return
	}
	if err := s.enforceRateLimit(r.Context(), "claim_rate_limited", claimAttemptRateLimitSpecs(actor.OwnerID)...); err != nil {
		if err.Error() == "claim_rate_limited" {
			respondRateLimit(w, err.Error())
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jobID := r.PathValue("id")
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	if err := lockResponderActorTx(r.Context(), tx, actor.OwnerType, actor.OwnerID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	var status, ownerType, ownerID string
	var tipAmount float64
	var responseID, claimOwnerType, claimOwnerID *string
	var claimExpiresAt *time.Time
	var timeLimitMinutes int
	var sessionID, requestMessageID string
	err = tx.QueryRow(r.Context(), `
SELECT status, owner_type, owner_id, tip_amount, session_id, request_message_id, response_message_id, claim_owner_type, claim_owner_id, claim_expires_at,
       COALESCE(NULLIF(metadata_json->>'time_limit_minutes', '')::int, $2::int)
FROM jobs
WHERE id = $1
FOR UPDATE`, jobID, int(s.cfg.AssignmentDeadline.Minutes())).Scan(
		&status,
		&ownerType,
		&ownerID,
		&tipAmount,
		&sessionID,
		&requestMessageID,
		&responseID,
		&claimOwnerType,
		&claimOwnerID,
		&claimExpiresAt,
		&timeLimitMinutes,
	)
	if err != nil {
		s.recordClaimFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusNotFound, "job not found")
		return
	}
	if responseID != nil {
		s.recordClaimFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "job_already_replied")
		return
	}
	if status != "system_pool" {
		s.recordClaimFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "job_not_pool")
		return
	}
	if ownerType == string(actor.OwnerType) && ownerID == actor.OwnerID {
		s.recordClaimFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusBadRequest, "prompter_cannot_claim")
		return
	}
	now := time.Now()
	if claimExpiresAt != nil && claimExpiresAt.After(now) {
		if claimOwnerType != nil && claimOwnerID != nil && *claimOwnerType == string(actor.OwnerType) && *claimOwnerID == actor.OwnerID {
			respondJSON(w, http.StatusOK, map[string]any{
				"ok":                  true,
				"job_id":              jobID,
				"claim_expires_at":    claimExpiresAt,
				"id":                  jobID,
				"status":              status,
				"tip_amount":          tipAmount,
				"time_limit_minutes":  timeLimitMinutes,
				"session_id":          sessionID,
				"request_message_id":  requestMessageID,
				"response_message_id": "",
				"prompter_vote":       "",
				"review_deadline_at":  nil,
				"claim_owner_type":    *claimOwnerType,
				"claim_owner_id":      *claimOwnerID,
				"work_deadline_at":    claimExpiresAt,
			})
			return
		}
		s.recordClaimFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "job_already_claimed")
		return
	}
	responderBusy, err := responderHasActiveWorkTx(r.Context(), tx, actor.OwnerType, actor.OwnerID, jobID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if responderBusy {
		s.recordClaimFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "responder_busy")
		return
	}
	if timeLimitMinutes <= 0 {
		timeLimitMinutes = int(s.cfg.AssignmentDeadline.Minutes())
	}
	if err := s.holdResponderStake(r.Context(), tx, jobID, actor.OwnerType, actor.OwnerID); err != nil {
		if err.Error() == "insufficient_balance" {
			s.recordClaimFailure(r.Context(), actor.OwnerID)
			respondErr(w, http.StatusPaymentRequired, "insufficient_stake_balance")
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var newExpiry time.Time
	if err := tx.QueryRow(r.Context(), `
UPDATE jobs
SET claim_owner_type = $2,
    claim_owner_id = $3,
    claim_expires_at = now() + make_interval(mins => $4::int)
WHERE id = $1
RETURNING claim_expires_at`, jobID, string(actor.OwnerType), actor.OwnerID, timeLimitMinutes).Scan(&newExpiry); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.clearResponderPoolSnapshot(r.Context(), actor)
	respondJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"job_id":              jobID,
		"claim_expires_at":    newExpiry,
		"id":                  jobID,
		"status":              status,
		"tip_amount":          tipAmount,
		"time_limit_minutes":  timeLimitMinutes,
		"session_id":          sessionID,
		"request_message_id":  requestMessageID,
		"response_message_id": "",
		"prompter_vote":       "",
		"review_deadline_at":  nil,
		"claim_owner_type":    string(actor.OwnerType),
		"claim_owner_id":      actor.OwnerID,
		"work_deadline_at":    newExpiry,
	})
}

func normalizeResponderCancelReason(reason string) (string, string) {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(reason)), " ")
	if normalized == "" {
		return "", "reason required"
	}
	if utf8.RuneCountInString(normalized) > responderCancelReasonLimit {
		return "", "reason_too_long"
	}
	return normalized, ""
}

func responderCancellationFeedbackContent(kind, reason string) string {
	return fmt.Sprintf("a responder cancelled the %s job due to %q", kind, reason)
}

func loadActiveAssignmentTx(ctx context.Context, tx pgx.Tx, jobID string) (string, string, string, string, string, error) {
	var assignmentID, responderType, responderID, dispatcherType, dispatcherID string
	err := tx.QueryRow(ctx, `
SELECT id, responder_owner_type, responder_owner_id, dispatcher_owner_type, dispatcher_owner_id
FROM assignments
WHERE job_id = $1
  AND status = 'active'
ORDER BY assigned_at DESC
LIMIT 1
FOR UPDATE`, jobID).Scan(&assignmentID, &responderType, &responderID, &dispatcherType, &dispatcherID)
	return assignmentID, responderType, responderID, dispatcherType, dispatcherID, err
}

func (s *Server) handleResponderJobCancel(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	jobID := r.PathValue("id")
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	reason, invalidReason := normalizeResponderCancelReason(body.Reason)
	if invalidReason != "" {
		respondErr(w, http.StatusBadRequest, invalidReason)
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	if err := lockResponderActorTx(r.Context(), tx, actor.OwnerType, actor.OwnerID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	var sessionID, status string
	var responseID, claimOwnerType, claimOwnerID *string
	var claimExpiresAt *time.Time
	if err := tx.QueryRow(r.Context(), `
SELECT session_id, status, response_message_id, claim_owner_type, claim_owner_id, claim_expires_at
FROM jobs
WHERE id = $1
FOR UPDATE`, jobID).Scan(&sessionID, &status, &responseID, &claimOwnerType, &claimOwnerID, &claimExpiresAt); err != nil {
		respondErr(w, http.StatusNotFound, "job not found")
		return
	}
	if responseID != nil {
		respondErr(w, http.StatusConflict, "job_already_replied")
		return
	}

	cancelMode := ""
	switch status {
	case "assigned":
		assignmentID, responderType, responderID, dispatcherType, dispatcherID, err := loadActiveAssignmentTx(r.Context(), tx, jobID)
		if err != nil {
			if err == pgx.ErrNoRows {
				respondErr(w, http.StatusConflict, "job_not_open")
				return
			}
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if responderType != string(actor.OwnerType) || responderID != actor.OwnerID {
			respondErr(w, http.StatusForbidden, "not_assigned_responder")
			return
		}
		if _, err := tx.Exec(r.Context(), `
UPDATE jobs
SET status = 'system_pool',
    last_system_pool_entered_at = now(),
    claim_owner_type = NULL,
    claim_owner_id = NULL,
    claim_expires_at = NULL
WHERE id = $1`, jobID); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := tx.Exec(r.Context(), `UPDATE assignments SET status = 'refused' WHERE id = $1`, assignmentID); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.refundResponderStakeWithReason(r.Context(), tx, jobID, actor.OwnerType, actor.OwnerID, "responder_stake_refund_assignment_cancel"); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if dispatcherType != "" && dispatcherID != "" {
			if err := s.settleDispatcherStakeAssignedCancel(r.Context(), tx, jobID, domain.OwnerType(dispatcherType), dispatcherID); err != nil {
				respondErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1,$2,$3,$4,'feedback','responder',$5)`,
			domain.NewID("msg"), sessionID, string(actor.OwnerType), actor.OwnerID, responderCancellationFeedbackContent("assigned", reason)); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		cancelMode = "assigned"
	case "system_pool":
		if claimOwnerType == nil || claimOwnerID == nil || claimExpiresAt == nil || !claimExpiresAt.After(time.Now()) {
			respondErr(w, http.StatusConflict, "job_not_claimed_by_you")
			return
		}
		if *claimOwnerType != string(actor.OwnerType) || *claimOwnerID != actor.OwnerID {
			respondErr(w, http.StatusConflict, "job_not_claimed_by_you")
			return
		}
		if _, err := tx.Exec(r.Context(), `
UPDATE jobs
SET claim_owner_type = NULL,
    claim_owner_id = NULL,
    claim_expires_at = NULL
WHERE id = $1`, jobID); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := s.slashResponderStake(r.Context(), tx, jobID, actor.OwnerType, actor.OwnerID, "responder_stake_slashed_claim_cancel"); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1,$2,$3,$4,'feedback','responder',$5)`,
			domain.NewID("msg"), sessionID, string(actor.OwnerType), actor.OwnerID, responderCancellationFeedbackContent("claimed", reason)); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		cancelMode = "claimed"
	default:
		respondErr(w, http.StatusConflict, "job_not_open")
		return
	}
	if err := s.clearAvailabilityTx(r.Context(), tx, actor); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.clearResponderPoolSnapshot(r.Context(), actor)
	respondJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"job_id":     jobID,
		"mode":       cancelMode,
		"reason":     reason,
		"job_status": "system_pool",
	})
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	jobID := r.PathValue("id")
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	var ownerType, ownerID, status string
	var responseID *string
	var claimOwnerType, claimOwnerID *string
	var claimExpiresAt *time.Time
	if err := tx.QueryRow(r.Context(), `
SELECT owner_type, owner_id, status, response_message_id, claim_owner_type, claim_owner_id, claim_expires_at
FROM jobs
WHERE id = $1
FOR UPDATE`, jobID).Scan(&ownerType, &ownerID, &status, &responseID, &claimOwnerType, &claimOwnerID, &claimExpiresAt); err != nil {
		respondErr(w, http.StatusNotFound, "job not found")
		return
	}
	if ownerType != string(actor.OwnerType) || ownerID != actor.OwnerID {
		respondErr(w, http.StatusForbidden, "forbidden")
		return
	}
	if responseID != nil {
		respondErr(w, http.StatusConflict, "cannot_cancel_replied_job")
		return
	}
	if status != "routing" && status != "system_pool" && status != "assigned" {
		respondErr(w, http.StatusConflict, "job_not_cancellable")
		return
	}

	activeWork := false
	var activeResponderType, activeResponderID string
	var activeDispatcherType, activeDispatcherID string
	if status == "assigned" {
		var hasActiveAssignment bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM assignments WHERE job_id = $1 AND status = 'active')`, jobID).Scan(&hasActiveAssignment); err == nil && hasActiveAssignment {
			activeWork = true
		}
		_ = tx.QueryRow(r.Context(), `SELECT responder_owner_type, responder_owner_id, dispatcher_owner_type, dispatcher_owner_id FROM assignments WHERE job_id = $1 AND status = 'active' ORDER BY assigned_at DESC LIMIT 1`, jobID).Scan(&activeResponderType, &activeResponderID, &activeDispatcherType, &activeDispatcherID)
	}
	if status == "system_pool" && claimExpiresAt != nil && claimExpiresAt.After(time.Now()) && claimOwnerType != nil && claimOwnerID != nil {
		activeWork = true
		activeResponderType = *claimOwnerType
		activeResponderID = *claimOwnerID
	}

	if _, err := tx.Exec(r.Context(), `
UPDATE jobs
SET status = 'cancelled',
    claim_owner_type = NULL,
    claim_owner_id = NULL,
    claim_expires_at = NULL
WHERE id = $1`, jobID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, _ = tx.Exec(r.Context(), `UPDATE assignments SET status = 'cancelled' WHERE job_id = $1 AND status = 'active'`, jobID)
	if activeResponderType != "" && activeResponderID != "" {
		if err := s.refundResponderStake(r.Context(), tx, jobID, domain.OwnerType(activeResponderType), activeResponderID); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if activeDispatcherType != "" && activeDispatcherID != "" {
		if err := s.refundDispatcherStake(r.Context(), tx, jobID, domain.OwnerType(activeDispatcherType), activeDispatcherID); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if activeWork && s.cfg.PrompterCancelPenalty > 0 {
		_ = s.adjustWallet(r.Context(), tx, actor.OwnerType, actor.OwnerID, -s.cfg.PrompterCancelPenalty)
		_ = s.ledger(r.Context(), tx, actor.OwnerType, actor.OwnerID, -s.cfg.PrompterCancelPenalty, "prompter_cancel_active_penalty", &jobID, nil)
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "penalized": activeWork, "penalty_amount": s.cfg.PrompterCancelPenalty})
}

func (s *Server) handleJobReply(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	jobID := r.PathValue("id")
	var body struct {
		Content string `json:"content"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if strings.TrimSpace(body.Content) == "" {
		respondErr(w, http.StatusBadRequest, "content required")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	var sid, ownerType, ownerID, status string
	var responseID *string
	if err := tx.QueryRow(r.Context(), `SELECT session_id, owner_type, owner_id, status, response_message_id FROM jobs WHERE id = $1 FOR UPDATE`, jobID).Scan(&sid, &ownerType, &ownerID, &status, &responseID); err != nil {
		respondErr(w, http.StatusNotFound, "job not found")
		return
	}
	if responseID != nil {
		respondErr(w, http.StatusConflict, "job_already_replied")
		return
	}
	if ownerType == string(actor.OwnerType) && ownerID == actor.OwnerID {
		respondErr(w, http.StatusBadRequest, "prompter_cannot_reply")
		return
	}
	if status == "assigned" {
		var ok bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM assignments WHERE job_id = $1 AND status = 'active' AND responder_owner_type = $2 AND responder_owner_id = $3)`, jobID, string(actor.OwnerType), actor.OwnerID).Scan(&ok); err != nil || !ok {
			respondErr(w, http.StatusForbidden, "not_assigned_responder")
			return
		}
	}
	if status == "system_pool" {
		var ok bool
		if err := tx.QueryRow(r.Context(), `
SELECT EXISTS(
  SELECT 1
  FROM jobs
  WHERE id = $1
    AND claim_owner_type = $2
    AND claim_owner_id = $3
    AND claim_expires_at > now()
)`, jobID, string(actor.OwnerType), actor.OwnerID).Scan(&ok); err != nil || !ok {
			respondErr(w, http.StatusConflict, "job_not_claimed_by_you")
			return
		}
	}
	if status != "assigned" && status != "system_pool" {
		respondErr(w, http.StatusConflict, "job_not_open")
		return
	}
	mid := domain.NewID("msg")
	if _, err := tx.Exec(r.Context(), `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1,$2,$3,$4,'text','responder',$5)`, mid, sid, string(actor.OwnerType), actor.OwnerID, body.Content); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, _ = tx.Exec(r.Context(), `UPDATE jobs SET status = 'review_pending', response_message_id = $1, review_deadline_at = now() + make_interval(hours => $2::int), claim_owner_type = NULL, claim_owner_id = NULL, claim_expires_at = NULL WHERE id = $3`, mid, int(s.cfg.ReviewWindow.Hours()), jobID)
	if _, err := tx.Exec(r.Context(), `UPDATE assignments SET status = 'success' WHERE job_id = $1 AND status = 'active' AND responder_owner_type = $2 AND responder_owner_id = $3`, jobID, string(actor.OwnerType), actor.OwnerID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.refreshStoredDispatchSessionSnippetTx(r.Context(), tx, sid); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"message_id": mid, "job_id": jobID})
}
