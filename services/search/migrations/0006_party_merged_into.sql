-- #506: a worker's name stays right after a party merge. When party merges a
-- person into another, the merged (losing) party row is removed from the index.
-- merged_into keeps the survivor on that removed row, so the workers created for
-- the merged person move to the survivor (and take its name), a replayed
-- worker.created that still names the merged person lands on the survivor, and
-- a later merge of the survivor carries them along. Null on every other row; the
-- table's policy is on the tenant only, so the column needs no policy of its own.
alter table search_record_tenant add column merged_into uuid;
create index search_record_tenant_merged_into on search_record_tenant (tenant_id, merged_into) where merged_into is not null;
