CREATE TABLE account_hooks (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL UNIQUE REFERENCES accounts(id) ON DELETE CASCADE,
  url TEXT NOT NULL,
  auth_token TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  notify_assignment_received BOOLEAN NOT NULL DEFAULT TRUE,
  notify_reply_received BOOLEAN NOT NULL DEFAULT FALSE,
  status TEXT NOT NULL DEFAULT 'pending_verification',
  verification_token TEXT UNIQUE,
  verification_requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  verified_at TIMESTAMPTZ,
  last_success_at TIMESTAMPTZ,
  last_failure_at TIMESTAMPTZ,
  consecutive_failures INT NOT NULL DEFAULT 0,
  failure_reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
