package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Server) handleSessionsCreate(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	_ = s.ensureWallet(r.Context(), actor.OwnerType, actor.OwnerID)
	var body struct {
		Title string `json:"title"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	title := strings.TrimSpace(body.Title)
	if len(title) > sessionTitleLimit {
		respondErr(w, http.StatusBadRequest, "title_too_long")
		return
	}
	var existingEmptySessionID string
	err := s.db.QueryRow(r.Context(), `
SELECT s.id
FROM sessions s
LEFT JOIN messages m ON m.session_id = s.id
WHERE s.owner_type = $1
  AND s.owner_id = $2
  AND s.deleted_at IS NULL
GROUP BY s.id, s.created_at
HAVING COUNT(m.id) = 0
ORDER BY s.created_at DESC
LIMIT 1`,
		string(actor.OwnerType), actor.OwnerID,
	).Scan(&existingEmptySessionID)
	switch {
	case err == nil:
		respondJSON(w, http.StatusConflict, map[string]any{
			"error":               "empty_session_exists",
			"existing_session_id": existingEmptySessionID,
		})
		return
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sid := domain.NewID("ses")
	_, err = s.db.Exec(r.Context(), `INSERT INTO sessions(id, owner_type, owner_id, status, title) VALUES ($1,$2,$3,'active',$4)`, sid, string(actor.OwnerType), actor.OwnerID, title)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, map[string]any{"id": sid, "title": title})
}

func (s *Server) handleSessionState(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	sid := r.PathValue("id")
	var ownerType, ownerID, title string
	if err := s.db.QueryRow(r.Context(), `SELECT owner_type, owner_id, COALESCE(title, '') FROM sessions WHERE id = $1 AND deleted_at IS NULL`, sid).Scan(&ownerType, &ownerID, &title); err != nil {
		respondErr(w, http.StatusNotFound, "session not found")
		return
	}
	if ownerType != string(actor.OwnerType) || ownerID != actor.OwnerID {
		respondErr(w, http.StatusForbidden, "forbidden")
		return
	}

	state, err := s.buildSessionState(r.Context(), sid)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	state["session_id"] = sid
	state["title"] = title
	respondJSON(w, http.StatusOK, state)
}

func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	rows, err := s.db.Query(r.Context(), `
SELECT s.id,
       s.created_at,
       COALESCE(MAX(m.created_at), s.created_at) AS updated_at,
       COUNT(m.id)::int,
       COALESCE(s.title, ''),
       EXISTS(
         SELECT 1
         FROM jobs j
         WHERE j.session_id = s.id
           AND j.response_message_id IS NOT NULL
           AND j.prompter_vote IS NULL
       ) AS pending_feedback
FROM sessions s
LEFT JOIN messages m ON m.session_id = s.id
WHERE s.owner_type = $1 AND s.owner_id = $2 AND s.deleted_at IS NULL
GROUP BY s.id, s.created_at, s.title
ORDER BY updated_at DESC
LIMIT 100`, string(actor.OwnerType), actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, title string
		var created, updated time.Time
		var messageCount int
		var pendingFeedback bool
		_ = rows.Scan(&id, &created, &updated, &messageCount, &title, &pendingFeedback)
		items = append(items, map[string]any{
			"id":               id,
			"title":            title,
			"created_at":       created,
			"updated_at":       updated,
			"message_count":    messageCount,
			"pending_feedback": pendingFeedback,
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSessionsGet(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	id := r.PathValue("id")
	var ownerType, ownerID, title string
	var created time.Time
	var numMessages int
	err := s.db.QueryRow(r.Context(), `
SELECT s.owner_type, s.owner_id, s.created_at, COALESCE(s.title, ''), COUNT(m.id)::int
FROM sessions s
LEFT JOIN messages m ON m.session_id = s.id
WHERE s.id = $1 AND s.deleted_at IS NULL
GROUP BY s.id, s.owner_type, s.owner_id, s.created_at, s.title`, id).Scan(&ownerType, &ownerID, &created, &title, &numMessages)
	if err != nil {
		respondErr(w, http.StatusNotFound, "session not found")
		return
	}
	if ownerType != string(actor.OwnerType) || ownerID != actor.OwnerID {
		respondErr(w, http.StatusForbidden, "forbidden")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"id": id, "title": title, "owner_type": ownerType, "owner_id": ownerID, "created_at": created, "num_messages": numMessages})
}

func (s *Server) handleSessionsPatch(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	id := r.PathValue("id")
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	title := strings.TrimSpace(body.Title)
	if len(title) > sessionTitleLimit {
		respondErr(w, http.StatusBadRequest, "title_too_long")
		return
	}
	res, err := s.db.Exec(r.Context(), `UPDATE sessions SET title = $1 WHERE id = $2 AND owner_type = $3 AND owner_id = $4 AND deleted_at IS NULL`, title, id, string(actor.OwnerType), actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.RowsAffected() == 0 {
		respondErr(w, http.StatusNotFound, "session not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true, "title": title})
}

func (s *Server) handleSessionsDelete(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	id := r.PathValue("id")
	var unresolved int
	if err := s.db.QueryRow(r.Context(), `
SELECT COUNT(*)::int
FROM jobs
WHERE session_id = $1
  AND (
    (response_message_id IS NULL AND status IN ('routing', 'system_pool', 'assigned'))
    OR
    (response_message_id IS NOT NULL AND prompter_vote IS NULL)
  )`, id).Scan(&unresolved); err == nil && unresolved > 0 {
		respondErr(w, http.StatusConflict, "session_has_unresolved_jobs")
		return
	}
	res, err := s.db.Exec(r.Context(), `UPDATE sessions SET deleted_at = now() WHERE id = $1 AND owner_type = $2 AND owner_id = $3 AND deleted_at IS NULL`, id, string(actor.OwnerType), actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.RowsAffected() == 0 {
		respondErr(w, http.StatusNotFound, "session not found")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMessagesList(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	sid := r.PathValue("id")
	if !s.canAccessSession(r.Context(), sid, actor) {
		respondErr(w, http.StatusForbidden, "forbidden")
		return
	}
	limit := 0
	beforeID := strings.TrimSpace(r.URL.Query().Get("before_id"))
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			respondErr(w, http.StatusBadRequest, "invalid_limit")
			return
		}
		if parsed > sessionMessagesLimitMax {
			parsed = sessionMessagesLimitMax
		}
		limit = parsed
	}
	if beforeID != "" && limit == 0 {
		respondErr(w, http.StatusBadRequest, "limit_required_for_before_id")
		return
	}

	type messageRow struct {
		id      string
		typ     string
		role    string
		content string
		created time.Time
	}
	rowsOut := []messageRow{}
	hasMoreOlder := false

	if limit == 0 {
		rows, err := s.db.Query(r.Context(), `SELECT id, type, role, content, created_at FROM messages WHERE session_id = $1 ORDER BY created_at, id`, sid)
		if err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		for rows.Next() {
			var row messageRow
			_ = rows.Scan(&row.id, &row.typ, &row.role, &row.content, &row.created)
			rowsOut = append(rowsOut, row)
		}
		items := make([]map[string]any, 0, len(rowsOut))
		for _, row := range rowsOut {
			items = append(items, map[string]any{"id": row.id, "type": row.typ, "role": row.role, "content": row.content, "created_at": row.created})
		}
		respondJSON(w, http.StatusOK, map[string]any{"items": items, "has_more_older": false, "next_before_id": ""})
		return
	}

	queryLimit := limit + 1
	var rows pgx.Rows
	var err error
	if beforeID == "" {
		rows, err = s.db.Query(r.Context(), `
SELECT id, type, role, content, created_at
FROM messages
WHERE session_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2`, sid, queryLimit)
	} else {
		var beforeCreated time.Time
		if err := s.db.QueryRow(r.Context(), `SELECT created_at FROM messages WHERE session_id = $1 AND id = $2`, sid, beforeID).Scan(&beforeCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				respondErr(w, http.StatusBadRequest, "invalid_before_id")
				return
			}
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		rows, err = s.db.Query(r.Context(), `
SELECT id, type, role, content, created_at
FROM messages
WHERE session_id = $1
  AND (created_at < $2 OR (created_at = $2 AND id < $3))
ORDER BY created_at DESC, id DESC
LIMIT $4`, sid, beforeCreated, beforeID, queryLimit)
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var row messageRow
		_ = rows.Scan(&row.id, &row.typ, &row.role, &row.content, &row.created)
		rowsOut = append(rowsOut, row)
	}
	if len(rowsOut) > limit {
		hasMoreOlder = true
		rowsOut = rowsOut[:limit]
	}
	items := make([]map[string]any, 0, len(rowsOut))
	for i := len(rowsOut) - 1; i >= 0; i-- {
		row := rowsOut[i]
		items = append(items, map[string]any{"id": row.id, "type": row.typ, "role": row.role, "content": row.content, "created_at": row.created})
	}
	nextBeforeID := ""
	if hasMoreOlder && len(items) > 0 {
		nextBeforeID = items[0]["id"].(string)
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items, "has_more_older": hasMoreOlder, "next_before_id": nextBeforeID})
}

func (s *Server) handleMessagesCreate(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	sid := r.PathValue("id")
	if !s.sessionOwned(r.Context(), sid, actor) {
		respondErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var body struct {
		Type             string  `json:"type"`
		Content          string  `json:"content"`
		TipAmount        float64 `json:"tip_amount"`
		TimeLimitMinutes *int    `json:"time_limit_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		respondErr(w, http.StatusBadRequest, "content required")
		return
	}
	if body.TipAmount < 0 {
		respondErr(w, http.StatusBadRequest, "tip_amount_must_be_non_negative")
		return
	}
	if body.TimeLimitMinutes != nil && *body.TimeLimitMinutes < 1 {
		respondErr(w, http.StatusBadRequest, "time_limit_minutes_must_be_at_least_1")
		return
	}
	if body.Type == "" {
		body.Type = "text"
	}
	if body.Type != "text" {
		respondErr(w, http.StatusBadRequest, "type must be text")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	var pending int
	if err := tx.QueryRow(r.Context(), `SELECT COUNT(1) FROM jobs WHERE session_id = $1 AND response_message_id IS NOT NULL AND prompter_vote IS NULL AND review_deadline_at > now()`, sid).Scan(&pending); err == nil && pending > 0 {
		respondErr(w, http.StatusConflict, "pending_feedback")
		return
	}
	if err := tx.QueryRow(r.Context(), `SELECT COUNT(1) FROM jobs WHERE session_id = $1 AND response_message_id IS NULL AND status IN ('routing','system_pool','assigned')`, sid).Scan(&pending); err == nil && pending > 0 {
		respondErr(w, http.StatusConflict, "pending_job")
		return
	}

	mid := domain.NewID("msg")
	if _, err := tx.Exec(r.Context(), `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1,$2,$3,$4,$5,'prompter',$6)`, mid, sid, string(actor.OwnerType), actor.OwnerID, body.Type, body.Content); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	fee := s.cfg.PostFee + body.TipAmount
	if err := s.chargeWallet(r.Context(), tx, actor.OwnerType, actor.OwnerID, fee); err != nil {
		respondErr(w, http.StatusPaymentRequired, err.Error())
		return
	}
	_ = s.ledger(r.Context(), tx, actor.OwnerType, actor.OwnerID, -fee, "post_fee_charge", nil, nil)

	jid := domain.NewID("job")
	var metadata map[string]any
	if body.TimeLimitMinutes != nil {
		metadata = map[string]any{"time_limit_minutes": *body.TimeLimitMinutes}
	}
	if _, err := tx.Exec(r.Context(), `
INSERT INTO jobs(id, session_id, request_message_id, owner_type, owner_id, status, activated_at, routing_ends_at, tip_amount, post_fee_amount, last_routing_entered_at, metadata_json)
VALUES ($1,$2,$3,$4,$5,'routing', now(), now() + make_interval(secs => $6::int), $7, $8, now(), $9)`,
		jid, sid, mid, string(actor.OwnerType), actor.OwnerID, int(s.cfg.RoutingWindow.Seconds()), body.TipAmount, s.cfg.PostFee, metadata); err != nil {
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
	s.notifyReplyReceived(context.Background(), sid, actor, "prompter")
	respondJSON(w, http.StatusCreated, map[string]any{"message_id": mid, "job_id": jid})
}

func (s *Server) buildSessionState(ctx context.Context, sid string) (map[string]any, error) {
	var jobID, status, responseMessageID, vote, claimOwnerType, claimOwnerID string
	var tipAmount float64
	var timeLimitMinutes int
	var reviewDeadline, claimExpiresAt *time.Time
	err := s.db.QueryRow(ctx, `
SELECT
  id,
  status,
  tip_amount,
  COALESCE(NULLIF(metadata_json->>'time_limit_minutes', '')::int, 0),
  COALESCE(response_message_id, ''),
  COALESCE(prompter_vote, ''),
  review_deadline_at,
  COALESCE(claim_owner_type, ''),
  COALESCE(claim_owner_id, ''),
  claim_expires_at
FROM jobs
WHERE session_id = $1
  AND (
    (response_message_id IS NOT NULL AND prompter_vote IS NULL)
    OR
    (response_message_id IS NULL AND status IN ('routing', 'system_pool', 'assigned'))
  )
ORDER BY
  CASE WHEN response_message_id IS NOT NULL AND prompter_vote IS NULL THEN 0 ELSE 1 END ASC,
  created_at DESC
LIMIT 1`, sid).Scan(
		&jobID,
		&status,
		&tipAmount,
		&timeLimitMinutes,
		&responseMessageID,
		&vote,
		&reviewDeadline,
		&claimOwnerType,
		&claimOwnerID,
		&claimExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{
			"state":            "ready_for_prompt",
			"can_send_message": true,
			"can_vote":         false,
			"active_job":       nil,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	status = effectiveJobStatus(status, responseMessageID, vote)
	now := time.Now()
	state := "waiting_for_responder"
	canSendMessage := false
	canVote := false
	if responseMessageID != "" && vote == "" {
		state = "feedback_required"
		canVote = true
	} else if status == "assigned" || (status == "system_pool" && claimOwnerID != "" && claimExpiresAt != nil && claimExpiresAt.After(now)) {
		state = "responder_working"
	}

	var workDeadlineAt *time.Time
	if status == "assigned" {
		_ = s.db.QueryRow(ctx, `
SELECT deadline_at
FROM assignments
WHERE job_id = $1
  AND status = 'active'
ORDER BY assigned_at DESC
LIMIT 1`, jobID).Scan(&workDeadlineAt)
	} else if status == "system_pool" && claimExpiresAt != nil && claimExpiresAt.After(now) {
		workDeadlineAt = claimExpiresAt
	}

	return map[string]any{
		"state":            state,
		"can_send_message": canSendMessage,
		"can_vote":         canVote,
		"active_job": map[string]any{
			"id":                  jobID,
			"status":              status,
			"tip_amount":          tipAmount,
			"time_limit_minutes":  timeLimitMinutes,
			"response_message_id": responseMessageID,
			"prompter_vote":       vote,
			"review_deadline_at":  reviewDeadline,
			"claim_owner_type":    claimOwnerType,
			"claim_owner_id":      claimOwnerID,
			"claim_expires_at":    claimExpiresAt,
			"work_deadline_at":    workDeadlineAt,
		},
	}, nil
}
