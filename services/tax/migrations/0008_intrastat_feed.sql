-- Intrastat feed (#13 deliverable 6): goods movements staged from fulfillment (dispatches) and receiving (arrivals), and what the
-- journal needs to turn a movement line into an Intrastat line: the commodity code, country of origin and net mass per unit of each product.
create table tax_intrastat_product (
  tenant_id uuid not null,
  product_id uuid not null,
  commodity_code text not null check (commodity_code ~ '^[0-9]{8}$'),
  origin_country text not null check (origin_country ~ '^[A-Z]{2}$'),
  net_mass_per_unit numeric(20,6) not null check (net_mass_per_unit >= 0),
  supplementary_per_unit numeric(20,6) not null default 0 check (supplementary_per_unit >= 0),
  updated_at timestamptz not null default now(),
  primary key (tenant_id, product_id)
);

create table tax_intrastat_movement (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  id uuid not null,
  flow text not null check (flow in ('arrival', 'dispatch')),
  source_ref text not null check (length(source_ref) between 1 and 100),
  movement_date date not null,
  partner_country text not null check (partner_country ~ '^[A-Z]{2}$'),
  transaction_code text not null default '',
  transport_mode text not null default '',
  delivery_terms text not null default '',
  port text not null default '',
  lines jsonb not null,
  status text not null default 'staged' check (status in ('staged', 'transferred', 'ignored')),
  created_at timestamptz not null default now(),
  primary key (tenant_id, legal_entity_id, id),
  unique (tenant_id, legal_entity_id, source_ref)
);
create index tax_intrastat_movement_staged on tax_intrastat_movement (tenant_id, legal_entity_id, flow, movement_date) where status = 'staged';

do $$
declare t text;
begin
  foreach t in array array['tax_intrastat_product', 'tax_intrastat_movement'] loop
    execute format('alter table %I enable row level security', t);
    execute format('alter table %I force row level security', t);
    execute format('create policy %I on %I as permissive using (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) with check (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
  end loop;
  execute 'create policy tax_intrastat_movement_legal_entity_scope on tax_intrastat_movement as restrictive using (current_setting(''passage.legal_entity_scopes'', true) = ''*'' or legal_entity_id::text = any(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))';
  if exists (select 1 from pg_roles where rolname = 'passage_app') then
    execute 'grant select, insert, update, delete on tax_intrastat_product, tax_intrastat_movement to passage_app';
  end if;
end
$$;
