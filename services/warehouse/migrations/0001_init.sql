-- Warehouse wave 31, slice 3: persisted locations with their stock, location
-- directives, work and work lines, the transactional outbox and idempotency
-- keys. Every table carries tenant_id first and forced row-level security
-- (dbx.RLSPolicy). Quantities are numeric(20,6); weights (kg) and volumes (m3)
-- numeric(18,6); never float.

create table location (
  tenant_id uuid not null,
  id uuid not null,
  warehouse_id uuid not null,
  name text not null check (char_length(name) between 1 and 60),
  zone text not null check (char_length(zone) between 1 and 40),
  pick_sequence int not null default 0 check (pick_sequence >= 0),
  status text not null check (status in ('ACTIVE', 'BLOCKED_IN', 'BLOCKED_OUT', 'BLOCKED', 'INACTIVE')),
  max_weight numeric(18,6) check (max_weight >= 0),
  max_volume numeric(18,6) check (max_volume >= 0),
  max_lps int check (max_lps >= 0),
  max_items int check (max_items >= 0),
  allow_mixed_items boolean not null default false,
  used_weight numeric(18,6) not null default 0 check (used_weight >= 0),
  used_volume numeric(18,6) not null default 0 check (used_volume >= 0),
  used_lps int not null default 0 check (used_lps >= 0),
  fire_section text not null default '',
  rack_group text not null default '',
  allowed_storage_classes text[] not null default '{}',
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, warehouse_id, name)
);
create index ix_location_zone_pick on location (tenant_id, warehouse_id, zone, pick_sequence);

-- On-hand quantity by location and item: the stand-in for quants (no LP,
-- dims or quality status yet). A row exists only while qty > 0.
create table location_stock (
  tenant_id uuid not null,
  location_id uuid not null,
  item_id text not null,
  qty numeric(20,6) not null check (qty > 0),
  primary key (tenant_id, location_id, item_id),
  foreign key (tenant_id, location_id) references location (tenant_id, id)
);

create table location_directive (
  tenant_id uuid not null,
  id uuid not null,
  warehouse_id uuid not null,
  sequence int not null check (sequence > 0),
  work_type text not null,
  direction text not null check (direction in ('PUT', 'PICK')),
  query jsonb not null default '{}',
  lines jsonb not null,
  version int not null default 1,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, warehouse_id, work_type, direction, sequence)
);

create table work (
  tenant_id uuid not null,
  id uuid not null,
  warehouse_id uuid not null,
  work_type text not null check (work_type in ('PURCHASE_PUTAWAY', 'SALES_PICK', 'TRANSFER_ISSUE', 'TRANSFER_RECEIPT',
    'PRODUCTION_PICK', 'RAF_PUTAWAY', 'REPLENISHMENT', 'MOVEMENT', 'COUNT', 'RETURN')),
  status text not null check (status in ('OPEN', 'ASSIGNED', 'IN_PROGRESS', 'COMPLETED', 'PARTIALLY_COMPLETED', 'CANCELLED', 'BLOCKED_NO_LOCATION')),
  priority smallint not null check (priority between 1 and 99),
  parent_work_id uuid,
  assigned_user text not null default '',
  assigned_at timestamptz,
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  foreign key (tenant_id, parent_work_id) references work (tenant_id, id)
);
-- Assignment scans OPEN work by priority (FR-31.7, BR-31.8 partial index) and
-- reclaims ASSIGNED work idle past the lock timeout (BR-31.3).
create index ix_work_open on work (tenant_id, warehouse_id, priority, created_at) where status = 'OPEN';
create index ix_work_assigned on work (tenant_id, warehouse_id, assigned_at) where status = 'ASSIGNED';
create index ix_work_list on work (tenant_id, warehouse_id, priority, created_at, id);

create table work_line (
  tenant_id uuid not null,
  work_id uuid not null,
  line_no int not null check (line_no > 0),
  item_id text not null,
  direction text not null check (direction in ('PUT', 'PICK')),
  qty numeric(20,6) not null check (qty > 0),
  confirmed_qty numeric(20,6) not null default 0 check (confirmed_qty >= 0 and confirmed_qty <= qty),
  weight numeric(18,6) not null default 0 check (weight >= 0),
  volume numeric(18,6) not null default 0 check (volume >= 0),
  lps int not null default 0 check (lps >= 0),
  from_location_id uuid,
  to_location_id uuid,
  status text not null check (status in ('OPEN', 'COMPLETED', 'SHORT', 'CANCELLED')),
  reason text not null default '',
  confirm_key text,
  attributes jsonb not null default '{}',
  primary key (tenant_id, work_id, line_no),
  foreign key (tenant_id, work_id) references work (tenant_id, id),
  foreign key (tenant_id, from_location_id) references location (tenant_id, id),
  foreign key (tenant_id, to_location_id) references location (tenant_id, id)
);
-- Open lines reserve capacity and stock while work is planned (BR-31.2).
create index ix_work_line_open_to on work_line (tenant_id, to_location_id) where status = 'OPEN' and to_location_id is not null;
create index ix_work_line_open_from on work_line (tenant_id, from_location_id) where status = 'OPEN' and from_location_id is not null;

-- Transactional outbox (dbx.OutboxDDL), relayed by Debezium.
create table if not exists outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);

-- Idempotency-Key replay store (dbx.IdempotencyDDL); keys expire after 24 h.
create table if not exists idempotency_keys (
  tenant_id uuid not null,
  key text not null,
  request_hash text not null,
  response_status int not null,
  response_body jsonb,
  created_at timestamptz not null default now(),
  primary key (tenant_id, key)
);

alter table location enable row level security;
alter table location force row level security;
create policy location_tenant_access on location as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table location_stock enable row level security;
alter table location_stock force row level security;
create policy location_stock_tenant_access on location_stock as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table location_directive enable row level security;
alter table location_directive force row level security;
create policy location_directive_tenant_access on location_directive as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table work enable row level security;
alter table work force row level security;
create policy work_tenant_access on work as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table work_line enable row level security;
alter table work_line force row level security;
create policy work_line_tenant_access on work_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table outbox enable row level security;
alter table outbox force row level security;
create policy outbox_tenant_access on outbox as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table idempotency_keys enable row level security;
alter table idempotency_keys force row level security;
create policy idempotency_keys_tenant_access on idempotency_keys as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
