package httpapi

import (
	"context"
	"errors"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (s *Server) ensureWallet(ctx context.Context, ownerType domain.OwnerType, ownerID string) error {
	_, err := s.db.Exec(ctx, `
INSERT INTO wallets(id, owner_type, owner_id, balance)
VALUES ($1, $2, $3, 0)
ON CONFLICT (owner_type, owner_id) DO NOTHING;`, domain.NewID("wal"), string(ownerType), ownerID)
	return err
}

func (s *Server) ledger(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID string, delta float64, reason string, jobID, assignmentID *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO wallet_ledger(id, owner_type, owner_id, delta, reason, job_id, assignment_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		domain.NewID("led"), string(ownerType), ownerID, delta, reason, jobID, assignmentID)
	return err
}

func (s *Server) adjustWallet(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID string, delta float64) error {
	_, err := tx.Exec(ctx, `UPDATE wallets SET balance = balance + $1 WHERE owner_type = $2 AND owner_id = $3`, delta, string(ownerType), ownerID)
	return err
}

func (s *Server) chargeWallet(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID string, amount float64) error {
	res, err := tx.Exec(ctx, `UPDATE wallets SET balance = balance - $1 WHERE owner_type = $2 AND owner_id = $3 AND balance >= $1`, amount, string(ownerType), ownerID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return errors.New("insufficient_balance")
	}
	return nil
}
