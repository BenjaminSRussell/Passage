-- Wave 17 purchasing slice 8: scheduling agreements and releases (FR-17.15,
-- BR-17.13). Additive only (expand/contract). Tenant RLS on every new table;
-- the legal entity scope policy sits on the agreement header, and schedule and
-- release rows are read through it, as for purchase agreements.

CREATE SEQUENCE IF NOT EXISTS purchase_scheduling_agreement_number_seq;

-- One agreement per vendor x product x site with its price term and release
-- profile (PRD 3.17.13 sa_release_profile, copied at create time).
CREATE TABLE IF NOT EXISTS purchase_scheduling_agreement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  product_id uuid NOT NULL,
  site_id uuid NOT NULL,
  currency char(3) NOT NULL,
  unit text NOT NULL,
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  target_qty numeric(20,6) NOT NULL CHECK (target_qty >= 0),
  valid_from date NOT NULL,
  valid_to date CHECK (valid_to IS NULL OR valid_to >= valid_from),
  frc_horizon_days integer NOT NULL CHECK (frc_horizon_days >= 0),
  jit_horizon_days integer NOT NULL CHECK (jit_horizon_days >= 0),
  firm_zone_days integer NOT NULL CHECK (firm_zone_days >= 0),
  tradeoff_zone_days integer NOT NULL CHECK (tradeoff_zone_days >= firm_zone_days),
  release_frequency text NOT NULL CHECK (release_frequency IN ('DAILY', 'WEEKLY', 'MONTHLY')),
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number)
);
CREATE INDEX IF NOT EXISTS purchase_scheduling_agreement_vendor ON purchase_scheduling_agreement (tenant_id, vendor_id);

-- The release schedule: dated quantities. received_qty is the cumulative receipt
-- against the line, written by wave 18; reduction_reason is the reason code of
-- the last reduction (required in the firm zone).
CREATE TABLE IF NOT EXISTS purchase_sa_schedule_line (
  tenant_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  delivery_date date NOT NULL,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  received_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (received_qty >= 0 AND received_qty <= qty),
  reduction_reason text,
  PRIMARY KEY (tenant_id, agreement_id, line_no),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES purchase_scheduling_agreement (tenant_id, id)
);

-- A numbered release REL-<agreement>-<n>. agreement_version is the schedule
-- version it snapshots: a run with the same kind and run date on an unchanged
-- schedule returns this release instead of a new one (PRD 3.17.16 job key).
-- cumulative_reset records an explicit reset that lets cumulative_released
-- drop below the previous release (BR-17.13).
CREATE TABLE IF NOT EXISTS purchase_sa_release (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  release_no integer NOT NULL CHECK (release_no > 0),
  number text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('FRC', 'JIT')),
  run_date date NOT NULL,
  agreement_version integer NOT NULL CHECK (agreement_version > 0),
  cumulative_reset boolean NOT NULL DEFAULT false,
  cumulative_released numeric(20,6) NOT NULL CHECK (cumulative_released >= 0),
  cumulative_received numeric(20,6) NOT NULL CHECK (cumulative_received >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, agreement_id, release_no),
  UNIQUE (tenant_id, number),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES purchase_scheduling_agreement (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS purchase_sa_release_run ON purchase_sa_release (tenant_id, agreement_id, kind, run_date, agreement_version);

CREATE TABLE IF NOT EXISTS purchase_sa_release_line (
  tenant_id uuid NOT NULL,
  release_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  schedule_line_no integer NOT NULL CHECK (schedule_line_no > 0),
  delivery_date date NOT NULL,
  zone text NOT NULL CHECK (zone IN ('FIRM', 'TRADEOFF', 'FORECAST')),
  in_horizon boolean NOT NULL,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  cumulative_released numeric(20,6) NOT NULL CHECK (cumulative_released >= 0),
  cumulative_received numeric(20,6) NOT NULL CHECK (cumulative_received >= 0),
  PRIMARY KEY (tenant_id, release_id, line_no),
  FOREIGN KEY (tenant_id, release_id) REFERENCES purchase_sa_release (tenant_id, id) ON DELETE CASCADE
);

DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_scheduling_agreement', 'purchase_sa_schedule_line', 'purchase_sa_release', 'purchase_sa_release_line']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
  EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                 'purchase_scheduling_agreement_legal_entity_scope', 'purchase_scheduling_agreement');
END $$;
