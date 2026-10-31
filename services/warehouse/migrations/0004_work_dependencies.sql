alter table work drop constraint work_status_check;
alter table work add constraint work_status_check check (status in ('OPEN', 'ASSIGNED', 'IN_PROGRESS', 'COMPLETED',
  'PARTIALLY_COMPLETED', 'CANCELLED', 'BLOCKED_NO_LOCATION', 'WAITING')) not valid;
alter table work validate constraint work_status_check;
alter table work add column depends_on uuid[] not null default '{}';
create index work_depends_on_idx on work using gin (depends_on) where cardinality(depends_on) > 0;
