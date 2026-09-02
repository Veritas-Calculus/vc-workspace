ALTER TABLE audit_events
  ADD COLUMN IF NOT EXISTS outcome text NOT NULL DEFAULT 'success';

UPDATE audit_events
SET outcome = 'failure'
WHERE event_type LIKE '%failed'
   OR event_type LIKE '%rejected'
   OR event_type LIKE '%denied';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'audit_events_outcome_check'
  ) THEN
    ALTER TABLE audit_events
      ADD CONSTRAINT audit_events_outcome_check CHECK (outcome IN ('success', 'failure'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS audit_events_actor_id_id_idx ON audit_events(actor_id, id DESC);
CREATE INDEX IF NOT EXISTS audit_events_event_type_id_idx ON audit_events(event_type, id DESC);
CREATE INDEX IF NOT EXISTS audit_events_outcome_id_idx ON audit_events(outcome, id DESC);
