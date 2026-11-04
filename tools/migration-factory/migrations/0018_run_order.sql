-- Migration factory, wave 49 slice 14 (decided item S13-d): releasing quarantined open_orders lines.
-- The open_orders.order_count total counts distinct orders, so adding a released line to it needs
-- to know whether the run's loaded side already holds another line of the same order. The run keeps
-- the orders its loaded side holds, one row per (order type, order number), written with the run
-- and by each release. Runs stored before this migration have none, but their quarantine rows keep
-- no whole line either (0017) and cannot be released, so nothing reads the missing rows.
--
-- The foreign key takes SHARE ROW EXCLUSIVE on the live pack_run table while the table is created:
-- the lock_timeout makes the migration fail (and the rollout retry) rather than block every pack
-- run behind a long transaction. The table carries tenant_id first and is held to the tenant by
-- forced row-level security.
SET LOCAL lock_timeout = '5s';

create table pack_run_order (
  tenant_id uuid not null,
  run_id uuid not null,
  order_type text not null check (order_type in ('SALES', 'PURCHASE')),
  order_number text not null check (order_number <> ''),
  created_at timestamptz not null default now(),
  primary key (tenant_id, run_id, order_type, order_number),
  foreign key (tenant_id, run_id) references pack_run (tenant_id, run_id)
);

alter table pack_run_order enable row level security;
alter table pack_run_order force row level security;
create policy pack_run_order_tenant_access on pack_run_order as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. The rows are insert-only for passage_app: a release adds an order
-- (on conflict do nothing needs no UPDATE) and nothing updates or deletes one. Skipped when the
-- role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON pack_run_order TO passage_app';
  END IF;
END
$$;
