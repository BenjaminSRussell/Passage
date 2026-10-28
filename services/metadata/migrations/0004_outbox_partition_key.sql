ALTER TABLE outbox
  ADD COLUMN IF NOT EXISTS partition_key text GENERATED ALWAYS AS (tenant_id::text || ':' || aggregate_id) STORED;
