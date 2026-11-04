-- Optimistic concurrency on the configuration rows that a person edits with PUT (FR-11.3, FR-11.5, API
-- contract): accounts, account structure versions, dimension values, description templates and journal
-- names. lock_version is the entity tag; every replace increments it and a PUT of an existing row must
-- send it in If-Match (428 when missing, 412 when stale). Additive only: existing rows start at 1. No new
-- table, so no new grant: these tables already carry SELECT, INSERT, UPDATE for passage_app.

ALTER TABLE main_account         ADD COLUMN IF NOT EXISTS lock_version int NOT NULL DEFAULT 1 CHECK (lock_version >= 1);
ALTER TABLE account_structure    ADD COLUMN IF NOT EXISTS lock_version int NOT NULL DEFAULT 1 CHECK (lock_version >= 1);
ALTER TABLE dimension_value      ADD COLUMN IF NOT EXISTS lock_version int NOT NULL DEFAULT 1 CHECK (lock_version >= 1);
ALTER TABLE description_template ADD COLUMN IF NOT EXISTS lock_version int NOT NULL DEFAULT 1 CHECK (lock_version >= 1);
ALTER TABLE journal_name         ADD COLUMN IF NOT EXISTS lock_version int NOT NULL DEFAULT 1 CHECK (lock_version >= 1);
