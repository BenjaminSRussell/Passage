-- Warehouse wave 31, slice 4: dock doors and appointments (FR-31.8) and ASN
-- receipts with their pallets and variance discrepancies (FR-31.9, BR-31.10).
-- Every table carries tenant_id first and forced row-level security
-- (dbx.RLSPolicy). Quantities are numeric(20,6), weights and volumes
-- numeric(18,6), never float.

create table dock_door (
  tenant_id uuid not null,
  id uuid not null,
  warehouse_id uuid not null,
  code text not null check (char_length(code) between 1 and 20),
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, warehouse_id, code)
);

-- Appointments start and end on the 15-minute grid and never overlap on one
-- door (AC-31.8). The store locks the door row before it checks for overlap,
-- so two concurrent bookings of one door are serialized.
create table dock_appointment (
  tenant_id uuid not null,
  id uuid not null,
  warehouse_id uuid not null,
  door_id uuid not null,
  direction text not null check (direction in ('INBOUND', 'OUTBOUND')),
  reference text not null check (char_length(reference) between 1 and 100),
  start_at timestamptz not null,
  end_at timestamptz not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  check (end_at > start_at and end_at - start_at <= interval '24 hours'),
  check (extract(epoch from start_at)::bigint % 900 = 0 and extract(epoch from end_at)::bigint % 900 = 0),
  foreign key (tenant_id, door_id) references dock_door (tenant_id, id)
);
create index ix_dock_appointment_door on dock_appointment (tenant_id, door_id, start_at);
create index ix_dock_appointment_warehouse on dock_appointment (tenant_id, warehouse_id, start_at);

-- One receipt per ASN and warehouse; its status follows its pallets.
create table receipt (
  tenant_id uuid not null,
  id uuid not null,
  warehouse_id uuid not null,
  asn_ref text not null check (char_length(asn_ref) between 1 and 100),
  tolerance_pct numeric(5,2) not null check (tolerance_pct between 0 and 20),
  status text not null check (status in ('OPEN', 'PARTIALLY_RECEIVED', 'RECEIVED')),
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, warehouse_id, asn_ref)
);

-- One row per ASN pallet (license plate). A RECEIVED pallet holds what
-- arrived, its variance to the ASN (BR-31.10) and the put-away work it created.
create table receipt_line (
  tenant_id uuid not null,
  receipt_id uuid not null,
  line_no int not null check (line_no > 0),
  item_id text not null check (char_length(item_id) between 1 and 60),
  lp_id text not null check (char_length(lp_id) between 1 and 40),
  po_line_ref text not null default '',
  expected_qty numeric(20,6) not null check (expected_qty > 0),
  weight numeric(18,6) not null default 0 check (weight >= 0),
  volume numeric(18,6) not null default 0 check (volume >= 0),
  status text not null check (status in ('EXPECTED', 'RECEIVED')),
  received_qty numeric(20,6) not null default 0 check (received_qty >= 0),
  variance_qty numeric(20,6) not null default 0,
  variance_pct numeric not null default 0,
  discrepancy boolean not null default false,
  reason text not null default '',
  work_id uuid,
  received_by text not null default '',
  received_at timestamptz,
  primary key (tenant_id, receipt_id, line_no),
  unique (tenant_id, receipt_id, lp_id),
  check ((status = 'RECEIVED') = (received_at is not null)),
  foreign key (tenant_id, receipt_id) references receipt (tenant_id, id),
  foreign key (tenant_id, work_id) references work (tenant_id, id)
);

alter table dock_door enable row level security;
alter table dock_door force row level security;
create policy dock_door_tenant_access on dock_door as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table dock_appointment enable row level security;
alter table dock_appointment force row level security;
create policy dock_appointment_tenant_access on dock_appointment as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table receipt enable row level security;
alter table receipt force row level security;
create policy receipt_tenant_access on receipt as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table receipt_line enable row level security;
alter table receipt_line force row level security;
create policy receipt_line_tenant_access on receipt_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
