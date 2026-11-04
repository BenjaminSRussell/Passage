-- Wave 17 purchasing slice 17: service PO lines (FR-17.23, BR-17.19). A PO line becomes a
-- SERVICE line when it has a specification: an optional outline of levels and service items, a
-- value limit and expected value for unplanned services, and an optional agreement line whose
-- remaining value also bounds them. Model service specifications are reusable outlines of a legal
-- entity (SAP ML10-ML12) that fill a line's outline. Additive only (expand/contract).

CREATE TABLE IF NOT EXISTS purchase_service_spec (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  line_no integer NOT NULL,
  value_limit numeric(20,6) NOT NULL DEFAULT 0 CHECK (value_limit >= 0),
  expected_value numeric(20,6) NOT NULL DEFAULT 0 CHECK (expected_value >= 0),
  contract_agreement_line_id uuid,
  model_id uuid,
  planned_total numeric(20,6) NOT NULL DEFAULT 0 CHECK (planned_total >= 0),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, purchase_order_id, line_no),
  FOREIGN KEY (tenant_id, purchase_order_id, line_no) REFERENCES purchase_order_line (tenant_id, purchase_order_id, line_no),
  FOREIGN KEY (tenant_id, contract_agreement_line_id) REFERENCES purchase_agreement_line (tenant_id, id),
  CHECK (expected_value <= value_limit)
);
CREATE INDEX IF NOT EXISTS purchase_service_spec_contract ON purchase_service_spec (tenant_id, contract_agreement_line_id)
  WHERE contract_agreement_line_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS purchase_service_item (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  line_no integer NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  parent_seq integer CHECK (parent_seq IS NULL OR parent_seq > 0),
  depth integer NOT NULL CHECK (depth BETWEEN 1 AND 5),
  node_kind text NOT NULL CHECK (node_kind IN ('LEVEL', 'ITEM')),
  description text NOT NULL DEFAULT '',
  product_id uuid,
  qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (qty >= 0),
  unit text NOT NULL DEFAULT '',
  unit_price numeric(20,6) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
  amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  PRIMARY KEY (tenant_id, purchase_order_id, line_no, seq),
  FOREIGN KEY (tenant_id, purchase_order_id, line_no) REFERENCES purchase_service_spec (tenant_id, purchase_order_id, line_no),
  CHECK (node_kind = 'LEVEL' OR (qty > 0 AND unit <> ''))
);

CREATE TABLE IF NOT EXISTS purchase_model_service_spec (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{1,20}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  currency char(3) NOT NULL,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE')),
  planned_total numeric(20,6) NOT NULL DEFAULT 0 CHECK (planned_total >= 0),
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS purchase_model_service_spec_unique ON purchase_model_service_spec (tenant_id, legal_entity_id, code);
CREATE INDEX IF NOT EXISTS purchase_model_service_spec_list ON purchase_model_service_spec (tenant_id, id DESC);

CREATE TABLE IF NOT EXISTS purchase_model_service_item (
  tenant_id uuid NOT NULL,
  model_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  parent_seq integer CHECK (parent_seq IS NULL OR parent_seq > 0),
  depth integer NOT NULL CHECK (depth BETWEEN 1 AND 5),
  node_kind text NOT NULL CHECK (node_kind IN ('LEVEL', 'ITEM')),
  description text NOT NULL DEFAULT '',
  product_id uuid,
  qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (qty >= 0),
  unit text NOT NULL DEFAULT '',
  unit_price numeric(20,6) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
  amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  PRIMARY KEY (tenant_id, model_id, seq),
  FOREIGN KEY (tenant_id, model_id) REFERENCES purchase_model_service_spec (tenant_id, id),
  CHECK (node_kind = 'LEVEL' OR (qty > 0 AND unit <> ''))
);

-- Row-level security: tenant policy on all four; the legal entity policy on the model table
-- (it carries legal_entity_id) and, through the PO, on the PO child tables like the delivery
-- schedule; the model items are scoped through their model.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_service_spec', 'purchase_service_item', 'purchase_model_service_spec', 'purchase_model_service_item']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
END $$;

CREATE POLICY purchase_model_service_spec_legal_entity_scope ON purchase_model_service_spec AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
    OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
    OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE POLICY purchase_model_service_item_legal_entity_scope ON purchase_model_service_item AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM purchase_model_service_spec AS scope_parent
      WHERE scope_parent.tenant_id = purchase_model_service_item.tenant_id
        AND scope_parent.id = purchase_model_service_item.model_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  ) WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM purchase_model_service_spec AS scope_parent
      WHERE scope_parent.tenant_id = purchase_model_service_item.tenant_id
        AND scope_parent.id = purchase_model_service_item.model_id
        AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
    )
  );

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_service_spec', 'purchase_service_item']
  LOOP
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (
      current_setting(''passage.legal_entity_scopes'', true) = ''*''
      OR EXISTS (SELECT 1 FROM purchase_order AS scope_parent
        WHERE scope_parent.tenant_id = %I.tenant_id AND scope_parent.id = %I.purchase_order_id
          AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))))
      WITH CHECK (
      current_setting(''passage.legal_entity_scopes'', true) = ''*''
      OR EXISTS (SELECT 1 FROM purchase_order AS scope_parent
        WHERE scope_parent.tenant_id = %I.tenant_id AND scope_parent.id = %I.purchase_order_id
          AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))))',
      t || '_legal_entity_scope', t, t, t, t, t);
  END LOOP;
END $$;

-- passage_app: a PO line's specification and its outline are replaced (INSERT, DELETE); a model
-- is changed in place (UPDATE) and its outline replaced (INSERT, DELETE).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON purchase_service_spec, purchase_service_item, purchase_model_service_item TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON purchase_model_service_spec TO passage_app';
  END IF;
END
$$;
