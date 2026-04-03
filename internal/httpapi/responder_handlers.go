package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

const (
	responderPollCheckInterval         = time.Second
	responderAvailabilityHeartbeatWait = 5 * time.Second
)

func (s *Server) handleRespondersAvailable(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	s.serveRespondersAvailable(w, r, actor)
}

func (s *Server) handleRespondersAvailablePublic(w http.ResponseWriter, r *http.Request) {
	s.serveRespondersAvailable(w, r, domain.Actor{})
}

func (s *Server) serveRespondersAvailable(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if err := s.markDispatcherActivity(r.Context(), actor); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	activeDispatchers, err := s.recentActiveDispatchers(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	bandSize := dispatchBandSize(activeDispatchers, maxDispatchResponders, dispatchRespondersBandBase)
	rows, err := s.db.Query(r.Context(), `
WITH poll_candidates AS (
  SELECT 'poll'::text AS availability_mode,
         ra.owner_type,
         ra.owner_id,
         ra.last_seen_at,
         ra.poll_started_at,
         COALESCE(a.name, 'account') AS display_name,
         COALESCE(a.responder_description, '') AS responder_description
  FROM responder_availability ra
  LEFT JOIN accounts a ON ra.owner_type = 'account' AND a.id = ra.owner_id
  WHERE ra.owner_type = 'account'
    AND ra.last_seen_at > now() - make_interval(secs => $1::int)
    AND ra.poll_started_at > now() - make_interval(secs => $4::int)
    AND NOT (ra.owner_type = $2 AND ra.owner_id = $3)
    AND NOT EXISTS (
      SELECT 1
      FROM assignments a2
      JOIN jobs j2 ON j2.id = a2.job_id
      WHERE a2.status = 'active'
        AND a2.responder_owner_type = ra.owner_type
        AND a2.responder_owner_id = ra.owner_id
        AND j2.response_message_id IS NULL
    )
    AND NOT EXISTS (
      SELECT 1
      FROM jobs j3
      WHERE j3.status = 'system_pool'
        AND j3.response_message_id IS NULL
        AND j3.claim_owner_type = ra.owner_type
        AND j3.claim_owner_id = ra.owner_id
        AND j3.claim_expires_at > now()
    )
    AND NOT EXISTS (
      SELECT 1
      FROM account_hooks ah
      WHERE ah.account_id = ra.owner_id
    )
),
hook_candidates AS (
  SELECT 'hook'::text AS availability_mode,
         'account'::text AS owner_type,
         ah.account_id AS owner_id,
         COALESCE(ah.last_success_at, ah.verified_at, ah.verification_requested_at, ah.updated_at, ah.created_at) AS last_seen_at,
         NULL::timestamptz AS poll_started_at,
         COALESCE(a.name, 'account') AS display_name,
         COALESCE(a.responder_description, '') AS responder_description
  FROM account_hooks ah
  JOIN accounts a ON a.id = ah.account_id
  WHERE ah.enabled = TRUE
    AND ah.status = 'active'
    AND ah.notify_assignment_received = TRUE
    AND NOT ('account' = $2 AND ah.account_id = $3)
    AND NOT EXISTS (
      SELECT 1
      FROM assignments a2
      JOIN jobs j2 ON j2.id = a2.job_id
      WHERE a2.status = 'active'
        AND a2.responder_owner_type = 'account'
        AND a2.responder_owner_id = ah.account_id
        AND j2.response_message_id IS NULL
    )
    AND NOT EXISTS (
      SELECT 1
      FROM jobs j3
      WHERE j3.status = 'system_pool'
        AND j3.response_message_id IS NULL
        AND j3.claim_owner_type = 'account'
        AND j3.claim_owner_id = ah.account_id
        AND j3.claim_expires_at > now()
    )
)
SELECT availability_mode,
       owner_type,
       owner_id,
       last_seen_at,
       poll_started_at,
       display_name,
       responder_description
FROM (
  SELECT * FROM poll_candidates
  UNION ALL
  SELECT * FROM hook_candidates
) candidates
ORDER BY last_seen_at DESC
LIMIT $5`, int(s.cfg.ResponderActiveWindow.Seconds()), string(actor.OwnerType), actor.OwnerID, int(s.cfg.PollAssignmentWait.Seconds()), bandSize)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	candidates := []availableResponderRow{}
	for rows.Next() {
		var row availableResponderRow
		_ = rows.Scan(&row.availabilityMode, &row.ownerType, &row.ownerID, &row.lastSeenAt, &row.pollStartedAt, &row.displayName, &row.description)
		candidates = append(candidates, row)
	}
	shuffleRespondersForDispatcher(candidates, actor, time.Now())
	if len(candidates) > maxDispatchResponders {
		candidates = candidates[:maxDispatchResponders]
	}
	items := []map[string]any{}
	for _, row := range candidates {
		item := map[string]any{
			"availability_mode":      row.availabilityMode,
			"owner_type":              row.ownerType,
			"owner_id":                row.ownerID,
			"display_name":            row.displayName,
			"last_seen_at":            row.lastSeenAt,
			"responder_description":   row.description,
		}
		if row.pollStartedAt != nil {
			item["poll_started_at"] = row.pollStartedAt
			item["assignment_wait_seconds"] = int(s.cfg.PollAssignmentWait.Seconds())
		}
		items = append(items, item)
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleResponderState(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	ctx := r.Context()
	if jobID, ok := s.findInProgressJob(ctx, actor); ok {
		_ = s.clearResponderPoolSnapshot(context.Background(), actor)
		respondJSON(w, http.StatusOK, map[string]any{"mode": "assigned", "job_id": jobID})
		return
	}

	candidates, cerr := s.loadResponderPoolSnapshot(ctx, actor)
	if cerr != nil {
		respondErr(w, http.StatusInternalServerError, cerr.Error())
		return
	}
	if len(candidates) > 0 {
		respondJSON(w, http.StatusOK, map[string]any{"mode": "pool", "candidates": candidates})
		return
	}

	var pollStartedAt time.Time
	err := s.db.QueryRow(ctx, `
SELECT poll_started_at
FROM responder_availability
WHERE owner_type = $1
  AND owner_id = $2
  AND last_seen_at > now() - make_interval(secs => $3::int)
  AND poll_started_at > now() - make_interval(secs => $4::int)
LIMIT 1`,
		string(actor.OwnerType),
		actor.OwnerID,
		int(s.cfg.ResponderActiveWindow.Seconds()),
		int(s.cfg.PollAssignmentWait.Seconds()),
	).Scan(&pollStartedAt)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{"mode": "idle"})
		return
	}

	waitUntil := pollStartedAt.Add(s.cfg.PollAssignmentWait)
	remaining := int(time.Until(waitUntil).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"mode":              "polling",
		"poll_started_at":   pollStartedAt,
		"wait_until":        waitUntil,
		"remaining_seconds": remaining,
	})
}

