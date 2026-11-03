-- PSG-25 slice 18: production's finish of an operation closes the open job-starts on its jobs
-- (FR-25.3). The consumer finds and locks the jobs of one (order, operation) on every finish, by
-- the references the schedule projects (order_ref compared in lower case). No new table; shop_job
-- keeps its forced row-level security and grants from 0007.

CREATE INDEX IF NOT EXISTS shop_job_operation_ix ON shop_job (tenant_id, lower(order_ref), operation_ref);
