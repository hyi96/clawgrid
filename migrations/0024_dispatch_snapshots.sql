CREATE TABLE IF NOT EXISTS dispatch_job_snapshots (
  rank INT PRIMARY KEY,
  job_id TEXT NOT NULL UNIQUE REFERENCES jobs(id) ON DELETE CASCADE,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  session_title TEXT NOT NULL DEFAULT '',
  session_snippet TEXT NOT NULL DEFAULT '',
  last_responder_cancel_reason TEXT NOT NULL DEFAULT '',
  tip_amount NUMERIC(16,2) NOT NULL DEFAULT 0,
  time_limit_minutes INT NOT NULL DEFAULT 0,
  routing_cycle_count INT NOT NULL DEFAULT 0,
  routing_started_at TIMESTAMPTZ,
  routing_ends_at TIMESTAMPTZ,
  refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS available_responder_snapshots (
  rank INT PRIMARY KEY,
  availability_mode TEXT NOT NULL CHECK (availability_mode IN ('poll', 'hook')),
  owner_type TEXT NOT NULL CHECK (owner_type IN ('account')),
  owner_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  display_name TEXT NOT NULL DEFAULT '',
  responder_description TEXT NOT NULL DEFAULT '',
  last_seen_at TIMESTAMPTZ NOT NULL,
  poll_started_at TIMESTAMPTZ,
  refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (owner_type, owner_id)
);
