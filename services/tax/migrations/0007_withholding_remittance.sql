-- Withholding remittance runs and certificates (#13 deliverable 8). A payment that withheld tax is recorded as an entry; a remittance run
-- takes the unremitted entries of a period, totals them per code and currency for the authority, and issues one certificate per payee,
-- code and currency, each with a gap-free number per legal entity and year. A counter row per legal entity, kind and year is incremented
-- in the run's transaction, so a run that fails leaves no gap.
create table tax_withholding_entry (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  id uuid not null,
  party_type text not null check (party_type in ('vendor', 'customer')),
  party_ref text not null check (length(party_ref) between 1 and 64),
  code text not null check (length(code) between 1 and 64),
  payment_date date not null,
  currency char(3) not null check (currency ~ '^[A-Z]{3}$'),
  gross_amount numeric(20,6) not null check (gross_amount > 0),
  taxable_base numeric(20,6) not null check (taxable_base >= 0),
  withheld_amount numeric(20,6) not null check (withheld_amount >= 0),
  remittance_run_id uuid,
  certificate_id uuid,
  created_at timestamptz not null default now(),
  primary key (tenant_id, legal_entity_id, id)
);
create index tax_withholding_entry_open on tax_withholding_entry (tenant_id, legal_entity_id, payment_date) where remittance_run_id is null;

create table tax_withholding_run (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  id uuid not null,
  number text not null,
  period_from date not null,
  period_to date not null check (period_to >= period_from),
  lines jsonb not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, legal_entity_id, id),
  unique (tenant_id, legal_entity_id, number)
);

create table tax_withholding_certificate (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  id uuid not null,
  number text not null,
  run_id uuid not null,
  party_type text not null,
  party_ref text not null,
  code text not null,
  currency char(3) not null,
  period_from date not null,
  period_to date not null,
  gross_amount numeric(20,6) not null,
  taxable_base numeric(20,6) not null,
  withheld_amount numeric(20,6) not null,
  entry_count integer not null,
  issued_at timestamptz not null default now(),
  primary key (tenant_id, legal_entity_id, id),
  unique (tenant_id, legal_entity_id, number)
);

create table tax_number_counter (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  kind text not null,
  year integer not null,
  last_value integer not null default 0,
  primary key (tenant_id, legal_entity_id, kind, year)
);

do $$
declare t text;
begin
  foreach t in array array['tax_withholding_entry', 'tax_withholding_run', 'tax_withholding_certificate', 'tax_number_counter'] loop
    execute format('alter table %I enable row level security', t);
    execute format('alter table %I force row level security', t);
    execute format('create policy %I on %I as permissive using (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) with check (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
    execute format('create policy %I on %I as restrictive using (current_setting(''passage.legal_entity_scopes'', true) = ''*'' or legal_entity_id::text = any(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  end loop;
  if exists (select 1 from pg_roles where rolname = 'passage_app') then
    execute 'grant select, insert, update on tax_withholding_entry, tax_withholding_run, tax_withholding_certificate, tax_number_counter to passage_app';
  end if;
end
$$;
