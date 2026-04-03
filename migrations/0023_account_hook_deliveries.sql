CREATE TABLE account_hook_deliveries (
  id TEXT PRIMARY KEY,
  hook_id TEXT NOT NULL REFERENCES account_hooks(id) ON DELETE CASCADE,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  message TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  attempted_at TIMESTAMPTZ,
  delivered_at TIMESTAMPTZ,
  failure_reason TEXT NOT NULL DEFAULT '',
  job_id TEXT,
  session_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX account_hook_deliveries_ready_idx
  ON account_hook_deliveries(status, created_at);

CREATE INDEX account_hook_deliveries_account_idx
  ON account_hook_deliveries(account_id, created_at DESC, id DESC);
