package app

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	dispatchJobSnapshotLimit        = 100
	availableResponderSnapshotLimit = 101
)

type dispatchJobSnapshotRow struct {
	jobID            string
	sessionID        string
	sessionTitle     string
	sessionSnippet   string
	cancelReason     string
	tipAmount        float64
	timeLimitMinutes int
	cycles           int
	routingStartedAt *time.Time
	routingEndsAt    *time.Time
}

type availableResponderSnapshotRow struct {
	availabilityMode string
	ownerType        string
	ownerID          string
	displayName      string
	description      string
	lastSeenAt       time.Time
	pollStartedAt    *time.Time
}

func responderCancellationReasonFromFeedbackContent(content string) string {
	content = strings.TrimSpace(content)
	for _, prefix := range []string{
		"a responder cancelled the assigned job due to ",
		"a responder cancelled the claimed job due to ",
	} {
		if strings.HasPrefix(content, prefix) {
			reason := strings.TrimSpace(strings.TrimPrefix(content, prefix))
			if len(reason) >= 2 && reason[0] == '"' && reason[len(reason)-1] == '"' {
				reason = reason[1 : len(reason)-1]
			}
			return reason
		}
	}
	return ""
}

func latestResponderCancelReasonsTx(ctx context.Context, tx pgx.Tx, sessionIDs []string) (map[string]string, error) {
	uniqueIDs := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		uniqueIDs = append(uniqueIDs, sessionID)
	}
	if len(uniqueIDs) == 0 {
		return map[string]string{}, nil
	}

	rows, err := tx.Query(ctx, `
SELECT DISTINCT ON (session_id) session_id, content
FROM messages
WHERE session_id = ANY($1)
  AND type = 'feedback'
  AND role = 'responder'
ORDER BY session_id, created_at DESC`, uniqueIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reasons := make(map[string]string, len(uniqueIDs))
	for rows.Next() {
		var sessionID, content string
		if err := rows.Scan(&sessionID, &content); err != nil {
			return nil, err
		}
		if reason := responderCancellationReasonFromFeedbackContent(strings.TrimSpace(content)); reason != "" {
			reasons[sessionID] = reason
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reasons, nil
}

func (s *Service) ProcessDispatchJobSnapshots(ctx context.Context) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
SELECT j.id,
       j.session_id,
       COALESCE(sess.title, ''),
       COALESCE(sess.dispatch_snippet, ''),
       j.tip_amount,
       COALESCE(NULLIF(j.metadata_json->>'time_limit_minutes', '')::int, 0),
       j.routing_cycle_count,
       j.last_routing_entered_at,
       j.routing_ends_at
FROM jobs j
JOIN sessions sess ON sess.id = j.session_id
WHERE j.status = 'routing'
  AND j.response_message_id IS NULL
ORDER BY j.routing_cycle_count DESC, j.tip_amount DESC, j.created_at ASC
LIMIT $1`, dispatchJobSnapshotLimit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	items := make([]dispatchJobSnapshotRow, 0, dispatchJobSnapshotLimit)
	sessionIDs := make([]string, 0, dispatchJobSnapshotLimit)
	for rows.Next() {
		var row dispatchJobSnapshotRow
		if err := rows.Scan(
			&row.jobID,
			&row.sessionID,
			&row.sessionTitle,
			&row.sessionSnippet,
			&row.tipAmount,
			&row.timeLimitMinutes,
			&row.cycles,
			&row.routingStartedAt,
			&row.routingEndsAt,
		); err != nil {
			return 0, err
		}
		items = append(items, row)
		sessionIDs = append(sessionIDs, row.sessionID)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()

	cancelReasons, err := latestResponderCancelReasonsTx(ctx, tx, sessionIDs)
	if err != nil {
		return 0, err
	}
	for i := range items {
		items[i].cancelReason = cancelReasons[items[i].sessionID]
	}

	if _, err := tx.Exec(ctx, `DELETE FROM dispatch_job_snapshots`); err != nil {
		return 0, err
	}
	for index, item := range items {
		if _, err := tx.Exec(ctx, `
INSERT INTO dispatch_job_snapshots(
  rank,
  job_id,
  session_id,
  session_title,
  session_snippet,
  last_responder_cancel_reason,
  tip_amount,
  time_limit_minutes,
  routing_cycle_count,
  routing_started_at,
  routing_ends_at,
  refreshed_at
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())`,
			index+1,
			item.jobID,
			item.sessionID,
			item.sessionTitle,
			item.sessionSnippet,
			item.cancelReason,
			item.tipAmount,
			item.timeLimitMinutes,
			item.cycles,
			item.routingStartedAt,
			item.routingEndsAt,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(items)), nil
}

func (s *Service) ProcessAvailableResponderSnapshots(ctx context.Context) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
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
    AND ra.poll_started_at > now() - make_interval(secs => $2::int)
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
LIMIT $3`, int(s.Cfg.ResponderActiveWindow.Seconds()), int(s.Cfg.PollAssignmentWait.Seconds()), availableResponderSnapshotLimit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	items := make([]availableResponderSnapshotRow, 0, availableResponderSnapshotLimit)
	for rows.Next() {
		var row availableResponderSnapshotRow
		if err := rows.Scan(
			&row.availabilityMode,
			&row.ownerType,
			&row.ownerID,
			&row.lastSeenAt,
			&row.pollStartedAt,
			&row.displayName,
			&row.description,
		); err != nil {
			return 0, err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM available_responder_snapshots`); err != nil {
		return 0, err
	}
	for index, item := range items {
		if _, err := tx.Exec(ctx, `
INSERT INTO available_responder_snapshots(
  rank,
  availability_mode,
  owner_type,
  owner_id,
  display_name,
  responder_description,
  last_seen_at,
  poll_started_at,
  refreshed_at
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())`,
			index+1,
			item.availabilityMode,
			item.ownerType,
			item.ownerID,
			item.displayName,
			item.description,
			item.lastSeenAt,
			item.pollStartedAt,
		); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(items)), nil
}
