alter table identity_operator
  add column singleton boolean not null default true,
  add constraint identity_operator_single_row check (singleton),
  add constraint identity_operator_singleton_key unique (singleton);
