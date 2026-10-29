-- Slice 5 (PSG-43): persisted cash advances (FR-43.15, BR-43.9, 8.43.4).
-- Amounts are in the advance currency. The status after payment is derived from the
-- amounts by the domain rule (CashAdvance.SettlementStatus) and written in the same
-- statement as the amounts. due_settlement_date is trip end plus the settlement-due
-- parameter in force when the advance was requested (8.43.13, default 30 days).

create table cash_advance (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  worker_id uuid not null,
  amount numeric(20,6) not null check (amount > 0),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  purpose text not null check (length(purpose) between 1 and 500),
  travel_requisition_id uuid,
  trip_end date not null,
  due_settlement_date date not null,
  status text not null default 'SUBMITTED' check (status in ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'PAID', 'PARTIALLY_SETTLED', 'SETTLED')),
  paid_amount numeric(20,6) not null default 0 check (paid_amount >= 0 and paid_amount <= amount),
  settled_amount numeric(20,6) not null default 0 check (settled_amount >= 0),
  returned_amount numeric(20,6) not null default 0 check (returned_amount >= 0),
  recovered_via_payroll_amount numeric(20,6) not null default 0 check (recovered_via_payroll_amount >= 0),
  ap_prepayment_id uuid,
  version int not null default 1 check (version > 0),
  requested_by text not null default '',
  decided_by text,
  create_key text not null,
  request_hash text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  decided_at timestamptz,
  paid_at timestamptz,
  primary key (tenant_id, id),
  -- Nothing clears more than was paid (BR-43.9: open = paid - settled - returned - recovered).
  constraint cash_advance_cleared check (settled_amount + returned_amount + recovered_via_payroll_amount <= paid_amount),
  constraint cash_advance_due check (due_settlement_date >= trip_end),
  constraint cash_advance_requisition_fk foreign key (tenant_id, travel_requisition_id) references travel_requisition (tenant_id, id),
  constraint cash_advance_create_key unique (tenant_id, create_key)
);

-- Open advances of a worker drive the EXP-ADV-002 block (8.43.4 index (worker_id, status)).
create index cash_advance_worker_status on cash_advance (tenant_id, worker_id, status);

alter table cash_advance enable row level security;
alter table cash_advance force row level security;
create policy cash_advance_tenant_access on cash_advance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy cash_advance_legal_entity_scope on cash_advance as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
