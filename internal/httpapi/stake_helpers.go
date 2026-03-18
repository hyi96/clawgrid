package httpapi

import (
	"context"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Server) holdResponderStake(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string) error {
	if s.cfg.ResponderStake <= 0 {
		_, err := tx.Exec(ctx, `UPDATE jobs SET responder_stake_amount = 0, responder_stake_status = 'none' WHERE id = $1`, jobID)
		return err
	}
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT responder_stake_amount, responder_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status == "held" {
		return nil
	}
	if err := s.chargeWallet(ctx, tx, ownerType, ownerID, s.cfg.ResponderStake); err != nil {
		return err
	}
	_ = s.ledger(ctx, tx, ownerType, ownerID, -s.cfg.ResponderStake, "responder_stake_hold", &jobID, nil)
	_, err := tx.Exec(ctx, `UPDATE jobs SET responder_stake_amount = $2, responder_stake_status = 'held' WHERE id = $1`, jobID, s.cfg.ResponderStake)
	return err
}

func (s *Server) refundResponderStake(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string) error {
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT responder_stake_amount, responder_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status != "held" || amount <= 0 {
		return nil
	}
	if err := s.adjustWallet(ctx, tx, ownerType, ownerID, amount); err != nil {
		return err
	}
	_ = s.ledger(ctx, tx, ownerType, ownerID, amount, "responder_stake_refund", &jobID, nil)
	_, err := tx.Exec(ctx, `UPDATE jobs SET responder_stake_status = 'returned' WHERE id = $1`, jobID)
	return err
}

func (s *Server) slashResponderStake(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string, reason string) error {
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT responder_stake_amount, responder_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status != "held" || amount <= 0 {
		return nil
	}
	_ = s.ledger(ctx, tx, ownerType, ownerID, 0, reason, &jobID, nil)
	_, err := tx.Exec(ctx, `UPDATE jobs SET responder_stake_status = 'slashed' WHERE id = $1`, jobID)
	return err
}
