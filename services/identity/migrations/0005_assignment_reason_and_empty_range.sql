alter table identity_role_assignments drop constraint identity_role_assignments_check;
alter table identity_role_assignments add constraint identity_role_assignments_valid_range check (valid_to is null or valid_to >= valid_from);

alter table identity_role_assignments
  add column reason text check (reason is null or char_length(reason) between 1 and 500),
  add column granted_by text;
