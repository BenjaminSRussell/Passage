-- Onboarding progress and per-tenant shell settings. A setup step with no row is not started; only "done" and
-- "skipped" are stored. The workspace override is the JSON the web shell merges over its shipped workspaces
-- (areas, workspaces and hide); the service stores it as the tenant admin wrote it and the shell validates it.
create table identity_tenant_setup_steps (
  tenant_id uuid not null references identity_tenants (tenant_id),
  step_key text not null check (step_key ~ '^[a-z][a-z0-9-]{0,62}$'),
  status text not null check (status in ('done', 'skipped')),
  updated_by text not null check (updated_by <> ''),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, step_key)
);

create table identity_tenant_settings (
  tenant_id uuid primary key references identity_tenants (tenant_id),
  workspace_config jsonb check (workspace_config is null or jsonb_typeof(workspace_config) = 'object'),
  updated_by text not null check (updated_by <> ''),
  updated_at timestamptz not null default now()
);

alter table identity_tenant_setup_steps enable row level security;
alter table identity_tenant_setup_steps force row level security;
create policy identity_tenant_setup_steps_tenant_access on identity_tenant_setup_steps as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

alter table identity_tenant_settings enable row level security;
alter table identity_tenant_settings force row level security;
create policy identity_tenant_settings_tenant_access on identity_tenant_settings as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON identity_tenant_setup_steps TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON identity_tenant_settings TO passage_app';
  END IF;
END
$$;
