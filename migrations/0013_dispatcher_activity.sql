CREATE TABLE IF NOT EXISTS dispatcher_activity (
  owner_type TEXT NOT NULL CHECK (owner_type IN ('guest', 'account')),
  owner_id TEXT NOT NULL,
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (owner_type, owner_id)
);

CREATE INDEX IF NOT EXISTS dispatcher_activity_last_seen_idx
  ON dispatcher_activity(last_seen_at);
