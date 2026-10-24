create table if not exists batch_checkpoints (
  tenant_id uuid not null,
  job_id text not null,
  partition_no integer not null,
  next_row bigint not null check (next_row >= 0),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, job_id, partition_no)
);

alter table batch_checkpoints enable row level security;
alter table batch_checkpoints force row level security;
create policy batch_checkpoints_tenant_access on batch_checkpoints
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table if not exists batch_processed_rows (
  tenant_id uuid not null,
  job_id text not null,
  partition_no integer not null,
  row_number bigint not null check (row_number >= 0),
  processed_at timestamptz not null default now(),
  primary key (tenant_id, job_id, partition_no, row_number)
);

alter table batch_processed_rows enable row level security;
alter table batch_processed_rows force row level security;
create policy batch_processed_rows_tenant_access on batch_processed_rows
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
