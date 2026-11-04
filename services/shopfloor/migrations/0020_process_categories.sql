-- PSG-25 slice 21: process message category master data (FR-25.17, D8c, O05C/O06C/O07C equivalent).
-- Until now the categories and their destinations were fixed defaults in code and no category
-- rejected out-of-limit values. A tenant's row here overrides the built-in default of the same code,
-- or adds a code of its own, with its destinations, the reject-out-of-limit flag (BR-25.13, SF-0114)
-- and characteristic definitions (type, unit, limits, required). The code never changes; an update
-- carries the version it read (If-Match). A message already stored keeps the destinations it was
-- routed to. New table; the fixed list of codes on process_message (0004) becomes the code format,
-- since a custom code is now allowed (a short ACCESS EXCLUSIVE lock while the check is re-validated).

CREATE TABLE IF NOT EXISTS process_category (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL CHECK (code ~ '^[a-z0-9][a-z0-9-]*$' AND char_length(code) <= 32),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  destinations text[] NOT NULL CHECK (cardinality(destinations) BETWEEN 1 AND 5
    AND destinations <@ ARRAY['ebr', 'journal', 'quality', 'kafka', 'webhook']),
  reject_out_of_limit boolean NOT NULL DEFAULT false,
  characteristics jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(characteristics) = 'array'),
  active boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_by text NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 200),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, code)
);
ALTER TABLE process_category ENABLE ROW LEVEL SECURITY;
ALTER TABLE process_category FORCE ROW LEVEL SECURITY;
CREATE POLICY process_category_tenant_access ON process_category AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE process_message DROP CONSTRAINT IF EXISTS process_message_category_code_check;
ALTER TABLE process_message ADD CONSTRAINT process_message_category_code_check
  CHECK (category_code ~ '^[a-z0-9][a-z0-9-]*$' AND char_length(category_code) <= 32);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON process_category TO passage_app';
  END IF;
END
$$;
