-- FR-17.13 purchasing policy per legal entity, and the actors that PO four-eyes
-- segregation (PRD 3.17.17 PO_FOUR_EYES) compares: who created a PO, who
-- submitted it, and who last changed its content (an edit or a reprice).
CREATE TABLE IF NOT EXISTS purchasing_policy (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  change_management_default boolean NOT NULL DEFAULT true,
  po_four_eyes boolean NOT NULL DEFAULT false,
  allow_invoice_before_receipt boolean NOT NULL DEFAULT false,
  row_version integer NOT NULL DEFAULT 1 CHECK (row_version > 0),
  updated_by uuid,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

ALTER TABLE purchasing_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE purchasing_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY purchasing_policy_tenant_access ON purchasing_policy AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY purchasing_policy_legal_entity_scope ON purchasing_policy AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
  )
  WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
  );

ALTER TABLE purchase_order ADD COLUMN IF NOT EXISTS created_by uuid;
ALTER TABLE purchase_order ADD COLUMN IF NOT EXISTS submitted_by uuid;
ALTER TABLE purchase_order ADD COLUMN IF NOT EXISTS changed_by uuid;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON purchasing_policy TO passage_app';
  END IF;
END
$$;
