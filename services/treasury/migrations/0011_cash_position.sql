-- Wave 36 (PSG-36) FR-36.8: intraday account reports (camt.052) behind the cash position.
-- A report is one account's balances at one moment; the cash position reads the freshest of the latest
-- report and the latest statement per bank account. Reports are append-only (a bank sends a new one).

create table intraday_report (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  bank_account_id uuid not null,
  report_id text not null check (length(report_id) between 1 and 100),
  format text not null check (format like 'camt.052.%'),
  file_hash text not null,
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  created_at_bank timestamptz not null,
  imported_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, file_hash),
  unique (tenant_id, bank_account_id, report_id),
  foreign key (tenant_id, bank_account_id, legal_entity_id) references bank_account (tenant_id, id, legal_entity_id)
);
create index ix_intraday_report_latest on intraday_report (tenant_id, bank_account_id, created_at_bank desc);

create table intraday_balance (
  tenant_id uuid not null,
  report_id uuid not null,
  seq int not null check (seq >= 1),
  legal_entity_id uuid not null,
  code text not null check (code in ('OPBD', 'ITBD', 'ITAV', 'CLAV', 'FWAV', 'PRCD', 'CLBD')),
  amount numeric(20,6) not null,
  as_of timestamptz not null,
  primary key (tenant_id, report_id, seq),
  foreign key (tenant_id, report_id) references intraday_report (tenant_id, id)
);

alter table intraday_report enable row level security;
alter table intraday_report force row level security;
create policy intraday_report_tenant_access on intraday_report as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy intraday_report_legal_entity_scope on intraday_report as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

alter table intraday_balance enable row level security;
alter table intraday_balance force row level security;
create policy intraday_balance_tenant_access on intraday_balance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy intraday_balance_legal_entity_scope on intraday_balance as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON intraday_report, intraday_balance TO passage_app';
  END IF;
END
$$;
