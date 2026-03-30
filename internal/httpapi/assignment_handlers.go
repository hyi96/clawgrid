package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"clawgrid/internal/domain"
)

func (s *Server) handleAssignmentsCreate(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	limited, err := s.isRateLimited(r.Context(), assignmentFailureRateLimitSpecs(actor.OwnerID)...)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if limited {
		respondRateLimit(w, "assignment_rate_limited")
		return
	}
	if err := s.enforceRateLimit(r.Context(), "assignment_rate_limited", assignmentAttemptRateLimitSpecs(actor.OwnerID)...); err != nil {
		if err.Error() == "assignment_rate_limited" {
			respondRateLimit(w, err.Error())
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body struct {
		JobID            string `json:"job_id"`
		ResponderOwnerID string `json:"responder_owner_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if body.JobID == "" || body.ResponderOwnerID == "" {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())
	if err := lockResponderActorTx(r.Context(), tx, domain.OwnerAccount, body.ResponderOwnerID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	responderExists, err := responderActorExistsTx(r.Context(), tx, domain.OwnerAccount, body.ResponderOwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !responderExists {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusNotFound, "responder_not_found")
		return
	}
	hookEnabled, err := s.accountHookDeliveryEnabled(r.Context(), domain.OwnerAccount, body.ResponderOwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !hookEnabled {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "responder_not_available")
		return
	}

	var jobOwnerType, jobOwnerID, sessionID, status string
	var timeLimitMinutes int
	if err := tx.QueryRow(r.Context(), `
SELECT owner_type,
       owner_id,
       session_id,
       status,
       COALESCE(NULLIF(metadata_json->>'time_limit_minutes', '')::int, $2::int)
FROM jobs
WHERE id = $1
FOR UPDATE`, body.JobID, int(s.cfg.AssignmentDeadline.Minutes())).Scan(&jobOwnerType, &jobOwnerID, &sessionID, &status, &timeLimitMinutes); err != nil {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusNotFound, "job not found")
		return
	}
	if status != "routing" {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "job_not_routing")
		return
	}
	if guard := assignmentGuard(
		string(actor.OwnerType),
		actor.OwnerID,
		jobOwnerType,
		jobOwnerID,
		string(domain.OwnerAccount),
		body.ResponderOwnerID,
	); guard != "" {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusBadRequest, guard)
		return
	}
	responderBusy, err := responderHasActiveWorkTx(r.Context(), tx, domain.OwnerAccount, body.ResponderOwnerID, "")
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if responderBusy {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "responder_busy")
		return
	}
	responderAvailable, err := responderHasLiveAvailabilityTx(
		r.Context(),
		tx,
		domain.OwnerAccount,
		body.ResponderOwnerID,
		int(s.cfg.ResponderActiveWindow.Seconds()),
		int(s.cfg.PollAssignmentWait.Seconds()),
	)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !responderAvailable {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "responder_not_available")
		return
	}
	selfDispatched := jobOwnerType == string(actor.OwnerType) && jobOwnerID == actor.OwnerID
	if err := s.holdResponderStake(r.Context(), tx, body.JobID, domain.OwnerAccount, body.ResponderOwnerID); err != nil {
		if err.Error() == "insufficient_balance" {
			s.recordAssignmentFailure(r.Context(), actor.OwnerID)
			respondErr(w, http.StatusPaymentRequired, "responder_insufficient_stake_balance")
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.holdDispatcherStake(r.Context(), tx, body.JobID, actor.OwnerType, actor.OwnerID, selfDispatched); err != nil {
		if err.Error() == "insufficient_balance" {
			s.recordAssignmentFailure(r.Context(), actor.OwnerID)
			respondErr(w, http.StatusPaymentRequired, "dispatcher_insufficient_balance")
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := domain.NewID("asn")
	_, err = tx.Exec(r.Context(), `
INSERT INTO assignments(id, job_id, dispatcher_owner_type, dispatcher_owner_id, responder_owner_type, responder_owner_id, deadline_at, status)
VALUES ($1,$2,$3,$4,$5,$6, now() + make_interval(mins => $7::int), 'active')`,
		id, body.JobID, string(actor.OwnerType), actor.OwnerID, string(domain.OwnerAccount), body.ResponderOwnerID, timeLimitMinutes)
	if err != nil {
		s.recordAssignmentFailure(r.Context(), actor.OwnerID)
		respondErr(w, http.StatusConflict, "assignment_conflict")
		return
	}
	_, _ = tx.Exec(r.Context(), `UPDATE jobs SET status = 'assigned' WHERE id = $1`, body.JobID)
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyAssignmentReceived(context.Background(), body.ResponderOwnerID, body.JobID, sessionID)
	respondJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func assignmentGuard(
	dispatcherOwnerType, dispatcherOwnerID string,
	jobOwnerType, jobOwnerID string,
	responderOwnerType, responderOwnerID string,
) string {
	if responderOwnerType == dispatcherOwnerType && responderOwnerID == dispatcherOwnerID {
		return "dispatcher_cannot_assign_self"
	}
	if responderOwnerType == jobOwnerType && responderOwnerID == jobOwnerID {
		return "prompter_cannot_be_responder"
	}
	return ""
}

func (s *Server) handleAssignmentsGet(w http.ResponseWriter, r *http.Request, _ domain.Actor) {
	id := r.PathValue("id")
	var status string
	var deadline time.Time
	if err := s.db.QueryRow(r.Context(), `SELECT status, deadline_at FROM assignments WHERE id = $1`, id).Scan(&status, &deadline); err != nil {
		respondErr(w, http.StatusNotFound, "assignment not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"id": id, "status": status, "deadline_at": deadline})
}
