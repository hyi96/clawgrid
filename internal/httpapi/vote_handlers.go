package httpapi

import (
	"encoding/json"
	"net/http"

	"clawgrid/internal/domain"
)

func (s *Server) handleJobVote(w http.ResponseWriter, r *http.Request, actor domain.Actor) {
	jobID := r.PathValue("id")
	var body struct {
		Vote string `json:"vote"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if body.Vote != "up" && body.Vote != "down" {
		respondErr(w, http.StatusBadRequest, "vote must be up or down")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback(r.Context())

	var ownerType, ownerID, sessionID string
	var tip float64
	var hasReply bool
	var voted *string
	if err := tx.QueryRow(r.Context(), `SELECT owner_type, owner_id, session_id, tip_amount, response_message_id IS NOT NULL, prompter_vote FROM jobs WHERE id = $1 FOR UPDATE`, jobID).Scan(&ownerType, &ownerID, &sessionID, &tip, &hasReply, &voted); err != nil {
		respondErr(w, http.StatusNotFound, "job not found")
		return
	}
	if ownerType != string(actor.OwnerType) || ownerID != actor.OwnerID {
		respondErr(w, http.StatusForbidden, "only prompter can vote")
		return
	}
	if !hasReply {
		respondErr(w, http.StatusConflict, "no_reply_to_vote")
		return
	}
	if voted != nil {
		respondErr(w, http.StatusConflict, "already_voted")
		return
	}

	var responderType, responderID string
	_ = tx.QueryRow(r.Context(), `SELECT owner_type, owner_id FROM messages WHERE id = (SELECT response_message_id FROM jobs WHERE id = $1)`, jobID).Scan(&responderType, &responderID)
	var asnID, dispatcherType, dispatcherID string
	_ = tx.QueryRow(r.Context(), `SELECT id, dispatcher_owner_type, dispatcher_owner_id FROM assignments WHERE job_id = $1 ORDER BY assigned_at DESC LIMIT 1`, jobID).Scan(&asnID, &dispatcherType, &dispatcherID)
	isSelfDispatched := dispatcherType == ownerType && dispatcherID == ownerID

	if body.Vote == "up" {
		_, _ = tx.Exec(r.Context(), `UPDATE jobs SET prompter_vote = 'up', status = 'completed' WHERE id = $1`, jobID)
		_, _ = tx.Exec(r.Context(), `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1,$2,$3,$4,'feedback','prompter',$5)`,
			domain.NewID("msg"), sessionID, ownerType, ownerID, "good")
		if responderID != "" {
			_ = s.refundResponderStake(r.Context(), tx, jobID, domain.OwnerType(responderType), responderID)
			_ = s.adjustWallet(r.Context(), tx, domain.OwnerType(responderType), responderID, s.cfg.ResponderPool+tip)
			_ = s.ledger(r.Context(), tx, domain.OwnerType(responderType), responderID, s.cfg.ResponderPool+tip, "responder_reward", &jobID, nil)
		}
		if dispatcherID != "" {
			_ = s.refundDispatcherStake(r.Context(), tx, jobID, domain.OwnerType(dispatcherType), dispatcherID)
			if !isSelfDispatched {
				_ = s.adjustWallet(r.Context(), tx, domain.OwnerType(dispatcherType), dispatcherID, s.cfg.DispatcherPool)
				_ = s.ledger(r.Context(), tx, domain.OwnerType(dispatcherType), dispatcherID, s.cfg.DispatcherPool, "dispatcher_reward", &jobID, &asnID)
			}
		}
	} else {
		_, _ = tx.Exec(r.Context(), `UPDATE jobs SET prompter_vote = 'down', status = 'failed' WHERE id = $1`, jobID)
		_, _ = tx.Exec(r.Context(), `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1,$2,$3,$4,'feedback','prompter',$5)`,
			domain.NewID("msg"), sessionID, ownerType, ownerID, "bad")
		refund := tip * s.cfg.BadFeedbackTipRefundRatio
		if refund > 0 {
			_ = s.adjustWallet(r.Context(), tx, actor.OwnerType, actor.OwnerID, refund)
			_ = s.ledger(r.Context(), tx, actor.OwnerType, actor.OwnerID, refund, "tip_bad_feedback_refund", &jobID, nil)
		}
		if responderID != "" {
			_ = s.slashResponderStake(r.Context(), tx, jobID, domain.OwnerType(responderType), responderID, "responder_stake_slashed_downvote")
		}
		if dispatcherID != "" {
			if !isSelfDispatched {
				_ = s.slashDispatcherStake(r.Context(), tx, jobID, domain.OwnerType(dispatcherType), dispatcherID, "dispatcher_stake_slashed_downvote")
			}
			_, _ = tx.Exec(r.Context(), `UPDATE assignments SET status = 'fail' WHERE id = $1`, asnID)
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"ok": true})
}
