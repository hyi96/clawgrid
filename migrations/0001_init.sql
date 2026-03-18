CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS guest_sessions (
  id TEXT PRIMARY KEY,
  guest_token_hash TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  email TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS api_keys (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  key_hash TEXT NOT NULL UNIQUE,
  label TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS wallets (
  id TEXT PRIMARY KEY,
  owner_type TEXT NOT NULL CHECK (owner_type IN ('guest', 'account')),
  owner_id TEXT NOT NULL,
  balance NUMERIC(16,2) NOT NULL DEFAULT 0,
  last_refresh_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(owner_type, owner_id)
);

CREATE TABLE IF NOT EXISTS wallet_ledger (
  id TEXT PRIMARY KEY,
  owner_type TEXT NOT NULL CHECK (owner_type IN ('guest', 'account')),
  owner_id TEXT NOT NULL,
  delta NUMERIC(16,2) NOT NULL,
  reason TEXT NOT NULL,
  job_id TEXT,
  assignment_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  owner_type TEXT NOT NULL CHECK (owner_type IN ('guest', 'account')),
  owner_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  owner_type TEXT NOT NULL CHECK (owner_type IN ('guest', 'account')),
  owner_id TEXT NOT NULL,
  type TEXT NOT NULL,
  content TEXT NOT NULL,
  reply_to_message_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  request_message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  owner_type TEXT NOT NULL CHECK (owner_type IN ('guest', 'account')),
  owner_id TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  activated_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  routing_ends_at TIMESTAMPTZ NOT NULL,
  response_message_id TEXT,
  tip_amount NUMERIC(16,2) NOT NULL DEFAULT 0,
  post_fee_amount NUMERIC(16,2) NOT NULL DEFAULT 2,
  prompter_vote TEXT,
  review_deadline_at TIMESTAMPTZ,
  routing_cycle_count INT NOT NULL DEFAULT 0,
  last_routing_entered_at TIMESTAMPTZ,
  last_system_pool_entered_at TIMESTAMPTZ,
  metadata_json JSONB
);

CREATE TABLE IF NOT EXISTS assignments (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  dispatcher_owner_type TEXT NOT NULL CHECK (dispatcher_owner_type IN ('guest', 'account')),
  dispatcher_owner_id TEXT NOT NULL,
  responder_owner_type TEXT NOT NULL CHECK (responder_owner_type IN ('guest', 'account')),
  responder_owner_id TEXT NOT NULL,
  assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deadline_at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS assignments_one_active_per_job ON assignments(job_id) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS responder_availability (
  id TEXT PRIMARY KEY,
  owner_type TEXT NOT NULL CHECK (owner_type IN ('guest', 'account')),
  owner_id TEXT NOT NULL,
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(owner_type, owner_id)
);

CREATE INDEX IF NOT EXISTS jobs_status_idx ON jobs(status);
CREATE INDEX IF NOT EXISTS jobs_expires_idx ON jobs(expires_at);
CREATE INDEX IF NOT EXISTS guest_sessions_last_seen_idx ON guest_sessions(last_seen_at);
