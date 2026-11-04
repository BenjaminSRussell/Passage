-- Wave 27 slice 30: replay of refused channel orders (PRD 5.27.10, "Intake
-- retry / DLQ replay"). The intake error log kept only the payload hash, so a
-- refused order could not be sent through intake again once its cause (a
-- listing, a configuration, a mapping) was fixed. The refused request body and
-- its Idempotency-Key are now kept on the OPEN row; they are cleared when the
-- row is resolved (the buyer data in a one-time order is not kept longer than
-- the failure needs), and replayed_order_id names the order a replay created.
-- request_payload holds an ordinary request in clear. A one-time customer
-- request carries the walk-in buyer's name, address and tax id, which this
-- service never stores in clear (0019), so its whole body is kept only as a
-- platform fieldcrypt envelope in request_payload_sealed (bound to the tenant
-- and to the field name intake_error.request_payload); at most one of the two
-- is set. No new table: intake_error keeps its slice 20 grants and policies.

ALTER TABLE intake_error ADD COLUMN IF NOT EXISTS request_payload jsonb;
ALTER TABLE intake_error ADD COLUMN IF NOT EXISTS request_payload_sealed bytea;
ALTER TABLE intake_error ADD COLUMN IF NOT EXISTS idempotency_key text;
ALTER TABLE intake_error ADD COLUMN IF NOT EXISTS replayed_order_id uuid;
ALTER TABLE intake_error ADD COLUMN IF NOT EXISTS replay_count int NOT NULL DEFAULT 0 CHECK (replay_count >= 0);
ALTER TABLE intake_error DROP CONSTRAINT IF EXISTS intake_error_one_payload;
ALTER TABLE intake_error ADD CONSTRAINT intake_error_one_payload
  CHECK (request_payload IS NULL OR request_payload_sealed IS NULL);
