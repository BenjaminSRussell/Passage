-- #25 (BR-24.2): the potency of the ingredient batches an order is charged from, so its control recipe carries
-- potency-adjusted quantities. One row per adjusted active line; the compensating line gives up the difference.
CREATE TABLE IF NOT EXISTS batch_order_potency (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  line_number integer NOT NULL,
  standard_potency numeric(18,6) NOT NULL CHECK (standard_potency > 0),
  batch_potency numeric(18,6) NOT NULL CHECK (batch_potency > 0),
  compensating_line integer NOT NULL,
  batch_ref text NOT NULL DEFAULT '' CHECK (char_length(batch_ref) <= 100),
  seq integer NOT NULL,
  set_by text NOT NULL,
  set_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, order_id, line_number),
  CHECK (line_number <> compensating_line)
);
ALTER TABLE batch_order_potency ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_order_potency FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_order_potency_tenant_access ON batch_order_potency AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON batch_order_potency TO passage_app';
  END IF;
END
$$;
