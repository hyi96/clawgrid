package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
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
	limit := walletLedgerDefaultLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			respondErr(w, http.StatusBadRequest, "invalid_limit")
			return
		}
		if parsed > walletLedgerLimitMax {
			parsed = walletLedgerLimitMax
		}
		limit = parsed
	}
	beforeID := strings.TrimSpace(r.URL.Query().Get("before_id"))
	queryLimit := limit + 1

	type ledgerRow struct {
		id      string
		delta   float64
		reason  string
		created time.Time
		jobID   *string
		asnID   *string
		sid     *string
	}

	rowsOut := []ledgerRow{}
	hasMoreOlder := false

	var (
		rows pgx.Rows
		err  error
	)
	if beforeID == "" {
		rows, err = s.db.Query(r.Context(), `
SELECT l.id, l.delta, l.reason, l.created_at, l.job_id, l.assignment_id, j.session_id
FROM wallet_ledger l
LEFT JOIN jobs j ON j.id = l.job_id
WHERE l.owner_type = $1
  AND l.owner_id = $2
ORDER BY l.created_at DESC, l.id DESC
LIMIT $3`, string(actor.OwnerType), actor.OwnerID, queryLimit)
	} else {
		var beforeCreated time.Time
		if err := s.db.QueryRow(r.Context(), `
SELECT created_at
FROM wallet_ledger
WHERE owner_type = $1
  AND owner_id = $2
  AND id = $3`, string(actor.OwnerType), actor.OwnerID, beforeID).Scan(&beforeCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				respondErr(w, http.StatusBadRequest, "invalid_before_id")
				return
			}
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		rows, err = s.db.Query(r.Context(), `
SELECT l.id, l.delta, l.reason, l.created_at, l.job_id, l.assignment_id, j.session_id
FROM wallet_ledger l
LEFT JOIN jobs j ON j.id = l.job_id
WHERE l.owner_type = $1
  AND l.owner_id = $2
  AND (l.created_at < $3 OR (l.created_at = $3 AND l.id < $4))
ORDER BY l.created_at DESC, l.id DESC
LIMIT $5`, string(actor.OwnerType), actor.OwnerID, beforeCreated, beforeID, queryLimit)
	}
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var row ledgerRow
		_ = rows.Scan(&row.id, &row.delta, &row.reason, &row.created, &row.jobID, &row.asnID, &row.sid)
		rowsOut = append(rowsOut, row)
	}
	if len(rowsOut) > limit {
		hasMoreOlder = true
		rowsOut = rowsOut[:limit]
	}
	for _, row := range rowsOut {
		items = append(items, map[string]any{
			"id":            row.id,
			"delta":         row.delta,
			"reason":        row.reason,
			"created_at":    row.created,
			"job_id":        row.jobID,
			"assignment_id": row.asnID,
			"session_id":    row.sid,
		})
	}
	nextBeforeID := ""
	if hasMoreOlder && len(items) > 0 {
		nextBeforeID = items[len(items)-1]["id"].(string)
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"items":          items,
		"has_more_older": hasMoreOlder,
		"next_before_id": nextBeforeID,
	})
}
