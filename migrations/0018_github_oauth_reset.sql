ALTER TABLE accounts
ADD COLUMN IF NOT EXISTS github_user_id TEXT,
ADD COLUMN IF NOT EXISTS github_login TEXT,
ADD COLUMN IF NOT EXISTS avatar_url TEXT;

ALTER TABLE accounts
DROP COLUMN IF EXISTS email,
DROP COLUMN IF EXISTS password_hash;

DROP INDEX IF EXISTS accounts_email_lower_unique;
DROP INDEX IF EXISTS accounts_name_lower_unique;

CREATE UNIQUE INDEX IF NOT EXISTS accounts_github_user_id_unique
  ON accounts(github_user_id)
  WHERE github_user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS github_oauth_states (
  id TEXT PRIMARY KEY,
  state_hash TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  used_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS github_oauth_states_created_at_idx
  ON github_oauth_states(created_at DESC);

CREATE TABLE IF NOT EXISTS github_oauth_completions (
  id TEXT PRIMARY KEY,
  code_hash TEXT NOT NULL UNIQUE,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  used_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS github_oauth_completions_created_at_idx
  ON github_oauth_completions(created_at DESC);
