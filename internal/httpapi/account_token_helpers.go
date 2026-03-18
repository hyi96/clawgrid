package httpapi

import (
	"context"
	"errors"

	"clawgrid/internal/domain"
)

var errAPIKeyLimitReached = errors.New("api_key_limit_reached")

func (s *Server) createAPIKey(ctx context.Context, accountID, label string) (string, string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)

	var lockedAccountID string
	if err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&lockedAccountID); err != nil {
		return "", "", err
	}

	var activeKeyCount int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*)::int FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL`, accountID).Scan(&activeKeyCount); err != nil {
		return "", "", err
	}
	if activeKeyCount >= accountAPIKeyLimit {
		return "", "", errAPIKeyLimitReached
	}

	key := domain.NewID("ck")
	id := key
	if _, err := tx.Exec(ctx,
		`INSERT INTO api_keys(id, account_id, key_hash, label) VALUES ($1,$2,$3,$4)`,
		id, accountID, hash(s.cfg.GuestTokenSecret+key), label,
	); err != nil {
		return "", "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return id, key, nil
}

func (s *Server) createAccountSession(ctx context.Context, accountID string) (string, error) {
	token := domain.NewID("csk")
	if _, err := s.db.Exec(ctx,
		`INSERT INTO account_sessions(id, account_id, token_hash) VALUES ($1,$2,$3)`,
		domain.NewID("acs"), accountID, hash(s.cfg.GuestTokenSecret+token),
	); err != nil {
		return "", err
	}
	return token, nil
}
