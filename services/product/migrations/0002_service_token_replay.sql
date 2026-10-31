create table service_token_replay (
  tenant_id uuid not null,
  jti text not null,
  expires_at timestamptz not null,
  primary key (tenant_id, jti)
);
create index service_token_replay_expires_idx on service_token_replay (tenant_id, expires_at);
alter table service_token_replay enable row level security;
create policy service_token_replay_tenant_access on service_token_replay as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DO $do$
BEGIN
  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION service_token_replay_purge() RETURNS void
      LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, %1$I, pg_temp
      AS $body$ DELETE FROM service_token_replay WHERE expires_at < pg_catalog.now() $body$;
  $f$, current_schema());
END
$do$;
REVOKE ALL ON FUNCTION service_token_replay_purge() FROM PUBLIC;
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT INSERT ON service_token_replay TO passage_app';
    EXECUTE 'GRANT EXECUTE ON FUNCTION service_token_replay_purge() TO passage_app';
  END IF;
END
$$;
