-- Wave 36 (PSG-36) slice 3: checks (FR-36.6), facilities and instruments (FR-36.7),
-- loan contracts and schedules (FR-36.19). Same RLS template as 0001_init.sql.
-- Idempotency-Key replays use the shared idempotency_keys table of 0001.

alter table bank_account add column next_check_number bigint not null default 1 check (next_check_number >= 1);

create table bank_check (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  check_number bigint not null check (check_number > 0),
  payee text not null check (length(payee) between 1 and 140),
  amount numeric(20,6) not null check (amount > 0),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  status text not null default 'created' check (status in ('created', 'printed', 'sent', 'cleared', 'voided', 'stopped')),
  void_reason text,
  replaces_id uuid,
  replaced_by_id uuid,
  check (status <> 'voided' or void_reason is not null),
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, bank_account_id, check_number),
  foreign key (tenant_id, bank_account_id) references bank_account (tenant_id, id)
);
alter table bank_check enable row level security;
alter table bank_check force row level security;
create policy bank_check_tenant_access on bank_check as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy bank_check_legal_entity_scope on bank_check as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table treasury_facility (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  facility_code text not null check (facility_code ~ '^[A-Z0-9-]{1,20}$'),
  bank_name text not null check (length(bank_name) between 1 and 120),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  limit_amount numeric(20,6) not null check (limit_amount > 0),
  expiry_date date not null,
  status text not null default 'active' check (status in ('active', 'closed')),
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, legal_entity_id, facility_code)
);
alter table treasury_facility enable row level security;
alter table treasury_facility force row level security;
create policy treasury_facility_tenant_access on treasury_facility as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy treasury_facility_legal_entity_scope on treasury_facility as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Letters of credit (import / export) and bank guarantees drawn on a facility (FR-36.7).
create table treasury_facility_instrument (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  facility_id uuid not null,
  kind text not null check (kind in ('lc_import', 'lc_export', 'guarantee')),
  reference text not null check (length(reference) between 1 and 64),
  beneficiary text not null check (length(beneficiary) between 1 and 140),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  amount numeric(20,6) not null check (amount > 0),
  issue_date date not null,
  expiry_date date not null check (expiry_date >= issue_date),
  linked_order_ref text check (linked_order_ref is null or length(linked_order_ref) between 1 and 64),
  status text not null default 'open' check (status in ('open', 'closed')),
  limit_override boolean not null default false,
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id),
  foreign key (tenant_id, facility_id) references treasury_facility (tenant_id, id)
);
create index ix_treasury_facility_instrument_open on treasury_facility_instrument (tenant_id, facility_id) where status = 'open';
alter table treasury_facility_instrument enable row level security;
alter table treasury_facility_instrument force row level security;
create policy treasury_facility_instrument_tenant_access on treasury_facility_instrument as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy treasury_facility_instrument_legal_entity_scope on treasury_facility_instrument as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Loan contracts (FR-36.19). Fixed rate only in this slice; floating fixings are a later slice.
create table treasury_loan (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  role text not null check (role in ('borrower', 'lender')),
  counterparty_name text not null check (length(counterparty_name) between 1 and 140),
  principal numeric(20,6) not null check (principal > 0),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  start_date date not null,
  maturity_date date not null check (maturity_date > start_date),
  schedule_type text not null check (schedule_type in ('Annuity', 'Linear', 'Bullet')),
  rate_type text not null default 'fixed' check (rate_type = 'fixed'),
  fixed_rate numeric(9,6) not null check (fixed_rate >= 0),
  day_count text not null check (day_count in ('ACT/360', 'ACT/365', '30/360')),
  period_months int not null check (period_months in (1, 3, 6, 12)),
  status text not null default 'active' check (status in ('draft', 'active', 'repaid', 'terminated')),
  version int not null default 1,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, id)
);
alter table treasury_loan enable row level security;
alter table treasury_loan force row level security;
create policy treasury_loan_tenant_access on treasury_loan as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy treasury_loan_legal_entity_scope on treasury_loan as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table treasury_loan_schedule_line (
  tenant_id uuid not null,
  loan_id uuid not null,
  legal_entity_id uuid not null,
  period_no int not null check (period_no > 0),
  period_start date not null,
  period_end date not null,
  days int not null check (days > 0),
  opening_principal numeric(20,6) not null,
  interest numeric(20,6) not null check (interest >= 0),
  principal_repayment numeric(20,6) not null check (principal_repayment >= 0),
  closing_principal numeric(20,6) not null,
  primary key (tenant_id, loan_id, period_no),
  foreign key (tenant_id, loan_id) references treasury_loan (tenant_id, id)
);
alter table treasury_loan_schedule_line enable row level security;
alter table treasury_loan_schedule_line force row level security;
create policy treasury_loan_schedule_line_tenant_access on treasury_loan_schedule_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy treasury_loan_schedule_line_legal_entity_scope on treasury_loan_schedule_line as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
