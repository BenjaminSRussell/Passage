-- Remaining pricing parameters (PRD 5.26.13). Per legal entity, two columns on pricing_parameter
-- (table, row level security and grant come from 0012): price_search_mode, the default of
-- searchNext on a new condition record, and small_order_thresholds, the SURCHARGE threshold per
-- currency (FR-26.6; at most 30 currencies, checked by the service; NULL means the reference table, 250 USD and 200 EUR). Per tenant, the new
-- table pricing_tenant_parameter: cache_ttl_seconds (NULL means the deployment's
-- PRICING_CACHE_TTL) and campaign_kill_switch_effect.
ALTER TABLE pricing_parameter
  ADD COLUMN IF NOT EXISTS price_search_mode text NOT NULL DEFAULT 'FIRST_MATCH'
    CHECK (price_search_mode IN ('FIRST_MATCH', 'SEARCH_NEXT')),
  ADD COLUMN IF NOT EXISTS small_order_thresholds jsonb
    CHECK (small_order_thresholds IS NULL OR jsonb_typeof(small_order_thresholds) = 'object');

CREATE TABLE IF NOT EXISTS pricing_tenant_parameter (
  tenant_id uuid NOT NULL PRIMARY KEY,
  cache_ttl_seconds integer CHECK (cache_ttl_seconds IS NULL OR cache_ttl_seconds BETWEEN 10 AND 600),
  campaign_kill_switch_effect text NOT NULL DEFAULT 'NEXT_RESOLVE'
    CHECK (campaign_kill_switch_effect IN ('NEXT_RESOLVE', 'IMMEDIATE_CACHE_FLUSH')),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  updated_by text NOT NULL DEFAULT '' CHECK (length(updated_by) <= 128),
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE pricing_tenant_parameter ENABLE ROW LEVEL SECURITY;
ALTER TABLE pricing_tenant_parameter FORCE ROW LEVEL SECURITY;
CREATE POLICY pricing_tenant_parameter_tenant_access ON pricing_tenant_parameter AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The store reads, inserts and updates by version. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pricing_tenant_parameter TO passage_app';
  END IF;
END
$$;
