-- Migration factory, wave 49 slice 8 (FR-49.9, CUT-1603, CUT-1605): the first approval of a cutover
-- rollback, waiting for a second, different authenticated CutoverManager. Approvers are never names
-- typed into a request: the first approval is stored here, and the second user's confirmation
-- executes the rollback with both of them as the plan's approvers (cutover_plan.rollback_approvers).
-- Like a GO approval (0007), an approval belongs to the go/no-go decision round it was given in
-- (round = the seq the plan's next decision takes), so a go/no-go decision recorded in between voids
-- it: the call to roll back was made on facts the decision has since replaced. One row per approver
-- and round, insert-only. The plan's version moves with the approval, so a client that read an older
-- plan cannot confirm over a newer one.

create table cutover_rollback_approval (
  tenant_id uuid not null,
  plan_id uuid not null,
  round int not null check (round >= 1),
  approver text not null check (approver <> '' and approver not like 'service:%'),
  plan_version int not null check (plan_version >= 2),
  approved_at timestamptz not null default now(),
  primary key (tenant_id, plan_id, round, approver),
  foreign key (tenant_id, plan_id) references cutover_plan (tenant_id, plan_id)
);

alter table cutover_rollback_approval enable row level security;
alter table cutover_rollback_approval force row level security;
create policy cutover_rollback_approval_tenant_access on cutover_rollback_approval as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON cutover_rollback_approval TO passage_app';
  END IF;
END
$$;

-- FR-49.6 "durations from the last rehearsal": a plan generated from an earlier plan's measured step
-- durations records which plan they came from and how many steps took one. No new table: the existing
-- INSERT and SELECT grants on cutover_plan cover the columns.
alter table cutover_plan add column rehearsal_plan_id uuid;
alter table cutover_plan add column rehearsal_carried int not null default 0 check (rehearsal_carried >= 0);
alter table cutover_plan add foreign key (tenant_id, rehearsal_plan_id) references cutover_plan (tenant_id, plan_id);
