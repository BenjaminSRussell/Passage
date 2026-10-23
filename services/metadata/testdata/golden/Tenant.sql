create table "tenants" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "slug" varchar(63) not null,
  "region" varchar(32) not null,
  "tier" text not null default 'starter',
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id),
  constraint "uq_tenants_slug" unique ("tenant_id", "slug"),
  constraint "ck_tenants_tier" check ("tier" in ('starter', 'professional', 'enterprise'))
);
alter table tenants enable row level security;
alter table tenants force row level security;
create policy tenants_tenant_access on tenants as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
