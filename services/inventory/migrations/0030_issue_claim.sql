create table if not exists inventory_issue_claim (
  tenant_id uuid not null,
  legal_entity_id uuid not null,
  idempotency_key text not null check (char_length(idempotency_key) between 1 and 255),
  product_id uuid not null,
  warehouse text not null check (char_length(warehouse) between 1 and 120),
  batch text not null default '' check (char_length(batch) <= 120),
  kind text not null check (kind in ('PICK', 'SHIP', 'CONSUME')),
  qty numeric(28,6) not null check (qty > 0),
  created_at timestamptz not null default now(),
  primary key (tenant_id, idempotency_key)
);
create index if not exists inventory_issue_claim_scope_idx
  on inventory_issue_claim (tenant_id, legal_entity_id, product_id, warehouse, kind);
alter table inventory_issue_claim enable row level security;
alter table inventory_issue_claim force row level security;
create policy inventory_issue_claim_tenant_access on inventory_issue_claim as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy inventory_issue_claim_legal_entity_scope on inventory_issue_claim as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

do $$
begin
  if exists (select 1 from pg_roles where rolname = 'passage_app') then
    execute 'grant select, insert, delete on inventory_issue_claim to passage_app';
  end if;
end
$$;
