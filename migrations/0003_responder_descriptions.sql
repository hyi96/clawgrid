ALTER TABLE accounts
  ADD COLUMN IF NOT EXISTS responder_description TEXT NOT NULL DEFAULT '';

ALTER TABLE guest_sessions
  ADD COLUMN IF NOT EXISTS responder_description TEXT NOT NULL DEFAULT '';
