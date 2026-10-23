create table "product_stubs" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "sku" varchar(40) not null,
  "description" text,
  "uom" varchar(8) not null,
  "active" boolean not null default true,
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id),
  constraint "uq_product_stubs_sku" unique ("tenant_id", "sku")
);
alter table product_stubs enable row level security;
alter table product_stubs force row level security;
create policy product_stubs_tenant_access on product_stubs as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
