-- Persisted tax configuration (13-1), VAT return boxes and EU sales list data (13-2),
-- Intrastat master data (13-3) and the Intrastat journal (13-4).

-- 13-1 jurisdiction tree. parent_code is a self reference by code; the API refuses cycles.
create table tax_jurisdiction (
  tenant_id uuid not null,
  code text not null check (code ~ '^[A-Za-z0-9._-]{1,32}$'),
  name text not null check (length(name) between 1 and 120),
  kind text not null check (kind in ('country', 'state', 'county', 'city', 'district')),
  country text not null check (country ~ '^[A-Z]{2}$'),
  parent_code text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, code),
  foreign key (tenant_id, parent_code) references tax_jurisdiction (tenant_id, code),
  check (parent_code is distinct from code)
);
alter table tax_jurisdiction enable row level security;
alter table tax_jurisdiction force row level security;
create policy tax_jurisdiction_tenant_access on tax_jurisdiction as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);


create table tax_code (
  tenant_id uuid not null,
  code text not null check (code ~ '^[A-Za-z0-9._-]{1,64}$'),
  name text not null check (length(name) between 1 and 120),
  jurisdiction_code text not null,
  mechanism text not null default 'standard' check (mechanism in ('standard', 'reverse_charge', 'use_tax')),
  calc_basis text not null default 'line' check (calc_basis in ('line', 'document')),
  rounding text not null default 'per_line' check (rounding in ('per_line', 'per_document')),
  compound_sequence integer not null default 0 check (compound_sequence >= 0),
  recoverable_pct numeric(7,4) check (recoverable_pct between 0 and 100),
  rates jsonb not null default '[]',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, code),
  foreign key (tenant_id, jurisdiction_code) references tax_jurisdiction (tenant_id, code)
);
alter table tax_code enable row level security;
alter table tax_code force row level security;
create policy tax_code_tenant_access on tax_code as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);


-- Party tax groups and item tax groups; the codes a group selects are the determination:
-- a document line takes the codes that appear in both its party group and its item group.
create table tax_group (
  tenant_id uuid not null,
  kind text not null check (kind in ('party', 'item')),
  code text not null check (code ~ '^[A-Za-z0-9._-]{1,64}$'),
  name text not null check (length(name) between 1 and 120),
  tax_codes text[] not null default '{}',
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, kind, code)
);
alter table tax_group enable row level security;
alter table tax_group force row level security;
create policy tax_group_tenant_access on tax_group as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);


-- 13-2 settings of one legal entity: its country (the EU sales list leaves it out) and VAT number.
create table tax_entity_setting (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  home_country text not null check (home_country ~ '^[A-Z]{2}$'),
  vat_id text not null default '',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, legal_entity_id)
);
alter table tax_entity_setting enable row level security;
alter table tax_entity_setting force row level security;
create policy tax_entity_setting_tenant_access on tax_entity_setting as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy tax_entity_setting_legal_entity_scope on tax_entity_setting as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));


alter table tax_transaction
  add column base_amount numeric(20,6),
  add column counterparty_country text check (counterparty_country ~ '^[A-Z]{2}$'),
  add column counterparty_vat_id text,
  add column supply_kind text check (supply_kind in ('goods', 'services', 'triangular'));

-- Return boxes: a box sums the base or the tax of the mapped code and direction pairs,
-- or (kind payable) the output and use tax less the input tax of the whole period.
create table tax_vat_box (
  tenant_id uuid not null,
  box text not null check (box ~ '^[A-Za-z0-9._-]{1,16}$'),
  label text not null check (length(label) between 1 and 160),
  kind text not null check (kind in ('base', 'tax', 'payable')),
  sort integer not null default 0,
  primary key (tenant_id, box)
);
alter table tax_vat_box enable row level security;
alter table tax_vat_box force row level security;
create policy tax_vat_box_tenant_access on tax_vat_box as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);


