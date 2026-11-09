-- Credit-note reversal (BR-29.6, AC-29.6, #29 P3). The revrec-creditnotes consumer reads billing's creditnote.booked and, per
-- credited line, reduces the unrecognized schedule of the credited obligations and reverses recognized revenue for what exceeds
-- it (domain.PlanReversal). One credit_note_reversal row per (credit note, line) makes the consumer idempotent per line; it
-- carries the amounts the roll-forward reports (deferred_amount as a negative adjustment, revenue_reversed as a reversal) and the
-- ledger posting of revenue_reversed (Dr 4000 / Cr 2400), made by POST /v1/revrec/journal-postings with back-off and parking.

-- What credit notes took off a SCHEDULED line; amount is what is left to recognize, and a line taken to zero is REVERSED.
ALTER TABLE revenue_schedule ADD COLUMN IF NOT EXISTS credited_amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (credited_amount >= 0);

CREATE TABLE IF NOT EXISTS credit_note_reversal (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  contract_id uuid NOT NULL,
  credit_note_id uuid NOT NULL,
  credit_note_number text NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  original_line_no integer NOT NULL,
  product_id uuid NOT NULL,
  currency char(3) NOT NULL,
  -- The credit note's posting date: billing's journal date, and the date of the reversal and its roll-forward movements.
  posting_date date NOT NULL,
  -- The deferred part billing debited to 2400 for the line (creditnote.booked lines[].deferred).
  deferred_amount numeric(20,6) NOT NULL CHECK (deferred_amount > 0),
  -- Taken off the unrecognized schedule, and recognized revenue reversed; together the deferred amount.
  deferred_reduced numeric(20,6) NOT NULL CHECK (deferred_reduced >= 0),
  revenue_reversed numeric(20,6) NOT NULL CHECK (revenue_reversed >= 0),
  -- Per obligation: [{"obligationId", "share", "reduced", "reversed"}].
  obligations jsonb NOT NULL,
  event_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  -- Ledger posting of revenue_reversed: the ledger is fixed at the first attempt, the entry id kept once the ledger accepts.
  ledger_id uuid,
  entry_id text,
  gl_attempt_count integer NOT NULL DEFAULT 0,
  gl_refusal_count integer NOT NULL DEFAULT 0,
  gl_next_attempt_at timestamptz,
  gl_last_error text,
  gl_parked_code text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, credit_note_id, line_no),
  FOREIGN KEY (tenant_id, contract_id) REFERENCES revenue_contract (tenant_id, id),
  CHECK (deferred_reduced + revenue_reversed = deferred_amount),
  CHECK (entry_id IS NULL OR revenue_reversed > 0)
);
ALTER TABLE credit_note_reversal ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_note_reversal FORCE ROW LEVEL SECURITY;
CREATE POLICY credit_note_reversal_tenant_access ON credit_note_reversal AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY credit_note_reversal_legal_entity_scope ON credit_note_reversal AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
-- The roll-forward sums per contract and date; the posting sweep scans the unposted reversals of an entity.
CREATE INDEX IF NOT EXISTS credit_note_reversal_contract_idx ON credit_note_reversal (tenant_id, contract_id, posting_date);
CREATE INDEX IF NOT EXISTS credit_note_reversal_unposted_idx ON credit_note_reversal (tenant_id, legal_entity_id, gl_next_attempt_at)
  WHERE revenue_reversed > 0 AND entry_id IS NULL AND gl_parked_code IS NULL;

-- The platform eventing inbox (eventing.NewPgInbox): one row per consumer and event id, written once every line of the event
-- is applied (each line is idempotent by its own key), so a redelivery is skipped.
CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);
ALTER TABLE consumer_inbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE consumer_inbox FORCE ROW LEVEL SECURITY;
CREATE POLICY consumer_inbox_tenant_access ON consumer_inbox AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- Reversal rows are inserted by the consumer and updated only by the posting sweep; nothing is deleted. The inbox is
-- insert-only.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON credit_note_reversal TO passage_app';
    EXECUTE 'GRANT SELECT, INSERT ON consumer_inbox TO passage_app';
  END IF;
END
$$;
