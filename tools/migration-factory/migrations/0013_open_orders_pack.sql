-- Migration factory, wave 49 slice 11 (FR-49.1, FR-49.2): the open_orders pack. Each loaded legacy
-- open order line (a sales order line not fully shipped, a purchase order line not fully received)
-- becomes one opening line with its legacy_xref row: the ordered and delivered quantity and the
-- unit price at the cutoff. They are the lines the sales and purchasing services take once they
-- have a bulk opening-order import API. The table carries tenant_id first and is held to the tenant
-- by forced row-level security.
--
-- The foreign keys take SHARE ROW EXCLUSIVE on the live pack_run and legacy_xref tables while the
-- table is created: the lock_timeout makes the migration fail (and the rollout retry) rather than
-- block every pack run behind a long transaction.
SET LOCAL lock_timeout = '5s';

-- Opening lines are write-once, like the cross-reference. One line per order type, order number and
-- line number and company: a second extract that names the same order line under another legacy id
-- would open the order twice (FR-49.10), so the key is unique.
create table open_order_opening_line (
  tenant_id uuid not null,
  line_id uuid not null,
  run_id uuid not null,
  legacy_xref_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  legacy_id text not null check (legacy_id <> ''),
  order_type text not null check (order_type in ('SALES', 'PURCHASE')),
  order_number text not null check (order_number <> ''),
  line_number text not null check (line_number <> ''),
  item text not null check (item <> ''),
  party text not null check (party <> ''),
  unit text not null check (unit <> ''),
  currency char(3) not null,
  ordered_qty numeric(28,6) not null check (ordered_qty > 0),
  delivered_qty numeric(28,6) not null check (delivered_qty >= 0 and delivered_qty < ordered_qty),
  unit_price numeric(28,6) not null check (unit_price >= 0),
  delivery_date date,
  created_at timestamptz not null default now(),
  primary key (tenant_id, line_id),
  unique (tenant_id, legacy_xref_id),
  unique (tenant_id, source_system, source_company, order_type, order_number, line_number),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id),
  foreign key (tenant_id, legacy_xref_id) references legacy_xref (tenant_id, xref_id)
);
-- A legacy id is unique per legacy table only (AX RecId of SalesLine or PurchLine, SAP VBAP or EKPO
-- key), so it is unique per order type: its legacy_xref row is under open_orders.sales or
-- open_orders.purchase.
create unique index open_order_opening_line_legacy on open_order_opening_line (tenant_id, source_system, source_company, order_type, legacy_id);

alter table open_order_opening_line enable row level security;
alter table open_order_opening_line force row level security;
create policy open_order_opening_line_tenant_access on open_order_opening_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. Opening lines are insert-only for passage_app: nothing updates or
-- deletes them. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON open_order_opening_line TO passage_app';
  END IF;
END
$$;
