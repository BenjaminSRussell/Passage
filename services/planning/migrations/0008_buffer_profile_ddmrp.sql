-- DDMRP buffer profiles and stored buffers (FR-22.16, data model BufferProfile
-- and DdmrpBuffer). A buffer profile holds the lead-time, variability and
-- order-cycle factors with the spike and ADU-window parameters (4.22.13
-- ranges); version is the ETag every update bumps and idempotency_key with
-- input_hash lets a retried create replay. A stored buffer keeps the zones,
-- net-flow position, planned quantity and priority of its last recalculation,
-- one row per item, site and warehouse scope. Quantities are numeric, never float.

CREATE TABLE IF NOT EXISTS buffer_profile (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,19}$'),
  item_type text NOT NULL CHECK (item_type IN ('manufactured', 'purchased', 'distributed')),
  lead_time_factor numeric(5,3) NOT NULL CHECK (lead_time_factor >= 0),
  variability_factor numeric(5,3) NOT NULL CHECK (variability_factor >= 0),
  order_cycle_days integer NOT NULL DEFAULT 0 CHECK (order_cycle_days BETWEEN 0 AND 3650),
  min_order_qty numeric(18,6) NOT NULL DEFAULT 0 CHECK (min_order_qty >= 0),
  spike_threshold_pct numeric(5,2) NOT NULL DEFAULT 50 CHECK (spike_threshold_pct BETWEEN 1 AND 500),
  spike_horizon_days integer CHECK (spike_horizon_days BETWEEN 0 AND 3650),
  adu_window_past_days integer NOT NULL DEFAULT 90 CHECK (adu_window_past_days BETWEEN 0 AND 365),
  adu_window_future_days integer NOT NULL DEFAULT 0 CHECK (adu_window_future_days BETWEEN 0 AND 365),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  input_hash text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT buffer_profile_code_key UNIQUE (tenant_id, code),
  CONSTRAINT buffer_profile_idempotency_key UNIQUE (tenant_id, idempotency_key),
  -- ADU needs a window of at least 7 days (4.22.13: 7–365 days).
  CHECK (adu_window_past_days + adu_window_future_days >= 7),
  CHECK ((idempotency_key IS NULL) = (input_hash IS NULL))
);
ALTER TABLE buffer_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE buffer_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY buffer_profile_tenant_access ON buffer_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

CREATE TABLE IF NOT EXISTS ddmrp_buffer (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  item_id uuid NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid,
  profile_id uuid NOT NULL,
  adu numeric(18,6) NOT NULL CHECK (adu >= 0),
  dlt_days numeric(8,2) NOT NULL CHECK (dlt_days >= 0),
  daf numeric(6,3) NOT NULL DEFAULT 1 CHECK (daf >= 0),
  red_base numeric(18,6) NOT NULL CHECK (red_base >= 0),
  red_safety numeric(18,6) NOT NULL CHECK (red_safety >= 0),
  yellow numeric(18,6) NOT NULL CHECK (yellow >= 0),
  green numeric(18,6) NOT NULL CHECK (green >= 0),
  top_of_red numeric(18,6) NOT NULL,
  top_of_yellow numeric(18,6) NOT NULL,
  top_of_green numeric(18,6) NOT NULL,
  qualified_demand numeric(18,6) NOT NULL DEFAULT 0 CHECK (qualified_demand >= 0),
  net_flow numeric(18,6) NOT NULL,
  planned_qty numeric(18,6) NOT NULL CHECK (planned_qty >= 0),
  priority numeric(18,6) NOT NULL,
  zone text NOT NULL CHECK (zone IN ('red', 'yellow', 'green')),
  calculated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CONSTRAINT ddmrp_buffer_profile_fkey FOREIGN KEY (tenant_id, profile_id) REFERENCES buffer_profile (tenant_id, id),
  -- Data model invariant: top of red ≤ top of yellow ≤ top of green.
  CHECK (top_of_red <= top_of_yellow AND top_of_yellow <= top_of_green)
);
-- One stored buffer per item, site and warehouse scope; the recalculation
-- upserts on this key.
CREATE UNIQUE INDEX IF NOT EXISTS ddmrp_buffer_scope_key ON ddmrp_buffer (
  tenant_id, item_id, site_id, coalesce(warehouse_id, '00000000-0000-0000-0000-000000000000'::uuid));
-- The buffer status inquiry (4.22.14) filters by site and zone and sorts by priority.
CREATE INDEX IF NOT EXISTS ddmrp_buffer_site_zone_ix ON ddmrp_buffer (tenant_id, site_id, zone, priority);
CREATE INDEX IF NOT EXISTS ddmrp_buffer_profile_ix ON ddmrp_buffer (tenant_id, profile_id);
ALTER TABLE ddmrp_buffer ENABLE ROW LEVEL SECURITY;
ALTER TABLE ddmrp_buffer FORCE ROW LEVEL SECURITY;
CREATE POLICY ddmrp_buffer_tenant_access ON ddmrp_buffer AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Item coverage now refers to a stored buffer profile (FR-22.16
-- buffer_profile_id). NOT VALID keeps rows written by slice 9 before profiles
-- existed; every new insert or update is checked. A null reference is not
-- checked (MATCH SIMPLE).
ALTER TABLE item_coverage ADD CONSTRAINT item_coverage_buffer_profile_fkey
  FOREIGN KEY (tenant_id, buffer_profile_id) REFERENCES buffer_profile (tenant_id, id) NOT VALID;
CREATE INDEX IF NOT EXISTS item_coverage_buffer_profile_ix ON item_coverage (tenant_id, buffer_profile_id);
