ALTER TABLE jobs
  ADD COLUMN IF NOT EXISTS claim_owner_type TEXT CHECK (claim_owner_type IN ('guest', 'account')),
  ADD COLUMN IF NOT EXISTS claim_owner_id TEXT,
  ADD COLUMN IF NOT EXISTS claim_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS jobs_claim_expires_idx ON jobs(claim_expires_at);
