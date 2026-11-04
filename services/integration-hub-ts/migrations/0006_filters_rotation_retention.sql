-- Integration hub: CESQL event filters on subscriptions, signing-secret rotation
-- with a grace period, the key-rotation re-seal job's finder, and retention of
-- hub_client_usage.
-- No new table: the columns are on hub_subscription (0001), which already carries
-- tenant_id, ENABLE + FORCE row-level security, the tenant policy and the
-- passage_app grants (SELECT, INSERT, UPDATE, DELETE); the store updates the new
-- columns under them and nothing else needs a grant. hub_client_usage keeps the
-- grants of 0004 (SELECT, INSERT, UPDATE): the purge below runs inside a
-- SECURITY DEFINER function as the owner, so passage_app gets no DELETE.

-- A CESQL expression (src/cesql.ts) over the event's attributes and data; an
-- event is delivered when its type matches a pattern and the expression holds.
ALTER TABLE hub_subscription ADD COLUMN IF NOT EXISTS event_filter text CHECK (event_filter IS NULL OR length(event_filter) BETWEEN 1 AND 1024);

-- Rotation: the previous signing secret stays valid until previous_secret_expires_at,
-- and every delivery in that window carries a signature for each secret (v1=new,v1=old),
-- so a receiver can switch over without dropping events.
ALTER TABLE hub_subscription ADD COLUMN IF NOT EXISTS previous_secret_sealed bytea CHECK (previous_secret_sealed IS NULL OR substring(previous_secret_sealed FROM 1 FOR 1) = '\x01'::bytea);
ALTER TABLE hub_subscription ADD COLUMN IF NOT EXISTS previous_secret_expires_at timestamptz;
ALTER TABLE hub_subscription ADD COLUMN IF NOT EXISTS secret_rotated_at timestamptz;
ALTER TABLE hub_subscription ADD CONSTRAINT hub_subscription_previous_secret_shape CHECK (
  (previous_secret_sealed IS NULL AND previous_secret_expires_at IS NULL)
  OR (previous_secret_sealed IS NOT NULL AND previous_secret_expires_at IS NOT NULL AND kind = 'webhook'));

-- Maintenance works across tenants like the retry poller does (see 0001): policies
-- for the owner role only (never passage_app), read by SECURITY DEFINER functions that
-- pin search_path with pg_temp last, answer ids or a count only, and that only
-- passage_app may execute. The re-seal itself then runs inside each tenant under the
-- tenant policies.
DO $do$
BEGIN
  IF current_user = 'passage_app' THEN
    RAISE EXCEPTION 'integration-hub migrations must run as the owner role, not passage_app';
  END IF;
  -- The purge's DELETE reads the rows it removes, so the owner needs a SELECT policy as well as the DELETE one:
  -- under FORCE ROW LEVEL SECURITY a non-superuser owner would otherwise see no row (or fail casting an empty
  -- passage.tenant_id left on a pooled connection).
  EXECUTE 'CREATE POLICY hub_client_usage_owner_scan ON hub_client_usage AS PERMISSIVE FOR SELECT TO CURRENT_USER USING (true)';
  EXECUTE 'CREATE POLICY hub_client_usage_owner_purge ON hub_client_usage AS PERMISSIVE FOR DELETE TO CURRENT_USER USING (true)';
  EXECUTE format($f$
    -- Subscriptions with a sealed value under a master key other than current_key_id (a rotated-out
    -- key), or a previous signing secret past its grace period. The key id is the envelope's own
    -- second byte (length) and the bytes after it (fieldcrypt envelope v1).
    CREATE OR REPLACE FUNCTION %1$I.hub_reseal_due(current_key_id text, now_ts timestamptz, max_rows integer)
    RETURNS TABLE (tenant_id uuid, subscription_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = %1$I, pg_temp AS $body$
      SELECT s.tenant_id, s.id
        FROM %1$I.hub_subscription s
       WHERE (s.secret_sealed IS NOT NULL
              AND convert_from(substring(s.secret_sealed FROM 3 FOR get_byte(s.secret_sealed, 1)), 'UTF8') <> current_key_id)
          OR (s.credentials_sealed IS NOT NULL
              AND convert_from(substring(s.credentials_sealed FROM 3 FOR get_byte(s.credentials_sealed, 1)), 'UTF8') <> current_key_id)
          OR (s.previous_secret_sealed IS NOT NULL
              AND (s.previous_secret_expires_at <= now_ts
                   OR convert_from(substring(s.previous_secret_sealed FROM 3 FOR get_byte(s.previous_secret_sealed, 1)), 'UTF8') <> current_key_id))
       ORDER BY s.tenant_id, s.id
       LIMIT greatest(max_rows, 1)
    $body$
    $f$, current_schema());
  EXECUTE format($f$
    -- Deletes up to max_rows (at most 10,000) usage rows older than the cutoff, across tenants; returns how many.
    -- The cutoff is never later than two days ago (the usage API reads the last 1,440 minutes), so a caller
    -- cannot use the function to empty the table.
    CREATE OR REPLACE FUNCTION %1$I.hub_purge_client_usage(cutoff timestamptz, max_rows integer)
    RETURNS bigint
    LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = %1$I, pg_temp AS $body$
      WITH doomed AS (
        SELECT ctid FROM %1$I.hub_client_usage
         WHERE minute < least(cutoff, now() - interval '2 days')
         LIMIT least(greatest(max_rows, 1), 10000)
      ), gone AS (
        DELETE FROM %1$I.hub_client_usage u USING doomed WHERE u.ctid = doomed.ctid RETURNING 1
      )
      SELECT count(*) FROM gone
    $body$
    $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION hub_reseal_due(text, timestamptz, integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION hub_purge_client_usage(timestamptz, integer) FROM PUBLIC;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION hub_reseal_due(text, timestamptz, integer) TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION hub_purge_client_usage(timestamptz, integer) TO passage_app';
  END IF;
END
$$;
