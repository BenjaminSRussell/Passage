-- One row per feature a tenant has switched on or off explicitly. A feature with no row is on, so a tenant that never
-- touches the settings keeps every module. updated_by and updated_at say who last changed the feature and when;
-- the full history is in identity_audit (tenantfeatures.updated).
create table identity_tenant_features (
  tenant_id uuid not null references identity_tenants (tenant_id),
  feature_key text not null check (feature_key ~ '^[a-z][a-z0-9-]{0,62}$'),
  enabled boolean not null,
  updated_by text not null check (updated_by <> ''),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, feature_key)
);

alter table identity_tenant_features enable row level security;
alter table identity_tenant_features force row level security;
create policy identity_tenant_features_tenant_access on identity_tenant_features as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON identity_tenant_features TO passage_app';
  END IF;
END
$$;
