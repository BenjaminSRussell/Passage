-- #472: the employee-vendor register maps an HR worker to the party vendor that pays their
-- expense reimbursements, per legal entity. A mapping is set once; the open items created
-- from approved expense reports name the mapped vendor as payee and keep the worker in
-- payee_worker_id. Proposals keep the PAYEE_UNVERIFIED items they left out.
CREATE TABLE IF NOT EXISTS ap_employee_vendor (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  worker_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  registered_by text NOT NULL CHECK (registered_by <> ''),
  registered_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, worker_id)
);
ALTER TABLE ap_employee_vendor ENABLE ROW LEVEL SECURITY;
ALTER TABLE ap_employee_vendor FORCE ROW LEVEL SECURITY;
CREATE POLICY ap_employee_vendor_tenant_access ON ap_employee_vendor AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ap_employee_vendor_legal_entity_scope ON ap_employee_vendor AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE ap_open_item ADD COLUMN IF NOT EXISTS payee_worker_id uuid;
ALTER TABLE ap_payment_proposal ADD COLUMN IF NOT EXISTS excluded_items jsonb NOT NULL DEFAULT '[]'::jsonb;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON ap_employee_vendor TO passage_app';
  END IF;
END
$$;
