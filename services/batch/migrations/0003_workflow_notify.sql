create table if not exists workflow_definition (
  tenant_id uuid not null,
  id uuid not null,
  entity varchar(48) not null,
  legal_entity_id uuid not null,
  version int not null,
  status text not null check (status in ('draft', 'active', 'retired')),
  comment_required boolean not null default false,
  admin_user_id uuid,
  delegation_max int not null default 3,
  steps jsonb not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, entity, legal_entity_id, version)
);
alter table workflow_definition enable row level security;
alter table workflow_definition force row level security;
create policy workflow_definition_tenant_access on workflow_definition as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy workflow_definition_legal_entity_scope on workflow_definition as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table if not exists workflow_instance (
  tenant_id uuid not null,
  id uuid not null,
  legal_entity_id uuid not null,
  definition_id uuid not null,
  definition_version int not null,
  entity varchar(48) not null,
  record_id uuid not null,
  status text not null,
  submitted_by uuid not null,
  submitted_at timestamptz not null default now(),
  current_step int,
  completed_at timestamptz,
  outcome_reason text,
  temporal_run_id text,
  primary key (tenant_id, id)
);
create index if not exists ix_workflow_instance_record on workflow_instance (tenant_id, entity, record_id);
alter table workflow_instance enable row level security;
alter table workflow_instance force row level security;
create policy workflow_instance_tenant_access on workflow_instance as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy workflow_instance_legal_entity_scope on workflow_instance as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*'
         or legal_entity_id::text = any(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

create table if not exists workflow_work_item (
  tenant_id uuid not null,
  id uuid not null,
  instance_id uuid not null,
  step int not null,
  assignee_id uuid not null,
  original_assignee_id uuid,
  status text not null,
  due_at timestamptz not null,
  acted_at timestamptz,
  comment text,
  delegation_depth int not null default 0,
  primary key (tenant_id, id)
);
create index if not exists ix_work_item_assignee on workflow_work_item (tenant_id, assignee_id, status);
alter table workflow_work_item enable row level security;
alter table workflow_work_item force row level security;
create policy workflow_work_item_tenant_access on workflow_work_item as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table if not exists batch_job (
  tenant_id uuid not null,
  id uuid not null,
  code text not null,
  handler text not null,
  params_schema jsonb,
  partition_strategy text not null default 'range',
  partition_count int not null default 1,
  concurrency int not null default 1,
  priority text not null default 'B',
  cron text,
  time_zone text not null default 'UTC',
  exclusive boolean not null default false,
  retry_policy jsonb,
  status text not null default 'active',
  created_at timestamptz not null default now(),
  primary key (tenant_id, id),
  unique (tenant_id, code)
);
alter table batch_job enable row level security;
alter table batch_job force row level security;
create policy batch_job_tenant_access on batch_job as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table if not exists batch_run (
  tenant_id uuid not null,
  id uuid not null,
  job_id uuid not null,
  params jsonb,
  status text not null,
  scheduled_at timestamptz not null default now(),
  started_at timestamptz,
  ended_at timestamptz,
  rows_total bigint not null default 0,
  rows_done bigint not null default 0,
  error text,
  triggered_by uuid,
  primary key (tenant_id, id)
);
create index if not exists ix_batch_run_job on batch_run (tenant_id, job_id, scheduled_at desc);
alter table batch_run enable row level security;
alter table batch_run force row level security;
create policy batch_run_tenant_access on batch_run as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table if not exists notify_template (
  tenant_id uuid not null,
  id uuid not null,
  code varchar(64) not null,
  channel text not null,
  language varchar(8) not null,
  layer text not null,
  version int not null,
  status text not null,
  subject varchar(255) not null,
  body text not null,
  sample_payload jsonb,
  primary key (tenant_id, id),
  unique (tenant_id, code, language, layer, version)
);
alter table notify_template enable row level security;
alter table notify_template force row level security;
create policy notify_template_tenant_access on notify_template as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table if not exists notify_outbound_message (
  tenant_id uuid not null,
  id uuid not null,
  channel text not null,
  recipient_user_id uuid,
  recipient_address varchar(254),
  template_code varchar(48),
  template_version int,
  language varchar(8),
  subject varchar(255),
  body_html text,
  body_text text,
  related_entity varchar(48),
  related_record_id uuid,
  status text not null,
  attempts int not null default 0,
  next_attempt_at timestamptz,
  last_error text,
  provider_message_id varchar(200),
  priority text not null default 'immediate',
  created_at timestamptz not null default now(),
  sent_at timestamptz,
  primary key (tenant_id, id)
);
create index if not exists ix_notify_outbound_status on notify_outbound_message (tenant_id, status, next_attempt_at);
alter table notify_outbound_message enable row level security;
alter table notify_outbound_message force row level security;
create policy notify_outbound_message_tenant_access on notify_outbound_message as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table if not exists notify_preference (
  tenant_id uuid not null,
  user_id uuid not null,
  event_class text not null,
  channels text[] not null,
  digest text not null default 'none',
  digest_time time,
  digest_day int,
  quiet_hours int4range,
  language varchar(8),
  primary key (tenant_id, user_id, event_class)
);
alter table notify_preference enable row level security;
alter table notify_preference force row level security;
create policy notify_preference_tenant_access on notify_preference as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table if not exists event_schema (
  tenant_id uuid not null,
  event_type text not null,
  version int not null,
  schema jsonb not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, event_type, version)
);
alter table event_schema enable row level security;
alter table event_schema force row level security;
create policy event_schema_tenant_access on event_schema as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

create table if not exists idempotency_keys (
  tenant_id uuid not null,
  key text not null,
  request_hash text not null,
  created_at timestamptz not null default now(),
  primary key (tenant_id, key)
);
alter table idempotency_keys enable row level security;
alter table idempotency_keys force row level security;
create policy idempotency_keys_tenant_access on idempotency_keys as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table outbox enable row level security;
alter table outbox force row level security;
drop policy if exists outbox_tenant_access on outbox;
create policy outbox_tenant_access on outbox as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
