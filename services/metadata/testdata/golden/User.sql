create table "users" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "subject" varchar(128) not null,
  "display_name" varchar(120) not null,
  "email" varchar(254),
  "principal_type" text not null default 'human',
  "active" boolean not null default true,
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id),
  constraint "uq_users_subject" unique ("tenant_id", "subject"),
  constraint "ck_users_principal_type" check ("principal_type" in ('human', 'service', 'agent'))
);
create index "ix_users_01" on "users" ("tenant_id", "email") where email is not null;
alter table users enable row level security;
alter table users force row level security;
create policy users_tenant_access on users as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
