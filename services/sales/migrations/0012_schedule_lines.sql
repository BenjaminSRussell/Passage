-- Wave 27 slice 8: schedule lines per order line (FR-27.6, AC-27.8).
-- A confirmed line is delivered in one or more schedule lines. Confirm writes
-- one schedule line per line; an ATP promise replaces it with the split the
-- promise computed (CONFIRMED up to the requested date, BACKORDER after it,
-- UNPROMISED for quantity no supply covers). A replaced line is SUPERSEDED,
-- never deleted, so the promise history stays readable.
-- source says where a schedule line's date came from: LINE copies the line's
-- confirmed ship date (confirm with delivery date control NONE, an allocation
-- move, a PATCH), ATP is a slice of a promise and names its bucket_date, which
-- with the line's product, site and warehouse is the atp_bucket key the
-- promise drew on (PRD 5.27.4 invariant).

CREATE TABLE IF NOT EXISTS order_schedule_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  line_id uuid NOT NULL,
  line_no int NOT NULL,
  seq int NOT NULL CHECK (seq >= 1),
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  delivery_date date,
  status text NOT NULL CHECK (status IN ('CONFIRMED', 'BACKORDER', 'UNPROMISED', 'CANCELLED', 'SUPERSEDED')),
  source text NOT NULL CHECK (source IN ('LINE', 'ATP')),
  bucket_date date,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, line_id, seq),
  CHECK ((status = 'UNPROMISED') = (delivery_date IS NULL) OR status IN ('CANCELLED', 'SUPERSEDED')),
  CHECK (bucket_date IS NULL OR (source = 'ATP' AND delivery_date IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS order_schedule_line_order ON order_schedule_line (tenant_id, order_id, line_no, seq);
CREATE INDEX IF NOT EXISTS order_schedule_line_backorder ON order_schedule_line (tenant_id, legal_entity_id, delivery_date)
  WHERE status IN ('BACKORDER', 'UNPROMISED');

ALTER TABLE order_schedule_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_schedule_line FORCE ROW LEVEL SECURITY;
CREATE POLICY order_schedule_line_tenant_access ON order_schedule_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY order_schedule_line_legal_entity_scope ON order_schedule_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts schedule lines and updates their status; it never deletes.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON order_schedule_line TO passage_app';
  END IF;
END
$$;
