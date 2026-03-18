package httpapi

import (
	"net/http"
	"time"

	"clawgrid/internal/domain"
)

func effectiveJobStatus(status, responseMessageID, vote string) string {
	if responseMessageID != "" && vote == "" {
		return "review_pending"
	}
	return status
}

func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if err := s.syncJobQueues(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	var ownerType, ownerID, status, vote string
	var tip float64
	var timeLimitMinutes int
	var sessionID, requestMessageID, responseMessageID, claimOwnerType, claimOwnerID string
	var reviewDeadline, claimExpiresAt *time.Time
	err := s.db.QueryRow(r.Context(), `
SELECT owner_type, owner_id, status, tip_amount, COALESCE(NULLIF(metadata_json->>'time_limit_minutes', '')::int, 0), session_id, request_message_id, COALESCE(response_message_id,''), COALESCE(prompter_vote,''), review_deadline_at, COALESCE(claim_owner_type, ''), COALESCE(claim_owner_id, ''), claim_expires_at
FROM jobs WHERE id = $1`, id).
		Scan(&ownerType, &ownerID, &status, &tip, &timeLimitMinutes, &sessionID, &requestMessageID, &responseMessageID, &vote, &reviewDeadline, &claimOwnerType, &claimOwnerID, &claimExpiresAt)
	if err != nil {
		respondErr(w, http.StatusNotFound, "job not found")
		return
	}
	if !s.canAccessJob(r.Context(), id, ownerType, ownerID, status, actor) {
		respondErr(w, http.StatusForbidden, "forbidden")
		return
	}
	status = effectiveJobStatus(status, responseMessageID, vote)
	var workDeadlineAt *time.Time
	if status == "system_pool" {
		workDeadlineAt = claimExpiresAt
	}
	if status == "assigned" {
		_ = s.db.QueryRow(r.Context(), `
SELECT deadline_at
FROM assignments
WHERE job_id = $1
  AND status = 'active'
  AND responder_owner_type = $2
  AND responder_owner_id = $3
ORDER BY assigned_at DESC
LIMIT 1`, id, string(actor.OwnerType), actor.OwnerID).Scan(&workDeadlineAt)
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"id":                  id,
		"status":              status,
		"tip_amount":          tip,
		"time_limit_minutes":  timeLimitMinutes,
		"session_id":          sessionID,
		"request_message_id":  requestMessageID,
		"response_message_id": responseMessageID,
		"prompter_vote":       vote,
		"review_deadline_at":  reviewDeadline,
		"claim_owner_type":    claimOwnerType,
		"claim_owner_id":      claimOwnerID,
		"claim_expires_at":    claimExpiresAt,
		"work_deadline_at":    workDeadlineAt,
	})
}

func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if err := s.syncJobQueues(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := r.URL.Query().Get("status")
	sessionID := r.URL.Query().Get("session_id")
	rows, err := s.db.Query(r.Context(), `
SELECT id, status, created_at, session_id, COALESCE(response_message_id,''), COALESCE(prompter_vote,''), review_deadline_at, COALESCE(claim_owner_type, ''), COALESCE(claim_owner_id, ''), claim_expires_at
FROM jobs
WHERE owner_type = $1
  AND owner_id = $2
  AND ($3 = '' OR status = $3)
  AND ($4 = '' OR session_id = $4)
ORDER BY created_at DESC
LIMIT 200`, string(actor.OwnerType), actor.OwnerID, status, sessionID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, st, sid, responseID, vote, claimOwnerType, claimOwnerID string
		var created time.Time
		var reviewDeadline, claimExpiresAt *time.Time
		_ = rows.Scan(&id, &st, &created, &sid, &responseID, &vote, &reviewDeadline, &claimOwnerType, &claimOwnerID, &claimExpiresAt)
		st = effectiveJobStatus(st, responseID, vote)
		items = append(items, map[string]any{
			"id":                  id,
			"status":              st,
			"created_at":          created,
			"session_id":          sid,
			"response_message_id": responseID,
			"prompter_vote":       vote,
			"review_deadline_at":  reviewDeadline,
			"claim_owner_type":    claimOwnerType,
			"claim_owner_id":      claimOwnerID,
			"claim_expires_at":    claimExpiresAt,
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRoutingJobs(w http.ResponseWriter, r *http.Request, _ domain.Actor) {
	if err := s.syncJobQueues(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rows, err := s.db.Query(r.Context(), `
SELECT j.id, j.session_id, j.routing_cycle_count, j.last_routing_entered_at, j.routing_ends_at, COALESCE(sess.title, ''), j.tip_amount, COALESCE(NULLIF(j.metadata_json->>'time_limit_minutes', '')::int, 0)
FROM jobs j
JOIN sessions sess ON sess.id = j.session_id
WHERE j.status = 'routing'
  AND j.response_message_id IS NULL
ORDER BY j.created_at ASC
LIMIT $1`, maxDispatchRoutingJobs)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, sid, sessionTitle string
		var tipAmount float64
		var timeLimitMinutes int
		var cycles int
		var enteredAt, endsAt *time.Time
		_ = rows.Scan(&id, &sid, &cycles, &enteredAt, &endsAt, &sessionTitle, &tipAmount, &timeLimitMinutes)
		sessionSnippet, err := s.buildDispatchSessionSnippet(r.Context(), sid)
		if err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, map[string]any{
			"id":                  id,
			"session_id":          sid,
			"session_title":       sessionTitle,
			"session_snippet":     sessionSnippet,
			"tip_amount":          tipAmount,
			"time_limit_minutes":  timeLimitMinutes,
			"is_rotated":          cycles > 0,
			"routing_cycle_count": cycles,
			"routing_started_at":  enteredAt,
			"routing_ends_at":     endsAt,
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}
