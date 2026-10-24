CREATE TABLE IF NOT EXISTS dimension_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('product', 'storage', 'tracking')),
  fields jsonb NOT NULL DEFAULT '[]',
  version int NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS item_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS item_model_group (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  costing_method text NOT NULL CHECK (costing_method IN ('fifo', 'lifo', 'weighted_average', 'weighted_average_date', 'standard', 'moving_average')),
  stocked boolean NOT NULL DEFAULT true,
  physical_negative boolean NOT NULL DEFAULT false,
  financial_negative boolean NOT NULL DEFAULT false,
  post_physical boolean NOT NULL DEFAULT true,
  post_financial boolean NOT NULL DEFAULT true,
  registration_required boolean NOT NULL DEFAULT false,
  receiving_required boolean NOT NULL DEFAULT false,
  picking_required boolean NOT NULL DEFAULT false,
  deduction_required boolean NOT NULL DEFAULT false,
  quarantine_required boolean NOT NULL DEFAULT false,
  include_physical_value boolean NOT NULL DEFAULT true,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS lifecycle_state (
  tenant_id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  category text NOT NULL CHECK (category IN ('draft', 'active', 'phase_out', 'obsolete')),
  default_for_new boolean NOT NULL DEFAULT false,
  applies_to text[] NOT NULL DEFAULT '{}',
  policies jsonb NOT NULL DEFAULT '{}',
  PRIMARY KEY (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS product (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_number text NOT NULL,
  subtype text NOT NULL CHECK (subtype IN ('product', 'master', 'variant')),
  master_id uuid,
  product_type text NOT NULL CHECK (product_type IN ('item', 'service')),
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  search_name text NOT NULL DEFAULT '',
  base_uom text NOT NULL,
  dimension_group_id uuid,
  storage_dimension_group_id uuid,
  tracking_dimension_group_id uuid,
  lifecycle_state text NOT NULL DEFAULT 'draft',
  dimension_values jsonb NOT NULL DEFAULT '{}',
  variant_hash text NOT NULL DEFAULT '',
  service jsonb,
  version int NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, product_number),
  UNIQUE (tenant_id, master_id, variant_hash)
);
CREATE INDEX IF NOT EXISTS product_search_name_idx ON product (tenant_id, search_name);
CREATE INDEX IF NOT EXISTS product_master_idx ON product (tenant_id, master_id);
CREATE TABLE IF NOT EXISTS released_product (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  item_number text NOT NULL,
  item_group_id uuid NOT NULL,
  item_model_group_id uuid NOT NULL,
  default_site_id uuid,
  default_warehouse_id uuid,
  lifecycle_state text NOT NULL DEFAULT 'active',
  blocked_purchase boolean NOT NULL DEFAULT false,
  blocked_sales boolean NOT NULL DEFAULT false,
  blocked_inventory boolean NOT NULL DEFAULT false,
  blocked_production boolean NOT NULL DEFAULT false,
  has_financial_txn boolean NOT NULL DEFAULT false,
  version int NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, product_id, legal_entity_id),
  UNIQUE (tenant_id, legal_entity_id, item_number)
);
CREATE TABLE IF NOT EXISTS released_product_site_default (
  tenant_id uuid NOT NULL,
  released_product_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  min_qty numeric(20,6) NOT NULL DEFAULT 0,
  multiple_qty numeric(20,6) NOT NULL DEFAULT 0,
  std_qty numeric(20,6) NOT NULL DEFAULT 0,
  lead_time_days int NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, released_product_id, site_id)
);
CREATE TABLE IF NOT EXISTS product_identifier (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id uuid NOT NULL,
  uom text NOT NULL DEFAULT '',
  type text NOT NULL,
  value text NOT NULL,
  is_primary boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, value)
);
CREATE TABLE IF NOT EXISTS product_uom_conversion (
  tenant_id uuid NOT NULL,
  product_id uuid NOT NULL,
  from_uom text NOT NULL,
  to_uom text NOT NULL,
  factor numeric(28,12) NOT NULL,
  conv_offset numeric(28,12) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, product_id, from_uom, to_uom)
);
CREATE TABLE IF NOT EXISTS attribute_def (
  tenant_id uuid NOT NULL,
  code text NOT NULL,
  kind text NOT NULL,
  unit text NOT NULL DEFAULT '',
  min_value numeric(28,12),
  max_value numeric(28,12),
  enum_values jsonb NOT NULL DEFAULT '[]',
  required boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS product_attribute_value (
  tenant_id uuid NOT NULL,
  product_id uuid NOT NULL,
  attribute_code text NOT NULL,
  value text NOT NULL,
  PRIMARY KEY (tenant_id, product_id, attribute_code)
);
CREATE TABLE IF NOT EXISTS category_hierarchy (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  role text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS category (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  hierarchy_id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  parent_id uuid,
  path text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS product_relation (
  tenant_id uuid NOT NULL,
  type_code text NOT NULL,
  from_product_id uuid NOT NULL,
  to_product_id uuid NOT NULL,
  sort_order int NOT NULL DEFAULT 0,
  valid_from text NOT NULL DEFAULT '',
  valid_to text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, type_code, from_product_id, to_product_id)
);
CREATE TABLE IF NOT EXISTS configuration_model (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  master_id uuid NOT NULL,
  version int NOT NULL,
  status text NOT NULL DEFAULT 'draft',
  model jsonb NOT NULL,
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS saved_configuration (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  model_id uuid NOT NULL,
  model_version int NOT NULL,
  scope text NOT NULL DEFAULT 'tenant',
  party_id uuid,
  name text NOT NULL,
  selections jsonb NOT NULL,
  status text NOT NULL DEFAULT 'valid',
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS catalog (
  tenant_id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  hierarchy text NOT NULL DEFAULT '',
  root text NOT NULL DEFAULT '',
  valid_from text NOT NULL DEFAULT '',
  valid_to text NOT NULL DEFAULT '',
  PRIMARY KEY (tenant_id, code)
);
CREATE TABLE IF NOT EXISTS document_info_record (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  type_code text NOT NULL,
  doc_number text NOT NULL,
  part text NOT NULL DEFAULT '000',
  doc_version text NOT NULL DEFAULT '00',
  description text NOT NULL DEFAULT '',
  status_code text NOT NULL DEFAULT 'draft',
  links jsonb NOT NULL DEFAULT '[]',
  originals jsonb NOT NULL DEFAULT '[]',
  text_tsv tsvector,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, type_code, doc_number, part, doc_version)
);
CREATE INDEX IF NOT EXISTS document_info_record_tsv_idx ON document_info_record USING gin (text_tsv);
CREATE TABLE IF NOT EXISTS gs1_prefix (
  tenant_id uuid NOT NULL,
  prefix text NOT NULL,
  next_number bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id)
);
CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
DO $$
DECLARE
  t text;
  scoped text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'dimension_group', 'item_group', 'item_model_group', 'lifecycle_state', 'product',
    'released_product', 'released_product_site_default', 'product_identifier',
    'product_uom_conversion', 'attribute_def', 'product_attribute_value',
    'category_hierarchy', 'category', 'product_relation', 'configuration_model',
    'saved_configuration', 'catalog', 'document_info_record', 'gs1_prefix',
    'idempotency_keys', 'outbox'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
  END LOOP;
  FOREACH scoped IN ARRAY ARRAY['released_product', 'released_product_site_default'] LOOP
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', scoped || '_legal_entity_scope', scoped);
  END LOOP;
END
$$;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON
      dimension_group, item_group, item_model_group, lifecycle_state, product,
      released_product, released_product_site_default, product_identifier,
      product_uom_conversion, attribute_def, product_attribute_value,
      category_hierarchy, category, product_relation, configuration_model,
      saved_configuration, catalog, document_info_record, gs1_prefix,
      idempotency_keys, outbox
    TO passage_app';
  END IF;
END
$$;
