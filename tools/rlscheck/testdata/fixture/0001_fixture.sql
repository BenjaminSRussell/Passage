create table good (tenant_id uuid not null, legal_entity_id uuid not null);
alter table good enable row level security;
alter table good force row level security;
create policy good_tenant on good as permissive
  using (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid)
  with check (tenant_id = nullif(current_setting('passage.tenant_id', true), '')::uuid);
create policy good_entity on good as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*' or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table bad_column (tenant_id uuid not null, owner_tenant_id uuid not null);
alter table bad_column enable row level security;
alter table bad_column force row level security;
create policy bad_column_tenant on bad_column as permissive
  using (owner_tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (owner_tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table extra_true (tenant_id uuid not null);
alter table extra_true enable row level security;
alter table extra_true force row level security;
create policy extra_true_tenant on extra_true as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy extra_true_open on extra_true as permissive using (true);

create table insert_true (tenant_id uuid not null);
alter table insert_true enable row level security;
alter table insert_true force row level security;
create policy insert_true_tenant on insert_true as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy insert_true_open on insert_true as permissive for insert with check (true);

create table select_restrictive (tenant_id uuid not null, legal_entity_id uuid not null);
alter table select_restrictive enable row level security;
alter table select_restrictive force row level security;
create policy select_restrictive_tenant on select_restrictive as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy select_restrictive_entity on select_restrictive as restrictive for select
  using (current_setting('passage.legal_entity_scopes', true) = '*' or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table null_admitting (tenant_id uuid not null, legal_entity_id uuid);
alter table null_admitting enable row level security;
alter table null_admitting force row level security;
create policy null_admitting_tenant on null_admitting as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy null_admitting_entity on null_admitting as restrictive
  using (legal_entity_id is null or current_setting('passage.legal_entity_scopes', true) = '*' or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
