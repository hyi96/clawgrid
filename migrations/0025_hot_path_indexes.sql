CREATE INDEX IF NOT EXISTS jobs_routing_candidates_idx
  ON jobs (routing_cycle_count DESC, tip_amount DESC, created_at ASC)
  WHERE status = 'routing' AND response_message_id IS NULL;

CREATE INDEX IF NOT EXISTS jobs_routing_expiry_live_idx
  ON jobs (routing_ends_at)
  WHERE status = 'routing' AND response_message_id IS NULL;

CREATE INDEX IF NOT EXISTS jobs_system_pool_rotation_idx
  ON jobs (last_system_pool_entered_at, claim_expires_at)
  WHERE status = 'system_pool' AND response_message_id IS NULL;

CREATE INDEX IF NOT EXISTS jobs_system_pool_claim_owner_idx
  ON jobs (claim_owner_type, claim_owner_id, claim_expires_at)
  WHERE status = 'system_pool' AND response_message_id IS NULL;

CREATE INDEX IF NOT EXISTS jobs_system_pool_claim_expiry_idx
  ON jobs (claim_expires_at)
  WHERE status = 'system_pool'
    AND response_message_id IS NULL
    AND claim_owner_type IS NOT NULL
    AND claim_owner_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS jobs_review_deadline_pending_idx
  ON jobs (review_deadline_at)
  WHERE response_message_id IS NOT NULL AND prompter_vote IS NULL;

CREATE INDEX IF NOT EXISTS assignments_active_deadline_idx
  ON assignments (deadline_at)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS assignments_active_responder_lookup_idx
  ON assignments (responder_owner_type, responder_owner_id, assigned_at DESC, job_id)
  WHERE status = 'active';

CREATE INDEX IF NOT EXISTS responder_availability_fresh_idx
  ON responder_availability (last_seen_at DESC, poll_started_at DESC, owner_id)
  WHERE owner_type = 'account';
