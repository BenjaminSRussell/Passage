-- Wave 17 purchasing slice 18: supply changes that arrive from other services. Fulfillment closes the
-- remainder of a drop-ship (dropship.closed.v1) and master planning applies an action message to a
-- purchase order (message.applied.v1); the purchasing consumer turns both into a change of the PO line
-- through the normal change management path and keeps one row per change here, applied or not, so a
-- buyer can see what moved a confirmed order without asking for it. Additive only (expand/contract).

-- A PO line gets a stable id. Fulfillment's drop-ship names the PO line it buys against by uuid
-- (purchase_order_line_id), and the line number alone is not an id outside this service. The column is
-- added without a default, given its default for new rows, backfilled once and then made NOT NULL.
-- purchase_order_line forces row-level security and the migration sets no tenant, so a migration role
-- that owns the table but is not a superuser (the deployed shape) would update no row under the policy
-- and SET NOT NULL would then fail on every existing line. The backfill runs with the policy lifted for
-- the owner and forced again in the same transaction (as fulfillment 0018 does).
SET LOCAL lock_timeout = '5s';
ALTER TABLE purchase_order_line ADD COLUMN IF NOT EXISTS id uuid;
ALTER TABLE purchase_order_line ALTER COLUMN id SET DEFAULT gen_random_uuid();
ALTER TABLE purchase_order_line NO FORCE ROW LEVEL SECURITY;
UPDATE purchase_order_line SET id = gen_random_uuid() WHERE id IS NULL;
ALTER TABLE purchase_order_line FORCE ROW LEVEL SECURITY;
ALTER TABLE purchase_order_line ALTER COLUMN id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS purchase_order_line_id ON purchase_order_line (tenant_id, id);

-- The at-least-once consumer's inbox (platform/eventing PgInbox): one row per handled event.
CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);

-- One row per inbound change. (source, source_key) is the idempotency key: the planning message id, or
-- the drop-ship id with its running closed total (a drop-ship can be closed more than once, and each
-- closing carries the total), so the same request delivered again in a new event changes nothing twice.
CREATE TABLE IF NOT EXISTS purchase_inbound_change (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  source text NOT NULL CHECK (source IN ('PLANNING_MESSAGE', 'DROPSHIP_CLOSED')),
  source_key text NOT NULL CHECK (length(source_key) BETWEEN 1 AND 100),
  event_id text NOT NULL,
  action text NOT NULL CHECK (length(action) BETWEEN 1 AND 40),
  purchase_order_id uuid,
  po_number text NOT NULL DEFAULT '',
  line_no integer CHECK (line_no IS NULL OR line_no > 0),
  line_id uuid,
  product_id uuid,
  status text NOT NULL CHECK (status IN ('APPLIED', 'REFUSED', 'IGNORED')),
  reason_code text NOT NULL DEFAULT '',
  reason text NOT NULL DEFAULT '',
  requested_qty numeric(20,6),
  requested_date date,
  qty_before numeric(20,6),
  qty_after numeric(20,6),
  date_before date,
  date_after date,
  version_no integer,
  reapproval_required boolean NOT NULL DEFAULT false,
  requested_by text NOT NULL DEFAULT '',
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, source, source_key)
);
-- The list pages newest first by id (UUIDv7, so in time order): the primary key serves the whole list,
-- this index the list of one order.
CREATE INDEX IF NOT EXISTS purchase_inbound_change_po ON purchase_inbound_change (tenant_id, purchase_order_id, id DESC)
  WHERE purchase_order_id IS NOT NULL;

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['consumer_inbox', 'purchase_inbound_change']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
  EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                 'purchase_inbound_change_legal_entity_scope', 'purchase_inbound_change');
END $$;

-- passage_app: both tables are append-only (the inbox and the change log are written once per event).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox, purchase_inbound_change TO passage_app';
  END IF;
END
$$;
