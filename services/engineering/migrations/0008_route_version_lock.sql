-- BR-9.3 for routings: a draft route version is edited under an optimistic-locking counter, as BOM
-- versions are. It starts at 1 and every edit, activation and cancellation adds 1. No new table, so
-- no grants change (passage_app already has SELECT, INSERT, UPDATE and DELETE on the routing tables).
ALTER TABLE route_version ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1 CHECK (version >= 1);
