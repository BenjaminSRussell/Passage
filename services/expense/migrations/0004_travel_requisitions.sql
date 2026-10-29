-- Slice 5 (PSG-43): persisted travel requisitions (FR-43.16, BR-43.13, 8.43.4).
-- The estimated total is the sum of the estimate lines, computed by the store in the
-- transaction that writes them. requested_by and decided_by are the gateway user ids
-- (X-Passage-User) that the MssApprove own-items rule compares (8.43.17).

create table travel_requisition (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  worker_id uuid not null,
  purpose text not null check (length(purpose) between 1 and 500),
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  trip_from date not null,
  trip_to date not null,
  destinations jsonb not null default '[]'::jsonb,
  estimate_lines jsonb not null default '[]'::jsonb,
  estimated_total numeric(20,6) not null check (estimated_total >= 0),
  status text not null default 'DRAFT' check (status in ('DRAFT', 'SUBMITTED', 'APPROVED', 'REJECTED', 'CLOSED')),
  version int not null default 1 check (version > 0),
  requested_by text not null default '',
  decided_by text,
  create_key text not null,
  request_hash text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  submitted_at timestamptz,
  decided_at timestamptz,
  primary key (tenant_id, id),
  constraint travel_requisition_window check (trip_to >= trip_from),
  constraint travel_requisition_create_key unique (tenant_id, create_key)
);
create index travel_requisition_worker on travel_requisition (tenant_id, worker_id, id);

alter table travel_requisition enable row level security;
alter table travel_requisition force row level security;
create policy travel_requisition_tenant_access on travel_requisition as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy travel_requisition_legal_entity_scope on travel_requisition as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
