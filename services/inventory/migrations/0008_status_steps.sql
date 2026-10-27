-- Wave 16 slice 6: transaction status steps and named backward moves (FR-16.2, BR-16.1).
-- Additive: one audit table. Each registration (CREATE) or status move writes one row
-- here in the same database transaction as the transaction row, the projection delta and
-- the events. The row's idempotency key makes a retried command a replay.

CREATE TABLE IF NOT EXISTS inventory_transaction_status_change (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  transaction_id uuid NOT NULL,
  command text NOT NULL CHECK (command IN ('CREATE', 'STEP', 'UNPICK', 'UNREGISTER', 'CANCEL_RESERVATION')),
  from_status text CHECK (from_status IN ('ON_ORDER', 'RESERVED_ORDERED', 'RESERVED_PHYSICAL', 'PICKED', 'DEDUCTED', 'SOLD',
                                          'ORDERED', 'ARRIVED', 'REGISTERED', 'RECEIVED', 'PURCHASED')),
  to_status text NOT NULL CHECK (to_status IN ('ON_ORDER', 'RESERVED_ORDERED', 'RESERVED_PHYSICAL', 'PICKED', 'DEDUCTED', 'SOLD',
                                               'ORDERED', 'ARRIVED', 'REGISTERED', 'RECEIVED', 'PURCHASED')),
  qty numeric(28,6) NOT NULL CHECK (qty <> 0),
  status_date date NOT NULL,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL,
  created_by uuid,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, transaction_id) REFERENCES inventory_transaction (tenant_id, id),
  CHECK ((command = 'CREATE') = (from_status IS NULL))
);
CREATE INDEX IF NOT EXISTS inventory_transaction_status_change_txn
  ON inventory_transaction_status_change (tenant_id, transaction_id, occurred_at);
ALTER TABLE inventory_transaction_status_change ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_transaction_status_change FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS inventory_transaction_status_change_tenant_access ON inventory_transaction_status_change;
CREATE POLICY inventory_transaction_status_change_tenant_access ON inventory_transaction_status_change AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DROP POLICY IF EXISTS inventory_transaction_status_change_legal_entity_scope ON inventory_transaction_status_change;
CREATE POLICY inventory_transaction_status_change_legal_entity_scope ON inventory_transaction_status_change AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The status-change audit is append-only, like the ledger it describes.
CREATE OR REPLACE FUNCTION inventory_transaction_status_change_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'inventory_transaction_status_change is append-only' USING ERRCODE = 'restrict_violation';
END $$;
DROP TRIGGER IF EXISTS inventory_transaction_status_change_append_only ON inventory_transaction_status_change;
CREATE TRIGGER inventory_transaction_status_change_append_only BEFORE UPDATE OR DELETE ON inventory_transaction_status_change
  FOR EACH ROW EXECUTE FUNCTION inventory_transaction_status_change_append_only();

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON inventory_transaction_status_change TO passage_app';
    -- 0004 and 0005 created their tables without granting the application role,
    -- so transfer orders and ABC runs failed with permission denied under it.
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON transfer_order, transfer_order_line, abc_run, abc_classification TO passage_app';
  END IF;
END
$$;
