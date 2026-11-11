create table "benefit_elections" (
  "tenant_id" uuid not null,
  "id" uuid not null,
  "plan_code" varchar(20) not null,
  "employee_ref" uuid not null,
  "coverage_tier" bytea,
  "medical_leave_reason" bytea,
  "accommodation_note" bytea,
  "payroll_memo" bytea,
  ext jsonb not null default '{}',
  version integer not null default 1,
  created_at timestamptz not null default now(),
  created_by uuid not null,
  updated_at timestamptz not null default now(),
  updated_by uuid not null,
  primary key (tenant_id, id)
);
create index "ix_benefit_elections_01" on "benefit_elections" ("tenant_id", "employee_ref");
alter table benefit_elections enable row level security;
alter table benefit_elections force row level security;
create policy benefit_elections_tenant_access on benefit_elections as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
