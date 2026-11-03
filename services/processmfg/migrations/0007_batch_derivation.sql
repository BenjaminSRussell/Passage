-- Process manufacturing, wave 24 slice 16: batch attribute inheritance rules and
-- the derivation log (FR-24.15, AC-24.11). A rule says how one attribute of a
-- receiver batch of an item is derived from the same attribute of its sender
-- batches; the log records each derivation: the receiver value before and after,
-- the senders used and the senders that had no value (PMF-0007). The senders of
-- a receiver's latest derivation are its sender links, read to refuse a
-- derivation cycle (PMF-0130). Rules are retired, never edited; log
-- rows are only inserted and read.

CREATE TABLE IF NOT EXISTS inheritance_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  item_id uuid NOT NULL,
  attribute text NOT NULL CHECK (char_length(attribute) BETWEEN 1 AND 40),
  aggregation text NOT NULL CHECK (aggregation IN ('copy','min','max','weighted-average','earliest-date','latest-date','fixed')),
  decimals integer CHECK (decimals BETWEEN 0 AND 6),
  fixed_num numeric(18,6),
  fixed_text text CHECK (char_length(fixed_text) <= 200),
  fixed_date date,
  retired_at timestamptz,
  retired_by text,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (decimals IS NULL OR aggregation = 'weighted-average'),
  CHECK ((aggregation = 'fixed' AND num_nonnulls(fixed_num, fixed_text, fixed_date) = 1)
      OR (aggregation <> 'fixed' AND num_nonnulls(fixed_num, fixed_text, fixed_date) = 0)),
  CHECK ((retired_at IS NULL) = (retired_by IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS inheritance_rule_active_uq
  ON inheritance_rule (tenant_id, legal_entity_id, item_id, attribute) WHERE retired_at IS NULL;

CREATE TABLE IF NOT EXISTS batch_derivation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  receiver_batch_id uuid NOT NULL,
  receiver_version integer NOT NULL CHECK (receiver_version > 0),
  rule_id uuid NOT NULL,
  attribute text NOT NULL,
  aggregation text NOT NULL,
  recorded boolean NOT NULL,
  num_value numeric(18,6),
  text_value text,
  date_value date,
  -- The receiver's value of the attribute before the derivation (none when it
  -- had no value).
  before_num numeric(18,6),
  before_text text,
  before_date date,
  senders jsonb NOT NULL,
  missing jsonb NOT NULL,
  run_by text NOT NULL,
  run_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (recorded OR num_nonnulls(num_value, text_value, date_value) = 0),
  CHECK (num_nonnulls(before_num, before_text, before_date) <= 1),
  FOREIGN KEY (tenant_id, receiver_batch_id) REFERENCES batch_record (tenant_id, id),
  FOREIGN KEY (tenant_id, rule_id) REFERENCES inheritance_rule (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS batch_derivation_receiver_idx ON batch_derivation (tenant_id, receiver_batch_id, run_at DESC);
CREATE INDEX IF NOT EXISTS batch_derivation_run_idx ON batch_derivation (tenant_id, run_at DESC);

ALTER TABLE inheritance_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE inheritance_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY inheritance_rule_tenant_access ON inheritance_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inheritance_rule_legal_entity_scope ON inheritance_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE batch_derivation ENABLE ROW LEVEL SECURITY;
ALTER TABLE batch_derivation FORCE ROW LEVEL SECURITY;
CREATE POLICY batch_derivation_tenant_access ON batch_derivation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY batch_derivation_legal_entity_scope ON batch_derivation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON inheritance_rule TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON batch_derivation TO passage_app';
  END IF;
END
$$;
