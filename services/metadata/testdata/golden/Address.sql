create table "addresses" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "line1" varchar(120) not null,
  "line2" varchar(120),
  "city" varchar(60),
  "postal_code" varchar(20),
  "country" varchar(2) not null,
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id)
);
alter table addresses enable row level security;
alter table addresses force row level security;
create policy addresses_tenant_access on addresses as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
