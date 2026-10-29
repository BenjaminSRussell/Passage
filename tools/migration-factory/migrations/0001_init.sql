-- Migration factory, wave 49 slice 1 (PRD 9.49.4): pack runs with control totals and
-- quarantine, the legacy cross-reference, and the cutover plan with its steps. Every table
-- carries tenant_id first and is held to the tenant by forced row-level security.

create table pack_run (
  tenant_id uuid not null,
  run_id uuid not null,
  pack text not null check (pack in ('open_items', 'gl_balances', 'onhand', 'open_orders', 'wip_prod', 'fixed_assets', 'project_wip', 'payroll_ytd', 'bank_balances')),
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null check (source_company <> ''),
  cutoff_ts timestamptz not null,
  input_hash text not null,
  status text not null check (status in ('EXTRACTED', 'VALIDATED', 'LOADED', 'RECONCILED', 'FAILED', 'ROLLED_BACK')),
  records_in int not null check (records_in >= 0),
  records_loaded int not null check (records_loaded >= 0),
  records_quarantined int not null check (records_quarantined >= 0),
  already_present int not null default 0 check (already_present >= 0),
  started_by text not null default '',
  created_at timestamptz not null default now(),
  primary key (tenant_id, run_id)
);
create index pack_run_created on pack_run (tenant_id, created_at desc, run_id);
create index pack_run_input_hash on pack_run (tenant_id, input_hash);

create table control_total (
  tenant_id uuid not null,
  total_id uuid not null,
  run_id uuid not null,
  seq int not null,
  name text not null,
  key_json jsonb not null,
  source_value numeric(28,6) not null,
  target_value numeric(28,6) not null,
  delta numeric(28,6) not null,
  tolerance numeric(28,6) not null default 0 check (tolerance >= 0),
  tolerance_reason text not null default '',
  status text not null check (status in ('MATCH', 'WITHIN_TOLERANCE', 'MISMATCH')),
  computed_at timestamptz not null default now(),
  primary key (tenant_id, total_id),
  unique (tenant_id, run_id, seq),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id),
  check (tolerance = 0 or tolerance_reason <> '')
);
create index control_total_run_status on control_total (tenant_id, run_id, status);

create table pack_quarantine (
  tenant_id uuid not null,
  run_id uuid not null,
  seq int not null,
  legacy_id text not null,
  rule text not null check (rule in ('REFERENTIAL', 'DOMAIN', 'UNIQUENESS')),
  reason text not null check (reason <> ''),
  record jsonb not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, run_id, seq),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);

-- legacy_xref is append-only (9.49.4 invariant): the application role may insert and read it
-- but not change or delete a row. The natural key makes pack re-runs idempotent (FR-49.10).
create table legacy_xref (
  tenant_id uuid not null,
  xref_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null,
  source_table text not null,
  source_id text not null check (source_id <> ''),
  passage_entity text not null,
  passage_id uuid not null,
  run_id uuid not null,
  amount numeric(28,6) not null,
  currency char(3) not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, source_system, source_company, source_table, source_id),
  unique (tenant_id, xref_id),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);
create index legacy_xref_passage on legacy_xref (tenant_id, passage_entity, passage_id);

create table cutover_plan (
  tenant_id uuid not null,
  plan_id uuid not null,
  t0 timestamptz not null,
  packs text[] not null,
  entities text[] not null,
  abort_offset_min int not null check (abort_offset_min between 720 and 4320),
  rolled_back boolean not null default false,
  rollback_approvers text[] not null default '{}',
  rolled_back_by text not null default '',
  rolled_back_at timestamptz,
  version int not null default 1 check (version >= 1),
  created_by text not null default '',
  created_at timestamptz not null default now(),
  primary key (tenant_id, plan_id),
  check (not rolled_back or cardinality(rollback_approvers) >= 2)
);
create index cutover_plan_created on cutover_plan (tenant_id, created_at desc, plan_id);

create table cutover_step (
  tenant_id uuid not null,
  plan_id uuid not null,
  seq int not null check (seq >= 1),
  offset_minutes int not null,
  name text not null,
  owner text not null,
  planned_duration_min int not null check (planned_duration_min >= 0),
  actual_duration_min int not null default 0 check (actual_duration_min >= 0),
  status text not null check (status in ('PENDING', 'DONE')),
  go_nogo boolean not null default false,
  pack text not null default '',
  completed_by text not null default '',
  completed_at timestamptz,
  primary key (tenant_id, plan_id, seq),
  unique (tenant_id, plan_id, name),
  foreign key (tenant_id, plan_id) references cutover_plan (tenant_id, plan_id)
);

create table if not exists outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now()
);

create table if not exists idempotency_keys (
  tenant_id uuid not null,
  key text not null,
  request_hash text not null,
  response_status int not null,
  response_body jsonb,
  created_at timestamptz not null default now(),
  primary key (tenant_id, key)
);

alter table pack_run enable row level security;
alter table pack_run force row level security;
create policy pack_run_tenant_access on pack_run as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table control_total enable row level security;
alter table control_total force row level security;
create policy control_total_tenant_access on control_total as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table pack_quarantine enable row level security;
alter table pack_quarantine force row level security;
create policy pack_quarantine_tenant_access on pack_quarantine as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table legacy_xref enable row level security;
alter table legacy_xref force row level security;
create policy legacy_xref_tenant_access on legacy_xref as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table cutover_plan enable row level security;
alter table cutover_plan force row level security;
create policy cutover_plan_tenant_access on cutover_plan as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table cutover_step enable row level security;
alter table cutover_step force row level security;
create policy cutover_step_tenant_access on cutover_step as permissive
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

-- Application role privileges. Pack runs, control totals, quarantine rows, the
-- cross-reference and the outbox are insert-only for passage_app; cutover plans and steps
-- change as the runbook advances. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON pack_run, control_total, pack_quarantine, legacy_xref, outbox TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON cutover_plan, cutover_step TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys TO passage_app';
  END IF;
END
$$;
