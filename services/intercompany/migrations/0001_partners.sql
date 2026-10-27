-- Wave 40 slice 4: outbox, idempotency keys and trading partners (FR-40.1).
-- Every table carries tenant_id first and is tenant-scoped by row-level
-- security (dbx.RLSPolicy); the GUC passage.tenant_id is set per transaction
-- by dbx.WithTenantTx.

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_access ON outbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int NOT NULL,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_keys_tenant_access ON idempotency_keys AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One pairing per direction: the seller entity's IC customer stands for the
-- buyer, the buyer entity's IC vendor for the seller. Percentages are
-- numeric, never float.
CREATE TABLE IF NOT EXISTS intercompany_partner (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  seller_entity text NOT NULL CHECK (length(seller_entity) BETWEEN 1 AND 64),
  buyer_entity text NOT NULL CHECK (length(buyer_entity) BETWEEN 1 AND 64),
  customer_account text NOT NULL CHECK (length(customer_account) BETWEEN 1 AND 64),
  vendor_account text NOT NULL CHECK (length(vendor_account) BETWEEN 1 AND 64),
  active boolean NOT NULL DEFAULT true,
  auto_create boolean NOT NULL DEFAULT true,
  auto_confirm boolean NOT NULL DEFAULT false,
  auto_post_packing_slip boolean NOT NULL DEFAULT false,
  auto_post_invoice boolean NOT NULL DEFAULT false,
  pricing text NOT NULL DEFAULT 'PriceList' CHECK (pricing IN ('PriceList', 'CostPlus', 'TransferPriceTable')),
  markup_pct numeric(10,6) NOT NULL DEFAULT 0 CHECK (markup_pct BETWEEN 0 AND 1000),
  tolerance_price_pct numeric(9,6) NOT NULL DEFAULT 0.5 CHECK (tolerance_price_pct BETWEEN 0 AND 100),
  tolerance_date_days integer NOT NULL DEFAULT 2 CHECK (tolerance_date_days BETWEEN 0 AND 30),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (seller_entity <> buyer_entity)
);

-- One active pairing per entity pair and direction (FR-40.1 notes).
CREATE UNIQUE INDEX IF NOT EXISTS intercompany_partner_one_active
  ON intercompany_partner (tenant_id, seller_entity, buyer_entity)
  WHERE active;
CREATE INDEX IF NOT EXISTS intercompany_partner_buyer
  ON intercompany_partner (tenant_id, buyer_entity);

ALTER TABLE intercompany_partner ENABLE ROW LEVEL SECURITY;
ALTER TABLE intercompany_partner FORCE ROW LEVEL SECURITY;
CREATE POLICY intercompany_partner_tenant_access ON intercompany_partner AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
