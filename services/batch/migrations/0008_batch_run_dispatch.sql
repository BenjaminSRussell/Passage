alter table batch_run add column if not exists legal_entity_id uuid;
alter table batch_run add column if not exists attempts int not null default 0;
alter table batch_run add column if not exists service_duties text[];

create index if not exists ix_batch_run_dispatch on batch_run (status, scheduled_at) where status in ('executing', 'error');

DROP POLICY IF EXISTS batch_run_legal_entity_scope ON batch_run;
CREATE POLICY batch_run_legal_entity_scope ON batch_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR (legal_entity_id IS NOT NULL
             AND legal_entity_id::text = ANY (string_to_array(current_setting('passage.legal_entity_scopes', true), ','))))
  WITH CHECK (legal_entity_id IS NOT NULL
              AND (current_setting('passage.legal_entity_scopes', true) = '*'
                   OR legal_entity_id::text = ANY (string_to_array(current_setting('passage.legal_entity_scopes', true), ','))));

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION batch_dispatchable_runs(handlers text[], max_attempts int, retry_after_secs int, max_rows int)
      RETURNS TABLE (tenant_id uuid, run_id uuid, handler text, legal_entity_id uuid, triggered_by uuid, service_duties text[])
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        SELECT r.tenant_id, r.id, j.handler, r.legal_entity_id, r.triggered_by, r.service_duties
        FROM batch_run r
        JOIN batch_job j ON j.tenant_id = r.tenant_id AND j.id = r.job_id
        WHERE j.handler = ANY (handlers)
          AND r.legal_entity_id IS NOT NULL AND r.triggered_by IS NOT NULL
          AND coalesce(cardinality(r.service_duties), 0) > 0
          AND r.attempts < max_attempts
          AND (r.claimed_until IS NULL OR r.claimed_until < now())
          AND (r.status = 'executing'
               OR (r.status = 'error' AND coalesce(r.ended_at, '-infinity') < now() - make_interval(secs => retry_after_secs)))
        ORDER BY r.scheduled_at, r.id
        LIMIT max_rows
      $body$;
    CREATE OR REPLACE FUNCTION batch_fail_exhausted_runs(handlers text[], max_attempts int)
      RETURNS TABLE (tenant_id uuid, run_id uuid)
      LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        UPDATE batch_run r
        SET status = 'failed', ended_at = now(), claimed_until = NULL,
            error = coalesce(nullif(r.error, ''), 'dispatch attempts exhausted')
        FROM batch_job j
        WHERE j.tenant_id = r.tenant_id AND j.id = r.job_id AND j.handler = ANY (handlers)
          AND r.status IN ('executing', 'error') AND r.attempts >= max_attempts
          AND (r.claimed_until IS NULL OR r.claimed_until < now())
        RETURNING r.tenant_id, r.id
      $body$;
    CREATE OR REPLACE FUNCTION batch_dispatch_can_scan()
      RETURNS boolean
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls))
      $body$;
  $f$, current_schema());
END
$do$;

REVOKE ALL ON FUNCTION batch_dispatchable_runs(text[], int, int, int) FROM PUBLIC;
REVOKE ALL ON FUNCTION batch_fail_exhausted_runs(text[], int) FROM PUBLIC;
REVOKE ALL ON FUNCTION batch_dispatch_can_scan() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (attempts) ON batch_run TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION batch_dispatchable_runs(text[], int, int, int) TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION batch_fail_exhausted_runs(text[], int) TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION batch_dispatch_can_scan() TO passage_app';
  END IF;
END
$$;
