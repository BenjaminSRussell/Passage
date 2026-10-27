-- Shared financial reason codes (FR-11.15), reversal reason and negative posting
-- parameters (BR-11.16). Additive only (expand/contract, Atlas).

CREATE TABLE IF NOT EXISTS reason_code (
    tenant_id                        uuid        NOT NULL,
    code                             text        NOT NULL CHECK (code ~ '^[A-Z0-9_]{1,10}$'),
    name                             text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    applies_to                       text[]      NOT NULL CHECK (cardinality(applies_to) >= 1
                                                   AND applies_to <@ ARRAY['ledger','bank','customer','vendor','fixed_asset','inventory_counting','tax','project']::text[]),
    requires_comment                 boolean     NOT NULL DEFAULT false,
    negative_posting_allowed         boolean     NOT NULL DEFAULT false,
    alternative_posting_date_allowed boolean     NOT NULL DEFAULT true,
    active                           boolean     NOT NULL DEFAULT true,
    created_at                       timestamptz NOT NULL DEFAULT now(),
    updated_at                       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, code)
);

ALTER TABLE reason_code ENABLE ROW LEVEL SECURITY;
ALTER TABLE reason_code FORCE ROW LEVEL SECURITY;
CREATE POLICY reason_code_tenant_access ON reason_code AS PERMISSIVE
    USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

-- The app role needs the catalogue (0005_grants.sql cannot cover a table created here).
-- Skipped when the role does not exist, as in 0005_grants.sql.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON reason_code TO passage_app';
  END IF;
END
$$;

-- Ledger parameters: reversal reason mandatory (default on), negative posting permitted (default off).
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS reversal_reason_required boolean NOT NULL DEFAULT true;
ALTER TABLE ledger ADD COLUMN IF NOT EXISTS negative_posting_enabled boolean NOT NULL DEFAULT false;

-- Reason recorded on the reversing entry.
ALTER TABLE journal_entry ADD COLUMN IF NOT EXISTS reason_code text;
ALTER TABLE journal_entry ADD COLUMN IF NOT EXISTS reason_comment text CHECK (char_length(reason_comment) <= 255);
ALTER TABLE journal_entry ADD CONSTRAINT journal_entry_reason_fk
    FOREIGN KEY (tenant_id, reason_code) REFERENCES reason_code (tenant_id, code);

-- Negative postings (BR-11.16) reduce the original side, so a projection
-- column may legitimately be negative. The journal itself stays positive-amount.
ALTER TABLE balance DROP CONSTRAINT IF EXISTS balance_debit_check;
ALTER TABLE balance DROP CONSTRAINT IF EXISTS balance_credit_check;
