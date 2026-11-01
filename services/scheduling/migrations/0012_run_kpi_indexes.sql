-- Wave 23 slice 17: indexes behind the run measures and the Late orders
-- inquiry (PRD 4.23.14, and the (run_id) index of PRD 4.23.19).
--
-- GET /runs/{id}/kpis reads the jobs and secondary bookings that still carry a
-- run's id; without (tenant_id, run_id) that is a scan of every job of the
-- tenant. GET /late-orders starts from the orders that have a late job or an
-- unplaced operation; the two partial indexes keep that to the late and
-- unplaced rows instead of the whole plan. No new table, so no new grant.

CREATE INDEX IF NOT EXISTS scheduled_job_run ON scheduled_job (tenant_id, run_id);
CREATE INDEX IF NOT EXISTS scheduled_job_secondary_run ON scheduled_job_secondary (tenant_id, run_id);
CREATE INDEX IF NOT EXISTS scheduled_job_late ON scheduled_job (tenant_id, late_minutes, order_id) WHERE late_minutes > 0;
CREATE INDEX IF NOT EXISTS scheduling_operation_unplaced ON scheduling_operation (tenant_id, order_id) WHERE status = 'unplaced';
