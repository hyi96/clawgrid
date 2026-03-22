package app

import (
	"context"

	"clawgrid/internal/config"
	"clawgrid/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	DB  *pgxpool.Pool
	Cfg config.Config
}

func NewService(db *pgxpool.Pool, cfg config.Config) *Service {
	return &Service{DB: db, Cfg: cfg}
}

func (s *Service) ProcessRoutingExpiry(ctx context.Context) (int64, error) {
	res, err := s.DB.Exec(ctx, `
UPDATE jobs
SET status = 'system_pool',
    last_system_pool_entered_at = now(),
    routing_cycle_count = routing_cycle_count + 1
WHERE status = 'routing'
  AND response_message_id IS NULL
  AND routing_ends_at <= now();`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

func (s *Service) ProcessPoolRotation(ctx context.Context) (int64, error) {
	res, err := s.DB.Exec(ctx, `
UPDATE jobs
SET status = 'routing',
    last_routing_entered_at = now(),
    routing_ends_at = now() + make_interval(secs => $1::int)
WHERE status = 'system_pool'
  AND response_message_id IS NULL
  AND (claim_expires_at IS NULL OR claim_expires_at <= now())
  AND last_system_pool_entered_at <= now() - make_interval(secs => $2::int);`, int(s.Cfg.RoutingWindow.Seconds()), int(s.Cfg.PoolDwellWindow.Seconds()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

func (s *Service) ProcessAssignmentTimeouts(ctx context.Context) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var affected int64
	type timedOutAssignment struct {
		assignmentID       string
		jobID              string
		responderOwnerType string
		responderOwnerID   string
	}
	type expiredClaim struct {
		jobID          string
		claimOwnerType string
		claimOwnerID   string
	}

	assignmentRows, err := tx.Query(ctx, `
SELECT a.id, a.job_id, a.responder_owner_type, a.responder_owner_id
FROM assignments a
JOIN jobs j ON j.id = a.job_id
WHERE a.status = 'active'
  AND a.deadline_at <= now()
FOR UPDATE OF a, j`)
	if err != nil {
		return 0, err
	}
	defer assignmentRows.Close()
	assignments := make([]timedOutAssignment, 0)
	for assignmentRows.Next() {
		var item timedOutAssignment
		if err := assignmentRows.Scan(&item.assignmentID, &item.jobID, &item.responderOwnerType, &item.responderOwnerID); err != nil {
			return 0, err
		}
		assignments = append(assignments, item)
	}
	if err := assignmentRows.Err(); err != nil {
		return 0, err
	}
	assignmentRows.Close()
	for _, item := range assignments {
		if _, err := tx.Exec(ctx, `UPDATE assignments SET status = 'timeout' WHERE id = $1`, item.assignmentID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
UPDATE jobs
SET status = 'system_pool',
    last_system_pool_entered_at = now()
WHERE id = $1
  AND response_message_id IS NULL`, item.jobID); err != nil {
			return 0, err
		}
		if err := s.slashResponderStakeTx(ctx, tx, item.jobID, domain.OwnerType(item.responderOwnerType), item.responderOwnerID, "responder_stake_slashed_timeout"); err != nil {
			return 0, err
		}
		affected++
	}

	claimRows, err := tx.Query(ctx, `
SELECT id, claim_owner_type, claim_owner_id
FROM jobs
WHERE status = 'system_pool'
  AND response_message_id IS NULL
  AND claim_owner_type IS NOT NULL
  AND claim_owner_id IS NOT NULL
  AND claim_expires_at <= now()
FOR UPDATE`)
	if err != nil {
		return 0, err
	}
	defer claimRows.Close()
	claims := make([]expiredClaim, 0)
	for claimRows.Next() {
		var item expiredClaim
		if err := claimRows.Scan(&item.jobID, &item.claimOwnerType, &item.claimOwnerID); err != nil {
			return 0, err
		}
		claims = append(claims, item)
	}
	if err := claimRows.Err(); err != nil {
		return 0, err
	}
	claimRows.Close()
	for _, item := range claims {
		if _, err := tx.Exec(ctx, `UPDATE jobs SET claim_owner_type = NULL, claim_owner_id = NULL, claim_expires_at = NULL WHERE id = $1`, item.jobID); err != nil {
			return 0, err
		}
		if err := s.slashResponderStakeTx(ctx, tx, item.jobID, domain.OwnerType(item.claimOwnerType), item.claimOwnerID, "responder_stake_slashed_timeout"); err != nil {
			return 0, err
		}
		affected++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return affected, nil
}

func (s *Service) ProcessAutoReview(ctx context.Context) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	type autoReviewCandidate struct {
		jobID              string
		sessionID          string
		prompterOwnerType  string
		prompterOwnerID    string
		responderOwnerType string
		responderOwnerID   string
	}

	rows, err := tx.Query(ctx, `
SELECT j.id, j.session_id, j.owner_type, j.owner_id, m.owner_type, m.owner_id
FROM jobs j
JOIN messages m ON m.id = j.response_message_id
WHERE j.response_message_id IS NOT NULL
  AND j.prompter_vote IS NULL
  AND j.review_deadline_at <= now()
FOR UPDATE OF j`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	candidates := make([]autoReviewCandidate, 0)
	var affected int64
	for rows.Next() {
		var item autoReviewCandidate
		if err := rows.Scan(&item.jobID, &item.sessionID, &item.prompterOwnerType, &item.prompterOwnerID, &item.responderOwnerType, &item.responderOwnerID); err != nil {
			return 0, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows.Close()
	for _, item := range candidates {
		if _, err := tx.Exec(ctx, `UPDATE jobs SET prompter_vote = 'auto', status = 'auto_settled' WHERE id = $1`, item.jobID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO messages(id, session_id, owner_type, owner_id, type, role, content) VALUES ($1,$2,$3,$4,'feedback','prompter',$5)`,
			domain.NewID("msg"), item.sessionID, item.prompterOwnerType, item.prompterOwnerID, "no feedback"); err != nil {
			return 0, err
		}
		if s.Cfg.AutoReviewPrompterPenalty > 0 {
			if err := s.adjustWalletTx(ctx, tx, domain.OwnerType(item.prompterOwnerType), item.prompterOwnerID, -s.Cfg.AutoReviewPrompterPenalty); err != nil {
				return 0, err
			}
			if err := s.ledgerTx(ctx, tx, domain.OwnerType(item.prompterOwnerType), item.prompterOwnerID, -s.Cfg.AutoReviewPrompterPenalty, "auto_review_prompter_penalty", &item.jobID, nil); err != nil {
				return 0, err
			}
		}
		if err := s.refundResponderStakeTx(ctx, tx, item.jobID, domain.OwnerType(item.responderOwnerType), item.responderOwnerID); err != nil {
			return 0, err
		}
		if s.Cfg.AutoReviewResponderReward > 0 {
			if err := s.adjustWalletTx(ctx, tx, domain.OwnerType(item.responderOwnerType), item.responderOwnerID, s.Cfg.AutoReviewResponderReward); err != nil {
				return 0, err
			}
			if err := s.ledgerTx(ctx, tx, domain.OwnerType(item.responderOwnerType), item.responderOwnerID, s.Cfg.AutoReviewResponderReward, "auto_review_responder_reward", &item.jobID, nil); err != nil {
				return 0, err
			}
		}
		affected++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return affected, nil
}

func (s *Service) ProcessWalletRefresh(ctx context.Context) (int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	res, err := tx.Exec(ctx, `
UPDATE wallets
SET balance = $1,
    last_refresh_at = now()
WHERE owner_type = 'account'
  AND balance < $2
  AND (last_refresh_at IS NULL OR last_refresh_at <= now() - make_interval(hours => $3::int));`, s.Cfg.AccountRefreshTarget, s.Cfg.AccountRefreshThreshold, int(s.Cfg.RefreshInterval.Hours()))
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

func (s *Service) ProcessRateLimitCleanup(ctx context.Context) (int64, error) {
	res, err := s.DB.Exec(ctx, `
DELETE FROM rate_limit_events
WHERE created_at <= now() - make_interval(hours => $1::int)`, 48)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

func (s *Service) ledgerTx(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID string, delta float64, reason string, jobID, assignmentID *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO wallet_ledger(id, owner_type, owner_id, delta, reason, job_id, assignment_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		domain.NewID("led"), string(ownerType), ownerID, delta, reason, jobID, assignmentID)
	return err
}

func (s *Service) adjustWalletTx(ctx context.Context, tx pgx.Tx, ownerType domain.OwnerType, ownerID string, delta float64) error {
	_, err := tx.Exec(ctx, `UPDATE wallets SET balance = balance + $1 WHERE owner_type = $2 AND owner_id = $3`, delta, string(ownerType), ownerID)
	return err
}

func (s *Service) refundResponderStakeTx(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string) error {
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT responder_stake_amount, responder_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status != "held" || amount <= 0 {
		return nil
	}
	if err := s.adjustWalletTx(ctx, tx, ownerType, ownerID, amount); err != nil {
		return err
	}
	if err := s.ledgerTx(ctx, tx, ownerType, ownerID, amount, "responder_stake_refund", &jobID, nil); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE jobs SET responder_stake_status = 'returned' WHERE id = $1`, jobID)
	return err
}

func (s *Service) slashResponderStakeTx(ctx context.Context, tx pgx.Tx, jobID string, ownerType domain.OwnerType, ownerID string, reason string) error {
	var amount float64
	var status string
	if err := tx.QueryRow(ctx, `SELECT responder_stake_amount, responder_stake_status FROM jobs WHERE id = $1`, jobID).Scan(&amount, &status); err != nil {
		return err
	}
	if status != "held" || amount <= 0 {
		return nil
	}
	if err := s.ledgerTx(ctx, tx, ownerType, ownerID, 0, reason, &jobID, nil); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE jobs SET responder_stake_status = 'slashed' WHERE id = $1`, jobID)
	return err
}
