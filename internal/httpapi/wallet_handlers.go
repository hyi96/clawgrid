package httpapi

import (
	"net/http"
	"time"

	"clawgrid/internal/domain"
)

func (s *Server) handleWalletCurrent(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	if err := s.ensureWallet(r.Context(), actor.OwnerType, actor.OwnerID); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var bal float64
	var last *time.Time
	if err := s.db.QueryRow(r.Context(), `SELECT balance, last_refresh_at FROM wallets WHERE owner_type = $1 AND owner_id = $2`, string(actor.OwnerType), actor.OwnerID).Scan(&bal, &last); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"owner_type": actor.OwnerType, "owner_id": actor.OwnerID, "balance": bal, "last_refresh_at": last})
}

func (s *Server) handleWalletLedger(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	rows, err := s.db.Query(r.Context(), `SELECT delta, reason, created_at, job_id, assignment_id FROM wallet_ledger WHERE owner_type = $1 AND owner_id = $2 ORDER BY created_at DESC LIMIT 100`, string(actor.OwnerType), actor.OwnerID)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var delta float64
		var reason string
		var created time.Time
		var jobID, asnID *string
		_ = rows.Scan(&delta, &reason, &created, &jobID, &asnID)
		items = append(items, map[string]any{"delta": delta, "reason": reason, "created_at": created, "job_id": jobID, "assignment_id": asnID})
	}
	respondJSON(w, http.StatusOK, map[string]any{"items": items})
}
