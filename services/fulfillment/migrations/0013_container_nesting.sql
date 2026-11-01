-- Fulfillment slice 12: nested containers (pallet -> carton). A pallet is a
-- container of its own (own SSCC, tare, returnable flag) that holds no lines:
-- its cartons point at it through parent_container_id and keep their lines.
-- No new table: container keeps its tenant policy and its grants (it is updated).
ALTER TABLE container ADD COLUMN IF NOT EXISTS parent_container_id uuid;
ALTER TABLE container DROP CONSTRAINT IF EXISTS container_parent_fk;
ALTER TABLE container ADD CONSTRAINT container_parent_fk
  FOREIGN KEY (tenant_id, parent_container_id) REFERENCES container (tenant_id, id);
ALTER TABLE container DROP CONSTRAINT IF EXISTS container_parent_not_self;
ALTER TABLE container ADD CONSTRAINT container_parent_not_self CHECK (parent_container_id IS NULL OR parent_container_id <> id);
CREATE INDEX IF NOT EXISTS container_parent_idx ON container (tenant_id, parent_container_id) WHERE parent_container_id IS NOT NULL;
