package httpapi

import (
	"context"
	"net/http"
	"time"

	"clawgrid/internal/app"
)

func (s *Server) handleLeaderboardsGet(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	if category == "" {
		category = app.LeaderboardCategoryJobSuccess
	}
	if !isLeaderboardCategory(category) {
		respondErr(w, http.StatusBadRequest, "invalid_leaderboard_category")
		return
	}
	if err := s.ensureLeaderboardSnapshots(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	snapshotDate := currentLeaderboardSnapshotDate()
	rows, err := s.db.Query(r.Context(), `
SELECT rank, account_id, account_name, metric_value::float8, metric_display, refreshed_at
FROM leaderboard_snapshots
WHERE category = $1
  AND snapshot_date = $2
ORDER BY rank ASC`, category, snapshotDate)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	items := []map[string]any{}
	var refreshedAt *time.Time
	for rows.Next() {
		var rank int
		var accountID, accountName, metricDisplay string
		var metricValue float64
		var rowRefreshedAt time.Time
		if err := rows.Scan(&rank, &accountID, &accountName, &metricValue, &metricDisplay, &rowRefreshedAt); err != nil {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		rowRefreshedAtCopy := rowRefreshedAt
		refreshedAt = &rowRefreshedAtCopy
		items = append(items, map[string]any{
			"rank":           rank,
			"account_id":     accountID,
			"account_name":   accountName,
			"metric_value":   metricValue,
			"metric_display": metricDisplay,
		})
	}
	if err := rows.Err(); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if refreshedAt == nil {
		now := time.Now().UTC()
		refreshedAt = &now
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"category":           category,
		"snapshot_date":      snapshotDate.Format("2006-01-02"),
		"refreshed_at":       refreshedAt,
		"qualification_rule": leaderboardQualificationRule(category),
		"items":              items,
	})
}

func (s *Server) ensureLeaderboardSnapshots(ctx context.Context) error {
	snapshotDate := currentLeaderboardSnapshotDate()
	var categoryCount int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(DISTINCT category)::int FROM leaderboard_snapshots WHERE snapshot_date = $1`, snapshotDate).Scan(&categoryCount); err != nil {
		return err
	}
	if categoryCount == 3 {
		return nil
	}
	_, err := s.svc.RefreshLeaderboards(ctx)
	return err
}

func currentLeaderboardSnapshotDate() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func isLeaderboardCategory(category string) bool {
	switch category {
	case app.LeaderboardCategoryJobSuccess, app.LeaderboardCategoryDispatchAccuracy, app.LeaderboardCategoryTotalAvailableFunds:
		return true
	default:
		return false
	}
}

func leaderboardQualificationRule(category string) string {
	switch category {
	case app.LeaderboardCategoryJobSuccess:
		return "shown only for accounts with >=50 responder outcomes"
	case app.LeaderboardCategoryDispatchAccuracy:
		return "shown only for accounts with >=50 rated dispatches"
	case app.LeaderboardCategoryTotalAvailableFunds:
		return "shown only for accounts with >=50 rated replies or >=50 rated dispatches"
	default:
		return ""
	}
}
