-- PSG-1506 (wave 15, slice 6): audit trail (FR-15.10, AuditTrailEntry).
-- One row per audited event: ledger kinds (posted, approvals, reversals,
-- period changes and reopen approvals, rate overrides, tax fallbacks,
-- allocation, year-end and rebuild runs) arrive from other services' events
-- through PutAuditEntries; reporting kinds (statement runs, definition and
-- hierarchy publishes, retires, schedule saves, audit exports) are written in
-- the same transaction as the change. Rows are append-only. source_event_id
-- keeps one row per source event, so a redelivered event is a replay.

CREATE TABLE IF NOT EXISTS audit_trail_entry (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  occurred_at timestamptz NOT NULL,
  recorded_at timestamptz NOT NULL DEFAULT now(),
  ledger_id uuid,
  period text CHECK (period ~ '^[0-9]{4}-[0-9]{2,3}$'),
  kind text NOT NULL CHECK (kind IN (
    'posted', 'replayed', 'reversed', 'batch_submitted', 'batch_approved', 'batch_rejected',
    'period_changed', 'reopen_approved', 'rate_override', 'tax_fallback', 'allocation_run',
    'year_end_run', 'rebuild',
    'statement_run', 'definition_published', 'definition_retired', 'hierarchy_published',
    'schedule_saved', 'audit_exported')),
  actor_id text NOT NULL CHECK (actor_id <> ''),
  object_ref text NOT NULL CHECK (object_ref <> ''),
  details jsonb NOT NULL DEFAULT '{}',
  trace_id text NOT NULL DEFAULT '',
  source_event_id text,
  PRIMARY KEY (tenant_id, id)
);
-- PRD §2.15.4: the audit view is indexed on (ledger_id, occurred_at); the
-- second index serves date-range reads across ledgers.
CREATE INDEX IF NOT EXISTS audit_trail_entry_ledger_time ON audit_trail_entry (tenant_id, ledger_id, occurred_at, id);
CREATE INDEX IF NOT EXISTS audit_trail_entry_time ON audit_trail_entry (tenant_id, occurred_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS audit_trail_entry_source_event ON audit_trail_entry (tenant_id, source_event_id)
  WHERE source_event_id IS NOT NULL;
ALTER TABLE audit_trail_entry ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_trail_entry FORCE ROW LEVEL SECURITY;
CREATE POLICY audit_trail_entry_tenant_access ON audit_trail_entry AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
