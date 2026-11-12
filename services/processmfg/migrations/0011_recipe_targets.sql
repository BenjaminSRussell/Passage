-- Process manufacturing, #25 (25-5): where the control recipe of a released batch order goes. A target says,
-- for the batches of one item in one legal entity, which shop floor resource runs them and whether the recipe
-- is rendered on a kiosk or sent to a process control system through an edge node (OPC-UA, or MQTT with the
-- topic the recipe document is published to). Releasing a batch order of that item generates the control recipe
-- in the shopfloor service; the order keeps the recipe's id, or the reason it could not be generated, so the
-- release can be repeated (:send-recipe).

CREATE TABLE IF NOT EXISTS recipe_target (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  item_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  destination text NOT NULL CHECK (destination IN ('kiosk', 'pcs-opcua', 'pcs-mqtt')),
  edge_id uuid,
  topic text CHECK (topic IS NULL OR char_length(topic) BETWEEN 1 AND 300),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, item_id),
  CHECK ((destination = 'kiosk') = (edge_id IS NULL)),
  CHECK ((destination = 'pcs-mqtt') = (topic IS NOT NULL))
);

ALTER TABLE recipe_target ENABLE ROW LEVEL SECURITY;
ALTER TABLE recipe_target FORCE ROW LEVEL SECURITY;
CREATE POLICY recipe_target_tenant_access ON recipe_target AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY recipe_target_legal_entity_scope ON recipe_target AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE batch_order ADD COLUMN IF NOT EXISTS control_recipe_id uuid;
ALTER TABLE batch_order ADD COLUMN IF NOT EXISTS control_recipe_error text;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON recipe_target TO passage_app';
  END IF;
END
$$;
