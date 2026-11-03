-- Migration factory, wave 49 slice 10 (FR-49.1, FR-49.2): the bank_balances pack. Each loaded
-- legacy bank account balance becomes one opening balance per bank account with its legacy_xref
-- row: the book balance at the cutoff in the account's currency, signed (an overdrawn account is
-- negative). They are the opening balances the treasury service takes once it has a bulk
-- opening-balance import API. The table carries tenant_id first and is held to the tenant by
-- forced row-level security.
--
-- The foreign keys take SHARE ROW EXCLUSIVE on the live pack_run and legacy_xref tables while the
-- table is created: the lock_timeout makes the migration fail (and the rollout retry) rather than
-- block every pack run behind a long transaction.
SET LOCAL lock_timeout = '5s';

-- Opening balances are write-once, like the cross-reference. One balance per bank account and
-- company: a second extract that names the same bank account under another legacy id would open
-- the account twice (FR-49.10), so the key is unique.
create table bank_opening_balance (
  tenant_id uuid not null,
  balance_id uuid not null,
  run_id uuid not null,
  legacy_xref_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  legacy_id text not null check (legacy_id <> ''),
  bank_account text not null check (bank_account <> ''),
  currency char(3) not null,
  balance numeric(28,6) not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, balance_id),
  unique (tenant_id, legacy_xref_id),
  unique (tenant_id, source_system, source_company, bank_account),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id),
  foreign key (tenant_id, legacy_xref_id) references legacy_xref (tenant_id, xref_id)
);
create index bank_opening_balance_legacy on bank_opening_balance (tenant_id, source_system, source_company, legacy_id);

alter table bank_opening_balance enable row level security;
alter table bank_opening_balance force row level security;
create policy bank_opening_balance_tenant_access on bank_opening_balance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. Opening balances are insert-only for passage_app: nothing updates
-- or deletes them. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON bank_opening_balance TO passage_app';
  END IF;
END
$$;
