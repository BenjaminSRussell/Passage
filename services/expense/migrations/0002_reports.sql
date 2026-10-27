-- Slice 4 (PSG-43): persisted expense reports and lines (FR-43.11, FR-43.12, 8.43.4).
-- Amounts are in the report currency. The stored totals satisfy
-- total = reimbursable + company-paid and equal the sum of the lines (8.43.4 invariant);
-- the store recomputes them in the transaction that changes a line.

create table expense_report (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  worker_id uuid not null,
  purpose text not null check (length(purpose) between 1 and 500),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  status text not null default 'DRAFT' check (status in ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'RETURNED', 'POSTED', 'PAID')),
  total_amount numeric(20,6) not null default 0 check (total_amount >= 0),
  reimbursable_amount numeric(20,6) not null default 0 check (reimbursable_amount >= 0),
  company_paid_amount numeric(20,6) not null default 0 check (company_paid_amount >= 0),
  version int not null default 1 check (version > 0),
  create_key text not null,
  request_hash text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  submitted_at timestamptz,
  primary key (tenant_id, id),
  constraint expense_report_total_invariant check (total_amount = reimbursable_amount + company_paid_amount),
  constraint expense_report_create_key unique (tenant_id, create_key)
);
create index expense_report_worker on expense_report (tenant_id, worker_id, id);
create index expense_report_status on expense_report (tenant_id, status, id);

create table expense_line (
  tenant_id uuid not null,
  id uuid not null,
  report_id uuid not null,
  line_no int not null check (line_no > 0),
  category text not null check (length(category) between 1 and 32),
  txn_date date not null,
  amount numeric(20,6) not null check (amount >= 0),
  merchant text not null default '' check (length(merchant) <= 200),
  attendees int not null default 0 check (attendees between 0 and 500),
  has_receipt boolean not null default false,
  justification text not null default '' check (length(justification) <= 500),
  paid_by text not null default 'EMPLOYEE' check (paid_by in ('EMPLOYEE', 'COMPANY_CARD')),
  policy_results jsonb not null default '[]'::jsonb,
  create_key text not null,
  request_hash text not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  constraint expense_line_report_fk foreign key (tenant_id, report_id) references expense_report (tenant_id, id) on delete cascade,
  constraint expense_line_number unique (tenant_id, report_id, line_no),
  constraint expense_line_create_key unique (tenant_id, report_id, create_key)
);

alter table expense_report enable row level security;
alter table expense_report force row level security;
create policy expense_report_tenant_access on expense_report as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy expense_report_legal_entity_scope on expense_report as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table expense_line enable row level security;
alter table expense_line force row level security;
create policy expense_line_tenant_access on expense_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
-- A line carries no legal_entity_id: it is visible only when its report is, so the
-- legal-entity scope follows the report.
create policy expense_line_legal_entity_scope on expense_line as restrictive
  using (exists (select 1 from expense_report r where r.tenant_id = expense_line.tenant_id and r.id = expense_line.report_id));
