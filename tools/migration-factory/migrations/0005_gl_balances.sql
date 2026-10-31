-- Migration factory, wave 49 slice 5 (FR-49.1, FR-49.2, BR-49.2): the gl_balances pack. Each
-- loaded legacy GL balance becomes one opening balance per (account, dimension combination,
-- currency) with its legacy_xref row; together they are the opening journal the general ledger
-- posts once its bulk opening-journal API exists. The table carries tenant_id first and is held
-- to the tenant by forced row-level security.

-- Opening balances are write-once, like the cross-reference. One balance per natural key and
-- company: a second extract that names the same account, dimensions and currency under another
-- legacy id would load the balance twice (FR-49.10), so the key is unique.
create table gl_opening_balance (
  tenant_id uuid not null,
  balance_id uuid not null,
  run_id uuid not null,
  legacy_xref_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  legacy_id text not null check (legacy_id <> ''),
  account text not null check (account <> ''),
  dimensions text not null default '',
  currency char(3) not null,
  balance numeric(28,6) not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, balance_id),
  unique (tenant_id, legacy_xref_id),
  unique (tenant_id, source_system, source_company, account, dimensions, currency),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id),
  foreign key (tenant_id, legacy_xref_id) references legacy_xref (tenant_id, xref_id)
);
create index gl_opening_balance_legacy on gl_opening_balance (tenant_id, source_system, source_company, legacy_id);

-- A gl_balances run may be reconciled with an open_items run of the same company (BR-49.2):
-- the open items per control account must equal the migrated GL balance of that account.
alter table pack_run add column open_items_run_id uuid;
alter table pack_run add constraint pack_run_open_items_run
  foreign key (tenant_id, open_items_run_id) references pack_run (tenant_id, run_id);
alter table pack_run add constraint pack_run_open_items_run_gl_only
  check (open_items_run_id is null or pack = 'gl_balances');

alter table gl_opening_balance enable row level security;
alter table gl_opening_balance force row level security;
create policy gl_opening_balance_tenant_access on gl_opening_balance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. Opening balances are insert-only for passage_app: nothing updates
-- or deletes them. The new pack_run column is covered by the pack_run grants of 0001 and 0002.
-- Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON gl_opening_balance TO passage_app';
  END IF;
END
$$;
