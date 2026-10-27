-- Document routing rules (FR-32.13, BR-32.12). Scope columns are text so user ids
-- from Keycloak and zone codes share one shape; at most one scope may be set.
CREATE TABLE IF NOT EXISTS print_routing_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('LP_LABEL', 'LOCATION_LABEL', 'ITEM_LABEL', 'GS1_CARTON_LABEL', 'PICK_LIST', 'COUNT_SHEET', 'COA', 'BOL')),
  user_id text,
  zone text,
  warehouse_id text,
  queue_code text NOT NULL CHECK (queue_code ~ '^[A-Z0-9][A-Z0-9_-]{1,39}$'),
  sequence integer NOT NULL CHECK (sequence >= 1),
  idempotency_key text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (num_nonnulls(user_id, zone, warehouse_id) <= 1)
);
ALTER TABLE print_routing_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE print_routing_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY print_routing_rule_tenant_access ON print_routing_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
