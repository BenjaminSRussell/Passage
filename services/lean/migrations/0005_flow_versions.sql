-- Flow versions (PRD 4.24.4 deliverable 3, FR-24.8): a new draft version of an
-- active flow keeps the flow's name, so the name is unique per value stream and
-- flow version instead of per value stream. Activating the new version expires
-- the version it replaces. No new table: production_flow already has tenant RLS
-- and its grants (0001), including UPDATE.

ALTER TABLE production_flow ADD COLUMN IF NOT EXISTS flow_version integer NOT NULL DEFAULT 1 CHECK (flow_version > 0);
ALTER TABLE production_flow ADD COLUMN IF NOT EXISTS previous_flow_id uuid;

ALTER TABLE production_flow DROP CONSTRAINT IF EXISTS production_flow_tenant_id_value_stream_id_name_key;
ALTER TABLE production_flow DROP CONSTRAINT IF EXISTS production_flow_name_version_key;
ALTER TABLE production_flow ADD CONSTRAINT production_flow_name_version_key UNIQUE (tenant_id, value_stream_id, name, flow_version);
ALTER TABLE production_flow DROP CONSTRAINT IF EXISTS production_flow_previous_fk;
ALTER TABLE production_flow ADD CONSTRAINT production_flow_previous_fk
  FOREIGN KEY (tenant_id, previous_flow_id) REFERENCES production_flow (tenant_id, id);
-- One new version per flow: at most one flow names a given predecessor.
CREATE UNIQUE INDEX IF NOT EXISTS production_flow_previous_ux ON production_flow (tenant_id, previous_flow_id) WHERE previous_flow_id IS NOT NULL;
