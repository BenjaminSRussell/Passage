alter table notify_outbound_message add column if not exists claim_token uuid;
create index if not exists ix_notify_outbound_claimable on notify_outbound_message ((coalesce(next_attempt_at, created_at))) where status = 'queued';
create index if not exists ix_notify_outbound_sending on notify_outbound_message (next_attempt_at) where status = 'sending';

DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION notify_claim_messages(channels text[], lease_secs int, max_rows int)
      RETURNS TABLE (tenant_id uuid, id uuid, channel text, recipient_address text, subject text, body_text text, attempts int, claim_token uuid)
      LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        UPDATE notify_outbound_message m
        SET status = 'sending', attempts = m.attempts + 1, claim_token = pg_catalog.gen_random_uuid(),
            next_attempt_at = pg_catalog.now() + pg_catalog.make_interval(secs => lease_secs::float8)
        WHERE (m.tenant_id, m.id) IN (
          SELECT q.tenant_id, q.id FROM notify_outbound_message q
          WHERE q.status = 'queued' AND q.channel = ANY (channels)
            AND (q.next_attempt_at IS NULL OR q.next_attempt_at <= pg_catalog.now())
          ORDER BY coalesce(q.next_attempt_at, q.created_at), q.id
          LIMIT max_rows
          FOR UPDATE SKIP LOCKED)
        RETURNING m.tenant_id, m.id, m.channel, m.recipient_address::text, m.subject::text, m.body_text, m.attempts, m.claim_token
      $body$;
    CREATE OR REPLACE FUNCTION notify_fail_unconfirmed_messages(max_rows int)
      RETURNS TABLE (tenant_id uuid, id uuid, channel text, attempts int)
      LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        UPDATE notify_outbound_message m
        SET status = 'failed', next_attempt_at = NULL, claim_token = NULL,
            last_error = 'delivery outcome unknown: the send lease expired before the result was recorded; not retried automatically to avoid a duplicate send'
        WHERE (m.tenant_id, m.id) IN (
          SELECT q.tenant_id, q.id FROM notify_outbound_message q
          WHERE q.status = 'sending' AND q.next_attempt_at < pg_catalog.now()
          LIMIT max_rows
          FOR UPDATE SKIP LOCKED)
        RETURNING m.tenant_id, m.id, m.channel, m.attempts
      $body$;
    CREATE OR REPLACE FUNCTION notify_distribute_can_scan()
      RETURNS boolean
      LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$
        SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = current_user AND (rolsuper OR rolbypassrls))
      $body$;
  $f$, current_schema());
END
$do$;

REVOKE ALL ON FUNCTION notify_claim_messages(text[], int, int) FROM PUBLIC;
REVOKE ALL ON FUNCTION notify_fail_unconfirmed_messages(int) FROM PUBLIC;
REVOKE ALL ON FUNCTION notify_distribute_can_scan() FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION notify_claim_messages(text[], int, int) TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION notify_fail_unconfirmed_messages(int) TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION notify_distribute_can_scan() TO passage_app';
  END IF;
END
$$;
