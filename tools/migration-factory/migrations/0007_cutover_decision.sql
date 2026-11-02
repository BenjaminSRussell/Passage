-- Migration factory, wave 49 slice 7 (FR-49.6, 9.49.5 "record go/no-go", AC-50.9): the go/no-go
-- decision of a cutover plan. A decision (GO or NO_GO) is recorded with its approvers, its reason
-- and the checklist it was taken on (the 14 phase-exit items of PRD 9.49.1, evaluated from the
-- factory's pack runs and parallel runs plus the evidence references the approvers attest), so the
-- audit record shows what was known at the time. Decisions are append-only: a plan carries a
-- history of NO_GO decisions and at most one GO (the store refuses a second). The plan's version
-- moves with every decision and every GO approval, so a client that read an older plan cannot
-- decide over a newer one.
--
-- Approvers are authenticated users, never names typed into a request (CUT-1605). A NO_GO is
-- approved by the user who records it. A GO takes two steps: a first CutoverManager approves it
-- (cutover_go_approval), and a second, different CutoverManager approves the same attestations,
-- which records the GO with both of them as approvers. An approval belongs to the decision round it
-- was given in (round = the seq the next decision takes), so a NO_GO recorded in between voids it.

create table cutover_decision (
  tenant_id uuid not null,
  plan_id uuid not null,
  seq int not null check (seq >= 1),
  decision text not null check (decision in ('GO', 'NO_GO')),
  approvers text[] not null check (cardinality(approvers) >= 1),
  reason text not null default '' check (length(reason) <= 2000),
  checklist jsonb not null,
  plan_version int not null check (plan_version >= 2),
  decided_by text not null check (decided_by <> ''),
  decided_at timestamptz not null default now(),
  primary key (tenant_id, plan_id, seq),
  foreign key (tenant_id, plan_id) references cutover_plan (tenant_id, plan_id),
  check (decision <> 'GO' or cardinality(approvers) >= 2),
  check (decision <> 'NO_GO' or reason <> ''),
  -- The recording user is always one of the approvers.
  check (decided_by = any(approvers))
);
-- At most one GO per plan, enforced by the database as well as by the store.
create unique index cutover_decision_one_go on cutover_decision (tenant_id, plan_id) where decision = 'GO';

-- The first approval of a GO, waiting for a second approver. One per user and round.
create table cutover_go_approval (
  tenant_id uuid not null,
  plan_id uuid not null,
  round int not null check (round >= 1),
  approver text not null check (approver <> ''),
  attestations jsonb not null,
  plan_version int not null check (plan_version >= 2),
  approved_at timestamptz not null default now(),
  primary key (tenant_id, plan_id, round, approver),
  foreign key (tenant_id, plan_id) references cutover_plan (tenant_id, plan_id)
);

alter table cutover_decision enable row level security;
alter table cutover_decision force row level security;
create policy cutover_decision_tenant_access on cutover_decision as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

alter table cutover_go_approval enable row level security;
alter table cutover_go_approval force row level security;
create policy cutover_go_approval_tenant_access on cutover_go_approval as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Application role privileges. Decisions and approvals are insert-only; the plan version moves
-- through the existing UPDATE grant on cutover_plan. Skipped when the role does not exist.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON cutover_decision TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON cutover_go_approval TO passage_app';
  END IF;
END
$$;
