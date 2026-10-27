-- Pricing context, wave 26 slice 6: vendor rebate agreements and their period claims
-- (FR-26.15, BR-26.11, AC-26.11). Additive: existing agreements become CUSTOMER agreements.

-- 0002 created the customer-or-customer-group check without a name. Find it by its
-- definition, drop it, and replace it with a named check that covers both party roles.
DO $$
DECLARE c record;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
           WHERE conrelid = 'rebate_agreement'::regclass AND contype = 'c'
             AND pg_get_constraintdef(oid) LIKE '%customer_id IS NULL%customer_group_id IS NULL%'
  LOOP
    EXECUTE format('ALTER TABLE rebate_agreement DROP CONSTRAINT %I', c.conname);
  END LOOP;
END $$;

ALTER TABLE rebate_agreement ADD COLUMN IF NOT EXISTS party_role text NOT NULL DEFAULT 'CUSTOMER'
  CHECK (party_role IN ('CUSTOMER', 'VENDOR'));
ALTER TABLE rebate_agreement ADD COLUMN IF NOT EXISTS vendor_id uuid;
ALTER TABLE rebate_agreement ADD CONSTRAINT rebate_agreement_party_check CHECK (
  (party_role = 'CUSTOMER' AND vendor_id IS NULL AND (customer_id IS NULL) <> (customer_group_id IS NULL))
  OR (party_role = 'VENDOR' AND vendor_id IS NOT NULL AND customer_id IS NULL AND customer_group_id IS NULL));
CREATE INDEX IF NOT EXISTS rebate_agreement_vendor_ix ON rebate_agreement (tenant_id, vendor_id) WHERE vendor_id IS NOT NULL;

-- A claim settles one period of a VENDOR agreement through w19 AP. Its amount is the
-- period's accruals less the earlier claims of the period (BR-26.11), so a period that
-- accrues more after its first claim gets a second claim for the difference, and a period
-- with nothing new to claim is refused (PRC-0015). Numbers RC-000001, RC-000002 ... run per
-- legal entity (§5.26.15).
CREATE TABLE IF NOT EXISTS vendor_rebate_claim (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  period_key text NOT NULL,
  number text NOT NULL CHECK (number ~ '^RC-[0-9]{6,}$'),
  accrued_total numeric(20,6) NOT NULL,
  prior_claimed numeric(20,6) NOT NULL,
  amount numeric(20,6) NOT NULL CHECK (amount <> 0),
  currency char(3) NOT NULL,
  settlement_mode text NOT NULL CHECK (settlement_mode IN ('CREDIT_NOTE', 'PAYMENT_DEDUCTION', 'VENDOR_INVOICE')),
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'APPROVED')),
  version int NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_by text NOT NULL DEFAULT '',
  approved_by text,
  approved_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, number),
  CHECK ((amount < 0) = (settlement_mode = 'VENDOR_INVOICE')),
  CHECK ((status = 'APPROVED') = (approved_at IS NOT NULL)),
  FOREIGN KEY (tenant_id, agreement_id, period_key) REFERENCES rebate_period (tenant_id, agreement_id, period_key)
);
CREATE INDEX IF NOT EXISTS vendor_rebate_claim_period_ix ON vendor_rebate_claim (tenant_id, agreement_id, period_key);

ALTER TABLE vendor_rebate_claim ENABLE ROW LEVEL SECURITY;
ALTER TABLE vendor_rebate_claim FORCE ROW LEVEL SECURITY;
CREATE POLICY vendor_rebate_claim_tenant_access ON vendor_rebate_claim AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY vendor_rebate_claim_legal_entity_scope ON vendor_rebate_claim AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
