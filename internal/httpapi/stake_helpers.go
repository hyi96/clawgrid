package httpapi

import (
	"context"
	"math"

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
	return s.refundResponderStakeWithReason(ctx, tx, jobID, ownerType, ownerID, "responder_stake_refund")
}

func (s *Server) refundResponderStakeWithReason(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string, reason string) error {
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
	_ = s.ledger(ctx, tx, ownerType, ownerID, amount, reason, &jobID, nil)
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

func (s *Server) holdDispatcherStake(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string, selfDispatched bool) error {
	if s.cfg.DispatcherStake <= 0 || selfDispatched {
		_, err := tx.Exec(ctx, `UPDATE jobs SET dispatcher_stake_amount = 0, dispatcher_stake_status = 'none' WHERE id = $1`, jobID)
		return err
	}
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT dispatcher_stake_amount, dispatcher_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status == "held" {
		return nil
	}
	if err := s.chargeWallet(ctx, tx, ownerType, ownerID, s.cfg.DispatcherStake); err != nil {
		return err
	}
	_ = s.ledger(ctx, tx, ownerType, ownerID, -s.cfg.DispatcherStake, "dispatcher_stake_hold", &jobID, nil)
	_, err := tx.Exec(ctx, `UPDATE jobs SET dispatcher_stake_amount = $2, dispatcher_stake_status = 'held' WHERE id = $1`, jobID, s.cfg.DispatcherStake)
	return err
}

func (s *Server) refundDispatcherStake(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string) error {
	return s.refundDispatcherStakeWithReason(ctx, tx, jobID, ownerType, ownerID, "dispatcher_stake_refund")
}

func (s *Server) refundDispatcherStakeWithReason(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string, reason string) error {
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT dispatcher_stake_amount, dispatcher_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status != "held" || amount <= 0 {
		return nil
	}
	if err := s.adjustWallet(ctx, tx, ownerType, ownerID, amount); err != nil {
		return err
	}
	_ = s.ledger(ctx, tx, ownerType, ownerID, amount, reason, &jobID, nil)
	_, err := tx.Exec(ctx, `UPDATE jobs SET dispatcher_stake_status = 'returned' WHERE id = $1`, jobID)
	return err
}

func (s *Server) slashDispatcherStake(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string, reason string) error {
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT dispatcher_stake_amount, dispatcher_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status != "held" || amount <= 0 {
		return nil
	}
	_ = s.ledger(ctx, tx, ownerType, ownerID, 0, reason, &jobID, nil)
	_, err := tx.Exec(ctx, `UPDATE jobs SET dispatcher_stake_status = 'slashed' WHERE id = $1`, jobID)
	return err
}

func floorToCents(value float64) float64 {
	return math.Floor((value*100)+1e-9) / 100
}

func (s *Server) settleDispatcherStakeAssignedCancel(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string) error {
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT dispatcher_stake_amount, dispatcher_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status != "held" || amount <= 0 {
		return nil
	}
	penaltyAmount := floorToCents(s.cfg.DispatcherRefusalPenalty)
	if penaltyAmount < 0 {
		penaltyAmount = 0
	}
	if penaltyAmount > amount {
		penaltyAmount = amount
	}
	refundAmount := floorToCents(amount - penaltyAmount)
	if refundAmount > 0 {
		if err := s.adjustWallet(ctx, tx, ownerType, ownerID, refundAmount); err != nil {
			return err
		}
		_ = s.ledger(ctx, tx, ownerType, ownerID, refundAmount, "dispatcher_stake_partial_refund_assignment_cancel", &jobID, nil)
	}
	_ = s.ledger(ctx, tx, ownerType, ownerID, 0, "dispatcher_stake_slashed_assignment_cancel", &jobID, nil)
	_, err := tx.Exec(ctx, `UPDATE jobs SET dispatcher_stake_status = 'partial_returned' WHERE id = $1`, jobID)
	return err
}
