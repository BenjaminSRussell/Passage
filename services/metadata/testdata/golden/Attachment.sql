create table "attachments" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "owner_id" uuid not null,
  "file_name" varchar(255) not null,
  "size_bytes" bigint not null,
  "content_type" varchar(100) not null,
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id)
);
create index "ix_attachments_01" on "attachments" ("tenant_id", "owner_id");
alter table attachments enable row level security;
alter table attachments force row level security;
create policy attachments_tenant_access on attachments as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
