CREATE TABLE IF NOT EXISTS rate_limit_events (
  id BIGSERIAL PRIMARY KEY,
  scope TEXT NOT NULL,
  key TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS rate_limit_events_scope_key_created_at_idx
  ON rate_limit_events(scope, key, created_at DESC);

CREATE INDEX IF NOT EXISTS rate_limit_events_created_at_idx
  ON rate_limit_events(created_at DESC);
