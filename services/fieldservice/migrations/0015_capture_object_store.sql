-- Captures move to an object store (#45): the bytes of a photo or signature are kept under object_key when the service has an
-- object store (FIELDSERVICE_CAPTURE_STORE), and in data (Postgres) when it has none. The retention job drops the bytes of
-- the captures of finished work orders (purged_at), keeping the row: its hash, size and position stay as the record that the
-- image existed, and the action log still names it.
ALTER TABLE service_wo_capture ALTER COLUMN data DROP NOT NULL;
ALTER TABLE service_wo_capture ADD COLUMN IF NOT EXISTS object_key text;
ALTER TABLE service_wo_capture ADD COLUMN IF NOT EXISTS purged_at timestamptz;
ALTER TABLE service_wo_capture ADD CONSTRAINT service_wo_capture_body_ck
  CHECK (purged_at IS NOT NULL OR data IS NOT NULL OR object_key IS NOT NULL);
ALTER TABLE service_wo_capture ADD CONSTRAINT service_wo_capture_key_ck
  CHECK (object_key IS NULL OR (data IS NULL AND char_length(object_key) BETWEEN 1 AND 512));
-- What the offload and purge passes look for: captures that still hold bytes.
CREATE INDEX IF NOT EXISTS service_wo_capture_live ON service_wo_capture (tenant_id, legal_entity_id) WHERE purged_at IS NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (data, object_key, purged_at) ON service_wo_capture TO passage_app';
  END IF;
END
$$;
