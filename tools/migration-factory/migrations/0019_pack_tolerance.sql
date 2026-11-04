-- Migration factory, wave 49 slice 15 (FR-49.2, decision D2): configured control-total tolerances,
-- `migration.tolerance.<pack>`. One row per (pack, total name), or per pack with the total name '*'
-- for every total of the pack that has no row of its own. A positive amount carries the reason it
-- was allowed; without a row every total compares at 0.00. The row is versioned (optimistic
-- concurrency) and each change is published as dev.passage.migration.tolerance.configured.v1. The
-- table carries tenant_id first and is held to the tenant by forced row-level security.

create table pack_tolerance (
  tenant_id uuid not null,
  pack text not null check (pack in ('open_items', 'gl_balances', 'onhand', 'open_orders', 'wip_prod', 'fixed_assets', 'project_wip', 'payroll_ytd', 'bank_balances')),
  total_name text not null check (total_name = '*' or total_name like pack || '.%'),
  amount numeric(28,6) not null check (amount >= 0),
  reason text not null default '',
  version int not null default 1 check (version >= 1),
  updated_by text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, pack, total_name),
  check (amount = 0 or reason <> '')
);

alter table pack_tolerance enable row level security;
alter table pack_tolerance force row level security;
create policy pack_tolerance_tenant_access on pack_tolerance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. A change updates the row in place, so UPDATE is granted; nothing
-- deletes a tolerance (set it back to 0). Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON pack_tolerance TO passage_app';
  END IF;
END
$$;
