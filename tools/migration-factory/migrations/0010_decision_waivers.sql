-- Migration factory, wave 49 slice 10 (FR-49.6, CUT-1608): waivers of staged-but-unposted packs in
-- the go/no-go. A pack whose load stays in the factory (domain.NotPosted) keeps `packs_reconciled`
-- open unless both approvers of a GO waive it with a reason. The first approver's waivers are stored
-- with the approval (the second approves the same set) and the decision keeps them as evidence.
-- Existing rows have none. No new table: the grants of 0007 cover the new columns.
--
-- Both tables are live. A column with a constant default is a catalog-only change (no rewrite, and
-- NOT NULL needs no scan because the default is not null), but each ALTER takes ACCESS EXCLUSIVE for
-- an instant: the lock_timeout makes the migration fail (and the rollout retry) instead of queueing
-- behind a long transaction while every later query on the table queues behind it.
SET LOCAL lock_timeout = '5s';

alter table cutover_decision add column waivers jsonb not null default '[]'::jsonb;
alter table cutover_go_approval add column waivers jsonb not null default '[]'::jsonb;
