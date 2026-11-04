-- PSG-1510 (wave 15, slice 10): balance verification against the ledger (FR-15.1).
-- One row per check of the reporting read model against the ledger's trial
-- balance of the same ledger: the net per account the read model holds
-- (currency type acct, all periods) against the ledger's GET /v1/ledger/balances.
-- hwm_before / hwm_after are the read model's high-water marks around the
-- ledger call; a mark that moved means postings arrived while the ledger was
-- read and the check is inconclusive (status 'inconclusive'), never a mismatch.
-- entries_pending is the most entries seen waiting ahead of the mark
-- (reporting_entry_pending) before or after the ledger call: the ledger is then
-- known to be ahead of the read model, so the check is inconclusive too.
-- diffs holds at most 500 differing accounts (diff_count is the full count).
CREATE TABLE IF NOT EXISTS balance_verification (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  ledger_id uuid NOT NULL,
  legal_entity_id uuid,
  status text NOT NULL CHECK (status IN ('matched', 'differs', 'inconclusive')),
  hwm_before bigint NOT NULL CHECK (hwm_before >= 0),
  hwm_after bigint NOT NULL CHECK (hwm_after >= 0),
  entries_pending integer NOT NULL DEFAULT 0 CHECK (entries_pending >= 0),
  accounts_compared integer NOT NULL CHECK (accounts_compared >= 0),
  diff_count integer NOT NULL CHECK (diff_count >= 0),
  diffs jsonb NOT NULL DEFAULT '[]',
  requested_by text NOT NULL DEFAULT '',
  request_hash text NOT NULL,
  idempotency_key text NOT NULL,
  verified_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS balance_verification_ledger ON balance_verification (tenant_id, ledger_id, verified_at DESC, id);
ALTER TABLE balance_verification ENABLE ROW LEVEL SECURITY;
ALTER TABLE balance_verification FORCE ROW LEVEL SECURITY;
CREATE POLICY balance_verification_tenant_access ON balance_verification AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY balance_verification_legal_entity_scope ON balance_verification AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Verifications are append-only: the store inserts and reads them.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON balance_verification TO passage_app';
  END IF;
END
$$;
