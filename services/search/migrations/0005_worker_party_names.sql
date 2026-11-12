-- #506: a worker is found by its person's name. HR's worker.created carries no
-- personal data, only the party person it was created for (party_person_id).
-- The index keeps that party id on the worker row and titles the worker with
-- the person's name from the party record: from the party row when the worker
-- is indexed after it, and from every later party event that changes the name.
-- The column is null for every other record type; the table's policy is on the
-- tenant only, so a nullable column needs no policy of its own.
alter table search_record_tenant add column party_id uuid;
create index search_record_tenant_worker_party on search_record_tenant (tenant_id, party_id) where party_id is not null;
