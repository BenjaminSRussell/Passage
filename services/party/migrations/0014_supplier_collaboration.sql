-- Supplier collaboration (#511, items 5 and 6): vendor requests with the onboarding questionnaire, document exchange with a vendor,
-- and controlled distribution of documents with acknowledgement, all with an insert-only audit trail. Every row belongs to one
-- legal entity, and every vendor row names the vendor it is scoped to; the services read the vendor from the caller's verified
-- token. Additive only.

-- A questionnaire is a versioned list of questions the buyer asks a prospective vendor. Questions are
-- [{"id","label","type":"TEXT|YESNO|CHOICE","required":bool,"options":[...]}].
CREATE TABLE IF NOT EXISTS vendor_questionnaire (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 140),
  version integer NOT NULL CHECK (version > 0),
  questions jsonb NOT NULL CHECK (jsonb_typeof(questions) = 'array' AND jsonb_array_length(questions) BETWEEN 1 AND 100),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, name, version)
);

-- One request of a vendor: ONBOARDING (the questionnaire of a prospective vendor), PROFILE_CHANGE or OTHER. An ONBOARDING request is
-- opened by the buyer (INVITED) with the vendor role blocked ('all'); the vendor answers and submits it; approval unblocks the role.
CREATE TABLE IF NOT EXISTS vendor_request (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  kind text NOT NULL CHECK (kind IN ('ONBOARDING', 'PROFILE_CHANGE', 'OTHER')),
  status text NOT NULL CHECK (status IN ('INVITED', 'SUBMITTED', 'APPROVED', 'REJECTED', 'WITHDRAWN')),
  subject text NOT NULL DEFAULT '' CHECK (char_length(subject) <= 200),
  details text NOT NULL DEFAULT '' CHECK (char_length(details) <= 4000),
  questionnaire_id uuid,
  answers jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(answers) = 'object'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  submitted_by text,
  submitted_at timestamptz,
  decided_by text,
  decided_at timestamptz,
  decision_note text NOT NULL DEFAULT '' CHECK (char_length(decision_note) <= 1000),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, vendor_id, legal_entity_id) REFERENCES vendor_role (tenant_id, id, legal_entity_id),
  FOREIGN KEY (tenant_id, questionnaire_id) REFERENCES vendor_questionnaire (tenant_id, id),
  CHECK ((decided_at IS NULL) = (decided_by IS NULL)),
  CHECK (kind <> 'ONBOARDING' OR questionnaire_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS vendor_request_vendor ON vendor_request (tenant_id, vendor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS vendor_request_status ON vendor_request (tenant_id, status, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS vendor_request_one_onboarding
  ON vendor_request (tenant_id, vendor_id) WHERE kind = 'ONBOARDING' AND status IN ('INVITED', 'SUBMITTED');

-- A document exchanged with one vendor, in either direction. The content is kept in the row (limit 5 MiB, checked by the API);
-- the object store and the virus scan that the PRD asks for are not built.
CREATE TABLE IF NOT EXISTS vendor_document (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  direction text NOT NULL CHECK (direction IN ('FROM_VENDOR', 'TO_VENDOR')),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
  file_name text NOT NULL CHECK (char_length(file_name) BETWEEN 1 AND 200),
  content_type text NOT NULL CHECK (char_length(content_type) BETWEEN 1 AND 100),
  size_bytes integer NOT NULL CHECK (size_bytes BETWEEN 1 AND 5242880),
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  content bytea NOT NULL,
  uploaded_by text NOT NULL,
  uploaded_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, vendor_id, legal_entity_id) REFERENCES vendor_role (tenant_id, id, legal_entity_id)
);
CREATE INDEX IF NOT EXISTS vendor_document_vendor ON vendor_document (tenant_id, vendor_id, uploaded_at DESC);

-- A controlled document: one numbered document with revisions. Publishing a revision does not reach any vendor until it is
-- distributed to them; a distribution is acknowledged by the vendor and stays on record.
CREATE TABLE IF NOT EXISTS controlled_document (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  doc_no text NOT NULL CHECK (char_length(doc_no) BETWEEN 1 AND 40),
  revision integer NOT NULL CHECK (revision > 0),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
  file_name text NOT NULL CHECK (char_length(file_name) BETWEEN 1 AND 200),
  content_type text NOT NULL CHECK (char_length(content_type) BETWEEN 1 AND 100),
  size_bytes integer NOT NULL CHECK (size_bytes BETWEEN 1 AND 5242880),
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  content bytea NOT NULL,
  requires_ack boolean NOT NULL DEFAULT true,
  published_by text NOT NULL,
  published_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, doc_no, revision)
);

CREATE TABLE IF NOT EXISTS controlled_distribution (
  tenant_id uuid NOT NULL,
  document_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sent_by text NOT NULL,
  sent_at timestamptz NOT NULL DEFAULT now(),
  acknowledged_by text,
  acknowledged_at timestamptz,
  PRIMARY KEY (tenant_id, document_id, vendor_id),
  FOREIGN KEY (tenant_id, document_id) REFERENCES controlled_document (tenant_id, id),
  FOREIGN KEY (tenant_id, vendor_id, legal_entity_id) REFERENCES vendor_role (tenant_id, id, legal_entity_id),
  CHECK ((acknowledged_at IS NULL) = (acknowledged_by IS NULL))
);
CREATE INDEX IF NOT EXISTS controlled_distribution_vendor ON controlled_distribution (tenant_id, vendor_id, sent_at DESC);

-- Insert-only record of what a vendor or a buyer did in the supplier collaboration.
CREATE TABLE IF NOT EXISTS vendor_portal_audit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  actor text NOT NULL,
  action text NOT NULL CHECK (char_length(action) BETWEEN 1 AND 60),
  subject_id uuid,
  detail text NOT NULL DEFAULT '' CHECK (char_length(detail) <= 500),
  at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS vendor_portal_audit_vendor ON vendor_portal_audit (tenant_id, vendor_id, at DESC);

DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['vendor_questionnaire', 'vendor_request', 'vendor_document', 'controlled_document', 'controlled_distribution', 'vendor_portal_audit'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_access', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS PERMISSIVE
      USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
      WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid)$p$, t || '_tenant_access', t);
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_legal_entity_scope', t);
    EXECUTE format($p$CREATE POLICY %I ON %I AS RESTRICTIVE
      USING (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
      WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
             OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))$p$, t || '_legal_entity_scope', t);
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON vendor_questionnaire, vendor_document, controlled_document, vendor_portal_audit TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON vendor_request, controlled_distribution TO passage_app';
  END IF;
END
$$;
