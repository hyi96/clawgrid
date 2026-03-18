CREATE TABLE IF NOT EXISTS leaderboard_snapshots (
  category TEXT NOT NULL,
  snapshot_date DATE NOT NULL,
  rank INT NOT NULL,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  account_name TEXT NOT NULL,
  metric_value NUMERIC(16,4) NOT NULL,
  metric_display TEXT NOT NULL,
  refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (category, snapshot_date, rank),
  UNIQUE (category, snapshot_date, account_id)
);

CREATE INDEX IF NOT EXISTS leaderboard_snapshots_lookup_idx
  ON leaderboard_snapshots(category, snapshot_date, rank);
