ALTER TABLE ledger_set_member
    ADD COLUMN IF NOT EXISTS calendar_code        text CHECK (calendar_code IS NULL OR calendar_code ~ '^[A-Z0-9_-]{1,20}$'),
    ADD COLUMN IF NOT EXISTS accounting_principle text CHECK (accounting_principle IS NULL OR accounting_principle ~ '^[A-Z0-9_.-]{1,20}$'),
    ADD COLUMN IF NOT EXISTS reporting_currency   text CHECK (reporting_currency IS NULL OR reporting_currency ~ '^[A-Z]{3}$');