create table tax_vat_box_code (
  tenant_id uuid not null,
  box text not null,
  code text not null,
  direction text not null check (direction in ('output', 'input', 'use')),
  primary key (tenant_id, box, code, direction),
  foreign key (tenant_id, box) references tax_vat_box (tenant_id, box) on delete cascade
);
alter table tax_vat_box_code enable row level security;
alter table tax_vat_box_code force row level security;
create policy tax_vat_box_code_tenant_access on tax_vat_box_code as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);


-- 13-3 Intrastat master data. One table, one row per kind and code; attrs holds the kind's extras
-- (supplementary_unit for commodity codes; eu_member and intrastat_code for country properties;
-- country for ports).
create table tax_intrastat_master (
  tenant_id uuid not null,
  kind text not null check (kind in ('commodity', 'transaction_code', 'transport_mode', 'port', 'country', 'delivery_terms')),
  code text not null check (code ~ '^[A-Za-z0-9]{1,10}$'),
  description text not null check (length(description) between 1 and 200),
  attrs jsonb not null default '{}',
  updated_at timestamptz not null default now(),
  primary key (tenant_id, kind, code)
);
alter table tax_intrastat_master enable row level security;
alter table tax_intrastat_master force row level security;
create policy tax_intrastat_master_tenant_access on tax_intrastat_master as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);


-- 13-4 Intrastat journal.
create table tax_intrastat_journal (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  flow text not null check (flow in ('arrival', 'dispatch')),
  period_from date not null check (extract(day from period_from) = 1),
  status text not null default 'draft' check (status in ('draft', 'validated', 'exported')),
  correction_of uuid,
  created_at timestamptz not null default now(),
  exported_at timestamptz,
  primary key (tenant_id, id),
  foreign key (tenant_id, correction_of) references tax_intrastat_journal (tenant_id, id)
);
create index tax_intrastat_journal_period_idx on tax_intrastat_journal (tenant_id, legal_entity_id, period_from);
alter table tax_intrastat_journal enable row level security;
alter table tax_intrastat_journal force row level security;
create policy tax_intrastat_journal_tenant_access on tax_intrastat_journal as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy tax_intrastat_journal_legal_entity_scope on tax_intrastat_journal as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));


create table tax_intrastat_line (
  tenant_id uuid not null,
  journal_id uuid not null,
  line_no integer not null check (line_no > 0),
  source_ref text not null default '' check (length(source_ref) <= 64),
  partner_country text not null default '',
  commodity_code text not null default '',
  transaction_code text not null default '',
  transport_mode text not null default '',
  delivery_terms text not null default '',
  port text not null default '',
  origin_country text not null default '',
  net_mass numeric(20,6) not null default 0 check (net_mass >= 0),
  supplementary_qty numeric(20,6) not null default 0 check (supplementary_qty >= 0),
  invoice_value numeric(20,6) not null default 0,
  statistical_value numeric(20,6) not null default 0,
  compressed_from integer not null default 1 check (compressed_from > 0),
  errors jsonb not null default '[]',
  primary key (tenant_id, journal_id, line_no),
  foreign key (tenant_id, journal_id) references tax_intrastat_journal (tenant_id, id) on delete cascade
);

-- Records already transferred into a journal; keeps a transfer idempotent after compression.
create table tax_intrastat_transferred (
  tenant_id uuid not null,
  journal_id uuid not null,
  source_ref text not null check (length(source_ref) between 1 and 64),
  primary key (tenant_id, journal_id, source_ref),
  foreign key (tenant_id, journal_id) references tax_intrastat_journal (tenant_id, id) on delete cascade
);
alter table tax_intrastat_transferred enable row level security;
alter table tax_intrastat_transferred force row level security;
create policy tax_intrastat_transferred_tenant_access on tax_intrastat_transferred as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
alter table tax_intrastat_line enable row level security;
alter table tax_intrastat_line force row level security;
create policy tax_intrastat_line_tenant_access on tax_intrastat_line as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

