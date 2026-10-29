-- Wave 44, slice 4: functional-location hierarchy with asset installation history
-- (FR-44.1, FR-44.2), and maintenance sequences with step hierarchy and generated
-- occurrences (FR-44.15, BR-44.10).

-- Functional locations form a tree per site. path is the slash-joined code chain
-- from the root (for example PLANT-1/LINE-2/PUMP-3), unique per site; depth is 1
-- at the root and may not exceed 12 (EAM-FL-001, enforced in the domain and by
-- the CHECK). The PRD names an ltree column; a text path with a pattern index
-- serves the same subtree reads without the extension (see NOT-IMPLEMENTED.md).
CREATE TABLE IF NOT EXISTS functional_location (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_.-]{0,39}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  site_id uuid NOT NULL,
  parent_id uuid,
  location_type text CHECK (location_type IS NULL OR location_type ~ '^[A-Z0-9_]{2,20}$'),
  depth integer NOT NULL CHECK (depth BETWEEN 1 AND 12),
  path text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, site_id, path),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((parent_id IS NULL) = (depth = 1)),
  FOREIGN KEY (tenant_id, parent_id) REFERENCES functional_location (tenant_id, id)
);

-- Subtree reads: path = p OR path LIKE p || '/%'.
CREATE INDEX IF NOT EXISTS ix_eam_floc_path ON functional_location (tenant_id, site_id, path text_pattern_ops);
CREATE INDEX IF NOT EXISTS ix_eam_floc_parent ON functional_location (tenant_id, parent_id);

-- An asset sits at one functional location at a time (null until installed).
ALTER TABLE asset ADD COLUMN IF NOT EXISTS functional_location_id uuid;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_eam_asset_floc'
                 AND conrelid = 'asset'::regclass) THEN
    ALTER TABLE asset ADD CONSTRAINT fk_eam_asset_floc
      FOREIGN KEY (tenant_id, functional_location_id) REFERENCES functional_location (tenant_id, id);
  END IF;
END $$;
CREATE INDEX IF NOT EXISTS ix_eam_asset_floc ON asset (tenant_id, functional_location_id);

-- Append-only install, move and remove history of an asset (FR-44.1, FR-44.2):
-- the location it left and the one it went to.
CREATE TABLE IF NOT EXISTS asset_location_history (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  asset_id uuid NOT NULL,
  action text NOT NULL CHECK (action IN ('INSTALL', 'MOVE', 'REMOVE')),
  from_location_id uuid,
  to_location_id uuid,
  actor text NOT NULL,
  at timestamptz NOT NULL,
  idempotency_key text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((action = 'INSTALL' AND from_location_id IS NULL AND to_location_id IS NOT NULL)
      OR (action = 'MOVE' AND from_location_id IS NOT NULL AND to_location_id IS NOT NULL)
      OR (action = 'REMOVE' AND from_location_id IS NOT NULL AND to_location_id IS NULL)),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  FOREIGN KEY (tenant_id, from_location_id) REFERENCES functional_location (tenant_id, id),
  FOREIGN KEY (tenant_id, to_location_id) REFERENCES functional_location (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS ix_eam_asset_loc_hist ON asset_location_history (tenant_id, asset_id, at);

-- Maintenance sequence: job-type steps on an asset or an asset type, each step
-- due every `multiple` occurrences of a base interval (BR-44.10). steps is a JSON
-- array of {code, jobType, multiple, hierarchy, includes[]}.
CREATE TABLE IF NOT EXISTS maintenance_sequence (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_-]{1,29}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  asset_type text CHECK (asset_type IS NULL OR asset_type ~ '^[A-Z0-9_]{2,20}$'),
  asset_id uuid,
  base_interval_value integer NOT NULL CHECK (base_interval_value BETWEEN 1 AND 100000),
  base_interval_unit text CHECK (base_interval_unit IS NULL OR base_interval_unit IN ('DAYS', 'WEEKS', 'MONTHS', 'YEARS')),
  counter_id uuid,
  steps jsonb NOT NULL CHECK (jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) BETWEEN 1 AND 50),
  last_occurrence_number integer NOT NULL DEFAULT 0 CHECK (last_occurrence_number >= 0),
  version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code),
  UNIQUE (tenant_id, idempotency_key),
  CHECK ((asset_id IS NULL) <> (asset_type IS NULL)),
  CHECK ((base_interval_unit IS NULL) <> (counter_id IS NULL)),
  CHECK (counter_id IS NULL OR asset_id IS NOT NULL),
  FOREIGN KEY (tenant_id, asset_id) REFERENCES asset (tenant_id, id),
  FOREIGN KEY (tenant_id, counter_id) REFERENCES counter (tenant_id, id)
);

-- One row per generated occurrence; the number is unique per sequence. The work
-- order carries the selected step's job type; job_types lists every job of the
-- occurrence (selected step first, then the extra due steps it does not include).
CREATE TABLE IF NOT EXISTS sequence_occurrence (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  sequence_id uuid NOT NULL,
  occurrence_number integer NOT NULL CHECK (occurrence_number >= 1),
  step_codes text[] NOT NULL,
  job_types text[] NOT NULL,
  work_order_id uuid,
  actor text NOT NULL,
  idempotency_key text,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, sequence_id, occurrence_number),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, sequence_id) REFERENCES maintenance_sequence (tenant_id, id),
  FOREIGN KEY (tenant_id, work_order_id) REFERENCES work_order (tenant_id, id)
);

-- Row-level security: tenant_access on every new tenant table (same shape as 0001).
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['functional_location', 'asset_location_history', 'maintenance_sequence',
                           'sequence_occurrence'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS %I_tenant_access ON %I', t, t);
    EXECUTE format('CREATE POLICY %I_tenant_access ON %I AS PERMISSIVE
      USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)
      WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t, t);
  END LOOP;
END $$;

-- Application role privileges (same rules as 0004). Functional locations are
-- create-only in this slice, and the location history and the sequence
-- occurrences are append-only. A sequence is row-locked (FOR UPDATE) and its
-- occurrence counter advanced, so it also gets UPDATE.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON functional_location, asset_location_history, sequence_occurrence TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON maintenance_sequence TO passage_app';
  END IF;
END
$$;
