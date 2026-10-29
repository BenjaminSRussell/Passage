-- Slice 4 (PSG-4604): dashboards, explorer exports, schedule deliveries and
-- catalog lineage. Every table is tenant-wide (no legal_entity_id), so only the
-- tenant policy applies: data scope is applied to the queries a dashboard or an
-- export runs, not to their definitions.

-- Embedded dashboard definitions (FR-46.8). Tiles are bound to published
-- governed metrics. version is the ETag source; replacing needs If-Match.
create table if not exists dashboard (
  tenant_id uuid not null,
  dashboard_id text not null check (dashboard_id ~ '^[a-z][a-z0-9_-]{1,62}$'),
  title text not null check (length(title) between 1 and 120),
  tiles jsonb not null check (jsonb_typeof(tiles) = 'array' and jsonb_array_length(tiles) between 1 and 12),
  refresh_seconds integer not null default 60 check (refresh_seconds between 15 and 600),
  version integer not null default 1 check (version >= 1),
  updated_by text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, dashboard_id)
);
alter table dashboard enable row level security;
alter table dashboard force row level security;
create policy dashboard_tenant_access on dashboard as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Explorer export jobs (FR-46.9, BR-46.7), the export log of §9.46.14.
-- query_hash fingerprints the requesting user, the format and the compiled,
-- scope-filtered plan, so an identical request within one hour returns the
-- existing job. plan is what the export runner executes.
create table if not exists export_job (
  tenant_id uuid not null,
  export_id uuid not null,
  requested_by text not null,
  query_hash text not null check (length(query_hash) = 64),
  plan jsonb not null,
  format text not null check (format in ('XLSX', 'CSV')),
  mode text not null check (mode in ('INLINE', 'TEMPORAL_JOB')),
  row_count bigint not null check (row_count >= 0),
  link_ttl_hours integer not null check (link_ttl_hours between 1 and 168),
  status text not null default 'QUEUED' check (status in ('QUEUED', 'READY', 'DELETED')),
  created_at timestamptz not null,
  delete_at timestamptz not null,
  primary key (tenant_id, export_id),
  check (delete_at > created_at)
);
alter table export_job enable row level security;
alter table export_job force row level security;
create policy export_job_tenant_access on export_job as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create index if not exists export_job_hash on export_job (tenant_id, query_hash, created_at desc);
create index if not exists export_job_log on export_job (tenant_id, created_at desc);

-- Schedule delivery attempts (FR-46.10, §9.46.14 "Schedule deliveries"). One
-- row per attempt; the attempts of one scheduled run share run_id. A failed
-- attempt is retried 1, 4 and 16 minutes later and the fourth failure is FAILED.
create table if not exists report_delivery (
  tenant_id uuid not null,
  delivery_id uuid not null,
  schedule_id uuid not null,
  run_id uuid not null,
  attempt integer not null check (attempt between 1 and 6),
  status text not null check (status in ('DELIVERED', 'RETRY', 'FAILED')),
  format text not null check (format in ('PDF', 'XLSX')),
  destination_type text not null check (destination_type in ('EMAIL', 'SFTP', 'INBOX')),
  row_count bigint not null default 0 check (row_count >= 0),
  bytes bigint not null default 0 check (bytes >= 0),
  error_text text not null default '' check (length(error_text) <= 500),
  run_at timestamptz not null,
  retry_at timestamptz,
  primary key (tenant_id, delivery_id),
  unique (tenant_id, run_id, attempt),
  foreign key (tenant_id, schedule_id) references report_schedule (tenant_id, schedule_id),
  check ((status = 'RETRY') = (retry_at is not null))
);
alter table report_delivery enable row level security;
alter table report_delivery force row level security;
create policy report_delivery_tenant_access on report_delivery as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create index if not exists report_delivery_schedule on report_delivery (tenant_id, schedule_id, run_at desc);

-- Lineage edges between catalog assets (FR-46.12). An edge says target is
-- derived from source. Written by the catalog sync or a steward; the lineage
-- view walks them in both directions.
create table if not exists lineage_edge (
  tenant_id uuid not null,
  source_fqn text not null check (source_fqn ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$'),
  target_fqn text not null check (target_fqn ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$'),
  created_at timestamptz not null default now(),
  primary key (tenant_id, source_fqn, target_fqn),
  check (source_fqn <> target_fqn)
);
alter table lineage_edge enable row level security;
alter table lineage_edge force row level security;
create policy lineage_edge_tenant_access on lineage_edge as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create index if not exists lineage_edge_target on lineage_edge (tenant_id, target_fqn);

-- Application role privileges for the slice-4 tables (see 0002_grants.sql).
-- Dashboards are replaced in place (UPDATE, locked FOR UPDATE). Export jobs,
-- delivery attempts and lineage edges are written once. The schedule
-- dispatcher locks due schedules FOR UPDATE and moves only their run columns,
-- so the UPDATE grant on report_schedule is limited to those columns. Nothing
-- here is deleted by the service.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON dashboard TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON export_job, report_delivery, lineage_edge TO passage_app';
    EXECUTE 'GRANT UPDATE (last_run_at, next_run_at, failure_count) ON report_schedule TO passage_app';
  END IF;
END
$$;
