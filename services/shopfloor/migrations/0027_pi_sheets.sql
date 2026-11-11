-- #25 (FR-25.16): the PI sheet designer. A PI sheet is a versioned template of the ISA-88 structure a control recipe
-- has (procedure, unit procedures, operations, phases, each phase holding ordered process instructions in the eight
-- categories). It is designed as a draft, released (immutable from then on), and a change is a new version of the
-- same code. Generating a control recipe from a released sheet copies its document to the recipe, so the kiosk
-- renders the same structure it was designed in and an order's recipe never changes under a later edit.

CREATE TABLE IF NOT EXISTS pi_sheet (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[A-Z0-9][A-Z0-9_.-]{0,39}$'),
  version integer NOT NULL CHECK (version > 0),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  status text NOT NULL CHECK (status IN ('draft', 'released', 'obsolete')),
  doc jsonb NOT NULL,
  rev integer NOT NULL DEFAULT 1 CHECK (rev > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_by text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  released_by text,
  released_at timestamptz,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code, version),
  CHECK ((status = 'draft') = (released_at IS NULL))
);
-- At most one draft per code: a new version is cut from the released one only when none is open.
CREATE UNIQUE INDEX IF NOT EXISTS pi_sheet_one_draft ON pi_sheet (tenant_id, code) WHERE status = 'draft';
CREATE INDEX IF NOT EXISTS pi_sheet_list ON pi_sheet (tenant_id, code, version DESC);

ALTER TABLE pi_sheet ENABLE ROW LEVEL SECURITY;
ALTER TABLE pi_sheet FORCE ROW LEVEL SECURITY;
CREATE POLICY pi_sheet_tenant_access ON pi_sheet AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- A control recipe made from a sheet remembers which one.
ALTER TABLE control_recipe ADD COLUMN IF NOT EXISTS pi_sheet_id uuid;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pi_sheet TO passage_app';
  END IF;
END
$$;
