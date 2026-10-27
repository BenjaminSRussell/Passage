-- Pricing context, wave 26 slice 5: campaigns with validity, target group, budget and kill
-- switch (FR-26.10), and versioned pricing procedures (FR-26.1). Additive only.

CREATE TABLE IF NOT EXISTS campaign (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
  valid_from date NOT NULL,
  valid_to date NOT NULL,
  target_customer_group text NOT NULL DEFAULT '' CHECK (length(target_customer_group) <= 64),
  budget numeric(20,6) NOT NULL CHECK (budget >= 0),
  currency char(3) NOT NULL,
  killed boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (valid_from <= valid_to)
);
CREATE INDEX IF NOT EXISTS campaign_entity_ix ON campaign (tenant_id, legal_entity_id, valid_from);

-- A condition record may belong to a campaign; the campaign's kill switch and validity gate
-- it in resolve (FR-26.2 optional campaign dimension, FR-26.10).
ALTER TABLE condition_record ADD COLUMN IF NOT EXISTS campaign_id uuid;
ALTER TABLE condition_record ADD CONSTRAINT condition_record_campaign_fk
  FOREIGN KEY (tenant_id, campaign_id) REFERENCES campaign (tenant_id, id);
CREATE INDEX IF NOT EXISTS condition_record_campaign_ix ON condition_record (tenant_id, campaign_id) WHERE campaign_id IS NOT NULL;

-- One version of a pricing procedure. steps holds the ordered steps (step_no, condition_type,
-- requirement CEL, exclusion group and mode, statistical, account key). ACTIVE and SUPERSEDED
-- versions are immutable (PRC-0008); revision is the optimistic version of a DRAFT's steps.
CREATE TABLE IF NOT EXISTS pricing_procedure (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  name text NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
  version integer NOT NULL CHECK (version >= 1),
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'ACTIVE', 'SUPERSEDED')),
  steps jsonb NOT NULL,
  revision integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  activated_at timestamptz,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, name, version),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS pricing_procedure_one_active_ix ON pricing_procedure (tenant_id, legal_entity_id, name)
  WHERE status = 'ACTIVE';

ALTER TABLE campaign ENABLE ROW LEVEL SECURITY;
ALTER TABLE campaign FORCE ROW LEVEL SECURITY;
CREATE POLICY campaign_tenant_access ON campaign AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY campaign_legal_entity_scope ON campaign AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE pricing_procedure ENABLE ROW LEVEL SECURITY;
ALTER TABLE pricing_procedure FORCE ROW LEVEL SECURITY;
CREATE POLICY pricing_procedure_tenant_access ON pricing_procedure AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY pricing_procedure_legal_entity_scope ON pricing_procedure AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
