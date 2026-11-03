-- Migration factory, wave 49 slice 9 (FR-49.1, FR-49.2, BR-49.3): the onhand pack. Each loaded
-- legacy on-hand line becomes one opening balance per (item, site, warehouse, location, batch,
-- serial) with its legacy_xref row, at legacy cost: the quantity and the total value. They are
-- the opening stock the inventory ledger takes once it has a bulk opening-balance API. The table
-- carries tenant_id first and is held to the tenant by forced row-level security.

-- Opening stock is write-once, like the cross-reference. One line per natural key and company: a
-- second extract that names the same item and dimension under another legacy id would open the
-- stock twice (FR-49.10), so the key is unique.
create table onhand_opening_balance (
  tenant_id uuid not null,
  balance_id uuid not null,
  run_id uuid not null,
  legacy_xref_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  legacy_id text not null check (legacy_id <> ''),
  item text not null check (item <> ''),
  site text not null check (site <> ''),
  warehouse text not null check (warehouse <> ''),
  location text not null default '',
  batch text not null default '',
  serial text not null default '',
  unit text not null check (unit <> ''),
  quantity numeric(28,6) not null check (quantity > 0),
  value numeric(28,6) not null check (value >= 0),
  currency char(3) not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, balance_id),
  unique (tenant_id, legacy_xref_id),
  unique (tenant_id, source_system, source_company, item, site, warehouse, location, batch, serial),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id),
  foreign key (tenant_id, legacy_xref_id) references legacy_xref (tenant_id, xref_id)
);
create index onhand_opening_balance_legacy on onhand_opening_balance (tenant_id, source_system, source_company, legacy_id);
-- A serial is one unit in one place: an item's serial opens once per company, whatever its
-- site, warehouse or location. Untracked stock (serial '') is not constrained here. The
-- dimension columns are not null (blank for none), so the keys above compare blanks as equal
-- and a missing location, batch or serial cannot slip a duplicate past them.
create unique index onhand_opening_balance_serial on onhand_opening_balance (tenant_id, source_system, source_company, item, serial)
  where serial <> '';

-- An onhand run may be reconciled with a gl_balances run of the same company (BR-49.3): the
-- migrated balance of the inventory account must equal the loaded on-hand value.
alter table pack_run add column gl_run_id uuid;
alter table pack_run add constraint pack_run_gl_run
  foreign key (tenant_id, gl_run_id) references pack_run (tenant_id, run_id);
alter table pack_run add constraint pack_run_gl_run_onhand_only
  check (gl_run_id is null or pack = 'onhand');

alter table onhand_opening_balance enable row level security;
alter table onhand_opening_balance force row level security;
create policy onhand_opening_balance_tenant_access on onhand_opening_balance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. Opening stock is insert-only for passage_app: nothing updates or
-- deletes it. The new pack_run column is covered by the pack_run grants of 0001 and 0002.
-- Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON onhand_opening_balance TO passage_app';
  END IF;
END
$$;
