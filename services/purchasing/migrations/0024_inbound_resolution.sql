-- Wave 17 purchasing slice 19: a buyer settles a refused inbound change. A change another service asked
-- of a purchase order and a purchasing rule refused (slice 18) can now be acknowledged (the buyer dealt
-- with it by hand) or retried (the buyer fixed what stopped it, and the request runs again against the
-- order as it is now); a cancelled drop-ship (dropship.cancelled.v1) is a third source. Additive only
-- (expand/contract): new columns with defaults and a wider source check.

SET LOCAL lock_timeout = '5s';

-- A retry re-plans the request, so the warehouse of a planning message is kept (it narrows the lines).
ALTER TABLE purchase_inbound_change ADD COLUMN IF NOT EXISTS warehouse_id uuid;
-- How many times the request has been run (the consumer's first run counts), and how it was settled:
-- '' while it waits, ACKNOWLEDGED by a buyer, RETRIED when a retry took it out of REFUSED.
ALTER TABLE purchase_inbound_change ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 1 CHECK (attempts >= 1);
ALTER TABLE purchase_inbound_change ADD COLUMN IF NOT EXISTS resolution text NOT NULL DEFAULT ''
  CHECK (resolution IN ('', 'ACKNOWLEDGED', 'RETRIED'));
ALTER TABLE purchase_inbound_change ADD COLUMN IF NOT EXISTS resolved_by text NOT NULL DEFAULT '';
ALTER TABLE purchase_inbound_change ADD COLUMN IF NOT EXISTS resolved_at timestamptz;
ALTER TABLE purchase_inbound_change ADD COLUMN IF NOT EXISTS resolution_note text NOT NULL DEFAULT '' CHECK (length(resolution_note) <= 1000);
ALTER TABLE purchase_inbound_change ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE purchase_inbound_change DROP CONSTRAINT IF EXISTS purchase_inbound_change_source_check;
ALTER TABLE purchase_inbound_change ADD CONSTRAINT purchase_inbound_change_source_check
  CHECK (source IN ('PLANNING_MESSAGE', 'DROPSHIP_CLOSED', 'DROPSHIP_CANCELLED'));

-- The buyer's work queue: refused changes nobody settled yet.
CREATE INDEX IF NOT EXISTS purchase_inbound_change_open_refused ON purchase_inbound_change (tenant_id, id DESC)
  WHERE status = 'REFUSED' AND resolution = '';

-- passage_app: the store now updates a change row (a retry rewrites its outcome, an acknowledgement
-- stamps it). Still no DELETE.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON purchase_inbound_change TO passage_app';
  END IF;
END
$$;
