-- Record search, #506: one trigram-indexed row per searchable record, fed by the
-- search-index event consumer. search_record holds records of one legal entity
-- (purchase orders) under tenant and legal-entity row-level security;
-- search_record_tenant holds tenant-wide records (parties, products), which
-- have no legal entity, under tenant row-level security. source_version
-- and source_at are the source version and time of the event that last wrote the
-- row: an older event never overwrites a newer one. removed rows are kept as tombstones so a late older event cannot
-- bring a removed record back.

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

create table search_record (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  record_type text not null check (record_type in ('purchase_order')),
  record_id uuid not null,
  number text not null,
  title text not null,
  subtitle text not null default '',
  status text not null default '',
  removed boolean not null default false,
  source_version integer not null default 0,
  source_at timestamptz not null,
  indexed_at timestamptz not null default now(),
  search_text text generated always as (lower(number || ' ' || title)) stored,
  number_key text generated always as (lower(number)) stored,
  primary key (tenant_id, record_type, record_id)
);
create index search_record_trgm on search_record using gin (search_text public.gin_trgm_ops);
create index search_record_number on search_record (tenant_id, record_type, number_key collate "C");

alter table search_record enable row level security;
alter table search_record force row level security;
create policy search_record_tenant_access on search_record as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy search_record_legal_entity_scope on search_record as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table search_record_tenant (
  tenant_id uuid not null,
  record_type text not null check (record_type in ('party', 'product')),
  record_id uuid not null,
  number text not null,
  title text not null,
  subtitle text not null default '',
  status text not null default '',
  removed boolean not null default false,
  source_version integer not null default 0,
  source_at timestamptz not null,
  indexed_at timestamptz not null default now(),
  search_text text generated always as (lower(number || ' ' || title)) stored,
  number_key text generated always as (lower(number)) stored,
  primary key (tenant_id, record_type, record_id)
);
create index search_record_tenant_trgm on search_record_tenant using gin (search_text public.gin_trgm_ops);
create index search_record_tenant_number on search_record_tenant (tenant_id, record_type, number_key collate "C");

alter table search_record_tenant enable row level security;
alter table search_record_tenant force row level security;
create policy search_record_tenant_tenant_access on search_record_tenant as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table consumer_inbox (
  tenant_id uuid not null,
  consumer text not null,
  event_id text not null,
  processed_at timestamptz not null default now(),
  primary key (tenant_id, consumer, event_id)
);
alter table consumer_inbox enable row level security;
alter table consumer_inbox force row level security;
create policy consumer_inbox_tenant_access on consumer_inbox as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
