-- Printer status inquiry (§6.32.14): queue depth counts QUEUED jobs per queue, and the
-- retention purge (§6.32.19) deletes by created_at. QUEUED rows are few, so a partial
-- index keeps the depth count cheap as the 30-day job log grows.
CREATE INDEX IF NOT EXISTS print_job_queued ON print_job (tenant_id, queue_code) WHERE status = 'QUEUED';
