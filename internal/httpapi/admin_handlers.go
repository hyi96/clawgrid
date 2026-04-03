package httpapi

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) handleInternalAutoReview(w http.ResponseWriter, r *http.Request) {
	s.runInternal(w, r, s.svc.ProcessAutoReview)
}

func (s *Server) handleInternalRoutingExpiry(w http.ResponseWriter, r *http.Request) {
	s.runInternal(w, r, s.svc.ProcessRoutingExpiry)
}

func (s *Server) handleInternalPoolRotation(w http.ResponseWriter, r *http.Request) {
	s.runInternal(w, r, s.svc.ProcessPoolRotation)
}

func (s *Server) handleInternalAssignmentTimeouts(w http.ResponseWriter, r *http.Request) {
	s.runInternal(w, r, s.svc.ProcessAssignmentTimeouts)
}

func (s *Server) handleInternalWalletRefresh(w http.ResponseWriter, r *http.Request) {
	s.runInternal(w, r, s.svc.ProcessWalletRefresh)
}

func (s *Server) handleInternalAccountHookDeliveries(w http.ResponseWriter, r *http.Request) {
	s.runInternal(w, r, s.svc.ProcessAccountHookDeliveries)
}

func (s *Server) handleInternalSessionCleanup(w http.ResponseWriter, r *http.Request) {
	s.runInternal(w, r, s.cleanupEmptySessions)
}

func (s *Server) handleInternalLeaderboardRefresh(w http.ResponseWriter, r *http.Request) {
	s.runInternal(w, r, s.svc.RefreshLeaderboards)
}

func (s *Server) handleAdminStuckJobs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT id, status, created_at FROM jobs WHERE status IN ('routing','assigned','system_pool') AND created_at < now() - interval '2 hours' ORDER BY created_at ASC LIMIT 100`)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, st string
		var created time.Time
		_ = rows.Scan(&id, &st, &created)
		items = append(items, map[string]any{"id": id, "status": st, "created_at": created})
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handlePrivateAdminOverview(w http.ResponseWriter, r *http.Request) {
	filterOwnerType := r.URL.Query().Get("owner_type")
	filterOwnerID := r.URL.Query().Get("owner_id")

	queueCounts := map[string]int{}
	rows, err := s.db.Query(r.Context(), `
SELECT status, COUNT(*)::int
FROM jobs
GROUP BY status`)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		_ = rows.Scan(&status, &count)
		queueCounts[status] = count
	}

	var activeAssignments int
	if err := s.db.QueryRow(r.Context(), `SELECT COUNT(*)::int FROM assignments WHERE status = 'active'`).Scan(&activeAssignments); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var availableResponders int
	if err := s.db.QueryRow(r.Context(), `
SELECT COUNT(*)::int
FROM responder_availability
WHERE last_seen_at > now() - make_interval(secs => $1::int)
  AND poll_started_at > now() - make_interval(secs => $2::int)`,
		int(s.cfg.ResponderActiveWindow.Seconds()), int(s.cfg.PollAssignmentWait.Seconds())).Scan(&availableResponders); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	visiblePool := []map[string]any{}
	eligiblePool := []map[string]any{}
	poolRows, err := s.db.Query(r.Context(), `
SELECT id, owner_type, owner_id, created_at, last_system_pool_entered_at,
       last_system_pool_entered_at + make_interval(secs => $1::int) AS pool_ends_at
FROM jobs
WHERE status = 'system_pool'
  AND response_message_id IS NULL
  AND (claim_expires_at IS NULL OR claim_expires_at <= now())
ORDER BY created_at ASC
LIMIT $2`, int(s.cfg.PoolDwellWindow.Seconds()), maxAdminVisiblePoolJobs)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer poolRows.Close()
	for poolRows.Next() {
		var id, ownerType, ownerID string
		var createdAt time.Time
		var enteredAt, endsAt *time.Time
		_ = poolRows.Scan(&id, &ownerType, &ownerID, &createdAt, &enteredAt, &endsAt)
		item := map[string]any{
			"id":                 id,
			"owner_type":         ownerType,
			"owner_id":           ownerID,
			"created_at":         createdAt,
			"pool_started_at":    enteredAt,
			"pool_ends_at":       endsAt,
			"excluded_for_owner": filterOwnerType != "" && filterOwnerID != "" && ownerType == filterOwnerType && ownerID == filterOwnerID,
		}
		visiblePool = append(visiblePool, item)
		if filterOwnerType == "" || filterOwnerID == "" || ownerType != filterOwnerType || ownerID != filterOwnerID {
			eligiblePool = append(eligiblePool, item)
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"ok":                  true,
		"admin_overview_path": "/_private/" + s.adminPathToken() + "/admin/overview",
		"filter": map[string]any{
			"owner_type": filterOwnerType,
			"owner_id":   filterOwnerID,
		},
		"timing": map[string]any{
			"worker_tick_ms":                  int(s.cfg.WorkerTick / time.Millisecond),
			"routing_window_seconds":          int(s.cfg.RoutingWindow.Seconds()),
			"pool_dwell_seconds":              int(s.cfg.PoolDwellWindow.Seconds()),
			"poll_assignment_wait_seconds":    int(s.cfg.PollAssignmentWait.Seconds()),
			"responder_active_window_seconds": int(s.cfg.ResponderActiveWindow.Seconds()),
		},
		"counts": map[string]any{
			"jobs_by_status":                 queueCounts,
			"active_assignments":             activeAssignments,
			"available_responders":           availableResponders,
			"visible_system_pool":            len(visiblePool),
			"eligible_system_pool_for_actor": len(eligiblePool),
		},
		"visible_system_pool_jobs":  visiblePool,
		"eligible_system_pool_jobs": eligiblePool,
	})
}

func (s *Server) cleanupEmptySessions(ctx context.Context) (int64, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM sessions s WHERE s.created_at < now() - interval '24 hours' AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.session_id = s.id)`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

func (s *Server) runInternal(w http.ResponseWriter, r *http.Request, fn func(context.Context) (int64, error)) {
	n, err := fn(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"affected": n})
}
