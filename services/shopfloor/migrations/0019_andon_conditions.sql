-- PSG-25 slice 20: the other two andon conditions of FR-25.11 (scrap rate over 5 % in a bucket,
-- quality fail) beside the downtime alert, and a supervisor's clear of a quality fail alert. An
-- alert may name the job it came from and a one-line detail. BR-25.10 stays: one open alert per
-- (resource, kind), enforced by the partial unique index from 0002. No new table, so the grants of
-- 0002 (andon_alert) and the forced row-level security stay as they are.

ALTER TABLE andon_alert DROP CONSTRAINT IF EXISTS andon_alert_kind_check;
ALTER TABLE andon_alert ADD CONSTRAINT andon_alert_kind_check CHECK (kind IN ('downtime', 'scrap-rate', 'quality-fail'));

ALTER TABLE andon_alert ADD COLUMN IF NOT EXISTS job_id uuid;
ALTER TABLE andon_alert ADD COLUMN IF NOT EXISTS detail text CHECK (detail IS NULL OR char_length(detail) BETWEEN 1 AND 500);
ALTER TABLE andon_alert ADD COLUMN IF NOT EXISTS cleared_by text;
ALTER TABLE andon_alert ADD COLUMN IF NOT EXISTS clear_reason text CHECK (clear_reason IS NULL OR char_length(clear_reason) BETWEEN 1 AND 500);
ALTER TABLE andon_alert ADD CONSTRAINT andon_alert_job_fk FOREIGN KEY (tenant_id, job_id) REFERENCES shop_job (tenant_id, id);
