-- Migration factory, wave 49 (#49): what a cutover rollback leaves behind besides the re-keying
-- export. The pack runs of the plan's entities and packs move to ROLLED_BACK (BR-49.1), and the
-- coexistence flags of the plan's entities that Passage mastered go back to LEGACY, so the legacy
-- coexistence adapters resume (FR-49.7, AC-49.7). Both happen in the rollback's transaction.
SET LOCAL lock_timeout = '5s';

-- The plan whose rollback moved the run to ROLLED_BACK, and when.
alter table pack_run add column rolled_back_plan_id uuid;
alter table pack_run add column rolled_back_at timestamptz;
alter table pack_run add constraint pack_run_rolled_back_plan
  check ((status = 'ROLLED_BACK') = (rolled_back_plan_id is not null and rolled_back_at is not null));

-- How many pack runs and coexistence flags the rollback handed back.
alter table cutover_plan add column rollback_pack_runs int not null default 0 check (rollback_pack_runs >= 0);
alter table cutover_plan add column rollback_coexistence int not null default 0 check (rollback_coexistence >= 0);
