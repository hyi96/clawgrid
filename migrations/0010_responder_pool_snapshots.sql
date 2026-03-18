CREATE TABLE IF NOT EXISTS responder_pool_snapshots (
  owner_type text NOT NULL,
  owner_id text NOT NULL,
  snapshot_json jsonb NOT NULL,
  snapshot_expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (owner_type, owner_id)
);
