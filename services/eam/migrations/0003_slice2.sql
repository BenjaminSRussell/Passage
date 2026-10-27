-- Wave 44, slice 2: criticality assessments (FR-44.22), fault catalogs and work order
-- fault registration (FR-44.16), downtime windows (BR-44.5), request triage history
-- (FR-44.6), and asset warranties with the work order warranty check (FR-44.17).

CREATE TABLE IF NOT EXISTS criticality_assessment (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  version integer NOT NULL CHECK (version >= 1),
  consequence_safety smallint NOT NULL CHECK (consequence_safety BETWEEN 1 AND 5),
  consequence_environment smallint NOT NULL CHECK (consequence_environment BETWEEN 1 AND 5),
  consequence_production smallint NOT NULL CHECK (consequence_production BETWEEN 1 AND 5),
  consequence_quality smallint NOT NULL CHECK (consequence_quality BETWEEN 1 AND 5),
  consequence_cost smallint NOT NULL CHECK (consequence_cost BETWEEN 1 AND 5),
  likelihood smallint NOT NULL CHECK (likelihood BETWEEN 1 AND 5),
  risk_score smallint NOT NULL CHECK (risk_score BETWEEN 1 AND 25),
  criticality smallint NOT NULL CHECK (criticality BETWEEN 1 AND 5),
  assessed_by text,
  assessed_at timestamptz NOT NULL,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, asset_id, version),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id)
);

-- One catalog per asset type; version is the ETag of PUT /fault-catalogs/{assetType}.
CREATE TABLE IF NOT EXISTS fault_catalog (
  tenant_id uuid NOT NULL,
  asset_type text NOT NULL CHECK (asset_type ~ '^[A-Z0-9_]{2,20}$'),
  version integer NOT NULL CHECK (version >= 1),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, asset_type)
);

CREATE TABLE IF NOT EXISTS fault_code (
  tenant_id uuid NOT NULL,
  asset_type text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('SYMPTOM', 'AREA', 'TYPE', 'CAUSE', 'REMEDY')),
  code text NOT NULL CHECK (code ~ '^[A-Z0-9_]{1,20}$'),
  text text NOT NULL CHECK (char_length(text) BETWEEN 1 AND 120),
  position smallint NOT NULL,
  PRIMARY KEY (tenant_id, asset_type, kind, code),
  FOREIGN KEY (tenant_id, asset_type) REFERENCES fault_catalog (tenant_id, asset_type)
);

CREATE TABLE IF NOT EXISTS fault_combination (
  tenant_id uuid NOT NULL,
  asset_type text NOT NULL,
  symptom_code text NOT NULL,
  area_code text NOT NULL,
  type_code text NOT NULL,
  position smallint NOT NULL,
  PRIMARY KEY (tenant_id, asset_type, symptom_code, area_code, type_code),
  FOREIGN KEY (tenant_id, asset_type) REFERENCES fault_catalog (tenant_id, asset_type)
);

-- Append-only fault registrations; the newest one per work order is current, so a
-- replayed key never overwrites a later correction.
CREATE TABLE IF NOT EXISTS work_order_fault (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  asset_type text NOT NULL,
  symptom_code text NOT NULL,
  area_code text NOT NULL,
  type_code text NOT NULL,
  cause_code text NOT NULL,
  remedy_code text NOT NULL,
  notes text CHECK (notes IS NULL OR char_length(notes) <= 1000),
  registered_by text,
  registered_at timestamptz NOT NULL,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS work_order_fault_wo ON work_order_fault (tenant_id, work_order_id, id);

-- Downtime windows of the production resource booked against a work order (BR-44.5).
CREATE TABLE IF NOT EXISTS downtime_window (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  work_order_id uuid NOT NULL,
  resource_id uuid NOT NULL,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  minutes integer NOT NULL CHECK (minutes >= 1),
  recorded_by text,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (ends_at > starts_at),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS downtime_window_wo ON downtime_window (tenant_id, work_order_id, starts_at);

-- Request triage: the latest note on the request, and an append-only history whose
-- key replays a retried action.
ALTER TABLE maintenance_request ADD COLUMN IF NOT EXISTS triage_note text
  CHECK (triage_note IS NULL OR char_length(triage_note) <= 500);

CREATE TABLE IF NOT EXISTS maintenance_request_triage (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  request_id uuid NOT NULL,
  action text NOT NULL CHECK (action IN ('TRIAGE', 'REJECT', 'DEFER')),
  from_status text NOT NULL,
  to_status text NOT NULL,
  note text,
  actor text,
  at timestamptz NOT NULL,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, request_id) REFERENCES maintenance_request (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS asset_warranty (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  warranty_type text NOT NULL CHECK (warranty_type ~ '^[A-Z0-9_]{2,20}$'),
  vendor_party_id uuid NOT NULL,
  start_basis text NOT NULL CHECK (start_basis IN ('PURCHASE', 'INSTALL')),
  starts_on date NOT NULL,
  months integer NOT NULL CHECK (months BETWEEN 1 AND 240),
  parts_covered boolean NOT NULL,
  labor_covered boolean NOT NULL,
  exclusions text[] NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'VOIDED')),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK (parts_covered OR labor_covered),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS asset_warranty_asset ON asset_warranty (tenant_id, asset_id);

ALTER TABLE work_order ADD COLUMN IF NOT EXISTS warranty_id uuid;
ALTER TABLE work_order ADD COLUMN IF NOT EXISTS warranty_covered boolean NOT NULL DEFAULT false;
ALTER TABLE work_order ADD CONSTRAINT work_order_warranty_fk
  FOREIGN KEY (tenant_id, warranty_id) REFERENCES asset_warranty (tenant_id, id);

-- Row-level security: tenant_access on every new tenant table (dbx.RLSPolicy shape, as 0001).
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['criticality_assessment', 'fault_catalog', 'fault_code', 'fault_combination',
                           'work_order_fault', 'downtime_window', 'maintenance_request_triage', 'asset_warranty'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
      WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t, t);
  END LOOP;
END $$;
