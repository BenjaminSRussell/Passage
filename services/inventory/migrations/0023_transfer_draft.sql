-- Wave 16 slice 22: transfer order drafts, delivery terms and deletion (FR-16.15, BR-16.13).
-- A DRAFT order is a working copy that nothing else sees: it is confirmed to CREATED, edited or
-- deleted. delivery_terms (incoterm-style code) and mode_of_delivery are free codes kept on the
-- header. A CREATED or DRAFT order has posted nothing and is referenced by nothing, so the store
-- deletes its row and its lines (transfer_order_line already holds DELETE from 0022; the foreign
-- key has no ON DELETE action, so the store removes the lines first in the same transaction).
--
-- transfer_order is a live table, so this file only changes the catalog: the new status check and
-- the two nullable columns without a default take their ACCESS EXCLUSIVE lock for an instant and
-- never scan or rewrite (the checks are NOT VALID; they hold for every new or updated row at once).
-- The lock_timeout makes the migration fail (and the rollout retry) instead of queueing behind a
-- long transaction while every later query queues behind it. 0024 validates the checks under
-- SHARE UPDATE EXCLUSIVE, like 0020 / 0021.
SET LOCAL lock_timeout = '5s';

ALTER TABLE transfer_order DROP CONSTRAINT IF EXISTS transfer_order_status_check;
ALTER TABLE transfer_order ADD CONSTRAINT transfer_order_status_check
  CHECK (status IN ('DRAFT', 'CREATED', 'SHIPPED', 'PARTIALLY_RECEIVED', 'RECEIVED', 'CANCELLED')) NOT VALID;
ALTER TABLE transfer_order ADD COLUMN IF NOT EXISTS delivery_terms text;
ALTER TABLE transfer_order ADD CONSTRAINT transfer_order_delivery_terms_check
  CHECK (delivery_terms IS NULL OR char_length(delivery_terms) BETWEEN 1 AND 20) NOT VALID;
ALTER TABLE transfer_order ADD COLUMN IF NOT EXISTS mode_of_delivery text;
ALTER TABLE transfer_order ADD CONSTRAINT transfer_order_mode_of_delivery_check
  CHECK (mode_of_delivery IS NULL OR char_length(mode_of_delivery) BETWEEN 1 AND 20) NOT VALID;

-- One row per deleted order. It keeps the order's Idempotency-Key taken, so a create retried after
-- a delete (a client that timed out and replays) is refused instead of bringing the order back
-- under a new id, and it records who deleted which order and when.
CREATE TABLE IF NOT EXISTS transfer_order_deleted (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  idempotency_key text NOT NULL,
  status text NOT NULL CHECK (status IN ('DRAFT', 'CREATED')),
  deleted_by text,
  deleted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
ALTER TABLE transfer_order_deleted ENABLE ROW LEVEL SECURITY;
ALTER TABLE transfer_order_deleted FORCE ROW LEVEL SECURITY;
CREATE POLICY transfer_order_deleted_tenant_access ON transfer_order_deleted AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY transfer_order_deleted_legal_entity_scope ON transfer_order_deleted AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT DELETE ON transfer_order TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON transfer_order_deleted TO passage_app';
  END IF;
END
$$;
