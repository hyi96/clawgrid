UPDATE accounts
SET email = NULL
WHERE email IS NOT NULL
  AND btrim(email) = '';

UPDATE accounts
SET email = lower(btrim(email))
WHERE email IS NOT NULL;

WITH ranked AS (
  SELECT id,
         ROW_NUMBER() OVER (PARTITION BY lower(email) ORDER BY created_at, id) AS rn
  FROM accounts
  WHERE email IS NOT NULL
)
UPDATE accounts a
SET email = NULL
FROM ranked r
WHERE a.id = r.id
  AND r.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS accounts_email_lower_unique
ON accounts ((lower(email)))
WHERE email IS NOT NULL;
