package httpapi

import (
	"context"
	"time"

	"clawgrid/internal/domain"
)

func systemPoolVisibleToActor(jobOwnerType, jobOwnerID, claimOwnerType, claimOwnerID string, claimExpiresAt *time.Time, actor domain.Actor, now time.Time) bool {
	if jobOwnerType == string(actor.OwnerType) && jobOwnerID == actor.OwnerID {
		return true
	}
	if claimExpiresAt != nil && claimExpiresAt.After(now) {
		return claimOwnerType == string(actor.OwnerType) && claimOwnerID == actor.OwnerID
	}
	return true
}

func (s *Server) sessionOwned(ctx context.Context, sid string, actor domain.Actor) bool {
	var ownerType, ownerID string
	err := s.db.QueryRow(ctx, `SELECT owner_type, owner_id FROM sessions WHERE id = $1`, sid).Scan(&ownerType, &ownerID)
	if err != nil {
		return false
	}
	return ownerType == string(actor.OwnerType) && ownerID == actor.OwnerID
}

func (s *Server) canAccessJob(ctx context.Context, jobID, ownerType, ownerID, status string, actor domain.Actor) bool {
	if ownerType == string(actor.OwnerType) && ownerID == actor.OwnerID {
		return true
	}
	if status == "system_pool" {
		var claimOwnerType, claimOwnerID string
		var claimExpiresAt *time.Time
		err := s.db.QueryRow(ctx, `
SELECT COALESCE(claim_owner_type, ''),
       COALESCE(claim_owner_id, ''),
       claim_expires_at
FROM jobs
WHERE id = $1`, jobID).Scan(&claimOwnerType, &claimOwnerID, &claimExpiresAt)
		return err == nil && systemPoolVisibleToActor(ownerType, ownerID, claimOwnerType, claimOwnerID, claimExpiresAt, actor, time.Now())
	}
	if status == "assigned" {
		var assigned bool
		err := s.db.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1 FROM assignments
  WHERE job_id = $1
    AND status = 'active'
    AND responder_owner_type = $2
    AND responder_owner_id = $3
)`, jobID, string(actor.OwnerType), actor.OwnerID).Scan(&assigned)
		return err == nil && assigned
	}
	return false
}

func (s *Server) canAccessSession(ctx context.Context, sid string, actor domain.Actor) bool {
	if s.sessionOwned(ctx, sid, actor) {
		return true
	}
	var allowed bool
	err := s.db.QueryRow(ctx, `
SELECT EXISTS(
  SELECT 1
  FROM jobs j
  WHERE j.session_id = $1
    AND j.status = 'system_pool'
    AND j.claim_owner_type = $2
    AND j.claim_owner_id = $3
    AND j.claim_expires_at > now()
) OR EXISTS(
  SELECT 1
  FROM jobs j
  JOIN assignments a ON a.job_id = j.id
  WHERE j.session_id = $1
    AND j.status = 'assigned'
    AND a.status = 'active'
    AND a.responder_owner_type = $2
    AND a.responder_owner_id = $3
)`, sid, string(actor.OwnerType), actor.OwnerID).Scan(&allowed)
	return err == nil && allowed
}
