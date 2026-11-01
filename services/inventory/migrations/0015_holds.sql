alter table on_hand add column if not exists held_physical numeric(28,6) not null default 0 check (held_physical >= 0);

create table if not exists inventory_hold (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  id uuid not null,
  idempotency_key text not null check (char_length(idempotency_key) between 1 and 255),
  allocation_no integer not null check (allocation_no > 0),
  product_id uuid not null,
  dimension_set_hash bytea not null,
  qty numeric(28,6) not null check (qty > 0),
  requested_qty numeric(28,6) not null check (requested_qty >= 0),
  released_qty numeric(28,6) not null default 0 check (released_qty >= 0),
  whole_lot boolean not null default false,
  reason text not null check (reason in ('QUALITY', 'DAMAGE', 'RECALL', 'LEGAL', 'OTHER')),
  source_type text not null check (source_type ~ '^[A-Z][A-Z0-9_]{1,31}$'),
  source_ref text not null check (char_length(source_ref) between 1 and 60),
  note text check (char_length(note) <= 500),
  state text not null check (state in ('ACTIVE', 'RELEASED')),
  version integer not null default 1 check (version >= 1),
  created_by text,
  created_at timestamptz not null default now(),
  released_at timestamptz,
  released_by text,
  primary key (tenant_id, id),
  unique (tenant_id, idempotency_key, allocation_no),
  foreign key (tenant_id, legal_entity_id, product_id, dimension_set_hash)
    references on_hand (tenant_id, legal_entity_id, product_id, dimension_set_hash),
  check (released_qty <= qty),
  check ((state = 'RELEASED') = (released_qty = qty)),
  check ((state = 'RELEASED') = (released_at is not null))
);
create index if not exists inventory_hold_row_idx
  on inventory_hold (tenant_id, legal_entity_id, product_id, dimension_set_hash, state);
create index if not exists inventory_hold_source_idx
  on inventory_hold (tenant_id, source_type, source_ref, state);
alter table inventory_hold enable row level security;
alter table inventory_hold force row level security;
create policy inventory_hold_tenant_access on inventory_hold as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy inventory_hold_legal_entity_scope on inventory_hold as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

do $$
begin
  if exists (select 1 from pg_roles where rolname = 'passage_app') then
    execute 'grant select, insert, update on inventory_hold to passage_app';
  end if;
end
$$;
