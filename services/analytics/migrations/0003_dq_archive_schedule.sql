-- Slice 2: data-quality rules and scores (FR-46.16), archive partitions
-- (FR-46.18), report schedules (FR-46.10) and the Idempotency-Key store for the
-- first creating endpoint. None of these tables carries legal_entity_id: rules,
-- archive partitions and schedules are tenant-wide, so only the tenant policy
-- applies.

create table if not exists idempotency_keys (
  tenant_id uuid not null,
  key text not null,
  request_hash text not null,
  response_status int not null,
  response_body jsonb,
  created_at timestamptz not null default now(),
  primary key (tenant_id, key)
);
alter table idempotency_keys enable row level security;
alter table idempotency_keys force row level security;
create policy idempotency_keys_tenant_access on idempotency_keys as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- One rule on one catalog asset. version is the optimistic-concurrency version
-- the API exposes as the ETag.
create table if not exists dq_rule (
  tenant_id uuid not null,
  rule_id text not null check (rule_id ~ '^[a-z][a-z0-9_]{1,62}$'),
  asset_fqn text not null check (asset_fqn ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$'),
  rule_type text not null check (rule_type in ('COMPLETENESS', 'VALIDITY', 'UNIQUENESS', 'REFERENTIAL', 'FRESHNESS')),
  expression text not null check (length(expression) between 1 and 2048),
  threshold_pct numeric(5,2) not null default 98 check (threshold_pct between 0 and 100),
  severity text not null check (severity in ('LOW', 'MEDIUM', 'HIGH')),
  owner_user_id uuid not null,
  status text not null check (status in ('ACTIVE', 'RETIRED')),
  mute_until timestamptz,
  version integer not null default 1 check (version >= 1),
  updated_by text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, rule_id)
);
alter table dq_rule enable row level security;
alter table dq_rule force row level security;
create policy dq_rule_tenant_access on dq_rule as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create index if not exists dq_rule_asset on dq_rule (tenant_id, asset_fqn);

-- One evaluation of one rule for one day; a later evaluation the same day
-- replaces the row. below_streak counts consecutive below-threshold
-- evaluations (BR-46.15); the notified flags make the owner task once per day
-- and the steward escalation once per run.
create table if not exists dq_score (
  tenant_id uuid not null,
  rule_id text not null,
  score_date date not null,
  asset_fqn text not null,
  pass_pct numeric(5,2) not null check (pass_pct between 0 and 100),
  failed_rows bigint not null check (failed_rows >= 0),
  total_rows bigint not null check (total_rows >= 0 and failed_rows <= total_rows),
  below boolean not null,
  below_streak integer not null default 0 check (below_streak >= 0),
  -- owner_notified: the owner's task for this rule and day was requested.
  -- steward_notified: the steward was told during the below-threshold run
  -- this evaluation belongs to (carried forward while the run continues).
  owner_notified boolean not null default false,
  steward_notified boolean not null default false check (not steward_notified or below),
  evaluated_at timestamptz not null default now(),
  primary key (tenant_id, rule_id, score_date),
  foreign key (tenant_id, rule_id) references dq_rule (tenant_id, rule_id)
);
alter table dq_score enable row level security;
alter table dq_score force row level security;
create policy dq_score_tenant_access on dq_score as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create index if not exists dq_score_asset on dq_score (tenant_id, asset_fqn, score_date desc);

-- Archive tier partitions (FR-46.18, BR-46.14). One row per domain, table and month.
create table if not exists archive_partition (
  tenant_id uuid not null,
  partition_id uuid not null,
  domain text not null check (domain ~ '^[a-z][a-z0-9_]{1,62}$'),
  table_name text not null check (table_name ~ '^[a-z][a-z0-9_]{1,62}$'),
  period text not null check (period ~ '^[0-9]{4}-(0[1-9]|1[0-2])$'),
  rows_oltp bigint not null default 0 check (rows_oltp >= 0),
  rows_archive bigint not null default 0 check (rows_archive >= 0),
  content_hash_oltp text not null default '',
  content_hash_archive text not null default '',
  status text not null check (status in ('PENDING', 'VERIFIED', 'DROPPED', 'RESTORED')),
  legal_hold boolean not null default false,
  open_period boolean not null default false,
  legal_hold_checked_at timestamptz,
  moved_at timestamptz,
  restore_until timestamptz,
  restored_by text not null default '',
  check (status = 'PENDING' or (moved_at is not null and rows_oltp = rows_archive
         and content_hash_oltp <> '' and content_hash_oltp = content_hash_archive)),
  check ((status = 'RESTORED') = (restore_until is not null)),
  primary key (tenant_id, partition_id),
  unique (tenant_id, domain, table_name, period)
);
alter table archive_partition enable row level security;
alter table archive_partition force row level security;
create policy archive_partition_tenant_access on archive_partition as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create index if not exists archive_partition_status on archive_partition (tenant_id, status, domain);

-- Scheduled dashboard and query deliveries (FR-46.10). Times are UTC; timezone
-- is the IANA zone the cron is evaluated in.
create table if not exists report_schedule (
  tenant_id uuid not null,
  schedule_id uuid not null,
  name text not null check (length(name) between 1 and 120),
  target_type text not null check (target_type in ('DASHBOARD', 'QUERY')),
  target_id text not null,
  cron text not null,
  timezone text not null,
  format text not null check (format in ('PDF', 'XLSX')),
  destination_json jsonb not null,
  last_run_at timestamptz,
  next_run_at timestamptz,
  failure_count integer not null default 0 check (failure_count >= 0),
  version integer not null default 1 check (version >= 1),
  created_by text not null default '',
  created_at timestamptz not null default now(),
  primary key (tenant_id, schedule_id)
);
alter table report_schedule enable row level security;
alter table report_schedule force row level security;
create policy report_schedule_tenant_access on report_schedule as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create index if not exists report_schedule_next_run on report_schedule (tenant_id, next_run_at);

-- Application role privileges for the slice-2 tables (see 0002_grants.sql).
-- Rules, scores, partitions and schedules are never deleted by the service;
-- idempotency keys are deleted once older than 24 hours.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_keys TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON dq_rule, dq_score, archive_partition TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON report_schedule TO passage_app';
  END IF;
END
$$;
