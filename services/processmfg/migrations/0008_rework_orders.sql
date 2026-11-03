-- Process manufacturing, wave 24 slice 17: rework batch orders (FR-24.18,
-- AC-24.18). A rework order ties a batch order made from a rework formula to
-- the stored batch it reworks, with the quantity put in, the planned output and
-- the FR-24.18 parameters. Receiving the output either puts the reworked batch
-- on the disposition the plan names (the batch number is kept) or registers a
-- new batch with a genealogy link to the source (relation 'rework').
--
-- batch_genealogy, batch_order and batch_record are live tables: the new
-- foreign keys take a short SHARE ROW EXCLUSIVE lock on the referenced tables
-- and the widened relation check is swapped under ACCESS EXCLUSIVE, so the
-- check is added NOT VALID (nothing is scanned under that lock, which lasts an
-- instant) and 0009 validates it under SHARE UPDATE EXCLUSIVE. The lock_timeout
-- makes the migration fail (and the rollout retry) instead of queueing behind a
-- long transaction while every later query on those tables queues behind it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS rework_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  batch_order_id uuid NOT NULL,
  source_batch_id uuid NOT NULL,
  item_id uuid NOT NULL,
  input_qty numeric(18,6) NOT NULL CHECK (input_qty > 0),
  output_qty numeric(18,6) NOT NULL CHECK (output_qty > 0),
  keep_batch_number boolean NOT NULL,
  quality_order_required boolean NOT NULL,
  output_disposition text NOT NULL CHECK (output_disposition IN ('available', 'on-hold', 'quarantine')),
  variance_type text NOT NULL CHECK (variance_type IN ('rework', 'capitalized')),
  status text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned', 'received')),
  output_batch_id uuid,
  received_qty numeric(18,6) CHECK (received_qty > 0),
  received_by text,
  received_at timestamptz,
  version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, batch_order_id),
  FOREIGN KEY (tenant_id, batch_order_id) REFERENCES batch_order (tenant_id, id),
  FOREIGN KEY (tenant_id, source_batch_id) REFERENCES batch_record (tenant_id, id),
  FOREIGN KEY (tenant_id, output_batch_id) REFERENCES batch_record (tenant_id, id)
);
-- One open rework per source batch.
CREATE UNIQUE INDEX IF NOT EXISTS rework_order_open_source_ix ON rework_order (tenant_id, source_batch_id) WHERE status = 'planned';
CREATE INDEX IF NOT EXISTS rework_order_list_ix ON rework_order (tenant_id, created_at DESC, id DESC);
-- The reworks of a batch, as the batch reworked or as the output (genealogy).
CREATE INDEX IF NOT EXISTS rework_order_source_ix ON rework_order (tenant_id, source_batch_id);
CREATE INDEX IF NOT EXISTS rework_order_output_ix ON rework_order (tenant_id, output_batch_id) WHERE output_batch_id IS NOT NULL;

ALTER TABLE rework_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE rework_order FORCE ROW LEVEL SECURITY;
CREATE POLICY rework_order_tenant_access ON rework_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY rework_order_legal_entity_scope ON rework_order AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Genealogy now also records a batch made by reworking another. Dropping the
-- old check and adding the wider one NOT VALID in the same transaction leaves
-- no moment without a check; every new or updated row is checked at once.
ALTER TABLE batch_genealogy DROP CONSTRAINT IF EXISTS batch_genealogy_relation_check;
ALTER TABLE batch_genealogy ADD CONSTRAINT batch_genealogy_relation_check CHECK (relation IN ('merge', 'rework')) NOT VALID;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON rework_order TO passage_app';
  END IF;
END
$$;
