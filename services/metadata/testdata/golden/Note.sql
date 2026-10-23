create table "notes" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "body" text not null,
  "author_id" uuid not null,
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  deleted_at timestamptz,
  primary key (tenant_id, id),
  foreign key ("tenant_id", "author_id") references "users" ("tenant_id", "id") on delete restrict
);
alter table notes enable row level security;
alter table notes force row level security;
create policy notes_tenant_access on notes as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
