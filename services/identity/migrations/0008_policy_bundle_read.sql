create policy identity_users_policy_compile_read on identity_users as permissive for select
  using (current_setting('passage.policy_compile', true) = 'on' and exists (select 1 from identity_operator));

create policy identity_role_assignments_policy_compile_read on identity_role_assignments as permissive for select
  using (current_setting('passage.policy_compile', true) = 'on' and exists (select 1 from identity_operator));