func (s *Server) handleResponderAvailability(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	started, err := s.beginPollingAvailability(r.Context(), actor)
	if err != nil {
		if err.Error() == "disable_hook_before_polling" {
			respondErr(w, http.StatusConflict, err.Error())
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !started {
		respondErr(w, http.StatusConflict, "already_polling")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleResponderAvailabilityDelete(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
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
	if jobID, ok := s.findInProgressJobTx(r.Context(), tx, actor); ok {
		if err := tx.Commit(r.Context()); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"ok": false, "mode": "assigned", "job_id": jobID})
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
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleResponderWork(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	ctx := r.Context()
	if jobID, ok := s.findInProgressJob(ctx, actor); ok {
		_ = s.clearResponderPoolSnapshot(context.Background(), actor)
		_ = s.clearAvailability(context.Background(), actor)
		respondJSON(w, http.StatusOK, map[string]any{"mode": "assigned", "job_id": jobID})
		return
	}
	started, err := s.beginPollingAvailability(ctx, actor)
	if err != nil {
		if err.Error() == "disable_hook_before_polling" {
			respondErr(w, http.StatusConflict, err.Error())
			return
		}
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !started {
		respondErr(w, http.StatusConflict, "already_polling")
		return
	}
	defer func() {
		_ = s.clearAvailability(context.Background(), actor)
	}()

	waitUntil := time.Now().Add(s.cfg.PollAssignmentWait)
	lastHeartbeat := time.Now()
	for time.Now().Before(waitUntil) {
		if jobID, ok := s.findInProgressJob(ctx, actor); ok {
			respondJSON(w, http.StatusOK, map[string]any{"mode": "assigned", "job_id": jobID})
			return
		}
		if time.Since(lastHeartbeat) >= responderAvailabilityHeartbeatWait {
			active, err := s.touchAvailability(ctx, actor)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				respondErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if !active {
				return
			}
			lastHeartbeat = time.Now()
		}
		sleepFor := time.Until(waitUntil)
		if sleepFor > responderPollCheckInterval {
			sleepFor = responderPollCheckInterval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepFor):
		}
	}

	activeResponders, err := s.recentActiveResponders(ctx)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	bandSize := dispatchBandSize(activeResponders, maxSystemPoolCandidates, systemPoolBandBase)

	rows, qerr := s.db.Query(ctx, `
SELECT jobs.id,
       jobs.session_id,
       COALESCE(sess.title, ''),
       COALESCE(sess.dispatch_snippet, ''),
       jobs.created_at,
       jobs.routing_cycle_count,
       jobs.last_system_pool_entered_at,
       jobs.last_system_pool_entered_at + make_interval(secs => $3::int) AS pool_ends_at,
       jobs.tip_amount,
       COALESCE(NULLIF(jobs.metadata_json->>'time_limit_minutes', '')::int, 0) AS time_limit_minutes
FROM jobs
JOIN sessions sess ON sess.id = jobs.session_id
	WHERE jobs.status = 'system_pool'
	  AND jobs.response_message_id IS NULL
	  AND (jobs.claim_expires_at IS NULL OR jobs.claim_expires_at <= now())
	  AND NOT (jobs.owner_type = $1 AND jobs.owner_id = $2)
	ORDER BY jobs.routing_cycle_count DESC, jobs.tip_amount DESC, jobs.created_at ASC
	LIMIT $4`, string(actor.OwnerType), actor.OwnerID, int(s.cfg.PoolDwellWindow.Seconds()), bandSize)
	if qerr != nil {
		respondErr(w, http.StatusInternalServerError, qerr.Error())
		return
	}
	defer rows.Close()
	candidateRows := []poolJobRow{}
	for rows.Next() {
		var row poolJobRow
		_ = rows.Scan(&row.id, &row.sessionID, &row.sessionTitle, &row.sessionSnippet, &row.createdAt, &row.cycles, &row.enteredAt, &row.endsAt, &row.tipAmount, &row.timeLimitMinutes)
		candidateRows = append(candidateRows, row)
	}
	shufflePoolJobsForResponder(candidateRows, actor, time.Now())
	if len(candidateRows) > maxSystemPoolCandidates {
		candidateRows = candidateRows[:maxSystemPoolCandidates]
	}
	sessionIDs := make([]string, 0, len(candidateRows))
	for _, row := range candidateRows {
		sessionIDs = append(sessionIDs, row.sessionID)
	}
	cancelReasons, err := s.loadLatestResponderCancelReasons(ctx, sessionIDs)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	candidates := []map[string]any{}
	for _, row := range candidateRows {
		sessionSnippet, err := s.ensureStoredDispatchSessionSnippet(ctx, row.sessionID, row.sessionSnippet)
		if err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		candidate := map[string]any{
			"id":                 row.id,
			"session_id":         row.sessionID,
			"session_title":      row.sessionTitle,
			"session_snippet":    sessionSnippet,
			"pool_started_at":    row.enteredAt,
			"pool_ends_at":       row.endsAt,
			"tip_amount":         row.tipAmount,
			"time_limit_minutes": row.timeLimitMinutes,
		}
		if reason := cancelReasons[row.sessionID]; reason != "" {
			candidate["last_responder_cancel_reason"] = reason
		}
		candidates = append(candidates, candidate)
	}
	if err := s.storeResponderPoolSnapshot(ctx, actor, candidates); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"mode": "pool", "candidates": candidates})
}

func (s *Server) findInProgressJob(ctx context.Context, actor domain.Actor) (string, bool) {
	return s.findInProgressJobTx(ctx, s.db, actor)
}

func (s *Server) findInProgressJobTx(ctx context.Context, query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor domain.Actor) (string, bool) {
	var jobID string
	err := query.QueryRow(ctx, `
SELECT job_id
FROM (
  SELECT a.job_id, 1 AS priority_rank, a.assigned_at AS sort_ts
  FROM assignments a
  JOIN jobs j ON j.id = a.job_id
  WHERE a.status = 'active'
    AND a.responder_owner_type = $1
    AND a.responder_owner_id = $2
    AND j.response_message_id IS NULL

  UNION ALL

  SELECT j.id AS job_id, 2 AS priority_rank, j.created_at AS sort_ts
  FROM jobs j
  WHERE j.status = 'system_pool'
    AND j.response_message_id IS NULL
    AND j.claim_owner_type = $1
    AND j.claim_owner_id = $2
    AND j.claim_expires_at > now()
) t
ORDER BY priority_rank ASC, sort_ts ASC
LIMIT 1`, string(actor.OwnerType), actor.OwnerID).Scan(&jobID)
	if err != nil {
		return "", false
	}
	return jobID, true
}

func (s *Server) beginPollingAvailability(ctx context.Context, actor domain.Actor) (bool, error) {
	hookDeliveryEnabled, err := s.accountHookDeliveryEnabled(ctx, actor.OwnerType, actor.OwnerID)
	if err != nil {
		return false, err
	}
	if hookDeliveryEnabled {
		return false, errors.New("disable_hook_before_polling")
	}
	if err := s.clearResponderPoolSnapshot(ctx, actor); err != nil {
		return false, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if err := lockResponderActorTx(ctx, tx, actor.OwnerType, actor.OwnerID); err != nil {
		return false, err
	}
	var marker int
	err = tx.QueryRow(ctx, `
INSERT INTO responder_availability(id, owner_type, owner_id, last_seen_at, poll_started_at)
VALUES ($1,$2,$3, now(), now())
ON CONFLICT (owner_type, owner_id)
DO UPDATE
SET id = excluded.id,
    last_seen_at = excluded.last_seen_at,
    poll_started_at = excluded.poll_started_at
WHERE responder_availability.last_seen_at <= now() - make_interval(secs => $4::int)
   OR responder_availability.poll_started_at <= now() - make_interval(secs => $5::int)
RETURNING 1`,
		domain.NewID("av"),
		string(actor.OwnerType),
		actor.OwnerID,
		int(s.cfg.ResponderActiveWindow.Seconds()),
		int(s.cfg.PollAssignmentWait.Seconds()),
	).Scan(&marker)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return marker == 1, nil
}

func (s *Server) touchAvailability(ctx context.Context, actor domain.Actor) (bool, error) {
	res, err := s.db.Exec(ctx, `
UPDATE responder_availability
SET last_seen_at = now()
WHERE owner_type = $1
  AND owner_id = $2`, string(actor.OwnerType), actor.OwnerID)
	if err != nil {
		return false, err
	}
	return res.RowsAffected() > 0, nil
}

func (s *Server) clearAvailability(ctx context.Context, actor domain.Actor) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockResponderActorTx(ctx, tx, actor.OwnerType, actor.OwnerID); err != nil {
		return err
	}
	if err := s.clearAvailabilityTx(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) clearAvailabilityTx(ctx context.Context, tx pgx.Tx, actor domain.Actor) error {
	_, err := tx.Exec(ctx, `DELETE FROM responder_availability WHERE owner_type = $1 AND owner_id = $2`, string(actor.OwnerType), actor.OwnerID)
	return err
}

func (s *Server) loadResponderPoolSnapshot(ctx context.Context, actor domain.Actor) ([]map[string]any, error) {
	var raw []byte
	var expiresAt time.Time
	err := s.db.QueryRow(ctx, `
SELECT snapshot_json, snapshot_expires_at
FROM responder_pool_snapshots
WHERE owner_type = $1
  AND owner_id = $2`,
		string(actor.OwnerType),
		actor.OwnerID,
	).Scan(&raw, &expiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !expiresAt.After(time.Now()) {
		_ = s.clearResponderPoolSnapshot(ctx, actor)
		return nil, nil
	}
	var candidates []map[string]any
	if err := json.Unmarshal(raw, &candidates); err != nil {
		return nil, err
	}
	now := time.Now()
	filtered := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		rawEndsAt, _ := candidate["pool_ends_at"].(string)
		if rawEndsAt == "" {
			filtered = append(filtered, candidate)
			continue
		}
		endsAt, err := time.Parse(time.RFC3339Nano, rawEndsAt)
		if err != nil || endsAt.After(now) {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 {
		_ = s.clearResponderPoolSnapshot(ctx, actor)
		return nil, nil
	}
	if len(filtered) != len(candidates) {
		if err := s.storeResponderPoolSnapshot(ctx, actor, filtered); err != nil {
			return nil, err
		}
	}
	return filtered, nil
}

func (s *Server) storeResponderPoolSnapshot(ctx context.Context, actor domain.Actor, candidates []map[string]any) error {
	if len(candidates) == 0 {
		return s.clearResponderPoolSnapshot(ctx, actor)
	}
	snapshotExpiresAt := time.Now()
	for _, candidate := range candidates {
		endsAt, ok := poolCandidateTime(candidate["pool_ends_at"])
		if ok && endsAt.After(snapshotExpiresAt) {
			snapshotExpiresAt = endsAt
		}
	}
	payload, err := json.Marshal(candidates)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
INSERT INTO responder_pool_snapshots(owner_type, owner_id, snapshot_json, snapshot_expires_at, created_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (owner_type, owner_id)
DO UPDATE
SET snapshot_json = excluded.snapshot_json,
    snapshot_expires_at = excluded.snapshot_expires_at,
    created_at = now()`,
		string(actor.OwnerType),
		actor.OwnerID,
		payload,
		snapshotExpiresAt,
	)
	return err
}

func poolCandidateTime(v any) (time.Time, bool) {
	switch tv := v.(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, tv)
		if err != nil {
			return time.Time{}, false
		}
		return parsed, true
	case time.Time:
		return tv, true
	case *time.Time:
		if tv == nil {
			return time.Time{}, false
		}
		return *tv, true
	default:
		return time.Time{}, false
	}
}

func (s *Server) clearResponderPoolSnapshot(ctx context.Context, actor domain.Actor) error {
	_, err := s.db.Exec(ctx, `
DELETE FROM responder_pool_snapshots
WHERE owner_type = $1
  AND owner_id = $2`,
		string(actor.OwnerType),
		actor.OwnerID,
	)
	return err
}
