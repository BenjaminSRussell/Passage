-- Wave 17 purchasing slice 29: requisition approval, rejection, reopening and cancellation (BR-17.1, FR-17.2).
-- approved_steps counts the levels of approver_chain that have approved in the current round; history is the
-- requisition's audit trail (submitted, each level's decision, reopened, cancelled), written once and never changed.
ALTER TABLE purchase_requisition ADD COLUMN IF NOT EXISTS approved_steps integer NOT NULL DEFAULT 0 CHECK (approved_steps >= 0);

CREATE TABLE IF NOT EXISTS purchase_requisition_history (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  requisition_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  action text NOT NULL CHECK (action IN ('SUBMITTED', 'APPROVED', 'REJECTED', 'REOPENED', 'CANCELLED')),
  step_no integer CHECK (step_no IS NULL OR step_no > 0),
  role text,
  actor_id uuid,
  comment text NOT NULL DEFAULT '',
  from_status text NOT NULL,
  to_status text NOT NULL,
  requisition_version integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, requisition_id) REFERENCES purchase_requisition (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS purchase_requisition_history_by_requisition ON purchase_requisition_history (tenant_id, requisition_id, created_at, id);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_requisition_history']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '',''))) WITH CHECK (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                   t || '_legal_entity_scope', t);
  END LOOP;
END $$;

-- passage_app: history rows are appended and read. purchase_requisition and its lines already carry UPDATE (0009).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON purchase_requisition_history TO passage_app';
  END IF;
END
$$;
