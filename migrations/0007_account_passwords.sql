ALTER TABLE accounts
ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';

UPDATE accounts
SET name = 'account-' || right(id, 6)
WHERE btrim(name) = '';

WITH ranked AS (
  SELECT id,
         ROW_NUMBER() OVER (PARTITION BY lower(name) ORDER BY created_at, id) AS rn
  FROM accounts
)
UPDATE accounts a
SET name = a.name || '-' || right(a.id, 6)
FROM ranked r
WHERE a.id = r.id
  AND r.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS accounts_name_lower_unique ON accounts ((lower(name)));
