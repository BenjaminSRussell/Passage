-- Wave 17 purchasing slice 33: RFQ types and RFQ amendments (FR-17.19, PRD 3.17.13 rfq_types).
-- An RFQ type is a per-legal-entity setup: whether its RFQs are sealed, whether they may be amended, the
-- ranking rule and a default scoring group. An RFQ copies the type's flags when it is created, so changing a
-- type never moves an RFQ already out. An amendment (addendum) of a sent RFQ records what changed, may extend
-- the reply deadline and replace the scoring criteria, and every invited vendor acknowledges it. Additive only.
CREATE TABLE IF NOT EXISTS rfq_type (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_-]{1,20}$'),
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
  sealed boolean NOT NULL DEFAULT false,
  amendments_allowed boolean NOT NULL DEFAULT true,
  ranking_rule text NOT NULL DEFAULT 'LOWEST_PRICE' CHECK (ranking_rule IN ('LOWEST_PRICE', 'LEAD_TIME', 'WEIGHTED_SCORE')),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'INACTIVE')),
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS rfq_type_unique ON rfq_type (tenant_id, legal_entity_id, code);
-- The target of purchase_rfq_type_fk: an RFQ may only name a type of its own legal entity.
CREATE UNIQUE INDEX IF NOT EXISTS rfq_type_entity_id ON rfq_type (tenant_id, legal_entity_id, id);
CREATE INDEX IF NOT EXISTS rfq_type_list ON rfq_type (tenant_id, id DESC);

-- The type's default scoring group; replaced as a set when the type is saved.
CREATE TABLE IF NOT EXISTS rfq_type_criterion (
  tenant_id uuid NOT NULL,
  rfq_type_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  name text NOT NULL CHECK (name <> '' AND length(name) <= 100),
  weight numeric(9,4) NOT NULL CHECK (weight > 0),
  PRIMARY KEY (tenant_id, rfq_type_id, seq),
  FOREIGN KEY (tenant_id, rfq_type_id) REFERENCES rfq_type (tenant_id, id)
);

ALTER TABLE purchase_rfq ADD COLUMN IF NOT EXISTS rfq_type_id uuid;
ALTER TABLE purchase_rfq ADD COLUMN IF NOT EXISTS amendments_allowed boolean NOT NULL DEFAULT true;
-- sealed_required: the type demands sealed bids, so a draft edit cannot clear the sealed flag.
ALTER TABLE purchase_rfq ADD COLUMN IF NOT EXISTS sealed_required boolean NOT NULL DEFAULT false;
ALTER TABLE purchase_rfq ADD COLUMN IF NOT EXISTS amendment_no integer NOT NULL DEFAULT 0 CHECK (amendment_no >= 0);
-- The legal entity is part of the key, so an RFQ of legal entity A can never reference a type of legal entity B.
ALTER TABLE purchase_rfq DROP CONSTRAINT IF EXISTS purchase_rfq_type_fk;
ALTER TABLE purchase_rfq ADD CONSTRAINT purchase_rfq_type_fk
  FOREIGN KEY (tenant_id, legal_entity_id, rfq_type_id) REFERENCES rfq_type (tenant_id, legal_entity_id, id);

-- One row per amendment of an RFQ: RFQ-n-A<k>. previous_deadline and new_deadline are set when it moved the deadline.
-- late_cleared_vendor_ids audits the vendors whose replies were late only against previous_deadline and count as
-- on time once the deadline moved (BR-17.11).
CREATE TABLE IF NOT EXISTS purchase_rfq_amendment (
  tenant_id uuid NOT NULL,
  rfq_id uuid NOT NULL,
  amendment_no integer NOT NULL CHECK (amendment_no > 0),
  summary text NOT NULL CHECK (length(summary) BETWEEN 1 AND 2000),
  previous_deadline timestamptz,
  new_deadline timestamptz,
  criteria_changed boolean NOT NULL DEFAULT false,
  late_cleared_vendor_ids uuid[] NOT NULL DEFAULT '{}',
  issued_by uuid,
  issued_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, rfq_id, amendment_no),
  FOREIGN KEY (tenant_id, rfq_id) REFERENCES purchase_rfq (tenant_id, id),
  CHECK ((previous_deadline IS NULL) = (new_deadline IS NULL)),
  CHECK (new_deadline IS NOT NULL OR cardinality(late_cleared_vendor_ids) = 0)
);

-- The invited vendors that must acknowledge an amendment; acknowledged_at is set when the buyer records the
-- acknowledgement (the portal does the same in wave 20).
CREATE TABLE IF NOT EXISTS purchase_rfq_amendment_ack (
  tenant_id uuid NOT NULL,
  rfq_id uuid NOT NULL,
  amendment_no integer NOT NULL,
  vendor_id uuid NOT NULL,
  acknowledged_at timestamptz,
  acknowledged_by uuid,
  PRIMARY KEY (tenant_id, rfq_id, amendment_no, vendor_id),
  FOREIGN KEY (tenant_id, rfq_id, amendment_no) REFERENCES purchase_rfq_amendment (tenant_id, rfq_id, amendment_no),
  FOREIGN KEY (tenant_id, rfq_id, vendor_id) REFERENCES purchase_rfq_invitation (tenant_id, rfq_id, vendor_id)
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['rfq_type', 'rfq_type_criterion', 'purchase_rfq_amendment', 'purchase_rfq_amendment_ack']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
  EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                 'rfq_type_legal_entity_scope', 'rfq_type');
END $$;

-- The child tables take the legal entity of their header row, as in 0010.
DO $$
DECLARE
  child_table text;
  parent_table text;
  parent_key text;
BEGIN
  FOR child_table, parent_table, parent_key IN
    SELECT * FROM (VALUES
      ('rfq_type_criterion', 'rfq_type', 'rfq_type_id'),
      ('purchase_rfq_amendment', 'purchase_rfq', 'rfq_id'),
      ('purchase_rfq_amendment_ack', 'purchase_rfq', 'rfq_id')
    ) AS scope(child_table, parent_table, parent_key)
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE USING (
         current_setting(''passage.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM %I AS scope_parent
           WHERE scope_parent.tenant_id = %I.tenant_id
             AND scope_parent.id = %I.%I
             AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))
         )
       ) WITH CHECK (
         current_setting(''passage.legal_entity_scopes'', true) = ''*''
         OR EXISTS (
           SELECT 1 FROM %I AS scope_parent
           WHERE scope_parent.tenant_id = %I.tenant_id
             AND scope_parent.id = %I.%I
             AND scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))
         )
       )',
      child_table || '_legal_entity_scope', child_table, parent_table,
      child_table, child_table, parent_key,
      parent_table, child_table, child_table, parent_key
    );
  END LOOP;
END $$;

-- passage_app: a type is changed in place (UPDATE) and its default criteria replaced as a set (DELETE); amendments
-- are append-only and an acknowledgement is set on its row; a draft RFQ's scoring criteria are replaced as a set.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON rfq_type, purchase_rfq_amendment_ack TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON rfq_type_criterion TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON purchase_rfq_amendment TO passage_app';
    EXECUTE 'GRANT DELETE ON purchase_rfq_criterion TO passage_app';
  END IF;
END
$$;
