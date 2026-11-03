-- Wave 16 slice 23: the supply and demand a transfer order holds while it is unfinished
-- (FR-16.15, BR-16.13). One row per order line records what the order has booked in the
-- on_hand projection: on_order_qty is demand at the shipping dimension set (the ON_ORDER
-- bucket), ordered_qty the expected receipt at the receiving dimension set (the ORDERED
-- bucket). The rebuild-and-compare reads this table the way it reads reservations and holds,
-- so a drifted bucket is detected and repaired. Rows exist only while a quantity is open and
-- are rewritten by every create, confirm, edit, ship, receive, cancel and delete.
--
-- Only a new table and its indexes are created; no live table is altered or locked. The
-- lock_timeout still bounds the catalog locks, as in 0020 / 0023, so the migration fails (and the
-- rollout retries) instead of queueing behind a long transaction.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS transfer_supply (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  transfer_order_id uuid NOT NULL,
  line_id uuid NOT NULL,
  product_id uuid NOT NULL,
  source_hash bytea NOT NULL,
  dest_hash bytea NOT NULL,
  on_order_qty numeric(28,6) NOT NULL DEFAULT 0 CHECK (on_order_qty >= 0),
  ordered_qty numeric(28,6) NOT NULL DEFAULT 0 CHECK (ordered_qty >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, line_id),
  CHECK (on_order_qty > 0 OR ordered_qty > 0)
);
CREATE INDEX IF NOT EXISTS transfer_supply_order_idx ON transfer_supply (tenant_id, transfer_order_id);
-- The rebuild-and-compare repairs one projection row at a time under its lock and reads the
-- supply booked at that row as shipping ('S') and as receiving ('D') dimension set.
CREATE INDEX IF NOT EXISTS transfer_supply_source_idx ON transfer_supply (tenant_id, product_id, source_hash);
CREATE INDEX IF NOT EXISTS transfer_supply_dest_idx ON transfer_supply (tenant_id, product_id, dest_hash);
ALTER TABLE transfer_supply ENABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_supply FORCE ROW LEVEL SECURITY;
CREATE POLICY transfer_supply_tenant_access ON transfer_supply AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY transfer_supply_legal_entity_scope ON transfer_supply AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON transfer_supply TO passage_app';
  END IF;
END
$$;
