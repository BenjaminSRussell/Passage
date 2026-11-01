create table if not exists quality_hold_link (
  tenant_id uuid not null,
  id uuid not null,
  idempotency_key text not null check (char_length(idempotency_key) between 1 and 255),
  action text not null check (action in ('PLACE', 'RELEASE')),
  source_type text not null check (source_type in ('QUALITY_ORDER', 'NONCONFORMANCE')),
  source_ref text not null,
  legal_entity_id uuid not null,
  acting_user text not null,
  item_id uuid not null,
  dims jsonb not null default '{}'::jsonb check (jsonb_typeof(dims) = 'object'),
  qty numeric(18,6) not null default 0 check (qty >= 0),
  keep_qty numeric(18,6) not null default 0 check (keep_qty >= 0),
  state text not null default 'PENDING' check (state in ('PENDING', 'DONE', 'FAILED')),
  attempts integer not null default 0 check (attempts >= 0),
  last_error text not null default '' check (char_length(last_error) <= 500),
  created_at timestamptz not null default now(),
  done_at timestamptz,
  primary key (tenant_id, id),
  unique (tenant_id, idempotency_key),
  check ((action = 'PLACE') = (qty > 0))
);
create index if not exists quality_hold_link_pending_idx on quality_hold_link (tenant_id, created_at, id) where state = 'PENDING';
alter table quality_hold_link enable row level security;
alter table quality_hold_link force row level security;
create policy quality_hold_link_tenant_access on quality_hold_link as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy quality_hold_link_legal_entity_scope on quality_hold_link as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

do $$
begin
  if exists (select 1 from pg_roles where rolname = 'passage_app') then
    execute 'grant select, insert, update on quality_hold_link to passage_app';
  end if;
end
$$;
