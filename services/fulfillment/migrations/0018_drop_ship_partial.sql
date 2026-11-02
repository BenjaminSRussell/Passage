-- Fulfillment: partial vendor confirmations of a drop-ship (FR-28.7). A vendor may
-- ship a purchase order line in parts: each confirmation books Dr 5000 / Cr 2110
-- for its own quantity at the PO cost and is kept in drop_ship_confirmation. The
-- drop-ship is PARTIALLY_CONFIRMED while quantity is open and CONFIRMED once
-- confirmed plus closed quantity reaches the ordered quantity. The vendor may
-- also short-ship and the remainder is closed (qty_closed) without a booking.
ALTER TABLE drop_ship
  ADD COLUMN IF NOT EXISTS qty_confirmed numeric(20,6) NOT NULL DEFAULT 0 CHECK (qty_confirmed >= 0),
  ADD COLUMN IF NOT EXISTS qty_closed numeric(20,6) NOT NULL DEFAULT 0 CHECK (qty_closed >= 0),
  ADD COLUMN IF NOT EXISTS closed_at timestamptz;

-- Drop-ships confirmed before this migration were confirmed whole. drop_ship forces
-- row-level security and the migration sets no tenant, so a migration role that is the
-- owner but not a superuser (the deployed shape) would update no row under the policy and
-- the split check below would then refuse every existing CONFIRMED row. The backfill runs
-- with the policy lifted for the owner and forced again in the same transaction.
ALTER TABLE drop_ship NO FORCE ROW LEVEL SECURITY;
UPDATE drop_ship SET qty_confirmed = qty WHERE status = 'CONFIRMED' AND qty_confirmed = 0;
ALTER TABLE drop_ship FORCE ROW LEVEL SECURITY;

-- One statement, so the table is checked once under its lock (drop_ship has a row per
-- drop-shipped order line, so the scan is short).
ALTER TABLE drop_ship
  DROP CONSTRAINT IF EXISTS drop_ship_status_check,
  DROP CONSTRAINT IF EXISTS drop_ship_check,
  ADD CONSTRAINT drop_ship_status_check
    CHECK (status IN ('OPEN', 'PARTIALLY_CONFIRMED', 'CONFIRMED', 'CANCELLED')),
  ADD CONSTRAINT drop_ship_confirmed_check
    CHECK ((status IN ('PARTIALLY_CONFIRMED', 'CONFIRMED')) = (shipped_on IS NOT NULL AND posting_date IS NOT NULL)),
  ADD CONSTRAINT drop_ship_qty_split_check
    CHECK (qty_confirmed + qty_closed <= qty
           AND (status <> 'CONFIRMED' OR (qty_confirmed > 0 AND qty_confirmed + qty_closed = qty))
           AND (status <> 'PARTIALLY_CONFIRMED' OR (qty_confirmed > 0 AND qty_confirmed + qty_closed < qty))
           AND (status NOT IN ('OPEN', 'CANCELLED') OR qty_confirmed = 0));

CREATE TABLE IF NOT EXISTS drop_ship_confirmation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  drop_ship_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq >= 1),
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  vendor_shipment_ref text,
  shipped_on date NOT NULL,
  posting_date date NOT NULL,
  journal jsonb NOT NULL DEFAULT '[]'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, drop_ship_id, seq),
  FOREIGN KEY (tenant_id, drop_ship_id) REFERENCES drop_ship (tenant_id, id)
);
-- A vendor shipment reference is booked once per drop-ship.
CREATE UNIQUE INDEX IF NOT EXISTS drop_ship_confirmation_ref ON drop_ship_confirmation (tenant_id, drop_ship_id, vendor_shipment_ref) WHERE vendor_shipment_ref IS NOT NULL;

ALTER TABLE drop_ship_confirmation ENABLE ROW LEVEL SECURITY;
ALTER TABLE drop_ship_confirmation FORCE ROW LEVEL SECURITY;
CREATE POLICY drop_ship_confirmation_tenant_access ON drop_ship_confirmation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY drop_ship_confirmation_legal_entity_scope ON drop_ship_confirmation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON drop_ship_confirmation TO passage_app';
  END IF;
END
$$;
