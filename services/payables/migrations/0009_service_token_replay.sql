create table service_token_replay (
  tenant_id uuid not null,
  jti text not null,
  expires_at timestamptz not null,
  primary key (tenant_id, jti)
);
create index service_token_replay_expires_idx on service_token_replay (tenant_id, expires_at);
alter table service_token_replay enable row level security;
alter table service_token_replay force row level security;
create policy service_token_replay_tenant_access on service_token_replay as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, DELETE ON service_token_replay TO passage_app';
  END IF;
END
$$;
