package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	LeaderboardCategoryJobSuccess          = "job_success_rate"
	LeaderboardCategoryDispatchAccuracy    = "dispatch_accuracy"
	LeaderboardCategoryTotalAvailableFunds = "total_available_credits"
	leaderboardRankLimit                   = 100
	leaderboardMinRatedCount               = 50
	leaderboardRefreshLockKey              = 938114
)

type LeaderboardEntry struct {
	AccountID     string
	AccountName   string
	MetricValue   float64
	MetricDisplay string
}

func leaderboardSnapshotDate(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Service) RefreshLeaderboards(ctx context.Context) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, leaderboardRefreshLockKey); err != nil {
		return 0, err
	}

	snapshotDate := leaderboardSnapshotDate(time.Now())
	if _, err := tx.Exec(ctx, `DELETE FROM leaderboard_snapshots WHERE snapshot_date = $1`, snapshotDate); err != nil {
		return 0, err
	}

	totalInserted := int64(0)
	for _, category := range []string{
		LeaderboardCategoryJobSuccess,
		LeaderboardCategoryDispatchAccuracy,
		LeaderboardCategoryTotalAvailableFunds,
	} {
		entries, err := s.buildLeaderboardEntries(ctx, tx, category)
		if err != nil {
			return 0, err
		}
		for index, entry := range entries {
			if _, err := tx.Exec(ctx, `
INSERT INTO leaderboard_snapshots(category, snapshot_date, rank, account_id, account_name, metric_value, metric_display, refreshed_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,now())`,
				category, snapshotDate, index+1, entry.AccountID, entry.AccountName, entry.MetricValue, entry.MetricDisplay); err != nil {
				return 0, err
			}
			totalInserted++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return totalInserted, nil
}

func (s *Service) buildLeaderboardEntries(ctx context.Context, tx pgx.Tx, category string) ([]LeaderboardEntry, error) {
	switch category {
	case LeaderboardCategoryJobSuccess:
		return queryLeaderboardEntries(ctx, tx, `
SELECT
  a.id,
  a.name,
  ROUND((100.0 * COUNT(*) FILTER (WHERE j.prompter_vote = 'up') / COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down')))::numeric, 1)::float8 AS metric_value,
  TO_CHAR(ROUND((100.0 * COUNT(*) FILTER (WHERE j.prompter_vote = 'up') / COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down')))::numeric, 1), 'FM999990.0') || '%' AS metric_display
FROM accounts a
JOIN messages m
  ON m.owner_type = 'account'
 AND m.owner_id = a.id
 AND m.type = 'text'
 AND m.role = 'responder'
JOIN jobs j ON j.response_message_id = m.id
WHERE j.prompter_vote IN ('up','down')
GROUP BY a.id, a.name
HAVING COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down')) >= $1
ORDER BY metric_value DESC, COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down')) DESC, a.name ASC
LIMIT $2`, leaderboardMinRatedCount, leaderboardRankLimit)
	case LeaderboardCategoryDispatchAccuracy:
		return queryLeaderboardEntries(ctx, tx, `
SELECT
  a.id,
  a.name,
  ROUND((100.0 * COUNT(*) FILTER (WHERE j.prompter_vote = 'up') / COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down')))::numeric, 1)::float8 AS metric_value,
  TO_CHAR(ROUND((100.0 * COUNT(*) FILTER (WHERE j.prompter_vote = 'up') / COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down')))::numeric, 1), 'FM999990.0') || '%' AS metric_display
FROM accounts a
JOIN jobs j ON TRUE
JOIN LATERAL (
  SELECT dispatcher_owner_id
  FROM assignments
  WHERE job_id = j.id
    AND dispatcher_owner_type = 'account'
  ORDER BY assigned_at DESC
  LIMIT 1
) last_asn ON last_asn.dispatcher_owner_id = a.id
WHERE j.prompter_vote IN ('up','down')
GROUP BY a.id, a.name
HAVING COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down')) >= $1
ORDER BY metric_value DESC, COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down')) DESC, a.name ASC
LIMIT $2`, leaderboardMinRatedCount, leaderboardRankLimit)
	case LeaderboardCategoryTotalAvailableFunds:
		return queryLeaderboardEntries(ctx, tx, `
WITH responder_rated AS (
  SELECT
    m.owner_id AS account_id,
    COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down'))::int AS rated_count
  FROM messages m
  JOIN jobs j ON j.response_message_id = m.id
  WHERE m.owner_type = 'account'
    AND m.type = 'text'
    AND m.role = 'responder'
    AND j.prompter_vote IN ('up','down')
  GROUP BY m.owner_id
),
dispatcher_rated AS (
  SELECT
    last_asn.dispatcher_owner_id AS account_id,
    COUNT(*) FILTER (WHERE j.prompter_vote IN ('up','down'))::int AS rated_count
  FROM jobs j
  JOIN LATERAL (
    SELECT dispatcher_owner_id
    FROM assignments
    WHERE job_id = j.id
      AND dispatcher_owner_type = 'account'
    ORDER BY assigned_at DESC
    LIMIT 1
  ) last_asn ON TRUE
  WHERE j.prompter_vote IN ('up','down')
  GROUP BY last_asn.dispatcher_owner_id
)
SELECT
  a.id,
  a.name,
  w.balance::float8 AS metric_value,
  TO_CHAR(w.balance, 'FM999999990.00') || ' credits' AS metric_display
FROM accounts a
JOIN wallets w
  ON w.owner_type = 'account'
 AND w.owner_id = a.id
LEFT JOIN responder_rated rr ON rr.account_id = a.id
LEFT JOIN dispatcher_rated dr ON dr.account_id = a.id
WHERE COALESCE(rr.rated_count, 0) >= $1
   OR COALESCE(dr.rated_count, 0) >= $1
ORDER BY w.balance DESC, a.name ASC
LIMIT $2`, leaderboardMinRatedCount, leaderboardRankLimit)
	default:
		return nil, fmt.Errorf("unknown leaderboard category: %s", category)
	}
}

func queryLeaderboardEntries(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]LeaderboardEntry, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]LeaderboardEntry, 0)
	for rows.Next() {
		var entry LeaderboardEntry
		if err := rows.Scan(&entry.AccountID, &entry.AccountName, &entry.MetricValue, &entry.MetricDisplay); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
