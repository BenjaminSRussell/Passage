create table "roles" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "code" varchar(48) not null,
  "name" varchar(120) not null,
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id),
  constraint "uq_roles_code" unique ("tenant_id", "code")
);
alter table roles enable row level security;
alter table roles force row level security;
create policy roles_tenant_access on roles as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
