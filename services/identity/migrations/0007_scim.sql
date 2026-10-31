alter table identity_users
  add column user_name text check (user_name is null or user_name <> ''),
  add column external_id text check (external_id is null or external_id <> '');

create unique index identity_users_user_name_idx on identity_users (tenant_id, lower(user_name)) where user_name is not null;

create table identity_scim_groups (
  tenant_id uuid not null references identity_tenants (tenant_id),
  group_id uuid not null,
  display_name text not null check (display_name <> ''),
  external_id text check (external_id is null or external_id <> ''),
  created_at timestamptz not null default now(),
  primary key (tenant_id, group_id)
);
create unique index identity_scim_groups_name_idx on identity_scim_groups (tenant_id, lower(display_name));

create table identity_scim_group_members (
  tenant_id uuid not null,
  group_id uuid not null,
  user_id uuid not null,
  primary key (tenant_id, group_id, user_id),
  foreign key (tenant_id, group_id) references identity_scim_groups (tenant_id, group_id) on delete cascade,
  foreign key (tenant_id, user_id) references identity_users (tenant_id, user_id)
);
create index identity_scim_group_members_user_idx on identity_scim_group_members (tenant_id, user_id);

alter table identity_scim_groups enable row level security;
alter table identity_scim_groups force row level security;
create policy identity_scim_groups_tenant_access on identity_scim_groups as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

alter table identity_scim_group_members enable row level security;
alter table identity_scim_group_members force row level security;
create policy identity_scim_group_members_tenant_access on identity_scim_group_members as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON identity_scim_groups, identity_scim_group_members TO passage_app';
  END IF;
END
$$;
