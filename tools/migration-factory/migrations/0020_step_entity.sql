-- Migration factory, wave 49 slice 16 (D5a): the per-entity runbook. A step of a plan generated
-- with expandByEntity names the legal entity it covers. No table is created here: cutover_step
-- keeps the grants of 0001 and 0004 (SELECT, INSERT, UPDATE).
alter table cutover_step add column entity text not null default '';
