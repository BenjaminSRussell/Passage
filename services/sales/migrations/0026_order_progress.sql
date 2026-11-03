-- Wave 27 slice 22: delivery and invoice progress of confirmed orders (BR-27.5,
-- FR-28.8). The line keeps the quantity delivered (packing slip posted) and
-- invoiced; order_progress is the immutable record of each report, one row per
-- (order, kind, source reference) so that the same shipment or invoice is never
-- applied twice, and per Idempotency-Key so that a retry is answered with the
-- stored record. lines holds what the report changed on each line.
--
-- sales_order_line is a large, live table, so this file only changes its
-- catalog: a column with a constant default is a fast default (no rewrite, and
-- NOT NULL needs no scan because the default is not null), and the checks are
-- added NOT VALID so nothing is scanned under the ACCESS EXCLUSIVE lock, which
-- lasts an instant. The lock_timeout makes the migration fail (and the rollout
-- retry) instead of queueing behind a long transaction while every later query
-- on the table queues behind it. 0027 validates the checks without that lock.
SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS delivered_qty numeric(20,6) NOT NULL DEFAULT 0;
ALTER TABLE sales_order_line ADD COLUMN IF NOT EXISTS invoiced_qty numeric(20,6) NOT NULL DEFAULT 0;
ALTER TABLE sales_order_line ADD CONSTRAINT sales_order_line_progress_check
  CHECK (delivered_qty >= 0 AND invoiced_qty >= 0) NOT VALID;

CREATE TABLE IF NOT EXISTS order_progress (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('DELIVERED', 'INVOICED')),
  source_ref text NOT NULL CHECK (char_length(source_ref) BETWEEN 1 AND 100),
  lines jsonb NOT NULL,
  order_status text NOT NULL,
  order_version int NOT NULL,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  recorded_by text NOT NULL,
  recorded_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, order_id) REFERENCES sales_order (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS order_progress_source ON order_progress (tenant_id, order_id, kind, source_ref);
CREATE UNIQUE INDEX IF NOT EXISTS order_progress_idempotency ON order_progress (tenant_id, idempotency_key);
CREATE INDEX IF NOT EXISTS order_progress_recent ON order_progress (tenant_id, recorded_at DESC, id);

ALTER TABLE order_progress ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_progress FORCE ROW LEVEL SECURITY;
CREATE POLICY order_progress_tenant_access ON order_progress AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY order_progress_legal_entity_scope ON order_progress AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Records are inserted and read; the store never updates or deletes them.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON order_progress TO passage_app';
  END IF;
END
$$;
