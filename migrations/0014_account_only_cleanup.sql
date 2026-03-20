DELETE FROM sessions s
WHERE s.owner_type <> 'account'
   OR EXISTS (
     SELECT 1
     FROM messages m
     WHERE m.session_id = s.id
       AND m.owner_type <> 'account'
   )
   OR EXISTS (
     SELECT 1
     FROM jobs j
     WHERE j.session_id = s.id
       AND j.owner_type <> 'account'
   )
   OR EXISTS (
     SELECT 1
     FROM assignments a
     JOIN jobs j ON j.id = a.job_id
     WHERE j.session_id = s.id
       AND (
         a.dispatcher_owner_type <> 'account'
         OR a.responder_owner_type <> 'account'
       )
   );

UPDATE jobs
SET claim_owner_type = NULL,
    claim_owner_id = NULL,
    claim_expires_at = NULL
WHERE claim_owner_type IS NOT NULL
  AND claim_owner_type <> 'account';

DELETE FROM wallets WHERE owner_type <> 'account';
DELETE FROM wallet_ledger WHERE owner_type <> 'account';
DELETE FROM responder_availability WHERE owner_type <> 'account';
DELETE FROM responder_pool_snapshots WHERE owner_type <> 'account';
DELETE FROM dispatcher_activity WHERE owner_type <> 'account';

DROP TABLE IF EXISTS guest_sessions;

DROP INDEX IF EXISTS jobs_expires_idx;

ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_owner_type_check;
ALTER TABLE wallets
  ADD CONSTRAINT wallets_owner_type_check
  CHECK (owner_type = 'account');

ALTER TABLE wallet_ledger DROP CONSTRAINT IF EXISTS wallet_ledger_owner_type_check;
ALTER TABLE wallet_ledger
  ADD CONSTRAINT wallet_ledger_owner_type_check
  CHECK (owner_type = 'account');

ALTER TABLE sessions DROP CONSTRAINT IF EXISTS sessions_owner_type_check;
ALTER TABLE sessions
  ADD CONSTRAINT sessions_owner_type_check
  CHECK (owner_type = 'account');

ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_owner_type_check;
ALTER TABLE messages
  ADD CONSTRAINT messages_owner_type_check
  CHECK (owner_type = 'account');

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_owner_type_check;
ALTER TABLE jobs
  ADD CONSTRAINT jobs_owner_type_check
  CHECK (owner_type = 'account');

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_claim_owner_type_check;
ALTER TABLE jobs
  ADD CONSTRAINT jobs_claim_owner_type_check
  CHECK (claim_owner_type IS NULL OR claim_owner_type = 'account');

ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_dispatcher_owner_type_check;
ALTER TABLE assignments
  ADD CONSTRAINT assignments_dispatcher_owner_type_check
  CHECK (dispatcher_owner_type = 'account');

ALTER TABLE assignments DROP CONSTRAINT IF EXISTS assignments_responder_owner_type_check;
ALTER TABLE assignments
  ADD CONSTRAINT assignments_responder_owner_type_check
  CHECK (responder_owner_type = 'account');

ALTER TABLE responder_availability DROP CONSTRAINT IF EXISTS responder_availability_owner_type_check;
ALTER TABLE responder_availability
  ADD CONSTRAINT responder_availability_owner_type_check
  CHECK (owner_type = 'account');

ALTER TABLE responder_pool_snapshots DROP CONSTRAINT IF EXISTS responder_pool_snapshots_owner_type_check;
ALTER TABLE responder_pool_snapshots
  ADD CONSTRAINT responder_pool_snapshots_owner_type_check
  CHECK (owner_type = 'account');

ALTER TABLE dispatcher_activity DROP CONSTRAINT IF EXISTS dispatcher_activity_owner_type_check;
ALTER TABLE dispatcher_activity
  ADD CONSTRAINT dispatcher_activity_owner_type_check
  CHECK (owner_type = 'account');

ALTER TABLE jobs DROP COLUMN IF EXISTS expires_at;
