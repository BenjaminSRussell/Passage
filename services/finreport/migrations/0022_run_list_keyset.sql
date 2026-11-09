-- PSG-1521 (wave 15, slice 21): keyset paging of the statement run list
-- (FR-15.6, FR-15.8). GET /v1/reporting/runs pages on (started_at, id), both
-- descending, optionally narrowed to a definition, ledger or period; the
-- index serves the unfiltered and the definition-filtered page. The original
-- statement_run_started index ordered id ascending and cannot serve a
-- descending row comparison, so it is replaced.
CREATE INDEX IF NOT EXISTS statement_run_started_keyset ON statement_run (tenant_id, started_at DESC, id DESC);
DROP INDEX IF EXISTS statement_run_started;
CREATE INDEX IF NOT EXISTS statement_run_definition_started ON statement_run (tenant_id, definition_code, started_at DESC, id DESC);
