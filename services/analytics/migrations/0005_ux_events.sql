-- UX events (#510): how people move through the web app's flows. One row per
-- event, written by the web app in batches. There is no user id: a session id
-- is a random value the browser makes for one tab and forgets with it, so an
-- event cannot be tied to a person. Search text is stored only after the
-- service has scrubbed it (no email addresses, no long numbers).
create table if not exists ux_event (
  tenant_id uuid not null,
  event_id bigint generated always as identity,
  session_id text not null check (session_id ~ '^[A-Za-z0-9-]{8,64}$'),
  release text not null check (release ~ '^[A-Za-z0-9._+-]{1,32}$'),
  event_type text not null check (event_type in ('funnel_step', 'validation_error', 'search_no_results', 'form_abandoned')),
  flow text check (flow in ('procure_to_pay', 'onboarding')),
  step text,
  route text check (route ~ '^/'),
  field text,
  query text check (char_length(query) <= 80),
  duration_ms integer check (duration_ms between 0 and 86400000),
  occurred_at timestamptz not null,
  received_at timestamptz not null default now(),
  primary key (tenant_id, event_id),
  check ((event_type = 'funnel_step') = (flow is not null and step is not null)),
  check (event_type <> 'validation_error' or (route is not null and field is not null)),
  check (event_type <> 'search_no_results' or query is not null),
  check (event_type <> 'form_abandoned' or route is not null)
);
alter table ux_event enable row level security;
alter table ux_event force row level security;
create policy ux_event_tenant_access on ux_event as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create index if not exists ux_event_flow on ux_event (tenant_id, flow, occurred_at) where event_type = 'funnel_step';
create index if not exists ux_event_type on ux_event (tenant_id, event_type, occurred_at);

-- Events are written once and never changed. Retention (delete older than 13
-- months) is an operations job on received_at.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON ux_event TO passage_app';
  END IF;
END
$$;
