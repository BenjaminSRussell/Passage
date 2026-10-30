delete from consumer_inbox;
alter table consumer_inbox add column if not exists tenant_id uuid not null;
alter table consumer_inbox drop constraint if exists consumer_inbox_pkey;
alter table consumer_inbox add primary key (tenant_id, consumer, event_id);

alter table consumer_inbox enable row level security;
alter table consumer_inbox force row level security;
drop policy if exists consumer_inbox_tenant_access on consumer_inbox;
create policy consumer_inbox_tenant_access on consumer_inbox as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
