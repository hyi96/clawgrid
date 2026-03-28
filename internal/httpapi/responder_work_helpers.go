package httpapi

import (
	"context"

	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
)

func lockResponderActorTx(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, string(ownerType), ownerID)
	return err
}

func responderActorExistsTx(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID string) (bool, error) {
	if ownerType != domain.OwnerAccount {
		return false, nil
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $1)`, ownerID).Scan(&exists)
	return exists, err
}

func responderHasActiveWorkTx(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID, excludeJobID string) (bool, error) {
	var busy bool
	err := tx.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM assignments a
  JOIN jobs j ON j.id = a.job_id
  WHERE a.status = 'active'
    AND a.responder_owner_type = $1
    AND a.responder_owner_id = $2
    AND j.response_message_id IS NULL
    AND ($3 = '' OR a.job_id <> $3)
) OR EXISTS(
  SELECT 1
  FROM jobs j
  WHERE j.status = 'system_pool'
    AND j.response_message_id IS NULL
    AND j.claim_owner_type = $1
    AND j.claim_owner_id = $2
    AND j.claim_expires_at > now()
    AND ($3 = '' OR j.id <> $3)
)`, string(ownerType), ownerID, excludeJobID).Scan(&busy)
	return busy, err
}

func responderHasLiveAvailabilityTx(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID string, activeWindowSeconds, pollWindowSeconds int) (bool, error) {
	var available bool
	err := tx.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM responder_availability
  WHERE owner_type = $1
    AND owner_id = $2
    AND last_seen_at > now() - make_interval(secs => $3::int)
    AND poll_started_at > now() - make_interval(secs => $4::int)
)`, string(ownerType), ownerID, activeWindowSeconds, pollWindowSeconds).Scan(&available)
	return available, err
}
