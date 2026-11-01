-- FR-17.17 signing limits. A signing limit is a worker's spending and approval limit for one
-- document type in one currency of a legal entity. The spending limit is what the worker may
-- request without an additional approval level; the approval limit is what the worker may
-- approve (BR-17.15). A limit is set or changed only by approving a signing_limit_request,
-- and it must be recertified by recertify_by (12 months after approval or recertification).
CREATE TABLE IF NOT EXISTS signing_limit (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('REQUISITION', 'PURCHASE_ORDER', 'INVOICE', 'EXPENSE')),
  currency char(3) NOT NULL,
  spending_limit numeric(20,6) NOT NULL CHECK (spending_limit >= 0),
  approval_limit numeric(20,6) NOT NULL CHECK (approval_limit >= 0),
  recertify_by date NOT NULL,
  last_recertified_on date,
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS signing_limit_unique
  ON signing_limit (tenant_id, legal_entity_id, worker_id, document_type, currency);
CREATE INDEX IF NOT EXISTS signing_limit_list ON signing_limit (tenant_id, id DESC);

CREATE TABLE IF NOT EXISTS signing_limit_request (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  document_type text NOT NULL CHECK (document_type IN ('REQUISITION', 'PURCHASE_ORDER', 'INVOICE', 'EXPENSE')),
  currency char(3) NOT NULL,
  spending_limit numeric(20,6) NOT NULL CHECK (spending_limit >= 0),
  approval_limit numeric(20,6) NOT NULL CHECK (approval_limit >= 0),
  reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 1000),
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED')),
  requested_by uuid NOT NULL,
  requested_at timestamptz NOT NULL DEFAULT now(),
  decided_by uuid,
  decided_at timestamptz,
  decision_note text NOT NULL DEFAULT '' CHECK (length(decision_note) <= 1000),
  signing_limit_id uuid,
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  PRIMARY KEY (tenant_id, id),
  CHECK ((status = 'PENDING') = (decided_by IS NULL))
);
CREATE INDEX IF NOT EXISTS signing_limit_request_list ON signing_limit_request (tenant_id, id DESC);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['signing_limit', 'signing_limit_request']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                   t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- passage_app: a limit changes on approval and recertification (UPDATE) and a request is
-- decided (UPDATE); neither is deleted.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON signing_limit, signing_limit_request TO passage_app';
  END IF;
END
$$;
