package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Server) handleRespondersAvailable(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	rows, err := s.db.Query(r.Context(), `
SELECT ra.owner_type, ra.owner_id, ra.last_seen_at, ra.poll_started_at,
       CASE
         WHEN ra.owner_type = 'account' THEN COALESCE(a.name, 'account')
         WHEN ra.owner_type = 'guest' THEN 'guest'
         ELSE ra.owner_type
       END AS display_name,
       CASE
         WHEN ra.owner_type = 'account' THEN COALESCE(a.responder_description, '')
         WHEN ra.owner_type = 'guest' THEN COALESCE(gs.responder_description, '')
         ELSE ''
       END AS responder_description
FROM responder_availability
  ra
LEFT JOIN accounts a ON ra.owner_type = 'account' AND a.id = ra.owner_id
LEFT JOIN guest_sessions gs ON ra.owner_type = 'guest' AND gs.id = ra.owner_id
WHERE ra.last_seen_at > now() - make_interval(secs => $1::int)
  AND NOT (ra.owner_type = $2 AND ra.owner_id = $3)
  AND ra.poll_started_at > now() - make_interval(secs => $4::int)
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
ORDER BY ra.last_seen_at DESC
LIMIT $5`, int(s.cfg.ResponderActiveWindow.Seconds()), string(actor.OwnerType), actor.OwnerID, int(s.cfg.PollAssignmentWait.Seconds()), maxDispatchResponders)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var ot, oid, displayName, desc string
		var seen, pollStarted time.Time
		_ = rows.Scan(&ot, &oid, &seen, &pollStarted, &displayName, &desc)
		items = append(items, map[string]any{
			"owner_type":              ot,
			"owner_id":                oid,
			"display_name":            displayName,
			"last_seen_at":            seen,
			"poll_started_at":         pollStarted,
			"responder_description":   desc,
			"assignment_wait_seconds": int(s.cfg.PollAssignmentWait.Seconds()),
		})
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleResponderState(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	ctx := r.Context()
	if err := s.syncJobQueues(ctx); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
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
	if err := s.clearAvailability(r.Context(), actor); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleResponderWork(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	ctx := r.Context()
	if err := s.syncJobQueues(ctx); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if jobID, ok := s.findInProgressJob(ctx, actor); ok {
		_ = s.clearResponderPoolSnapshot(context.Background(), actor)
		_ = s.clearAvailability(context.Background(), actor)
		respondJSON(w, http.StatusOK, map[string]any{"mode": "assigned", "job_id": jobID})
		return
	}
	started, err := s.beginPollingAvailability(ctx, actor)
	if err != nil {
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
	for time.Now().Before(waitUntil) {
		if jobID, ok := s.findInProgressJob(ctx, actor); ok {
			respondJSON(w, http.StatusOK, map[string]any{"mode": "assigned", "job_id": jobID})
			return
		}
		if err := s.touchAvailability(ctx, actor); err != nil {
			if ctx.Err() != nil {
				return
			}
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		sleepFor := time.Until(waitUntil)
		if sleepFor > 250*time.Millisecond {
			sleepFor = 250 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepFor):
		}
		if err := s.syncJobQueues(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	rows, qerr := s.db.Query(ctx, `
SELECT id,
       last_system_pool_entered_at,
       last_system_pool_entered_at + make_interval(secs => $3::int) AS pool_ends_at,
       tip_amount,
       COALESCE(NULLIF(metadata_json->>'time_limit_minutes', '')::int, 0) AS time_limit_minutes
FROM jobs
	WHERE status = 'system_pool'
	  AND response_message_id IS NULL
	  AND expires_at > now()
	  AND (claim_expires_at IS NULL OR claim_expires_at <= now())
	  AND NOT (owner_type = $1 AND owner_id = $2)
	ORDER BY created_at ASC
	LIMIT $4`, string(actor.OwnerType), actor.OwnerID, int(s.cfg.PoolDwellWindow.Seconds()), maxSystemPoolCandidates)
	if qerr != nil {
		respondErr(w, http.StatusInternalServerError, qerr.Error())
		return
	}
	defer rows.Close()
	candidates := []map[string]any{}
	for rows.Next() {
		var id string
		var tipAmount float64
		var timeLimitMinutes int
		var enteredAt, endsAt *time.Time
		_ = rows.Scan(&id, &enteredAt, &endsAt, &tipAmount, &timeLimitMinutes)
		candidates = append(candidates, map[string]any{
			"id":                 id,
			"pool_started_at":    enteredAt,
			"pool_ends_at":       endsAt,
			"tip_amount":         tipAmount,
			"time_limit_minutes": timeLimitMinutes,
		})
	}
	if err := s.storeResponderPoolSnapshot(ctx, actor, candidates); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"mode": "pool", "candidates": candidates})
}

func (s *Server) findInProgressJob(ctx context.Context, actor domain.Actor) (string, bool) {
	var jobID string
	err := s.db.QueryRow(ctx, `
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
	if err := s.clearResponderPoolSnapshot(ctx, actor); err != nil {
		return false, err
	}
	var marker int
	err := s.db.QueryRow(ctx, `
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
	return marker == 1, nil
}

func (s *Server) touchAvailability(ctx context.Context, actor domain.Actor) error {
	_, err := s.db.Exec(ctx, `
UPDATE responder_availability
SET last_seen_at = now()
WHERE owner_type = $1
  AND owner_id = $2`, string(actor.OwnerType), actor.OwnerID)
	return err
}

func (s *Server) clearAvailability(ctx context.Context, actor domain.Actor) error {
	_, err := s.db.Exec(ctx, `DELETE FROM responder_availability WHERE owner_type = $1 AND owner_id = $2`, string(actor.OwnerType), actor.OwnerID)
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
