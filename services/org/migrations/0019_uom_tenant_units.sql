ALTER TABLE reference_status DROP CONSTRAINT IF EXISTS reference_status_kind_check;
ALTER TABLE reference_status ADD CONSTRAINT reference_status_kind_check CHECK (kind IN ('country', 'currency', 'region', 'language', 'uom'));

ALTER TABLE unit_of_measure ADD COLUMN IF NOT EXISTS idempotency_key text;
CREATE UNIQUE INDEX IF NOT EXISTS unit_of_measure_idempotency ON unit_of_measure (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
