-- Wave 17 purchasing slice 5: sealed-bid RFQs and weighted scoring (FR-17.19,
-- BR-17.16). Additive only (expand/contract). Tenant RLS on every new table.

-- A sealed RFQ hides its replies until reply_deadline; opened_at/opened_by log
-- the first time the replies were shown after it (AC-17.14).
ALTER TABLE purchase_rfq ADD COLUMN IF NOT EXISTS sealed boolean NOT NULL DEFAULT false;
ALTER TABLE purchase_rfq ADD COLUMN IF NOT EXISTS opened_at timestamptz;
ALTER TABLE purchase_rfq ADD COLUMN IF NOT EXISTS opened_by uuid;
ALTER TABLE purchase_rfq ADD CONSTRAINT purchase_rfq_opened_sealed CHECK (opened_at IS NULL OR sealed);

-- WEIGHTED_SCORE joins the ranking rules; 0004's inline check has the default name.
ALTER TABLE purchase_rfq DROP CONSTRAINT IF EXISTS purchase_rfq_ranking_rule_check;
ALTER TABLE purchase_rfq ADD CONSTRAINT purchase_rfq_ranking_rule_check
  CHECK (ranking_rule IN ('LOWEST_PRICE', 'LEAD_TIME', 'WEIGHTED_SCORE'));

-- The scoring group of an RFQ: weighted criteria in display order.
CREATE TABLE IF NOT EXISTS purchase_rfq_criterion (
  tenant_id uuid NOT NULL,
  rfq_id uuid NOT NULL,
  id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  name text NOT NULL CHECK (name <> '' AND length(name) <= 100),
  weight numeric(9,4) NOT NULL CHECK (weight > 0),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, rfq_id, seq),
  UNIQUE (tenant_id, rfq_id, id),
  FOREIGN KEY (tenant_id, rfq_id) REFERENCES purchase_rfq (tenant_id, id)
);

-- The buyer's 1-10 score of one invited vendor on one criterion of the same RFQ.
CREATE TABLE IF NOT EXISTS purchase_rfq_score (
  tenant_id uuid NOT NULL,
  rfq_id uuid NOT NULL,
  criterion_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  score integer NOT NULL CHECK (score BETWEEN 1 AND 10),
  scored_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, rfq_id, criterion_id, vendor_id),
  FOREIGN KEY (tenant_id, rfq_id, criterion_id) REFERENCES purchase_rfq_criterion (tenant_id, rfq_id, id),
  FOREIGN KEY (tenant_id, rfq_id, vendor_id) REFERENCES purchase_rfq_invitation (tenant_id, rfq_id, vendor_id)
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_rfq_criterion', 'purchase_rfq_score']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
END $$;
