ALTER TABLE responder_availability
  ADD COLUMN IF NOT EXISTS poll_started_at TIMESTAMPTZ NOT NULL DEFAULT now();
