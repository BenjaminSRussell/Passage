-- Quota arrangements (FR-22.17, data model QuotaArrangement and QuotaSource).
-- An arrangement splits the net requirements of one item at one site across
-- its sources (vendor, own production with a production version, transfer from
-- a warehouse) by quota rating (BR-22.12). Validity periods of arrangements for
-- the same item and site must not overlap (data model invariant); the store
-- checks that under a transaction-scoped advisory lock on the item and site.
-- version is the ETag every update bumps; idempotency_key with input_hash lets
-- a retried create replay. Quantities are numeric, never float.

CREATE TABLE IF NOT EXISTS quota_arrangement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  item_id uuid NOT NULL,
  site_id uuid NOT NULL,
  valid_from date NOT NULL,
  valid_to date NOT NULL,
  split boolean NOT NULL DEFAULT false,
  min_split_qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (min_split_qty >= 0),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  input_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT quota_arrangement_idempotency_key UNIQUE (tenant_id, idempotency_key),
  CHECK (valid_to >= valid_from),
  CHECK ((idempotency_key IS NULL) = (input_hash IS NULL))
);
CREATE INDEX IF NOT EXISTS quota_arrangement_item_ix ON quota_arrangement (tenant_id, item_id, site_id, valid_from);
ALTER TABLE quota_arrangement ENABLE ROW LEVEL SECURITY;
ALTER TABLE quota_arrangement FORCE ROW LEVEL SECURITY;
CREATE POLICY quota_arrangement_tenant_access ON quota_arrangement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A source names exactly the reference of its type. allocated_qty is carried
-- over from a legacy system on create (SAP EQUP) and later accumulated from
-- the orders created against the arrangement. A per-period maximum needs its
-- period length and the other way round.
CREATE TABLE IF NOT EXISTS quota_source (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  arrangement_id uuid NOT NULL,
  line integer NOT NULL CHECK (line >= 1),
  source_type text NOT NULL CHECK (source_type IN ('vendor', 'production', 'transfer')),
  vendor_id uuid,
  production_version_id uuid,
  from_warehouse_id uuid,
  quota numeric(9,4) NOT NULL CHECK (quota >= 0),
  base_qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (base_qty >= 0),
  allocated_qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (allocated_qty >= 0),
  max_qty_per_period numeric(18,6) CHECK (max_qty_per_period > 0),
  period_days integer CHECK (period_days BETWEEN 1 AND 366),
  priority integer NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 999),
  blocked boolean NOT NULL DEFAULT false,
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT quota_source_arrangement_fkey FOREIGN KEY (tenant_id, arrangement_id)
    REFERENCES quota_arrangement (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT quota_source_line_key UNIQUE (tenant_id, arrangement_id, line),
  CHECK ((source_type = 'vendor') = (vendor_id IS NOT NULL)),
  CHECK ((source_type = 'production') = (production_version_id IS NOT NULL)),
  CHECK ((source_type = 'transfer') = (from_warehouse_id IS NOT NULL)),
  CHECK ((max_qty_per_period IS NULL) = (period_days IS NULL))
);
ALTER TABLE quota_source ENABLE ROW LEVEL SECURITY;
ALTER TABLE quota_source FORCE ROW LEVEL SECURITY;
CREATE POLICY quota_source_tenant_access ON quota_source AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
