-- Pricing context, wave 26 slice 10: rebate agreement lifecycle (FR-26.11). An agreement can
-- be revised (name, end date, tiers before the first accrual) and closed early. Additive:
-- existing agreements stay ACTIVE at version 1. No new table, so no new grant: the store
-- already updates and locks rebate_agreement (see 0008_grants.sql).

ALTER TABLE rebate_agreement ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1 CHECK (version >= 1);
ALTER TABLE rebate_agreement ADD COLUMN IF NOT EXISTS closed_at timestamptz;

-- 0002 created the status check without a name and only allowed ACTIVE.
DO $$
DECLARE c record;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
           WHERE conrelid = 'rebate_agreement'::regclass AND contype = 'c'
             AND pg_get_constraintdef(oid) LIKE '%status%ACTIVE%'
  LOOP
    EXECUTE format('ALTER TABLE rebate_agreement DROP CONSTRAINT %I', c.conname);
  END LOOP;
END $$;
ALTER TABLE rebate_agreement DROP CONSTRAINT IF EXISTS rebate_agreement_status_check;
ALTER TABLE rebate_agreement ADD CONSTRAINT rebate_agreement_status_check CHECK (status IN ('ACTIVE', 'CLOSED'));
