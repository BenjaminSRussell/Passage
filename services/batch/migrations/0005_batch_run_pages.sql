create table if not exists batch_run_pages (
  tenant_id uuid not null,
  run_id uuid not null,
  page_no int not null check (page_no >= 0),
  cursor text not null default '',
  next_cursor text not null default '',
  items int not null default 0,
  succeeded int not null default 0,
  skipped int not null default 0,
  status text not null check (status in ('ok', 'failed')),
  error text,
  updated_at timestamptz not null default now(),
  primary key (tenant_id, run_id, page_no)
);

alter table batch_run_pages enable row level security;
alter table batch_run_pages force row level security;
create policy batch_run_pages_tenant_access on batch_run_pages as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Page progress is upserted; the run keeps its append-only shape except the
-- progress columns a job executor finishes. Skipped when passage_app is absent.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON batch_run_pages TO passage_app';
    EXECUTE 'GRANT UPDATE (status, ended_at, error, rows_done) ON batch_run TO passage_app';
  END IF;
END
$$;
