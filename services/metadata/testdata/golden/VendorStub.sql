create table "vendor_stubs" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "legal_entity_id" uuid not null,
  "account" varchar(20) not null,
  "name" varchar(120) not null,
  "status" text not null default 'active',
  "credit_limit" numeric(20,6),
  "credit_limit_currency" char(3),
  "tax_id" varchar(32),
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id),
  constraint "uq_vendor_stubs_account" unique ("tenant_id", "account"),
  constraint "ck_vendor_stubs_status" check ("status" in ('active', 'blocked', 'preferred'))
);
create index "ix_vendor_stubs_01" on "vendor_stubs" ("tenant_id", "name");
alter table vendor_stubs enable row level security;
alter table vendor_stubs force row level security;
create policy vendor_stubs_tenant_access on vendor_stubs as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy vendor_stubs_legal_entity_scope on vendor_stubs as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
