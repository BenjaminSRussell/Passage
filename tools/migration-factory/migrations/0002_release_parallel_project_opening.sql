-- Migration factory, wave 49 slice 2 (PRD 9.49.4): releasing quarantined records, the
-- parallel-run review (parallel_run, variance) and project beginning balances
-- (project_opening_line). Every new table carries tenant_id first and is held to the tenant by
-- forced row-level security.

-- A pack run changes after its first write when a released record completes its load, so it
-- carries an optimistic version. parties is the master-data snapshot the run validated against;
-- a release checks a corrected party against it.
alter table pack_run add column version int not null default 1 check (version >= 1);
alter table pack_run add column parties text[] not null default '{}';

-- A quarantined record is OPEN until an operator corrects and releases it (FR-49.11). The
-- record column keeps the record as extracted; released_record holds the corrected record.
alter table pack_quarantine add column status text not null default 'OPEN' check (status in ('OPEN', 'RELEASED'));
alter table pack_quarantine add column released_record jsonb;
alter table pack_quarantine add column released_by text not null default '';
alter table pack_quarantine add column released_at timestamptz;
alter table pack_quarantine add constraint pack_quarantine_released
  check (status = 'OPEN' or (released_record is not null and released_at is not null));

-- The legacy-id lookup (9.49.5 GET /migration/xref) may leave the company out, which the
-- natural-key primary key cannot serve past source_system; this index serves it (9.49.19).
create index legacy_xref_lookup on legacy_xref (tenant_id, source_system, source_table, source_id);

-- One replayed legacy period compared with Passage (FR-49.5). Starting is idempotent by
-- (period, source_system). A run is SIGNED_OFF only with no unexplained variance (9.49.4).
create table parallel_run (
  tenant_id uuid not null,
  parallel_run_id uuid not null,
  period text not null check (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  documents_replayed int not null check (documents_replayed >= 0),
  revenue numeric(28,6) not null check (revenue >= 0),
  explained_max_pct numeric(9,6) not null check (explained_max_pct between 0 and 1),
  input_hash text not null,
  state text not null check (state in ('REPLAYING', 'DIFFING', 'REVIEW', 'SIGNED_OFF')),
  unexplained_count int not null check (unexplained_count >= 0),
  unexplained_amount numeric(28,6) not null,
  explained_amount numeric(28,6) not null,
  started_by text not null default '',
  signed_off_by text not null default '',
  signed_off_at timestamptz,
  version int not null default 1 check (version >= 1),
  created_at timestamptz not null default now(),
  primary key (tenant_id, parallel_run_id),
  unique (tenant_id, period, source_system),
  check (state <> 'SIGNED_OFF' or (unexplained_count = 0 and signed_off_by <> '' and signed_off_at is not null))
);
create index parallel_run_created on parallel_run (tenant_id, created_at desc, parallel_run_id);

create table variance (
  tenant_id uuid not null,
  variance_id uuid not null,
  parallel_run_id uuid not null,
  seq int not null check (seq >= 1),
  area text not null check (area in ('TB', 'INV', 'AP', 'AR', 'FA')),
  key_text text not null check (key_text <> ''),
  legacy_value numeric(28,6) not null,
  passage_value numeric(28,6) not null,
  delta numeric(28,6) not null,
  class text not null check (class in ('EXPECTED', 'EXPLAINED', 'UNEXPLAINED')),
  rule_id text not null default '',
  reason text not null default '',
  explained_by text not null default '',
  explained_at timestamptz,
  primary key (tenant_id, variance_id),
  unique (tenant_id, parallel_run_id, seq),
  foreign key (tenant_id, parallel_run_id) references parallel_run (tenant_id, parallel_run_id),
  check (class <> 'EXPLAINED' or reason <> ''),
  check (class <> 'EXPECTED' or rule_id <> '')
);
create index variance_run_class on variance (tenant_id, parallel_run_id, class);

-- Project beginning balances (FR-49.12). Lines are write-once, never post to the GL
-- (BR-49.11) and each one has its legacy_xref row.
create table project_opening_line (
  tenant_id uuid not null,
  line_id uuid not null,
  run_id uuid not null,
  legacy_xref_id uuid not null,
  source_system text not null check (source_system in ('AX2012', 'D365FO', 'SAPECC', 'S4HANA')),
  source_company text not null,
  legacy_id text not null check (legacy_id <> ''),
  project_id text not null check (project_id <> ''),
  category text not null check (category in ('HOUR', 'EXPENSE', 'ITEM', 'FEE', 'ON_ACCOUNT')),
  transaction_date date not null,
  qty numeric(28,6) not null,
  cost_amount numeric(28,6) not null,
  sales_amount numeric(28,6) not null,
  currency char(3) not null,
  worker_id text not null default '',
  activity_id text not null default '',
  dimensions_json jsonb not null default '{}',
  billing_status text not null check (billing_status in ('CHARGEABLE_UNBILLED', 'BILLED', 'NON_CHARGEABLE')),
  wip_cost numeric(28,6) not null,
  wip_sales numeric(28,6) not null,
  beginning_balance boolean not null default true check (beginning_balance),
  created_at timestamptz not null default now(),
  primary key (tenant_id, line_id),
  unique (tenant_id, legacy_xref_id),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id),
  foreign key (tenant_id, legacy_xref_id) references legacy_xref (tenant_id, xref_id)
);
create index project_opening_line_project on project_opening_line (tenant_id, source_system, source_company, project_id, legacy_id);

alter table parallel_run enable row level security;
alter table parallel_run force row level security;
create policy parallel_run_tenant_access on parallel_run as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table variance enable row level security;
alter table variance force row level security;
create policy variance_tenant_access on variance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table project_opening_line enable row level security;
alter table project_opening_line force row level security;
create policy project_opening_line_tenant_access on project_opening_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. A release updates the run (counts, status, version), its
-- control totals and the quarantine row under a row lock; parallel runs and variances change
-- as the review explains and signs off; project opening lines are insert-only. legacy_xref
-- stays append-only and nothing is deleted. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT UPDATE ON pack_run, control_total, pack_quarantine TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON parallel_run, variance TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON project_opening_line TO passage_app';
  END IF;
END
$$;
