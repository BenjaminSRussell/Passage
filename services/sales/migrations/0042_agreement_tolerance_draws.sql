-- Wave 27 slice 35: sales agreement over-delivery tolerance and a release that
-- follows its order line (FR-27.10, BR-27.9).
-- overdelivery_pct: a release may draw up to commitment_qty * (1 + pct / 100)
-- (0 keeps the old rule: no more than the commitment).
-- agreement_return: a release can now be given back in parts (a line reduced by
-- PATCH, then the order cancelled), so release_id is no longer unique.
-- agreement_draw: quantity drawn after the release when the release line's
-- quantity is raised by PATCH. A release's outstanding quantity is
-- quantity + sum(draws) - sum(returns).

ALTER TABLE sales_agreement ADD COLUMN IF NOT EXISTS overdelivery_pct numeric(5,2) NOT NULL DEFAULT 0
  CHECK (overdelivery_pct >= 0 AND overdelivery_pct <= 100);

DO $$
DECLARE c record;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
           WHERE conrelid = 'sales_agreement'::regclass AND contype = 'c'
             AND pg_get_constraintdef(oid) LIKE '%released_qty <= commitment_qty%'
  LOOP
    EXECUTE format('ALTER TABLE sales_agreement DROP CONSTRAINT %I', c.conname);
  END LOOP;
END
$$;
ALTER TABLE sales_agreement DROP CONSTRAINT IF EXISTS sales_agreement_released_within_ceiling;
ALTER TABLE sales_agreement ADD CONSTRAINT sales_agreement_released_within_ceiling
  CHECK (released_qty * 100 <= commitment_qty * (100 + overdelivery_pct));

ALTER TABLE agreement_return DROP CONSTRAINT IF EXISTS agreement_return_tenant_id_release_id_key;
CREATE INDEX IF NOT EXISTS agreement_return_release ON agreement_return (tenant_id, release_id);

ALTER TABLE sales_agreement_version DROP CONSTRAINT IF EXISTS sales_agreement_version_kind_check;
ALTER TABLE sales_agreement_version ADD CONSTRAINT sales_agreement_version_kind_check
  CHECK (kind IN ('REVISED', 'CLOSED', 'RETURNED', 'DRAWN'));

CREATE TABLE IF NOT EXISTS agreement_draw (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  release_id uuid NOT NULL,
  sales_order_id uuid NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  actor text NOT NULL,
  drawn_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES sales_agreement (tenant_id, id),
  FOREIGN KEY (tenant_id, release_id) REFERENCES agreement_release (tenant_id, id),
  FOREIGN KEY (tenant_id, sales_order_id) REFERENCES sales_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS agreement_draw_release ON agreement_draw (tenant_id, release_id);

ALTER TABLE agreement_draw ENABLE ROW LEVEL SECURITY;
ALTER TABLE agreement_draw FORCE ROW LEVEL SECURITY;
CREATE POLICY agreement_draw_tenant_access ON agreement_draw AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY agreement_draw_legal_entity_scope ON agreement_draw AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- agreement_draw is append-only: the store only inserts and reads it.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON agreement_draw TO passage_app';
  END IF;
END
$$;
