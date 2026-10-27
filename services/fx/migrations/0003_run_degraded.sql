ALTER TABLE provider_import_run DROP CONSTRAINT IF EXISTS provider_import_run_status_check;
ALTER TABLE provider_import_run ADD CONSTRAINT provider_import_run_status_check
  CHECK (status IN ('running', 'succeeded', 'failed', 'degraded'));
