-- 30-x: realized FX gain/loss at settlement (BR-30.4). A legal entity opts in to cross-currency settlement and names its
-- functional currency and the gain/loss accounts (defaults 7100 gain, 7200 loss). The realized difference of a settlement is
-- booked as its own single-currency journal in the functional currency, so the settlement keeps the amounts it was made with.
CREATE TABLE IF NOT EXISTS ar_fx_setting (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  cross_currency_enabled boolean NOT NULL DEFAULT false,
  functional_currency char(3) CHECK (functional_currency IS NULL OR functional_currency ~ '^[A-Z]{3}$'),
  gain_account text NOT NULL DEFAULT '7100' CHECK (gain_account ~ '^[0-9A-Za-z._-]{1,20}$'),
  loss_account text NOT NULL DEFAULT '7200' CHECK (loss_account ~ '^[0-9A-Za-z._-]{1,20}$'),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id),
  CHECK (NOT cross_currency_enabled OR functional_currency IS NOT NULL)
);
ALTER TABLE ar_fx_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE ar_fx_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY ar_fx_setting_tenant_access ON ar_fx_setting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY ar_fx_setting_legal_entity_scope ON ar_fx_setting AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
-- A cross-currency settlement keeps what it took off the item (in the item's currency), the item's currency and the two rates
-- that priced it; fx_gain_loss (migration 0001) holds the realized difference in the functional currency, positive for a gain.
ALTER TABLE settlement ADD COLUMN IF NOT EXISTS item_currency char(3) CHECK (item_currency IS NULL OR item_currency ~ '^[A-Z]{3}$');
ALTER TABLE settlement ADD COLUMN IF NOT EXISTS item_amount numeric(20,6) CHECK (item_amount IS NULL OR item_amount > 0);
ALTER TABLE settlement ADD COLUMN IF NOT EXISTS payment_rate numeric(20,10) CHECK (payment_rate IS NULL OR payment_rate > 0);
ALTER TABLE settlement ADD COLUMN IF NOT EXISTS item_rate numeric(20,10) CHECK (item_rate IS NULL OR item_rate > 0);
-- The time a deposit was released, so a backfilled release journal is dated then. Rows released before this column have none; the backfill
-- dates them by their received date.
ALTER TABLE down_payment ADD COLUMN IF NOT EXISTS released_at timestamptz;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON ar_fx_setting TO passage_app';
  END IF;
END
$$;
