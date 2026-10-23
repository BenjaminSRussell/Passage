create table "legal_entities" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "code" varchar(16) not null,
  "name" varchar(120) not null,
  "base_currency" varchar(3) not null,
  "country" varchar(2) not null,
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id),
  constraint "uq_legal_entities_code" unique ("tenant_id", "code")
);
alter table legal_entities enable row level security;
alter table legal_entities force row level security;
create policy legal_entities_tenant_access on legal_entities as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
