-- Print queue management (FR-32.13, §6.32.6): a printer can be edited, moved between queues
-- and retired, and a routing rule can be edited and deleted. A retired printer keeps its row
-- (the job log names it by code) but leaves its queue, so the failover rank is unique among
-- active printers only.
ALTER TABLE print_device ADD COLUMN IF NOT EXISTS retired_at timestamptz;
ALTER TABLE print_device DROP CONSTRAINT IF EXISTS print_device_tenant_id_queue_code_failover_rank_key;
CREATE UNIQUE INDEX IF NOT EXISTS print_device_active_rank ON print_device (tenant_id, queue_code, failover_rank) WHERE retired_at IS NULL;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE ON print_device TO passage_app';
    EXECUTE 'GRANT UPDATE, DELETE ON print_routing_rule TO passage_app';
  END IF;
END
$$;
