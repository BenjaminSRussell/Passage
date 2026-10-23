create table if not exists outbox (
  id text primary key,
  tenant_id uuid not null,
  aggregate_type text not null,
  aggregate_id text not null,
  type text not null,
  payload jsonb not null,
  created_at timestamptz not null default now(),
  partition_key text generated always as (tenant_id::text || ':' || aggregate_id) stored,
  headers jsonb
);

create index if not exists ix_outbox_created_at on outbox (created_at, id);

create table if not exists consumer_inbox (
  consumer text not null,
  event_id text not null,
  processed_at timestamptz not null default now(),
  primary key (consumer, event_id)
);

create index if not exists ix_consumer_inbox_processed_at on consumer_inbox (processed_at);
