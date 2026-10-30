alter table batch_run add column if not exists legal_entity_id uuid;
alter table batch_run add column if not exists attempts int not null default 0;

DROP POLICY IF EXISTS batch_run_legal_entity_scope ON batch_run;
CREATE POLICY batch_run_legal_entity_scope ON batch_run AS RESTRICTIVE
  USING (legal_entity_id IS NULL
         OR current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY (string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE OR REPLACE FUNCTION batch_dispatchable_runs(handlers text[], max_attempts int, retry_after_secs int, max_rows int)
  RETURNS TABLE (tenant_id uuid, run_id uuid, handler text, legal_entity_id uuid, triggered_by uuid)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path FROM CURRENT
  AS $$
    SELECT r.tenant_id, r.id, j.handler, r.legal_entity_id, r.triggered_by
    FROM batch_run r
    JOIN batch_job j ON j.tenant_id = r.tenant_id AND j.id = r.job_id
    WHERE j.handler = ANY (handlers)
      AND r.legal_entity_id IS NOT NULL AND r.triggered_by IS NOT NULL
      AND r.attempts < max_attempts
      AND (r.claimed_until IS NULL OR r.claimed_until < now())
      AND (r.status = 'executing'
           OR (r.status = 'error' AND coalesce(r.ended_at, '-infinity') < now() - make_interval(secs => retry_after_secs)))
    ORDER BY r.scheduled_at, r.id
    LIMIT max_rows
  $$;
REVOKE ALL ON FUNCTION batch_dispatchable_runs(text[], int, int, int) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT UPDATE (attempts) ON batch_run TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION batch_dispatchable_runs(text[], int, int, int) TO passage_app';
  END IF;
END
$$;
